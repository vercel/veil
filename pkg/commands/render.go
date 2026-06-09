package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/logging"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/render"
	"github.com/vercel/veil/pkg/resource"
	"github.com/vercel/veil/pkg/variables"
	"github.com/vercel/veil/pkg/vfs"
)

// renderResponse is the JSON payload emitted by `veil render` in
// `--output json` mode. It lists every resource that rendered, so an
// agent batching many files through one invocation can map each result
// back to its source path.
type renderResponse struct {
	Rendered []renderedResource `json:"rendered"`
}

// renderedResource describes one successfully rendered resource.
type renderedResource struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	OutDir string   `json:"outDir"`
	Files  []string `json:"files"`
}

// Render returns the "render" subcommand.
func Render() *cli.Command {
	configDefault := "veil.json"
	if cwd, err := os.Getwd(); err == nil {
		if reg, err := config.Discover(cwd); err == nil {
			configDefault = reg.ConfigPath
		}
	}

	return &cli.Command{
		Name:      "render",
		Usage:     "Render one or more resources",
		UsageText: "veil render <path>... [flags]",
		Arguments: []cli.Argument{
			&cli.StringArgs{
				Name:      "paths",
				UsageText: "Paths to the resource files to render (one or more)",
				Min:       1,
				Max:       -1,
			},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Usage: "Output directory for rendered files (each resource gets a subdirectory)",
				Value: "out",
			},
			&cli.StringFlag{
				Name:  "config",
				Usage: "Path to veil.json",
				Value: configDefault,
			},
			&cli.StringSliceFlag{
				Name:  "registry",
				Usage: "Path to a compiled registry.json (repeatable)",
			},
			&cli.StringMapFlag{
				Name:    "var",
				Aliases: []string{"variable"},
				Usage:   "Variable binding in name=value form (repeatable)",
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "Dump all logs (including hook console.log output) to stdout at debug level",
			},
			&cli.BoolFlag{
				Name:    "build",
				Aliases: []string{"b"},
				Usage:   "Compile the project's kinds into an in-memory registry before rendering, instead of reading a prebuilt one from disk",
			},
			&cli.IntFlag{
				Name:  "jobs",
				Usage: "Number of resources to render concurrently (default: number of CPUs)",
				Value: runtime.NumCPU(),
			},
		},
		Action: withResult(runRender),
	}
}

func runRender(ctx context.Context, c *cli.Command) (*renderResponse, error) {
	p := interact.Default()

	pathArgs := c.StringArgs("paths")
	if len(pathArgs) == 0 {
		return nil, fmt.Errorf("render: at least one resource path is required")
	}

	// --debug is a convenience: reconfigure slog to dump everything (including
	// hook console.log output) to stdout at debug level. Overrides whatever
	// the root Before handler set up.
	if c.Bool("debug") {
		if _, err := logging.Setup(slog.LevelDebug, []string{"stdout"}, c.Root().Writer, c.Root().ErrWriter); err != nil {
			return nil, fmt.Errorf("configuring --debug logging: %w", err)
		}
	}

	// Load the project config, resolve variables, and compile the kind
	// registry + resource catalog exactly once, then reuse them across
	// every path. Rendering N files in one invocation pays the registry
	// and catalog cost a single time instead of once per file.
	reg, err := registry.LoadProject(c.String("config"))
	if err != nil {
		return nil, err
	}

	vars, err := variables.Resolve(reg.Variables, c.StringMap("var"), os.LookupEnv)
	if err != nil {
		return nil, err
	}

	// The kind registry is either compiled fresh into an in-memory
	// registry (--build, so no prebuilt registry needs to live on disk)
	// or read from a prebuilt registry.json resolved from --registry /
	// veil.json.
	var kindReg registry.Registry
	if c.Bool("build") {
		// Compile the project's kinds into an in-memory FS, then read them
		// back through an FSStore — the same load + compile + cache path a
		// disk or HTTP registry takes, so no prebuilt registry need live on
		// disk.
		mem := vfs.NewMem()
		if _, err := runBuildPipeline(ctx, reg, mem, buildPipelineOpts{}, nil); err != nil {
			return nil, fmt.Errorf("building registry: %w", err)
		}
		kindReg, err = registry.FromStore(&registry.FSStore{FS: mem})
		if err != nil {
			return nil, err
		}
	} else {
		registries, err := resolveRegistries(c.StringSlice("registry"), reg)
		if err != nil {
			return nil, err
		}
		kindReg, err = registry.Load(registries)
		if err != nil {
			return nil, err
		}
	}

	projectFS := os.DirFS(reg.Root)
	handles, err := resource.Discover(ctx, projectFS, reg.ResourceDiscovery.GetPaths())
	if err != nil {
		return nil, fmt.Errorf("discovering resources: %w", err)
	}
	catalog, err := resource.NewCatalog(projectFS, handles)
	if err != nil {
		return nil, fmt.Errorf("building resource catalog: %w", err)
	}

	outDir, err := filepath.Abs(c.String("out"))
	if err != nil {
		return nil, fmt.Errorf("resolving --out: %w", err)
	}

	// In pretty mode this styles a line to the terminal; in JSON mode the
	// printer routes it through slog so it lands as a structured log entry.
	p.Info("Rendering kubernetes files.")

	resp := &renderResponse{Rendered: []renderedResource{}}
	var errs []error

	jobsN := c.Int("jobs")
	if jobsN < 1 {
		jobsN = 1
	}

	// Bounded worker pool. Each hook runtime mounts the project root as its
	// own filesystem root, so no process-global state is shared and renders
	// run concurrently. Results land in a fixed-index slice so the response
	// order is deterministic regardless of completion order.
	results := make([]renderedResource, len(pathArgs))
	jobErrs := make([]error, len(pathArgs))
	sem := make(chan struct{}, jobsN)
	var wg sync.WaitGroup
	for i, pathArg := range pathArgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, pathArg string) {
			defer wg.Done()
			defer func() { <-sem }()
			relFS, err := resolveProjectRel(reg.Root, pathArg)
			if err != nil {
				jobErrs[i] = fmt.Errorf("%s: %w", pathArg, err)
				return
			}
			rr, err := renderEntry(reg, kindReg, catalog, projectFS, vars, outDir, relFS)
			if err != nil {
				jobErrs[i] = fmt.Errorf("%s: %w", pathArg, err)
				return
			}
			results[i] = rr
		}(i, pathArg)
	}
	wg.Wait()

	for i := range pathArgs {
		if jobErrs[i] != nil {
			errs = append(errs, jobErrs[i])
			continue
		}
		resp.Rendered = append(resp.Rendered, results[i])
	}
	if len(errs) > 0 {
		return resp, errors.Join(errs...)
	}
	return resp, nil
}

// resolveProjectRel turns a user-supplied path (relative to the current
// working directory) into a project-root-relative catalog key. Must run
// before the render pool chdir's, since it depends on the original cwd.
func resolveProjectRel(root, pathArg string) (string, error) {
	absPath, err := filepath.Abs(pathArg)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no file at %s (resolved from %q against working directory)", absPath, pathArg)
		}
		return "", fmt.Errorf("checking %s: %w", absPath, err)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", fmt.Errorf("resolving against project root: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("is outside the project root %s", root)
	}
	return filepath.ToSlash(rel), nil
}

// renderEntry loads a resource by its project-relative key and renders it
// through the shared registry + catalog. Safe to call concurrently: each
// hook runtime mounts Root as its filesystem root, so no process-global
// working directory is involved. Returns the rendered-resource descriptor
// for the JSON response.
func renderEntry(reg *config.Registry, kindReg registry.Registry, catalog resource.Catalog, projectFS fs.FS, vars map[string]any, outDir, relFS string) (renderedResource, error) {
	entry, err := catalog.LoadByPath(relFS)
	if err != nil {
		return renderedResource{}, err
	}

	rendered, err := render.Render(&render.Options{
		Kind:      entry.GetMetadata().GetKind(),
		Name:      entry.GetMetadata().GetName(),
		OutDir:    outDir,
		Root:      reg.Root,
		FS:        projectFS,
		Registry:  kindReg,
		Catalog:   catalog,
		Variables: vars,
	})
	if err != nil {
		return renderedResource{}, err
	}

	return renderedResource{
		Kind:   entry.GetMetadata().GetKind(),
		Name:   rendered.Name,
		Path:   relFS,
		OutDir: rendered.OutDir,
		Files:  rendered.Files,
	}, nil
}

// resolveRegistries returns the alias→path sources to load, honoring
// precedence: --registry > VEIL_REGISTRY env > veil.json registries.
// Paths from --registry and env always land under the default alias
// (`""`), since CLI flags don't carry alias names. veil.json registries
// keep their declared aliases; paths there resolve against the
// veil.json directory, while CLI/env paths resolve against cwd. There
// is no implicit fallback — registries must be declared somewhere
// (typically veil.json), or rendering fails.
func resolveRegistries(cliRegs []string, reg *config.Registry) ([]registry.Reference, error) {
	if len(cliRegs) > 0 {
		return absSources(defaultAliasSources(cliRegs), "")
	}
	if env := os.Getenv("VEIL_REGISTRY"); env != "" {
		var parts []string
		for _, p := range strings.Split(env, string(os.PathListSeparator)) {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			return absSources(defaultAliasSources(parts), "")
		}
	}
	sources := make([]registry.Reference, 0, len(reg.Registries))
	aliases := make([]string, 0, len(reg.Registries))
	for alias := range reg.Registries {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		sources = append(sources, registry.Reference{Alias: alias, Path: reg.Registries[alias]})
	}
	return absSources(sources, reg.Root)
}

// defaultAliasSources wraps each path with the empty default-alias key.
// Used for CLI/env entries, which don't carry alias names.
func defaultAliasSources(paths []string) []registry.Reference {
	out := make([]registry.Reference, 0, len(paths))
	for _, p := range paths {
		out = append(out, registry.Reference{Alias: "", Path: p})
	}
	return out
}

// absSources resolves each source's Path relative to baseDir. When
// baseDir is empty, paths are resolved against cwd.
func absSources(sources []registry.Reference, baseDir string) ([]registry.Reference, error) {
	out := make([]registry.Reference, 0, len(sources))
	for _, s := range sources {
		if filepath.IsAbs(s.Path) {
			out = append(out, registry.Reference{Alias: s.Alias, Path: filepath.Clean(s.Path)})
			continue
		}
		base := baseDir
		if base == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			base = cwd
		}
		out = append(out, registry.Reference{Alias: s.Alias, Path: filepath.Clean(filepath.Join(base, s.Path))})
	}
	return out, nil
}
