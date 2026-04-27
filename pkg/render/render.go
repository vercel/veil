// Package render implements the `veil render` pipeline: load the entry
// resource via the catalog, resolve overlays by matching each overlay's
// `if` regex map against the resolved variables, validate the merged
// spec against the kind's schema, execute hooks in order, follow
// declared dependencies, and write the final bundle to disk.
package render

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/hook"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/protoencode"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/resource"
)

// Options configures a Render call.
type Options struct {
	// Kind and Name identify the entry-point resource. Render loads it
	// via the Catalog and walks outward — overlays merged in, then
	// dependent hooks invoked for each declared dependency (each of
	// which is also loaded via the Catalog).
	Kind string
	Name string

	// OutDir is the root directory where rendered bundles are written. Each
	// instance gets a subdirectory named after metadata.name.
	OutDir string

	// Root is the veil project root — the directory housing veil.json.
	// Threaded through to the hook context as `ctx.root` and used to
	// chdir the process before running hooks so `ctx.std` / `ctx.os`
	// paths resolve against the project root regardless of where the
	// user invoked `veil render` from. If empty, falls back to the
	// caller's CWD.
	Root string

	// Registry resolves compiled kinds on demand. The render pipeline asks
	// it for each resource's kind by name; the registry handles index
	// resolution, lazy loading, and caching internally.
	Registry registry.Registry

	// FS is the read-only project filesystem rooted at the project
	// root. Used to read overlay files, schemas, and any other
	// auxiliary content the render pipeline pulls in. CLI typically
	// passes os.DirFS(reg.Root); tests may pass an fstest.MapFS.
	FS fs.FS

	// Catalog resolves the entry-point resource and any dependency
	// targets by (kind, name). Built from the same fs.FS, lazy and
	// cached.
	Catalog resource.Catalog

	// Variables is the resolved map of input variable values, keyed by
	// name. Each variable's stringified value is what an overlay's `if`
	// regex matches against; hooks receive the same map as `ctx.vars`.
	Variables map[string]any
}

// RenderedResource describes one successfully rendered resource.
type RenderedResource struct {
	Name   string
	OutDir string
	Files  []string
}

// Render renders the resource identified by (opts.Kind, opts.Name).
// The catalog supplies the entry-point resource and any dependency
// targets it reaches. Overlays and dependent hooks fan out from this
// single starting point.
func Render(opts *Options) (*RenderedResource, error) {
	if opts.Catalog == nil {
		return nil, fmt.Errorf("no catalog configured")
	}
	if opts.Kind == "" || opts.Name == "" {
		return nil, fmt.Errorf("kind and name are required")
	}
	r, err := opts.Catalog.LoadResource(opts.Kind, opts.Name)
	if err != nil {
		return nil, err
	}

	// chdir into the veil project root so hook-side std/os paths resolve
	// relative to the root (wazero captures os.Getwd() at qjs.New time).
	// Restore on exit — this is process-global state.
	root := opts.Root
	if root != "" {
		prev, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting cwd: %w", err)
		}
		if err := os.Chdir(root); err != nil {
			return nil, fmt.Errorf("chdir to root %s: %w", root, err)
		}
		defer os.Chdir(prev)
	} else {
		root, _ = os.Getwd()
	}

	rendered, err := renderResource(r, root, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.GetMetadata().GetName(), err)
	}
	return rendered, nil
}

func renderResource(r *resource.Resource, root string, opts *Options) (*RenderedResource, error) {
	kindName := r.GetMetadata().GetKind()
	resourceName := r.GetMetadata().GetName()
	logger := slog.Default().With("kind", kindName, "resource", resourceName, "path", r.Path)

	if opts.Registry == nil {
		return nil, fmt.Errorf("no registry configured")
	}
	logger.Debug("loading compiled kind")
	loaded, err := opts.Registry.LoadKind(kindName)
	if err != nil {
		return nil, err
	}
	kind := loaded.Kind
	schemaPath := loaded.SchemaPath

	logger.Debug("applying overlays", "count", len(r.GetMetadata().GetOverlays()))
	mergedSpec, err := applyOverlays(opts.FS, r, opts.Variables)
	if err != nil {
		return nil, fmt.Errorf("overlays: %w", err)
	}

	specSchema, err := loadSpecSubschema(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("loading spec schema: %w", err)
	}
	applySchemaDefaults(mergedSpec, specSchema)

	// Build the post-overlay resource that downstream code (validator +
	// hook ctx) operates on: clone the original, replace its spec with the
	// merged+defaulted result, and drop overlays since they've already
	// been applied.
	resolved, err := resolveResource(r.Resource, mergedSpec)
	if err != nil {
		return nil, fmt.Errorf("building resolved resource: %w", err)
	}

	logger.Debug("validating spec against schema")
	if err := validateResource(schemaPath, resolved); err != nil {
		return nil, fmt.Errorf("schema validation: %w", err)
	}

	// Promote the flat source map to the identity → File structure that
	// flows through the hook pipeline. Identity starts as the declared
	// source path; hooks may remap the destination via File.setOutputPath
	// without changing identity.
	bundle := make(hook.Bundle, len(kind.Sources))
	for k, v := range kind.Sources {
		bundle[k] = hook.File{Path: k, Content: v}
	}

	resourceMap, err := resourceToMap(resolved)
	if err != nil {
		return nil, fmt.Errorf("encoding resource for hook ctx: %w", err)
	}

	ctx := map[string]any{
		"resource": resourceMap,
		"vars":     opts.Variables,
		"root":     root,
	}
	renderHooks := kind.GetHooks().GetRender()
	logger.Info("running render hooks", "count", len(renderHooks))
	for _, h := range renderHooks {
		newBundle, err := invokeHook(logger, h, kindName, resourceName, ctx, bundle)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.GetName(), err)
		}
		bundle = newBundle
	}

	deps := resolved.GetDependencies()
	if len(deps) > 0 {
		logger.Info("applying dependencies", "count", len(deps))
	}
	for _, dep := range deps {
		newBundle, err := applyDependency(logger, bundle, dep, kindName, resourceName, resourceMap, root, opts)
		if err != nil {
			return nil, fmt.Errorf("dependency %s/%s: %w", dep.GetKind(), dep.GetName(), err)
		}
		bundle = newBundle
	}

	outDir := filepath.Join(opts.OutDir, resourceName)
	files, err := writeBundle(outDir, bundle)
	if err != nil {
		return nil, err
	}

	return &RenderedResource{
		Name:   resourceName,
		OutDir: outDir,
		Files:  files,
	}, nil
}

// resolveResource clones r, replaces its spec with mergedSpec, and clears
// the overlay list (overlays are an authoring-time concept that's already
// been applied — re-exposing them in the post-resolve resource would be
// misleading for hooks and unnecessary noise for the validator).
func resolveResource(r *veilv1.Resource, mergedSpec map[string]any) (*veilv1.Resource, error) {
	out := proto.Clone(r).(*veilv1.Resource)
	specStruct, err := structpb.NewStruct(mergedSpec)
	if err != nil {
		return nil, fmt.Errorf("converting merged spec to struct: %w", err)
	}
	out.Spec = specStruct
	if out.Metadata != nil {
		out.Metadata.Overlays = nil
	}
	return out, nil
}

// resourceToMap marshals a Resource via protojson and re-parses it as a
// generic map, suitable for embedding in the hook ctx (which round-trips
// through goccy/go-json).
func resourceToMap(r *veilv1.Resource) (map[string]any, error) {
	data, err := protoencode.Marshal.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// applyDependency resolves one declared dependency from the consumer's
// resource: it looks up the target in the catalog, applies the target's
// own overlays + schema defaults so `ctx.self` matches what the target
// would see at render time, then runs every dependent hook the target
// kind registers for this consumer kind. Each hook receives the
// consumer's bundle and may mutate it before returning.
func applyDependency(parent *slog.Logger, bundle hook.Bundle, dep *veilv1.Dependency, consumerKind, consumerName string, consumerMap map[string]any, root string, opts *Options) (hook.Bundle, error) {
	if opts.Catalog == nil {
		return nil, fmt.Errorf("no catalog configured")
	}
	targetKind := dep.GetKind()
	targetName := dep.GetName()
	logger := parent.With("dep_kind", targetKind, "dep_name", targetName)

	logger.Debug("resolving dependency target")
	target, err := opts.Catalog.LoadResource(targetKind, targetName)
	if err != nil {
		return nil, err
	}

	resolvedTarget, err := resolveTargetResource(target, opts)
	if err != nil {
		return nil, fmt.Errorf("resolving target: %w", err)
	}

	loadedKind, err := opts.Registry.LoadKind(targetKind)
	if err != nil {
		return nil, fmt.Errorf("loading target kind: %w", err)
	}

	var dependentEntry *veilv1.DependentHook
	for _, d := range loadedKind.Kind.GetHooks().GetDependents() {
		if d.GetKind() == consumerKind {
			dependentEntry = d
			break
		}
	}
	if dependentEntry == nil {
		return nil, fmt.Errorf("target kind %q does not list %q as a valid consumer", targetKind, consumerKind)
	}

	targetMap, err := resourceToMap(resolvedTarget)
	if err != nil {
		return nil, fmt.Errorf("encoding target: %w", err)
	}

	var paramsMap map[string]any
	if p := dep.GetParams(); p != nil {
		paramsMap = p.AsMap()
	}
	depCtx := map[string]any{
		"self":     targetMap,
		"consumer": consumerMap,
		"params":   paramsMap,
		"vars":     opts.Variables,
		"root":     root,
	}

	for _, h := range dependentEntry.GetHooks() {
		newBundle, err := invokeHook(logger, h, targetKind, targetName, depCtx, bundle)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.GetName(), err)
		}
		bundle = newBundle
	}
	return bundle, nil
}

// resolveTargetResource is the dependent-hook side of the same overlay
// + spec-defaults pipeline that runs for the consumer in
// renderResource. The resulting Resource is what `ctx.self` exposes to
// the dependent hook — overlays applied, schema defaults filled in,
// overlays cleared from metadata. No schema validation: targets are
// inspected, not re-rendered.
func resolveTargetResource(r *resource.Resource, opts *Options) (*veilv1.Resource, error) {
	mergedSpec, err := applyOverlays(opts.FS, r, opts.Variables)
	if err != nil {
		return nil, fmt.Errorf("overlays: %w", err)
	}
	loaded, err := opts.Registry.LoadKind(r.GetMetadata().GetKind())
	if err != nil {
		return nil, fmt.Errorf("loading kind: %w", err)
	}
	specSchema, err := loadSpecSubschema(loaded.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("loading spec schema: %w", err)
	}
	applySchemaDefaults(mergedSpec, specSchema)
	return resolveResource(r.Resource, mergedSpec)
}

// invokeHook constructs a Hook from compiled code, calls RenderHook,
// and always closes the underlying runtime. The parent logger is
// extended with the hook name so log lines stay traceable across
// multi-hook renders. The hook's own console.log calls flow through
// the same scoped logger; warn/error calls additionally surface on
// the user's terminal via interact.Default().
func invokeHook(parent *slog.Logger, h *veilv1.Hook, kindName, resourceName string, ctx any, bundle hook.Bundle) (hook.Bundle, error) {
	hookName := h.GetName()
	logger := parent.With("hook", hookName)

	env, err := resolveHookEnv(h, kindName, resourceName, hookName)
	if err != nil {
		return nil, err
	}
	if len(env) > 0 {
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		sort.Strings(names)
		logger.Info("granting env access", "vars", names)
	}

	logger.Info("running hook")
	start := time.Now()

	display := func(level, msg string) {
		p := interact.Default()
		switch level {
		case "warn":
			p.Warnf("WARN [%s/%s/%s] %s", kindName, resourceName, hookName, msg)
		case "error":
			p.Errorf("ERROR [%s/%s/%s] %s", kindName, resourceName, hookName, msg)
		}
	}

	hk, err := hook.New(
		h.GetContent(),
		hook.WithLogger(logger),
		hook.WithDisplay(display),
		hook.WithEnv(env),
	)
	if err != nil {
		logger.Error("hook failed", "stage", "init", "duration", time.Since(start).String(), "err", err.Error())
		return nil, err
	}
	defer hk.Close()

	out, err := hk.RenderHook(ctx, bundle)
	if err != nil {
		logger.Error("hook failed", "stage", "render", "duration", time.Since(start).String(), "err", err.Error())
		return nil, err
	}
	logger.Info("hook completed", "duration", time.Since(start).String())
	return out, nil
}

// resolveHookEnv reads each declared env var off the host. If any
// declared var is unset, the call returns an aggregated error listing
// every missing name with its description so the user can fix them all
// in one pass. The returned map contains only what the hook declared
// (and only what was actually present), so the runtime exposure layer
// can blindly forward it.
func resolveHookEnv(h *veilv1.Hook, kindName, resourceName, hookName string) (map[string]string, error) {
	declared := h.GetAccess().GetEnv()
	if len(declared) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(declared))
	var missing []string
	for _, e := range declared {
		name := e.GetName()
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
			continue
		}
		missing = append(missing, fmt.Sprintf("  - %s: %s", name, e.GetDescription()))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"hook %s/%s/%s requires environment variables that are not set on the host:\n%s",
			kindName, resourceName, hookName, strings.Join(missing, "\n"),
		)
	}
	return env, nil
}

// applyOverlays evaluates each overlay's `if` map against the resolved
// variables. The overlay applies when every (varName, regex) entry
// matches the corresponding variable's stringified value; matching
// overlays' specs are deep-merged into the base spec in declaration
// order.
func applyOverlays(fsys fs.FS, r *resource.Resource, vars map[string]any) (map[string]any, error) {
	baseSpec := r.GetSpec().AsMap()
	overlays := r.GetMetadata().GetOverlays()
	if len(overlays) == 0 {
		return baseSpec, nil
	}

	result := baseSpec
	for i, ov := range overlays {
		matches, err := overlayMatches(ov.GetIf(), vars)
		if err != nil {
			return nil, fmt.Errorf("overlay[%d]: %w", i, err)
		}
		if !matches {
			continue
		}

		// Overlay paths are fs.FS-relative (forward-slash, no leading
		// dot), resolved against the resource's own directory so a
		// resource at "services/api/api.json" with overlay "./staging.json"
		// reads "services/api/staging.json".
		overlayPath := path.Join(path.Dir(r.Path), filepath.ToSlash(ov.GetFile()))
		data, err := fs.ReadFile(fsys, overlayPath)
		if err != nil {
			return nil, fmt.Errorf("reading overlay %s: %w", overlayPath, err)
		}
		var overlayDoc struct {
			Spec map[string]any `json:"spec"`
		}
		if err := json.Unmarshal(data, &overlayDoc); err != nil {
			return nil, fmt.Errorf("parsing overlay %s: %w", overlayPath, err)
		}
		result = deepMerge(result, overlayDoc.Spec)
	}
	return result, nil
}

// overlayMatches reports whether every (varName, regex) entry in the
// overlay's `if` map matches the corresponding resolved variable's
// stringified value. An empty map is treated as "always matches".
// Errors when a referenced variable is not declared, or when a pattern
// fails to compile.
func overlayMatches(conditions map[string]string, vars map[string]any) (bool, error) {
	for name, pattern := range conditions {
		val, ok := vars[name]
		if !ok {
			return false, fmt.Errorf("if[%q]: variable not declared in veil.json", name)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("if[%q]: invalid regex %q: %w", name, pattern, err)
		}
		if !re.MatchString(stringifyVar(val)) {
			return false, nil
		}
	}
	return true, nil
}

// stringifyVar renders a resolved variable value as the string the
// overlay regex matches against. Strings pass through; numbers and
// bools format via fmt's defaults so callers can write
// `replicas: "^[3-9]$"` against a numeric variable.
func stringifyVar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// loadSpecSubschema reads the composite kind.schema.json and returns its
// `properties.spec` subschema — the author-facing schema that declares
// each spec field, including its `default` values. Returns an empty map
// if the composite schema has no spec subschema.
func loadSpecSubschema(schemaPath string) (map[string]any, error) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	props, _ := root["properties"].(map[string]any)
	spec, _ := props["spec"].(map[string]any)
	if spec == nil {
		return map[string]any{}, nil
	}
	return spec, nil
}

// applySchemaDefaults walks an object-typed JSON Schema and fills in any
// missing fields in data with the corresponding `default` value from the
// schema. Recurses into nested object properties. Arrays and scalar
// leaves are not defaulted beyond the direct property match.
func applySchemaDefaults(data map[string]any, schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, present := data[name]; !present {
			if def, ok := prop["default"]; ok {
				data[name] = cloneValue(def)
			}
		}
		if child, ok := data[name].(map[string]any); ok {
			applySchemaDefaults(child, prop)
		}
	}
}

// validateResource validates the resource against the composite
// kind.schema.json. The resource passed in must already have its spec
// merged via resolveResource — i.e. overlays are applied, so what's
// validated is exactly what hooks (and the final renderer) will see.
func validateResource(schemaPath string, r *veilv1.Resource) error {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("reading schema %s: %w", schemaPath, err)
	}
	var schemaDoc any
	if err := json.Unmarshal(data, &schemaDoc); err != nil {
		return fmt.Errorf("parsing schema %s: %w", schemaPath, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("mem://schema", schemaDoc); err != nil {
		return fmt.Errorf("registering schema: %w", err)
	}
	schema, err := compiler.Compile("mem://schema")
	if err != nil {
		return fmt.Errorf("compiling schema: %w", err)
	}

	doc, err := resourceToMap(r)
	if err != nil {
		return fmt.Errorf("encoding resource for validation: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		// santhosh-tekuri/jsonschema embeds the in-memory schema URL
		// (`mem://schema#…`) in every message — strip it so users see
		// just the JSON-pointer location and the failure text.
		return errors.New(stripSchemaURL(err.Error()))
	}
	return nil
}

var schemaURLRE = regexp.MustCompile(`'mem://schema#?[^']*'`)

func stripSchemaURL(msg string) string {
	msg = schemaURLRE.ReplaceAllString(msg, "kind schema")
	return strings.TrimPrefix(msg, "jsonschema validation failed with kind schema\n")
}

// writeBundle materializes a hook.Bundle to disk under outDir, using each
// entry's File.Path as the relative destination. Two entries resolving to
// the same destination path are a hard error — a hook somewhere rerouted
// two files onto the same output slot.
func writeBundle(outDir string, bundle hook.Bundle) ([]string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", outDir, err)
	}

	// Iterate identity keys in sorted order so collision errors and
	// returned file lists are deterministic.
	identities := make([]string, 0, len(bundle))
	for k := range bundle {
		identities = append(identities, k)
	}
	sort.Strings(identities)

	usedPaths := make(map[string]string, len(bundle))
	pathsOut := make([]string, 0, len(bundle))
	for _, id := range identities {
		file := bundle[id]
		// Tombstoned entries are skipped at write time — downstream hooks
		// already had their chance to observe them via File.isDeleted().
		if file.Deleted {
			continue
		}
		path := file.Path
		if path == "" {
			path = id
		}
		if prev, ok := usedPaths[path]; ok {
			return nil, fmt.Errorf("path collision: identities %q and %q both resolve to %q", prev, id, path)
		}
		usedPaths[path] = id
		pathsOut = append(pathsOut, path)

		fullPath := filepath.Join(outDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", filepath.Dir(fullPath), err)
		}
		if err := os.WriteFile(fullPath, []byte(file.Content), 0644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", fullPath, err)
		}
	}
	sort.Strings(pathsOut)
	return pathsOut, nil
}

// cloneMap deep-copies a map[string]any so overlay merges don't mutate the
// caller's map.
func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		c := make([]any, len(t))
		for i, x := range t {
			c[i] = cloneValue(x)
		}
		return c
	default:
		return t
	}
}

// deepMerge returns a new map with `overlay` merged into `base`. Map values
// merge recursively; scalars and arrays replace.
func deepMerge(base, overlay map[string]any) map[string]any {
	out := cloneMap(base)
	for k, v := range overlay {
		if existing, ok := out[k]; ok {
			if em, emOk := existing.(map[string]any); emOk {
				if vm, vmOk := v.(map[string]any); vmOk {
					out[k] = deepMerge(em, vm)
					continue
				}
			}
		}
		out[k] = cloneValue(v)
	}
	return out
}
