// Package bundle resolves and bundles hook TypeScript/JavaScript via
// esbuild — the single static asset that the QuickJS runtime ultimately
// evaluates. Module resolution is filesystem-driven (it walks an fs.FS
// the caller provides) so the same code path works for on-disk hooks
// and for an in-memory test fixture.
package bundle

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Options configures a Bundle call.
type Options struct {
	// Minify enables whitespace, identifier, and syntax minification.
	Minify bool

	// GlobalName, when non-empty, emits IIFE format assigning the module
	// to a global variable of that name. When empty, ESM format is used.
	GlobalName string
}

// Bundle takes an entrypoint path and an fs.FS root, resolves all imports
// (including bare specifiers from node_modules/ within the FS), transpiles
// TypeScript, and returns a single bundled JavaScript string. Pass nil
// opts for the defaults (no minification, ESM output).
func Bundle(entrypoint string, root fs.FS, opts *Options) (string, error) {
	if opts == nil {
		opts = &Options{}
	}
	format := api.FormatESModule
	if opts.GlobalName != "" {
		format = api.FormatIIFE
	}
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{entrypoint},
		Bundle:            true,
		Write:             false,
		Format:            format,
		GlobalName:        opts.GlobalName,
		Target:            api.ES2022,
		Platform:          api.PlatformNeutral,
		MinifyWhitespace:  opts.Minify,
		MinifyIdentifiers: opts.Minify,
		MinifySyntax:      opts.Minify,
		// Inline sourcemap travels with the bundle so the host can map
		// runtime error positions back to the original .ts source.
		// SourcesContentExclude omits the embedded original-source text —
		// we only need positions, and including the sources roughly
		// doubles the inflation (the example's js-yaml dependency takes
		// the bundle from ~67 KB to ~150 KB with positions vs. ~300 KB
		// with positions+sources).
		Sourcemap:      api.SourceMapInline,
		SourcesContent: api.SourcesContentExclude,
		Plugins:        []api.Plugin{fsPlugin(root)},
	})

	if len(result.Errors) > 0 {
		msg := result.Errors[0]
		loc := ""
		if msg.Location != nil {
			loc = fmt.Sprintf(" (%s:%d)", msg.Location.File, msg.Location.Line)
		}
		return "", fmt.Errorf("bundle error: %s%s", msg.Text, loc)
	}

	if len(result.OutputFiles) == 0 {
		return "", fmt.Errorf("bundle produced no output")
	}

	return string(result.OutputFiles[0].Contents), nil
}

// fsPlugin returns an esbuild plugin that resolves and loads all files from
// an fs.FS, including bare specifiers via node_modules/ within the FS.
func fsPlugin(root fs.FS) api.Plugin {
	return api.Plugin{
		Name: "fs",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				p := args.Path

				if strings.HasPrefix(p, ".") {
					// Relative import — resolve against importer's directory.
					if args.Importer != "" {
						p = path.Join(path.Dir(args.Importer), p)
					}
				} else if args.Importer != "" {
					// Bare specifier — resolve from node_modules/.
					p = resolveNodeModule(root, args.Path, args.Importer)
					if p == "" {
						return api.OnResolveResult{}, fmt.Errorf("cannot resolve %q (no node_modules entry found)", args.Path)
					}
				}

				// fs.FS paths must not start with "./" or contain ".." — clean to
				// normalize entrypoints that arrive as "./foo" or ".veil/…".
				p = path.Clean(p)

				resolved := resolveFile(root, p)
				if resolved == "" {
					return api.OnResolveResult{}, fmt.Errorf("cannot resolve %q from %q", args.Path, args.Importer)
				}

				return api.OnResolveResult{
					Path:      resolved,
					Namespace: "fs",
				}, nil
			})

			build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "fs"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				f, err := root.Open(args.Path)
				if err != nil {
					return api.OnLoadResult{}, fmt.Errorf("reading %s: %w", args.Path, err)
				}
				defer f.Close()

				data, err := io.ReadAll(f)
				if err != nil {
					return api.OnLoadResult{}, fmt.Errorf("reading %s: %w", args.Path, err)
				}

				contents := string(data)
				loader := loaderForPath(args.Path)

				return api.OnLoadResult{
					Contents: &contents,
					Loader:   loader,
				}, nil
			})
		},
	}
}

// resolveNodeModule walks up from the importer's directory looking for
// node_modules/<pkg> in the fs.FS, similar to Node's resolution algorithm.
func resolveNodeModule(root fs.FS, pkg string, importer string) string {
	dir := path.Dir(importer)
	for {
		candidate := path.Join(dir, "node_modules", pkg)
		// Try as a file first.
		if resolved := resolveFile(root, candidate); resolved != "" {
			return resolved
		}
		// Try package.json main/module field.
		if entry := resolvePackageJSON(root, candidate); entry != "" {
			return entry
		}
		// Try index.js/index.ts.
		if resolved := resolveFile(root, path.Join(candidate, "index")); resolved != "" {
			return resolved
		}

		parent := path.Dir(dir)
		if parent == dir || dir == "." || dir == "" {
			break
		}
		dir = parent
	}
	return ""
}

// resolvePackageJSON reads the package.json in dir and returns the module or
// main entry point path.
func resolvePackageJSON(root fs.FS, dir string) string {
	pkgPath := path.Join(dir, "package.json")
	f, err := root.Open(pkgPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	// Simple extraction — avoid pulling in encoding/json for a hot path.
	// Look for "module" or "main" fields.
	content := string(data)
	for _, field := range []string{`"module"`, `"main"`} {
		idx := strings.Index(content, field)
		if idx < 0 {
			continue
		}
		rest := content[idx+len(field):]
		// Skip : and whitespace, find the quoted value.
		rest = strings.TrimLeft(rest, ": \t\n\r")
		if len(rest) > 0 && rest[0] == '"' {
			end := strings.Index(rest[1:], `"`)
			if end >= 0 {
				entry := rest[1 : end+1]
				return path.Join(dir, entry)
			}
		}
	}
	return ""
}

// resolveFile tries to find a file in the fs.FS, attempting the path as-is
// and with common extensions. Directories are skipped.
func resolveFile(root fs.FS, p string) string {
	candidates := []string{p, p + ".ts", p + ".js"}
	for _, c := range candidates {
		if f, err := root.Open(c); err == nil {
			info, err := f.Stat()
			f.Close()
			if err == nil && !info.IsDir() {
				return c
			}
		}
	}
	return ""
}

func loaderForPath(p string) api.Loader {
	if strings.HasSuffix(p, ".ts") {
		return api.LoaderTS
	}
	if strings.HasSuffix(p, ".json") {
		return api.LoaderJSON
	}
	return api.LoaderJS
}
