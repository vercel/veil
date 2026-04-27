package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/logging"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/render"
	"github.com/vercel/veil/pkg/resource"
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
		Usage:     "Render a single resource",
		UsageText: "veil render <path> [flags]",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "path",
				UsageText: "Path to the resource JSON file to render",
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
		},
		Action: runRender,
	}
}

func runRender(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	pathArg := c.StringArg("path")
	if pathArg == "" {
		return fmt.Errorf("render: path is required (pass a resource file)")
	}

	// --debug is a convenience: reconfigure slog to dump everything (including
	// hook console.log output) to stdout at debug level. Overrides whatever
	// the root Before handler set up.
	if c.Bool("debug") {
		if _, err := logging.Setup(slog.LevelDebug, []string{"stdout"}, c.Root().Writer, c.Root().ErrWriter); err != nil {
			return fmt.Errorf("configuring --debug logging: %w", err)
		}
	}

	reg, err := registry.LoadProject(c.String("config"))
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

	vars, err := variables.Resolve(reg.Variables, c.StringMap("var"), os.LookupEnv)
	if err != nil {
		return err
	}

	registries, err := resolveRegistries(c.StringSlice("registry"), reg)
	if err != nil {
		return err
	}
	kindReg, err := registry.Load(registries)
	if err != nil {
		return err
	}

	projectFS := os.DirFS(reg.Root)
	handles, err := resource.Discover(ctx, projectFS, reg.ResourceDiscovery.GetPaths())
	if err != nil {
		return fmt.Errorf("discovering resources: %w", err)
	}
	catalog, err := resource.NewCatalog(projectFS, handles)
	if err != nil {
		return fmt.Errorf("building resource catalog: %w", err)
	}

	absPath, err := filepath.Abs(pathArg)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	rel, err := filepath.Rel(reg.Root, absPath)
	if err != nil {
		return fmt.Errorf("resolving %s against project root: %w", pathArg, err)
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is outside the project root %s", pathArg, reg.Root)
	}
	relFS := filepath.ToSlash(rel)

	outDir, err := filepath.Abs(c.String("out"))
	if err != nil {
		return fmt.Errorf("resolving --out: %w", err)
	}

	entry, err := catalog.LoadByPath(relFS)
	if err != nil {
		return err
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

	p.Successf("Rendered %s", rendered.Name)
	p.KeyValue("out", displayPath(rendered.OutDir))
	for _, f := range rendered.Files {
		p.Mutedf("  %s", f)
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
