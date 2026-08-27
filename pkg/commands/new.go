package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/build"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/embeds"
	"github.com/vercel/veil/pkg/fsutil"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/protoencode"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/vfs"
)

var nameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// newResponse is the JSON payload for the `veil new` scaffolding
// commands: what was created and where.
type newResponse struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

// New returns the "new" command group for scaffolding kinds, hooks, and
// resources.
func New() *cli.Command {
	return &cli.Command{
		Name:  "new",
		Usage: "Scaffold new kinds, hooks, or resources",
		Commands: []*cli.Command{
			newKind(),
			newHook(),
			newResource(),
		},
	}
}

func newKind() *cli.Command {
	return &cli.Command{
		Name:      "kind",
		Usage:     "Scaffold a new kind",
		UsageText: "veil new kind <name>",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "name",
				UsageText: "Name of the kind (lowercase, hyphens allowed)",
			},
		},
		Action: withResult(runNewKind),
	}
}

func newHook() *cli.Command {
	return &cli.Command{
		Name:  "hook",
		Usage: "Scaffold a new hook for a kind or a resource",
		UsageText: `veil new hook <name> --kind <kind>
   veil new hook <name> --resource <path-to-resource-yaml>
   veil new hook <name>     # auto-detects kind / resource from cwd`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "kind",
				Usage: "Name of the kind this hook belongs to (mutually exclusive with --resource)",
			},
			&cli.StringFlag{
				Name:  "resource",
				Usage: "Path to the resource yaml this hook belongs to (mutually exclusive with --kind)",
			},
			&cli.StringFlag{
				Name:  "source",
				Usage: "Bind this hook to one of the kind's declared sources (typed hook, --kind only): render(ctx, obj) instead of render(ctx, fs)",
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "name",
				UsageText: "Name of the hook (lowercase, hyphens allowed)",
			},
		},
		Action: withResult(runNewHook),
	}
}

func newResource() *cli.Command {
	return &cli.Command{
		Name:      "resource",
		Usage:     "Scaffold a new resource of a given kind",
		UsageText: "veil new resource <name> --kind <kind> [--out <path>]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "kind",
				Usage:    "Kind reference (`<kind>` for the default registry or `<alias>/<kind>` for a named one)",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "out",
				Usage: "Path to write the resource JSON file (default: ./<name>.json)",
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "name",
				UsageText: "Name of the resource (lowercase, hyphens allowed)",
			},
		},
		Action: withResult(runNewResource),
	}
}

func runNewKind(ctx context.Context, c *cli.Command) (*newResponse, error) {
	p := interact.Default()

	name := c.StringArg("name")
	if err := validateName("kind name", name); err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	var rb rollback
	defer rb.run()

	initialized, err := ensureVeilJSON(cwd)
	if err != nil {
		return nil, err
	}
	if initialized {
		rb.removeFile(filepath.Join(cwd, "veil.json"))
		p.Successf("Initialized %s", filepath.Join(cwd, "veil.json"))
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return nil, err
	}

	for _, k := range reg.Kinds {
		if k.Name == name {
			return nil, fmt.Errorf("kind %q already exists", name)
		}
	}

	kindDir := filepath.Join(reg.KindsDir(), name)
	if _, err := os.Stat(kindDir); err == nil {
		return nil, fmt.Errorf("directory %s already exists", kindDir)
	}

	// When a shared types package is configured, scaffold the kind in package
	// mode: its hook imports the package (and `veil build` emits the kind's
	// module) rather than a local veil-types.ts. typesPkgName is the
	// repo-owned package name; importName ("" in inline mode) drives how the
	// kind is registered in veil.json.
	typesImport := "./veil-types"
	typesPkgName := ""
	importName := ""
	importValue := ""
	if typesDir := reg.TypesOutputDir(); typesDir != "" {
		typesPkgName, err = typesPackageName(typesDir)
		if err != nil {
			return nil, err
		}
		typesImport = typesPkgName + "/" + name
		importName = typesImport
		importValue = "workspace:*"
	}

	sourcesDir := filepath.Join(kindDir, "sources")
	hookSrcDir := filepath.Join(kindDir, "hooks", "src")
	if err := os.MkdirAll(sourcesDir, 0755); err != nil {
		return nil, fmt.Errorf("creating kind directory: %w", err)
	}
	if err := os.MkdirAll(hookSrcDir, 0755); err != nil {
		return nil, fmt.Errorf("creating hooks directory: %w", err)
	}
	rb.removeTree(kindDir)

	schema := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema",
		"type":        "object",
		"description": fmt.Sprintf("Schema for the %q resource spec", name),
		"properties":  map[string]any{},
		"required":    []string{},
	}
	if err := writeJSON(filepath.Join(kindDir, "schema.json"), schema); err != nil {
		return nil, err
	}

	sourceBlurb := fmt.Sprintf("This is a source file for %s.\n", name)
	if err := os.WriteFile(filepath.Join(sourcesDir, "source.txt"), []byte(sourceBlurb), 0644); err != nil {
		return nil, fmt.Errorf("writing source.txt: %w", err)
	}

	helloTS := build.HookTemplate("hello-world", typesImport)
	if err := os.WriteFile(filepath.Join(hookSrcDir, "hello-world.ts"), []byte(helloTS), 0644); err != nil {
		return nil, fmt.Errorf("writing hello-world.ts: %w", err)
	}

	// In package mode the hooks dir is a workspace package that depends on the
	// shared types package, so `veil build` can wire and resolve the import.
	if typesPkgName != "" {
		hooksName := name + "-hooks"
		if scope, _, ok := strings.Cut(typesPkgName, "/"); ok && strings.HasPrefix(typesPkgName, "@") {
			hooksName = scope + "/" + hooksName
		}
		hooksPkg := map[string]any{
			"name":            hooksName,
			"version":         "0.0.0",
			"private":         true,
			"devDependencies": map[string]any{typesPkgName: importValue},
		}
		if err := writeJSON(filepath.Join(kindDir, "hooks", "package.json"), hooksPkg); err != nil {
			return nil, err
		}
	}

	kindJSON := map[string]any{
		"$schema": embeds.KindDefinitionSchemaURL,
		"name":    name,
		"sources": []string{"./sources/source.txt"},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"path": "./hooks/src/hello-world.ts"},
			},
		},
		"schema": "./schema.json",
	}
	if err := writeJSON(filepath.Join(kindDir, "kind.json"), kindJSON); err != nil {
		return nil, err
	}

	configPath := reg.ConfigPath
	prevVeil, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	rel, err := filepath.Rel(reg.Root, filepath.Join(kindDir, "kind.json"))
	if err != nil {
		return nil, fmt.Errorf("computing relative kind path: %w", err)
	}
	relKind := "./" + filepath.ToSlash(rel)
	if err := registerKindInVeilJSON(configPath, relKind, importName, importValue); err != nil {
		return nil, err
	}
	rb.restoreFile(configPath, prevVeil)

	p.Successf("Scaffolded kind %q at %s", name, kindDir)

	reg, err = config.Discover(cwd)
	if err != nil {
		return nil, fmt.Errorf("re-discovering registry after scaffold: %w", err)
	}
	if _, err := runBuildPipeline(ctx, reg, vfs.NewDir(filepath.Join(reg.Root, config.PublicDir, "r")), buildPipelineOpts{typecheck: true, writeTypes: true}); err != nil {
		return nil, err
	}
	rb.commit()
	return &newResponse{Kind: name, Path: kindDir}, nil
}

func runNewHook(ctx context.Context, c *cli.Command) (*newResponse, error) {
	name := c.StringArg("name")
	if err := validateName("hook name", name); err != nil {
		return nil, err
	}

	kindName := c.String("kind")
	resourcePath := c.String("resource")
	source := c.String("source")
	if kindName != "" && resourcePath != "" {
		return nil, fmt.Errorf("--kind and --resource are mutually exclusive")
	}
	if source != "" && resourcePath != "" {
		return nil, fmt.Errorf("--source requires --kind (typed hooks bind to a kind's own declared sources, not a resource)")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	// Auto-detect when the user passed neither flag. Look for an
	// unambiguous parent in cwd: a single kind.{yaml,json} or a single
	// resource yaml with metadata.kind set. Multiple matches → ask the
	// user to specify; no matches → ask the user to specify.
	if kindName == "" && resourcePath == "" {
		detectedKind, detectedResource, err := detectHookParent(cwd)
		if err != nil {
			return nil, err
		}
		kindName = detectedKind
		resourcePath = detectedResource
	}

	if resourcePath != "" {
		return runNewHookOnResource(cwd, name, resourcePath)
	}
	return runNewHookOnKind(ctx, cwd, name, kindName, source)
}

// runNewHookOnKind is the original `veil new hook --kind X` path: write
// the hook .ts under <kindDir>/hooks/src/, append it to the kind file's
// hooks.render, and re-run the build pipeline. When source is non-empty
// it must match one of the kind's declared sources — the scaffolded
// hook is then a TypedRenderHook<T> bound to it (T is the generated
// interface for that source's schema, or `unknown` if it declared
// none) instead of the whole-bundle RenderHook.
func runNewHookOnKind(ctx context.Context, cwd, name, kindName, source string) (*newResponse, error) {
	p := interact.Default()
	reg, err := config.Discover(cwd)
	if err != nil {
		return nil, err
	}

	var k *config.Kind
	for _, candidate := range reg.Kinds {
		if candidate.Name == kindName {
			k = candidate
			break
		}
	}
	if k == nil {
		return nil, fmt.Errorf("kind %q not found in registry", kindName)
	}

	var typeName string
	if source != "" {
		found := false
		for _, p := range k.SourcePaths() {
			if p == source {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("source %q is not declared in kind %q's `sources`", source, kindName)
		}
		_, typeNames, err := build.SourceSchemaTypes(k)
		if err != nil {
			return nil, fmt.Errorf("resolving source types: %w", err)
		}
		typeName = typeNames[source]
	}

	outPath := filepath.Join(k.Dir, "hooks", "src", name+".ts")
	if _, err := os.Stat(outPath); err == nil {
		return nil, fmt.Errorf("hook %s already exists", outPath)
	}

	var rb rollback
	defer rb.run()

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return nil, fmt.Errorf("creating hooks directory: %w", err)
	}

	var ts string
	if source != "" {
		ts = build.TypedHookTemplate(name, hookTypesImport(k), typeName)
	} else {
		ts = build.HookTemplate(name, hookTypesImport(k))
	}
	if err := os.WriteFile(outPath, []byte(ts), 0644); err != nil {
		return nil, fmt.Errorf("writing hook: %w", err)
	}
	rb.removeFile(outPath)

	kindPath := k.Path
	prevKind, err := os.ReadFile(kindPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", kindPath, err)
	}
	relHook := "./" + filepath.ToSlash(filepath.Join("hooks", "src", name+".ts"))
	if err := appendHookToKind(kindPath, "render", relHook, source); err != nil {
		return nil, err
	}
	rb.restoreFile(kindPath, prevKind)

	p.Successf("Scaffolded hook %s", outPath)

	reg, err = config.Discover(cwd)
	if err != nil {
		return nil, fmt.Errorf("re-discovering registry after scaffold: %w", err)
	}
	if _, err := runBuildPipeline(ctx, reg, vfs.NewDir(filepath.Join(reg.Root, config.PublicDir, "r")), buildPipelineOpts{typecheck: true, writeTypes: true}); err != nil {
		return nil, err
	}
	rb.commit()
	return &newResponse{Kind: kindName, Name: name, Path: outPath}, nil
}

// runNewHookOnResource scaffolds a resource-local hook. The .ts lands
// alongside the resource (under <resourceDir>/hooks/<name>.ts so the
// dir matches the path the resource yaml declares) and the resource's
// metadata.hooks.render array gets the new entry. No build re-run —
// resource hooks compile on demand at render time.
//
// Resource hooks reuse the kind's generated TS surface (Spec, FS,
// RenderHook, Dependency, etc.) so the scaffolder writes a
// veil-types.ts next to the hook by calling the same build.VeilTypes
// generator the kind compiler uses. The generator output is keyed to
// the resource's kind, so authors get the same IDE completion they'd
// get inside the kind's own hooks/src/.
func runNewHookOnResource(cwd, name, resourcePath string) (*newResponse, error) {
	p := interact.Default()
	abs := resourcePath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, resourcePath)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("resource %s: %w", resourcePath, err)
	}
	if !looksLikeResourceFile(abs) {
		return nil, fmt.Errorf("%s does not look like a resource file (no metadata.kind)", resourcePath)
	}

	kindName, err := resourceKindName(abs)
	if err != nil {
		return nil, err
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return nil, err
	}
	var k *config.Kind
	for _, candidate := range reg.Kinds {
		if candidate.Name == kindName {
			k = candidate
			break
		}
	}
	if k == nil {
		return nil, fmt.Errorf("resource references kind %q but no such kind is registered in veil.json", kindName)
	}
	graph, err := build.BuildGraph(reg.Kinds)
	if err != nil {
		return nil, fmt.Errorf("building kind graph: %w", err)
	}

	resourceDir := filepath.Dir(abs)
	hooksDir := filepath.Join(resourceDir, "hooks")
	outPath := filepath.Join(hooksDir, name+".ts")
	if _, err := os.Stat(outPath); err == nil {
		return nil, fmt.Errorf("hook %s already exists", outPath)
	}

	var rb rollback
	defer rb.run()

	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return nil, fmt.Errorf("creating hooks directory: %w", err)
	}

	if k.Import != nil {
		// Package mode: the kind's types live in the shared types package, so
		// wire the resource's hooks package.json to depend on it instead of
		// writing a per-resource veil-types.ts. The module itself is emitted
		// by `veil build` on the kind path.
		pkg, _ := build.SplitImportSpecifier(k.Import.GetName())
		// Floor the search at the resource's own directory so the dependency
		// never lands in an unrelated ancestor manifest (a service's app
		// package.json or the monorepo root).
		pkgPath := nearestPackageJSON(hooksDir, resourceDir)
		var prevPkg []byte
		if pkgPath != "" {
			prevPkg, _ = os.ReadFile(pkgPath)
		}
		if err := ensureTypesDep(hooksDir, resourceDir, pkg, k.Import.GetValue()); err != nil {
			return nil, fmt.Errorf("wiring types dependency: %w", err)
		}
		if pkgPath != "" && prevPkg != nil {
			rb.restoreFile(pkgPath, prevPkg)
		}
	} else {
		// Inline mode: generate the veil-types.ts next to the hook so its
		// `import … from './veil-types'` resolves. It may already exist (from
		// a prior `veil new hook --resource` on the same resource); overwrite
		// either way — the output is fully derived from the current kind
		// state. Snapshot prior bytes so a downstream failure restores them.
		types, err := build.VeilTypes(k, reg.Variables, graph, "")
		if err != nil {
			return nil, fmt.Errorf("generating veil-types.ts for resource hook: %w", err)
		}
		typesPath := filepath.Join(hooksDir, "veil-types.ts")
		typesExists := false
		var prevTypes []byte
		if data, err := os.ReadFile(typesPath); err == nil {
			typesExists = true
			prevTypes = data
		}
		if err := os.WriteFile(typesPath, []byte(types), 0644); err != nil {
			return nil, fmt.Errorf("writing veil-types.ts: %w", err)
		}
		if typesExists {
			rb.restoreFile(typesPath, prevTypes)
		} else {
			rb.removeFile(typesPath)
		}
	}

	ts := build.HookTemplate(name, hookTypesImport(k))
	if err := os.WriteFile(outPath, []byte(ts), 0644); err != nil {
		return nil, fmt.Errorf("writing hook: %w", err)
	}
	rb.removeFile(outPath)

	prev, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", abs, err)
	}
	relHook := "./" + filepath.ToSlash(filepath.Join("hooks", name+".ts"))
	if err := appendHookToResource(abs, "render", relHook); err != nil {
		return nil, err
	}
	rb.restoreFile(abs, prev)

	p.Successf("Scaffolded hook %s (registered on %s)", outPath, abs)
	rb.commit()
	return &newResponse{Name: name, Path: outPath}, nil
}

// resourceKindName reads metadata.kind out of a resource file. Used by
// the resource-hook scaffolder to figure out which kind's types to
// generate next to the hook.
func resourceKindName(path string) (string, error) {
	var raw map[string]any
	if err := protoencode.ReadFile(path, &raw); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	meta, _ := raw["metadata"].(map[string]any)
	if meta == nil {
		return "", fmt.Errorf("%s: missing metadata block", path)
	}
	kind, _ := meta["kind"].(string)
	if kind == "" {
		return "", fmt.Errorf("%s: metadata.kind is not set", path)
	}
	return kind, nil
}

// detectHookParent scans cwd looking for an unambiguous kind or
// resource file the new hook should attach to. Used when the user
// runs `veil new hook <name>` without explicit --kind / --resource.
//
// Resolution rules:
//
//   - If cwd contains exactly one kind.{json,yaml,yml} → kind mode for
//     that file (returns the kind name).
//   - Otherwise, if cwd contains exactly one resource yaml/json whose
//     metadata.kind is set → resource mode for that file (returns the
//     resource path).
//   - Anything ambiguous (multiple candidates, mixed kind + resource
//     files, or none) returns an actionable error.
func detectHookParent(cwd string) (kindName, resourcePath string, err error) {
	var kindFiles []string
	for _, candidate := range []string{"kind.json", "kind.yaml", "kind.yml"} {
		p := filepath.Join(cwd, candidate)
		if _, statErr := os.Stat(p); statErr == nil {
			kindFiles = append(kindFiles, p)
		}
	}
	if len(kindFiles) > 1 {
		return "", "", fmt.Errorf("multiple kind files in %s — pass --kind explicitly", cwd)
	}
	if len(kindFiles) == 1 {
		var raw map[string]any
		if err := protoencode.ReadFile(kindFiles[0], &raw); err != nil {
			return "", "", fmt.Errorf("reading %s: %w", kindFiles[0], err)
		}
		n, _ := raw["name"].(string)
		if n == "" {
			return "", "", fmt.Errorf("%s: missing name field — pass --kind explicitly", kindFiles[0])
		}
		return n, "", nil
	}

	resourceCandidates, err := findResourceFiles(cwd)
	if err != nil {
		return "", "", err
	}
	switch len(resourceCandidates) {
	case 0:
		return "", "", fmt.Errorf("no kind.{json,yaml,yml} or resource yaml found in %s — pass --kind or --resource", cwd)
	case 1:
		return "", resourceCandidates[0], nil
	default:
		return "", "", fmt.Errorf("multiple resource files in %s — pass --resource explicitly", cwd)
	}
}

// findResourceFiles enumerates *.json / *.yaml / *.yml entries
// directly inside dir and returns the subset whose top-level
// metadata.kind is a non-empty string. Files that don't parse or
// don't look like resources are silently skipped — detect is best
// effort.
func findResourceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var hits []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if looksLikeResourceFile(p) {
			hits = append(hits, p)
		}
	}
	return hits, nil
}

// looksLikeResourceFile reports whether path parses as a JSON/YAML
// document with a non-empty metadata.kind field. The cheapest
// possible "is this a resource" check — no schema validation, no
// proto decode.
func looksLikeResourceFile(path string) bool {
	var raw map[string]any
	if err := protoencode.ReadFile(path, &raw); err != nil {
		return false
	}
	meta, _ := raw["metadata"].(map[string]any)
	if meta == nil {
		return false
	}
	kind, _ := meta["kind"].(string)
	return kind != ""
}

func runNewResource(ctx context.Context, c *cli.Command) (*newResponse, error) {
	p := interact.Default()

	name := c.StringArg("name")
	if err := validateName("resource name", name); err != nil {
		return nil, err
	}
	kindRef := c.String("kind")
	if kindRef == "" {
		return nil, fmt.Errorf("--kind is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return nil, err
	}

	// Load registries to validate the kind exists and to find its
	// compiled schema. The user must have run `veil build` (or used
	// `veil new kind`, which builds automatically) at least once for
	// local kinds; aliased external registries should already have a
	// registry.json on disk wherever veil.json points to.
	registries, err := resolveRegistries(nil, reg)
	if err != nil {
		return nil, err
	}
	kindReg, err := registry.Load(registries)
	if err != nil {
		return nil, err
	}
	loaded, err := kindReg.LoadKind(kindRef)
	if err != nil {
		return nil, fmt.Errorf("kind %q: %w", kindRef, err)
	}

	outPath := c.String("out")
	if outPath == "" {
		outPath = filepath.Join(cwd, name+".json")
	} else if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(cwd, outPath)
	}
	if _, err := os.Stat(outPath); err == nil {
		return nil, fmt.Errorf("resource file %s already exists", outPath)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return nil, fmt.Errorf("creating directory: %w", err)
	}

	schemaRel, err := filepath.Rel(filepath.Dir(outPath), loaded.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("resolving schema path: %w", err)
	}

	resourceJSON := map[string]any{
		"$schema": filepath.ToSlash(schemaRel),
		"metadata": map[string]any{
			"kind": kindRef,
			"name": name,
		},
		"spec": map[string]any{},
	}
	if err := writeJSON(outPath, resourceJSON); err != nil {
		return nil, err
	}

	p.Successf("Scaffolded resource %q at %s", name, outPath)
	return &newResponse{Kind: kindRef, Name: name, Path: outPath}, nil
}

// rollback collects undo actions in order. If commit() is not called
// before run() executes (deferred), the actions run in reverse order to
// restore the pre-scaffold state. Used so that a failed follow-up build
// does not leave the user with a partially-applied scaffold on disk.
type rollback struct {
	actions   []func()
	committed bool
}

func (r *rollback) commit() { r.committed = true }

func (r *rollback) run() {
	if r.committed {
		return
	}
	for i := len(r.actions) - 1; i >= 0; i-- {
		r.actions[i]()
	}
}

func (r *rollback) removeFile(path string) {
	r.actions = append(r.actions, func() { _ = os.Remove(path) })
}

func (r *rollback) removeTree(path string) {
	r.actions = append(r.actions, func() { _ = os.RemoveAll(path) })
}

func (r *rollback) restoreFile(path string, prev []byte) {
	r.actions = append(r.actions, func() { _ = os.WriteFile(path, prev, 0644) })
}

func validateName(label, name string) error {
	if name == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("%s %q must start with a lowercase letter and contain only lowercase letters, digits, '-', and '_'", label, name)
	}
	return nil
}

// ensureVeilJSON creates a bare veil.json at cwd if no project config
// file (veil.json, veil.yaml, or veil.yml) exists in cwd or any
// ancestor. Returns true when it created one. The scaffolder always
// writes JSON — users who prefer YAML can rename and rewrite the
// file by hand once it exists; subsequent mutations preserve whichever
// format the file ends up in.
func ensureVeilJSON(cwd string) (bool, error) {
	if fsutil.FindAncestorAny(cwd, config.VeilFiles) != "" {
		return false, nil
	}
	if err := writeJSON(filepath.Join(cwd, "veil.json"), bareVeilJSON()); err != nil {
		return false, err
	}
	return true, nil
}

// bareVeilJSON returns the default veil.json contents used by both the
// auto-init in `veil new kind` and the explicit `veil init` command.
// The empty-alias registry entry points at the project's local build
// output so the schema's required field is satisfied and `veil render`
// resolves kinds from `veil build` output without further configuration.
func bareVeilJSON() map[string]any {
	return map[string]any{
		"$schema": embeds.VeilConfigDefinitionSchemaURL,
		"kinds":   []string{},
		"registries": map[string]string{
			"": "./" + filepath.ToSlash(filepath.Join(config.PublicDir, "r", "registry.json")),
		},
	}
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// registerKindInVeilJSON appends relKind to the kinds[] array in the project
// config file (veil.json, veil.yaml, or veil.yml), preserving the existing
// list. When importName is non-empty the kind is added in the object form
// `{path, import: {name, value}}` (package mode); otherwise as a bare path
// string. The file format is detected by extension; YAML round-trips to YAML.
func registerKindInVeilJSON(configPath, relKind, importName, importValue string) error {
	return mutateGeneric(configPath, func(cfg map[string]any) error {
		var kinds []any
		if raw, ok := cfg["kinds"]; ok && raw != nil {
			arr, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s: \"kinds\" must be an array", configPath)
			}
			kinds = arr
		}
		// A kinds entry is either a bare path string or a {path, import?}
		// object. Preserve existing entries (including the object form)
		// untouched and append the new path only if absent — dedupe by path.
		for _, v := range kinds {
			switch entry := v.(type) {
			case string:
				if entry == relKind {
					return nil
				}
			case map[string]any:
				if p, _ := entry["path"].(string); p == relKind {
					return nil
				}
			default:
				return fmt.Errorf("%s: \"kinds\" entries must be a path string or {path, import?} object", configPath)
			}
		}
		var entry any = relKind
		if importName != "" {
			entry = map[string]any{
				"path":   relKind,
				"import": map[string]any{"name": importName, "value": importValue},
			}
		}
		cfg["kinds"] = append(kinds, entry)
		return nil
	})
}

// typesPackageName reads the repo-owned name of the shared types package at
// typesDir. veil never sets this name — it requires the package.json to exist
// and declare one, so package-mode imports resolve to a real package.
func typesPackageName(typesDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(typesDir, "package.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("types package has no package.json at %s — create one (with a name) before scaffolding a package-mode kind", typesDir)
		}
		return "", err
	}
	m, err := decodePackageJSON(data)
	if err != nil {
		return "", fmt.Errorf("parsing %s/package.json: %w", typesDir, err)
	}
	name, _ := m["name"].(string)
	if name == "" {
		return "", fmt.Errorf("%s/package.json must declare a name", typesDir)
	}
	return name, nil
}

// appendHookToResource appends relHook to the resource file's
// metadata.hooks.<lifecycle> array. The resource file may be JSON or
// YAML — the format is preserved across the mutation. Creates the
// metadata.hooks block (and the lifecycle array within it) when
// absent.
func appendHookToResource(resourcePath, lifecycle, relHook string) error {
	return mutateGeneric(resourcePath, func(raw map[string]any) error {
		meta, _ := raw["metadata"].(map[string]any)
		if meta == nil {
			return fmt.Errorf("%s: missing metadata block", resourcePath)
		}
		hooksObj, _ := meta["hooks"].(map[string]any)
		if hooksObj == nil {
			hooksObj = map[string]any{}
		}

		var list []map[string]any
		if existing, ok := hooksObj[lifecycle]; ok && existing != nil {
			arr, ok := existing.([]any)
			if !ok {
				return fmt.Errorf("%s: \"metadata.hooks.%s\" must be an array", resourcePath, lifecycle)
			}
			for _, v := range arr {
				switch entry := v.(type) {
				case string:
					list = append(list, map[string]any{"path": entry})
				case map[string]any:
					list = append(list, entry)
				default:
					return fmt.Errorf("%s: \"metadata.hooks.%s\" entries must be strings or objects with a \"path\" field", resourcePath, lifecycle)
				}
			}
		}

		for _, entry := range list {
			if entry["path"] == relHook {
				return nil
			}
		}
		list = append(list, map[string]any{"path": relHook})
		hooksObj[lifecycle] = list
		meta["hooks"] = hooksObj
		raw["metadata"] = meta
		return nil
	})
}

// appendHookToKind appends relHook to the kind file's hooks.<lifecycle>
// array, with `source` set on the new entry when non-empty (a typed
// hook binding — see Typed hooks in SPEC.md). The kind file may be
// JSON or YAML — the format is preserved across the mutation.
func appendHookToKind(kindPath, lifecycle, relHook, source string) error {
	return mutateGeneric(kindPath, func(raw map[string]any) error {
		hooksObj, _ := raw["hooks"].(map[string]any)
		if hooksObj == nil {
			hooksObj = map[string]any{}
		}

		var list []map[string]any
		if existing, ok := hooksObj[lifecycle]; ok && existing != nil {
			arr, ok := existing.([]any)
			if !ok {
				return fmt.Errorf("%s: \"hooks.%s\" must be an array", kindPath, lifecycle)
			}
			for _, v := range arr {
				entry, ok := v.(map[string]any)
				if !ok {
					return fmt.Errorf("%s: \"hooks.%s\" entries must be objects with a \"path\" field", kindPath, lifecycle)
				}
				list = append(list, entry)
			}
		}

		for _, entry := range list {
			if entry["path"] == relHook {
				return nil
			}
		}
		entry := map[string]any{"path": relHook}
		if source != "" {
			entry["source"] = source
		}
		list = append(list, entry)
		hooksObj[lifecycle] = list
		raw["hooks"] = hooksObj
		return nil
	})
}

// mutateGeneric reads a JSON or YAML file, calls fn on the parsed
// top-level map, and writes the result back in the file's original
// format. Format is detected by extension via protoencode's decoder
// map — JSON files round-trip with two-space indent and a trailing
// newline; YAML files round-trip via yaml.v3 (which sorts keys
// alphabetically and strips comments).
func mutateGeneric(path string, fn func(doc map[string]any) error) error {
	var doc map[string]any
	if err := protoencode.ReadFile(path, &doc); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := fn(doc); err != nil {
		return err
	}
	if err := protoencode.WriteFileAny(path, doc); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
