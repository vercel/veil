package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/build"
	"github.com/vercel/veil/pkg/bundle"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/embeds"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/protoencode"
	"github.com/vercel/veil/pkg/tsc"
)

// Build returns the "build" command — compiles every kind into
// <out>/<name>/kind.json (sources + minified hooks) and emits the composite
// resource JSON schema at <out>/<name>/kind.schema.json, plus a top-level
// <out>/registry.json indexing them.
func Build() *cli.Command {
	configDefault := "veil.json"
	outDefault := filepath.Join(config.PublicDir, "r")
	if cwd, err := os.Getwd(); err == nil {
		if reg, err := config.Discover(cwd); err == nil {
			configDefault = filepath.Join(reg.Root, "veil.json")
			outDefault = filepath.Join(reg.Root, config.PublicDir, "r")
		}
	}

	return &cli.Command{
		Name:      "build",
		Usage:     "Compile every kind into a self-contained JSON document and its composite schema",
		UsageText: "veil build [--config <path>] [--out <dir>] [--no-typecheck]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "Path to veil.json",
				Value: configDefault,
			},
			&cli.StringFlag{
				Name:  "out",
				Usage: "Output directory for compiled kinds and schemas",
				Value: outDefault,
			},
			&cli.BoolFlag{
				Name:  "no-typecheck",
				Usage: "Skip running tsc --noEmit on each kind's hooks",
			},
		},
		Action: runBuild,
	}
}

func runBuild(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	reg, err := config.Load(c.String("config"))
	if err != nil {
		return err
	}
	slog.Debug("loaded registry", "root", reg.Root, "kinds", len(reg.Kinds))

	configPath := filepath.Join(reg.Root, "veil.json")
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, configPath); err == nil && !strings.HasPrefix(rel, "..") {
			configPath = rel
		}
	}
	p.Infof("Using %s", configPath)

	return runBuildPipeline(reg, c.String("out"), !c.Bool("no-typecheck"), p)
}

// runBuildPipeline compiles every kind into <outDir>/<name>/kind.json and
// writes its composite JSON schema to <outDir>/<name>/kind.schema.json,
// plus an index at <outDir>/registry.json. Called by `veil build` and by
// `veil new kind|hook` so scaffolding leaves a buildable state. When
// typecheck is true, each kind's hooks are type-checked via `tsgo` or
// `tsc` if either is on PATH.
func runBuildPipeline(reg *config.Registry, outDir string, typecheck bool, p interact.Printer) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	var metadataSchema map[string]any
	if err := json.Unmarshal(embeds.MetadataSchema, &metadataSchema); err != nil {
		return fmt.Errorf("parsing embedded metadata schema: %w", err)
	}
	delete(metadataSchema, "$schema")
	delete(metadataSchema, "title")

	// Bundle entrypoints are relative to the project root (= reg.Root).
	fsys := os.DirFS(reg.Root)

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

	var checker tsc.Checker
	if typecheck {
		checker = tsc.Find()
		if checker == nil && p != nil {
			p.Warn("no TypeScript compiler on PATH — skipping type check. Install `tsgo` or `tsc` to enable it.")
		}
	}

	registry := &veilv1.Registry{Kinds: make(map[string]*veilv1.RegistryEntry, len(reg.Kinds))}

	var errs []error
	for _, k := range reg.Kinds {
		if err := validateKind(k); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		kindDir := filepath.Join(outDir, k.Name)
		if err := os.MkdirAll(kindDir, 0755); err != nil {
			errs = append(errs, fmt.Errorf("%s: creating output dir: %w", k.Name, err))
			continue
		}

		schemaPath := filepath.Join(kindDir, "kind.schema.json")
		if err := build.ResourceSchema(k, metadataSchema, schemaPath); err != nil {
			errs = append(errs, fmt.Errorf("%s: generating schema: %w", k.Name, err))
			continue
		}

		// Regenerate types before bundling so hook imports resolve against
		// the freshest schema — stale references surface as bundle errors
		// in the step below.
		hookSrcDir := filepath.Join(k.Dir, "hooks", "src")
		typesPath := filepath.Join(hookSrcDir, "veil-types.ts")
		if err := writeKindTypes(k, reg.Variables); err != nil {
			errs = append(errs, fmt.Errorf("%s: writing types: %w", k.Name, err))
			continue
		}

		if checker != nil {
			if err := checker.Check(hookSrcDir); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
				continue
			}
		}

		ck, err := compileKind(k, reg.Variables, reg.Root, fsys)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		jsonPath := filepath.Join(kindDir, "kind.json")
		if err := protoencode.WriteFile(jsonPath, ck); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		registry.Kinds[k.Name] = &veilv1.RegistryEntry{
			Name:   k.Name,
			Path:   "./" + filepath.ToSlash(filepath.Join(k.Name, "kind.json")),
			Schema: "./" + filepath.ToSlash(filepath.Join(k.Name, "kind.schema.json")),
		}

		if p != nil {
			p.Successf("Built %s", k.Name)
			p.KeyValue("compiled", displayPath(jsonPath))
			p.KeyValue("schema", displayPath(schemaPath))
			p.KeyValue("types", displayPath(typesPath))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	registryPath := filepath.Join(outDir, "registry.json")
	if err := protoencode.WriteFile(registryPath, registry); err != nil {
		return fmt.Errorf("writing registry: %w", err)
	}
	if p != nil {
		p.Successf("Built registry")
		p.KeyValue("registry", displayPath(registryPath))
	}
	return nil
}

// compileKind reads a kind's sources, bundles+minifies each render hook,
// bundles each per-consumer dependent hook, and inlines per-consumer params
// schemas. `variables` is the project-level variable declaration from
// veil.json, copied verbatim so the compiled document is self-contained at
// render time.
func compileKind(k config.Kind, variables map[string]*veilv1.Variable, projectRoot string, fsys fs.FS) (*veilv1.Kind, error) {
	sources := make(map[string]string, len(k.Sources))
	for _, src := range k.Sources {
		abs := src
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(k.Dir, src)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("reading source %s: %w", src, err)
		}
		key, err := filepath.Rel(k.Dir, abs)
		if err != nil {
			return nil, fmt.Errorf("resolving source key for %s: %w", src, err)
		}
		sources[filepath.ToSlash(key)] = string(data)
	}

	render, err := compileHookList(k, projectRoot, fsys, k.GetHooks().GetRender())
	if err != nil {
		return nil, err
	}

	dependents, err := compileDependents(k, projectRoot, fsys)
	if err != nil {
		return nil, err
	}

	return &veilv1.Kind{
		Name:       k.Name,
		Sources:    sources,
		Hooks:      &veilv1.Hooks{Render: render},
		Variables:  variables,
		Dependents: dependents,
	}, nil
}

// compileHookList bundles+minifies every hook path in paths, resolving
// each entrypoint relative to the kind's project root.
func compileHookList(k config.Kind, projectRoot string, fsys fs.FS, paths []string) ([]*veilv1.Hook, error) {
	hooks := make([]*veilv1.Hook, 0, len(paths))
	for _, h := range paths {
		abs := h
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(k.Dir, h)
		}
		entrypoint, err := filepath.Rel(projectRoot, abs)
		if err != nil {
			return nil, fmt.Errorf("resolving hook entrypoint for %s: %w", h, err)
		}
		code, err := bundle.Bundle(filepath.ToSlash(entrypoint), fsys, bundle.Options{
			Minify:     true,
			GlobalName: "__veilMod",
		})
		if err != nil {
			return nil, fmt.Errorf("bundling %s: %w", h, err)
		}
		name, err := filepath.Rel(k.Dir, abs)
		if err != nil {
			return nil, fmt.Errorf("resolving hook name for %s: %w", h, err)
		}
		hooks = append(hooks, &veilv1.Hook{
			Name:    filepath.ToSlash(name),
			Content: code,
		})
	}
	return hooks, nil
}

// compileDependents bundles each per-consumer dependent entry's hooks and
// inlines the params JSON Schema referenced by params_path. Returns nil
// when the kind declares no dependents.
func compileDependents(k config.Kind, projectRoot string, fsys fs.FS) ([]*veilv1.Dependent, error) {
	if len(k.Dependents) == 0 {
		return nil, nil
	}
	out := make([]*veilv1.Dependent, 0, len(k.Dependents))
	for _, d := range k.Dependents {
		hooks, err := compileHookList(k, projectRoot, fsys, d.Hooks)
		if err != nil {
			return nil, fmt.Errorf("dependents[%q]: %w", d.Kind, err)
		}
		paramsAbs := d.ParamsPath
		if !filepath.IsAbs(paramsAbs) {
			paramsAbs = filepath.Join(k.Dir, d.ParamsPath)
		}
		paramsRaw, err := os.ReadFile(paramsAbs)
		if err != nil {
			return nil, fmt.Errorf("dependents[%q]: reading params_path %s: %w", d.Kind, d.ParamsPath, err)
		}
		// Validate it parses as JSON; embed verbatim text to preserve formatting.
		var probe map[string]any
		if err := json.Unmarshal(paramsRaw, &probe); err != nil {
			return nil, fmt.Errorf("dependents[%q]: params_path %s is not valid JSON: %w", d.Kind, d.ParamsPath, err)
		}
		out = append(out, &veilv1.Dependent{
			Kind:         d.Kind,
			Hooks:        hooks,
			ParamsSchema: string(paramsRaw),
		})
	}
	return out, nil
}

// writeKindTypes emits veil-types.ts alongside the hook .ts files in
// hooks/src/ so `import … from './veil-types'` resolves naturally and
// the package.json sitting one level up at hooks/ stays separate from
// the source code.
func writeKindTypes(k config.Kind, variables map[string]*veilv1.Variable) error {
	ts, err := build.VeilTypes(k, variables)
	if err != nil {
		return err
	}
	dir := filepath.Join(k.Dir, "hooks", "src")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "veil-types.ts"), []byte(ts), 0644)
}

// validateKind checks that a kind's referenced files exist and that its
// spec schema parses as JSON.
func validateKind(k config.Kind) error {
	var errs []error

	if k.Schema != "" {
		if _, err := build.LoadSpecSchema(k); err != nil {
			errs = append(errs, err)
		}
	}

	check := func(label string, paths []string) {
		for _, p := range paths {
			abs := p
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(k.Dir, p)
			}
			if _, err := os.Stat(abs); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", label, p, err))
			}
		}
	}
	check("source", k.Sources)
	check("render hook", k.GetHooks().GetRender())

	for _, d := range k.Dependents {
		check(fmt.Sprintf("dependent[%q] hook", d.Kind), d.Hooks)
		check(fmt.Sprintf("dependent[%q] params_path", d.Kind), []string{d.ParamsPath})
	}

	return errors.Join(errs...)
}
