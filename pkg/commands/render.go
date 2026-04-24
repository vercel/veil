package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/logging"
	"github.com/vercel/veil/pkg/render"
	"github.com/vercel/veil/pkg/variables"
)

// Render returns the "render" subcommand.
func Render() *cli.Command {
	configDefault := "veil.json"
	if cwd, err := os.Getwd(); err == nil {
		if reg, err := config.Discover(cwd); err == nil {
			configDefault = filepath.Join(reg.Root, "veil.json")
		}
	}

	return &cli.Command{
		Name:      "render",
		Usage:     "Render deployment configuration",
		UsageText: "veil render <path> [flags]",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "path",
				UsageText: "Path to a resource file (*.json) or directory of resources",
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
			&cli.StringSliceFlag{
				Name:    "var",
				Aliases: []string{"variable"},
				Usage:   "Variable binding in name=value form (repeatable)",
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "Dump all logs (including hook console.log output) to stdout at debug level",
			},
		},
		Action: runRender,
	}
}

func runRender(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	pathArg := c.StringArg("path")
	if pathArg == "" {
		return fmt.Errorf("render: path is required (pass a resource file or directory)")
	}

	// --debug is a convenience: reconfigure slog to dump everything (including
	// hook console.log output) to stdout at debug level. Overrides whatever
	// the root Before handler set up.
	if c.Bool("debug") {
		if _, err := logging.Setup(slog.LevelDebug, []string{"stdout"}, c.Root().Writer, c.Root().ErrWriter); err != nil {
			return fmt.Errorf("configuring --debug logging: %w", err)
		}
	}

	reg, err := loadRegistry(c.String("config"))
	if err != nil {
		return err
	}

	configPath := filepath.Join(reg.Root, "veil.json")
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, configPath); err == nil && !strings.HasPrefix(rel, "..") {
			configPath = rel
		}
	}
	p.Infof("Using %s", configPath)

	vars, err := variables.Resolve(reg.Variables, c.StringSlice("var"), os.LookupEnv)
	if err != nil {
		return err
	}

	registries, err := resolveRegistries(c.StringSlice("registry"), reg)
	if err != nil {
		return err
	}
	kinds, err := loadCompiledRegistries(registries)
	if err != nil {
		return err
	}

	path, err := filepath.Abs(pathArg)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	outDir, err := filepath.Abs(c.String("out"))
	if err != nil {
		return fmt.Errorf("resolving --out: %w", err)
	}

	result, err := render.Render(render.Options{
		Dir:           path,
		OutDir:        outDir,
		Root:          reg.Root,
		RegistryKinds: kinds,
		Variables:     vars,
	})
	if err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	displayPath := func(path string) string {
		if cwd == "" {
			return path
		}
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return path
	}

	if len(result.Rendered) == 0 {
		p.Warn("no resources found in " + displayPath(path))
		return nil
	}
	for _, r := range result.Rendered {
		p.Successf("Rendered %s", r.Name)
		p.KeyValue("out", displayPath(r.OutDir))
		for _, f := range r.Files {
			p.Mutedf("  %s", f)
		}
	}
	return nil
}

// resolveRegistries returns absolute paths to every registry.json to load,
// honoring precedence: --registry > VEIL_REGISTRY env > veil.json registries
// field > implicit <.veil dir>/r/registry.json (when present). Paths from
// --registry and env are resolved against cwd; paths from veil.json are
// resolved against the veil.json's directory.
func resolveRegistries(cliRegs []string, reg *config.Registry) ([]string, error) {
	if len(cliRegs) > 0 {
		return absPaths(cliRegs, "")
	}
	if env := os.Getenv("VEIL_REGISTRY"); env != "" {
		var parts []string
		for _, p := range strings.Split(env, string(os.PathListSeparator)) {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			return absPaths(parts, "")
		}
	}
	if len(reg.Registries) > 0 {
		return absPaths(reg.Registries, reg.Root)
	}
	local := filepath.Join(reg.Root, config.PublicDir, "r", "registry.json")
	if _, err := os.Stat(local); err == nil {
		return []string{local}, nil
	}
	return nil, nil
}

// absPaths resolves each path relative to baseDir. When baseDir is empty,
// paths are resolved against cwd.
func absPaths(paths []string, baseDir string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
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
		out = append(out, filepath.Clean(filepath.Join(base, p)))
	}
	return out, nil
}

// loadCompiledRegistries reads every registry.json and returns a merged map
// from kind name to the absolute path of its compiled kind.json. Duplicate
// kind names across registries are rejected.
func loadCompiledRegistries(paths []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("loading registry %s: %w", p, err)
		}
		var r struct {
			Kinds map[string]struct {
				Path string `json:"path"`
			} `json:"kinds"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parsing registry %s: %w", p, err)
		}
		dir := filepath.Dir(p)
		for name, entry := range r.Kinds {
			if entry.Path == "" {
				return nil, fmt.Errorf("registry %s: kind %q is missing \"path\"", p, name)
			}
			resolved := entry.Path
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(dir, resolved)
			}
			resolved = filepath.Clean(resolved)
			if existing, ok := out[name]; ok {
				return nil, fmt.Errorf("kind %q provided by multiple registries: %s and %s", name, existing, resolved)
			}
			out[name] = resolved
		}
	}
	return out, nil
}
