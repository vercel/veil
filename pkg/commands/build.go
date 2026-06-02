package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
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
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/tsc"
	"github.com/vercel/veil/pkg/vfs"
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
			configDefault = reg.ConfigPath
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
		Action: withResult(runBuild),
	}
}

// builtKind describes one compiled kind in a buildResponse.
type builtKind struct {
	Name     string `json:"name"`
	Compiled string `json:"compiled"`
	Schema   string `json:"schema"`
	Types    string `json:"types"`
}

// buildResponse is the JSON payload for `veil build`.
type buildResponse struct {
	Kinds    []builtKind `json:"kinds"`
	Registry string      `json:"registry"`
}

func runBuild(ctx context.Context, c *cli.Command) (*buildResponse, error) {
	p := interact.Default()

	reg, err := config.Load(c.String("config"))
	if err != nil {
		return nil, err
	}
	slog.Debug("loaded registry", "root", reg.Root, "kinds", len(reg.Kinds))

	configPath := reg.ConfigPath
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, configPath); err == nil && !strings.HasPrefix(rel, "..") {
			configPath = rel
		}
	}
	p.Infof("Using %s", configPath)

	return runBuildPipeline(reg, newDirSink(vfs.NewDir(c.String("out"))), buildPipelineOpts{
		typecheck:  !c.Bool("no-typecheck"),
		writeTypes: true,
	}, p)
}

// buildPipelineOpts tunes runBuildPipeline.
type buildPipelineOpts struct {
	// typecheck runs tsc/tsgo over each kind's hooks when a compiler is
	// on PATH.
	typecheck bool
	// writeTypes regenerates veil-types.ts in each kind's hooks/src (a
	// source-tree artifact). Off for in-memory builds (e.g. render
	// --build), which must not touch the working tree.
	writeTypes bool
}

// runBuildPipeline compiles every kind (sources + minified hooks +
// composite schema) and hands each one to sink. The disk sink serializes
// them to <name>/kind.json, <name>/kind.schema.json, and an index
// registry.json; the in-memory sink populates a registry.MemRegistry that
// the caller reads straight back. Called by `veil build`, `veil new
// kind|hook` (so scaffolding leaves a buildable state), and `veil render
// --build` (into memory).
func runBuildPipeline(reg *config.Registry, sink buildSink, opts buildPipelineOpts, p interact.Printer) (*buildResponse, error) {
	resp := &buildResponse{Kinds: []builtKind{}}

	var metadataSchema map[string]any
	if err := json.Unmarshal(embeds.MetadataSchema, &metadataSchema); err != nil {
		return nil, fmt.Errorf("parsing embedded metadata schema: %w", err)
	}
	delete(metadataSchema, "$schema")
	delete(metadataSchema, "title")

	// Bake the project's declared variable names into each overlay's
	// `if` properties so a typo in a variable reference fails schema
	// validation rather than silently never matching.
	build.ShapeOverlayIf(metadataSchema, reg.Variables)

	// Bundle entrypoints are relative to the project root (= reg.Root).
	fsys := os.DirFS(reg.Root)

	var checker tsc.Checker
	if opts.typecheck {
		checker = tsc.Find()
		if checker == nil && p != nil {
			p.Warn("no TypeScript compiler on PATH — skipping type check. Install `tsgo` or `tsc` to enable it.")
		}
	}

	graph, err := build.BuildGraph(reg.Kinds)
	if err != nil {
		return nil, fmt.Errorf("building kind graph: %w", err)
	}

	var errs []error
	for _, k := range reg.Kinds {
		if err := validateKind(k); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		schemaBytes, err := build.ResourceSchemaBytes(k, metadataSchema, graph)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: generating schema: %w", k.Name, err))
			continue
		}

		// Regenerate types before bundling so hook imports resolve against
		// the freshest schema. Skipped for in-memory builds, which must
		// not write into the source tree.
		typesPath := ""
		if opts.writeTypes {
			if err := writeKindTypes(k, reg.Variables, graph); err != nil {
				errs = append(errs, fmt.Errorf("%s: writing types: %w", k.Name, err))
				continue
			}
			typesPath = cwdRel(filepath.Join(k.Dir, "hooks", "src", "veil-types.ts"))
		}

		if checker != nil {
			if err := checker.Check(hookFiles(k)); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
				continue
			}
		}

		ck, err := compileKind(k, reg.Variables, reg.Root, fsys)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		compiledPath, schemaPath, err := sink.emit(k.Name, ck, schemaBytes)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.Name, err))
			continue
		}

		resp.Kinds = append(resp.Kinds, builtKind{
			Name:     k.Name,
			Compiled: compiledPath,
			Schema:   schemaPath,
			Types:    typesPath,
		})

		if p != nil {
			p.Successf("Built %s", k.Name)
			if compiledPath != "" {
				p.KeyValue("compiled", compiledPath)
			}
			if schemaPath != "" {
				p.KeyValue("schema", schemaPath)
			}
			if typesPath != "" {
				p.KeyValue("types", typesPath)
			}
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	registryPath, err := sink.finish()
	if err != nil {
		return nil, fmt.Errorf("writing registry: %w", err)
	}
	resp.Registry = registryPath
	if p != nil {
		p.Successf("Built registry")
		if registryPath != "" {
			p.KeyValue("registry", registryPath)
		}
	}
	return resp, nil
}

// buildSink consumes the build pipeline's compiled output: emit is called
// once per compiled kind, finish once after all kinds succeed. It's the
// one seam where on-disk and in-memory builds diverge — everything before
// it (compile, typecheck, schema generation) is shared.
type buildSink interface {
	// emit records one compiled kind, returning the display strings for
	// the kind body and schema (empty for an in-memory sink) used in the
	// build response and printer output.
	emit(name string, kind *veilv1.Kind, schema []byte) (compiledPath, schemaPath string, err error)
	// finish finalizes the registry index, returning its display string.
	finish() (registryPath string, err error)
}

// dirSink is the on-disk buildSink: it serializes each compiled kind to
// <name>/kind.json + <name>/kind.schema.json under dst and accumulates an
// index that finish writes as registry.json. Used by `veil build` and
// `veil new kind|hook`.
type dirSink struct {
	dst   *vfs.Dir
	index *veilv1.Registry
}

func newDirSink(dst *vfs.Dir) *dirSink {
	return &dirSink{dst: dst, index: &veilv1.Registry{Kinds: map[string]*veilv1.RegistryEntry{}}}
}

// display renders a registry-relative output path as the cwd-relative
// on-disk location shown to the user.
func (d *dirSink) display(rel string) string {
	return cwdRel(filepath.Join(d.dst.Root(), filepath.FromSlash(rel)))
}

func (d *dirSink) emit(name string, kind *veilv1.Kind, schema []byte) (string, string, error) {
	schemaRel := path.Join(name, "kind.schema.json")
	if err := d.dst.WriteFile(schemaRel, schema); err != nil {
		return "", "", err
	}
	kindBytes, err := protoencode.MarshalFile(kind, embeds.KindSchemaURL)
	if err != nil {
		return "", "", err
	}
	jsonRel := path.Join(name, "kind.json")
	if err := d.dst.WriteFile(jsonRel, kindBytes); err != nil {
		return "", "", err
	}
	d.index.Kinds[name] = &veilv1.RegistryEntry{
		Name:   name,
		Path:   "./" + jsonRel,
		Schema: "./" + schemaRel,
	}
	return d.display(jsonRel), d.display(schemaRel), nil
}

func (d *dirSink) finish() (string, error) {
	regBytes, err := protoencode.MarshalFile(d.index, embeds.RegistrySchemaURL)
	if err != nil {
		return "", err
	}
	if err := d.dst.WriteFile("registry.json", regBytes); err != nil {
		return "", err
	}
	return d.display("registry.json"), nil
}

// memSink is the in-memory buildSink: emit hands each compiled kind
// straight to a registry.MemRegistry (no marshaling, no files), which
// `veil render --build` then reads through the Registry interface.
type memSink struct {
	reg *registry.MemRegistry
}

func newMemSink() *memSink { return &memSink{reg: registry.NewMemRegistry()} }

func (m *memSink) emit(name string, kind *veilv1.Kind, schema []byte) (string, string, error) {
	m.reg.Add(name, &registry.LoadedKind{Kind: kind, Schema: schema})
	return "", "", nil
}

func (m *memSink) finish() (string, error) { return "", nil }

// cwdRel renders an absolute path relative to the working directory when
// that's a clean subpath, else returns it unchanged.
func cwdRel(abs string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return abs
	}
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}

// compileKind reads a kind's sources, bundles+minifies each render hook,
// bundles each per-consumer dependent hook, and inlines per-consumer params
// schemas. `variables` is the merged variable declaration set (veil.json
// plus every kind's kind.json), copied verbatim so the compiled document
// is self-contained at render time.
func compileKind(k *config.Kind, variables map[string]*veilv1.Variable, projectRoot string, fsys fs.FS) (*veilv1.Kind, error) {
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

	render, err := compileRenderHookDefs(k, projectRoot, fsys, k.RenderHooks())
	if err != nil {
		return nil, fmt.Errorf("render hooks: %w", err)
	}

	validate, err := compileRenderHookDefs(k, projectRoot, fsys, k.ValidateHooks())
	if err != nil {
		return nil, fmt.Errorf("validate hooks: %w", err)
	}

	postRender, err := compileRenderHookDefs(k, projectRoot, fsys, k.PostRenderHooks())
	if err != nil {
		return nil, fmt.Errorf("post_render hooks: %w", err)
	}

	dependents, err := compileDependents(k, projectRoot, fsys)
	if err != nil {
		return nil, err
	}

	return &veilv1.Kind{
		Name:    k.Name,
		Sources: sources,
		Hooks: &veilv1.Hooks{
			Render:     render,
			Dependents: dependents,
			Validate:   validate,
			PostRender: postRender,
		},
		Variables: variables,
	}, nil
}

// compileHookList bundles+minifies every hook path in paths, resolving
// each entrypoint relative to the kind's project root.
func compileHookList(k *config.Kind, projectRoot string, fsys fs.FS, paths []string) ([]*veilv1.Hook, error) {
	hooks := make([]*veilv1.Hook, 0, len(paths))
	for _, h := range paths {
		hk, err := compileHook(k, projectRoot, fsys, h)
		if err != nil {
			return nil, err
		}
		hooks = append(hooks, hk)
	}
	return hooks, nil
}

// compileRenderHookDefs compiles a slice of typed RenderHookDefinitions,
// bundling each path and stamping the entry's declared `access` onto
// the resulting compiled Hook so the runner can pre-flight required
// env vars at render time.
func compileRenderHookDefs(k *config.Kind, projectRoot string, fsys fs.FS, defs []*veilv1.RenderHookDefinition) ([]*veilv1.Hook, error) {
	hooks := make([]*veilv1.Hook, 0, len(defs))
	for _, d := range defs {
		hk, err := compileHook(k, projectRoot, fsys, d.GetPath())
		if err != nil {
			return nil, err
		}
		hk.Access = d.GetAccess()
		hooks = append(hooks, hk)
	}
	return hooks, nil
}

// compileHook bundles a single hook source file and returns it as a
// compiled Hook (without any access info — callers attach that).
func compileHook(k *config.Kind, projectRoot string, fsys fs.FS, h string) (*veilv1.Hook, error) {
	abs := h
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(k.Dir, h)
	}
	entrypoint, err := filepath.Rel(projectRoot, abs)
	if err != nil {
		return nil, fmt.Errorf("resolving hook entrypoint for %s: %w", h, err)
	}
	code, err := bundle.Bundle(filepath.ToSlash(entrypoint), fsys, &bundle.Options{
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
	return &veilv1.Hook{
		Name:    filepath.ToSlash(name),
		Content: code,
	}, nil
}

// compileDependents bundles each per-consumer dependent entry's hooks and
// inlines the params JSON Schema referenced by params_path. Returns nil
// when the kind declares no dependents.
func compileDependents(k *config.Kind, projectRoot string, fsys fs.FS) ([]*veilv1.DependentHook, error) {
	dependents := k.GetHooks().GetDependents()
	if len(dependents) == 0 {
		return nil, nil
	}
	out := make([]*veilv1.DependentHook, 0, len(dependents))
	for _, d := range dependents {
		hooks, err := compileHookList(k, projectRoot, fsys, d.Paths)
		if err != nil {
			return nil, fmt.Errorf("dependents[%q]: %w", d.Kind, err)
		}
		paramsAbs := d.ParamsPath
		if !filepath.IsAbs(paramsAbs) {
			paramsAbs = filepath.Join(k.Dir, d.ParamsPath)
		}
		// Source may be authored in JSON or YAML; the compiled
		// kind.json always embeds JSON-encoded params so downstream
		// consumers (render, hook bundler) don't need a YAML parser
		// to interpret it.
		var probe map[string]any
		if err := protoencode.ReadFile(paramsAbs, &probe); err != nil {
			return nil, fmt.Errorf("dependents[%q]: reading params_path %s: %w", d.Kind, d.ParamsPath, err)
		}
		paramsJSON, err := json.Marshal(probe)
		if err != nil {
			return nil, fmt.Errorf("dependents[%q]: encoding params_path %s as JSON: %w", d.Kind, d.ParamsPath, err)
		}
		out = append(out, &veilv1.DependentHook{
			Kind:         d.Kind,
			Hooks:        hooks,
			ParamsSchema: string(paramsJSON),
		})
	}
	return out, nil
}

// writeKindTypes emits veil-types.ts alongside the hook .ts files in
// hooks/src/ so `import … from './veil-types'` resolves naturally and
// the package.json sitting one level up at hooks/ stays separate from
// the source code.
func writeKindTypes(k *config.Kind, variables map[string]*veilv1.Variable, graph *build.KindGraph) error {
	ts, err := build.VeilTypes(k, variables, graph)
	if err != nil {
		return err
	}
	dir := filepath.Join(k.Dir, "hooks", "src")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "veil-types.ts"), []byte(ts), 0644)
}

// hookFiles returns the absolute paths of every hook .ts file declared
// in a kind — render hooks plus dependent hooks. Used to scope tsc to
// exactly the files veil ships, so it never tries to type-check unrelated
// files in a surrounding monorepo.
func hookFiles(k *config.Kind) []string {
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(k.Dir, p)
	}
	var files []string
	for _, d := range k.RenderHooks() {
		files = append(files, abs(d.GetPath()))
	}
	for _, d := range k.ValidateHooks() {
		files = append(files, abs(d.GetPath()))
	}
	for _, d := range k.PostRenderHooks() {
		files = append(files, abs(d.GetPath()))
	}
	for _, d := range k.GetHooks().GetDependents() {
		for _, p := range d.GetPaths() {
			files = append(files, abs(p))
		}
	}
	return files
}

// validateKind checks that a kind's referenced files exist and that its
// spec schema parses as JSON.
func validateKind(k *config.Kind) error {
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
	for _, d := range k.RenderHooks() {
		check("render hook", []string{d.GetPath()})
	}
	for _, d := range k.ValidateHooks() {
		check("validate hook", []string{d.GetPath()})
	}
	for _, d := range k.PostRenderHooks() {
		check("post_render hook", []string{d.GetPath()})
	}

	for _, d := range k.GetHooks().GetDependents() {
		check(fmt.Sprintf("dependent[%q] path", d.Kind), d.Paths)
		check(fmt.Sprintf("dependent[%q] params_path", d.Kind), []string{d.ParamsPath})
	}

	return errors.Join(errs...)
}
