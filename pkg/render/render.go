// Package render implements the `veil render` pipeline: discover resource
// instances, resolve overlays via CEL, validate specs against their kind's
// schema, execute hooks in order, and write the final bundle to disk.
package render

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/hook"
	"github.com/vercel/veil/pkg/interact"
)

// resourceJSON is the protojson marshal/unmarshal config used wherever we
// move a Resource between disk, the validator, or the hook ctx. Discarding
// unknown fields keeps user-authored JSON forgiving (extra annotations,
// editor metadata, etc. don't break loading).
var resourceJSON = struct {
	Marshal   protojson.MarshalOptions
	Unmarshal protojson.UnmarshalOptions
}{
	Marshal:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false},
	Unmarshal: protojson.UnmarshalOptions{DiscardUnknown: true},
}

// Options configures a Render call.
type Options struct {
	// Dir is the directory scanned for resource JSON files.
	Dir string

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

	// RegistryKinds maps kind name → absolute path to the compiled kind.json.
	// Typically produced by loading one or more registry.json files.
	RegistryKinds map[string]string

	// Variables is the resolved map of input variable values, keyed by name.
	// Exposed to overlay CEL expressions (as `var.<name>`) and hook context
	// (as `ctx.var.<name>`).
	Variables map[string]any
}

// Result summarizes a Render invocation.
type Result struct {
	Rendered []RenderedResource
}

// RenderedResource describes one successfully rendered resource.
type RenderedResource struct {
	Name   string
	OutDir string
	Files  []string
}

// LoadedResource pairs a proto-defined Resource with the absolute filesystem
// path it was loaded from. The path is needed to resolve overlay file
// references but isn't part of the Resource's wire shape.
type LoadedResource struct {
	*veilv1.Resource
	Path string
}

// CompiledKind is the render-time view of a compiled kind.json artifact.
type CompiledKind struct {
	Name    string            `json:"name"`
	Sources map[string]string `json:"sources"`
	Hooks   CompiledHooks     `json:"hooks"`
}

// CompiledHooks groups compiled hook lists by lifecycle point.
type CompiledHooks struct {
	Render []CompiledHook `json:"render,omitempty"`
}

// CompiledHook is one hook in a compiled kind.
type CompiledHook struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Render executes the full pipeline for every resource discovered in
// opts.Dir.
func Render(opts Options) (Result, error) {
	resources, err := Discover(opts.Dir)
	if err != nil {
		return Result{}, err
	}

	// chdir into the veil project root so hook-side std/os paths resolve
	// relative to the root (wazero captures os.Getwd() at qjs.New time).
	// Restore on exit — this is process-global state.
	root := opts.Root
	if root != "" {
		prev, err := os.Getwd()
		if err != nil {
			return Result{}, fmt.Errorf("getting cwd: %w", err)
		}
		if err := os.Chdir(root); err != nil {
			return Result{}, fmt.Errorf("chdir to root %s: %w", root, err)
		}
		defer os.Chdir(prev)
	} else {
		root, _ = os.Getwd()
	}

	var res Result
	for _, r := range resources {
		rendered, err := renderResource(r, root, opts)
		if err != nil {
			return res, fmt.Errorf("%s: %w", r.GetMetadata().GetName(), err)
		}
		res.Rendered = append(res.Rendered, rendered)
	}
	return res, nil
}

// Discover loads Resources from a path. If path is a file, exactly that
// file is loaded (and any parse/shape error surfaces — a file path is an
// explicit request). If path is a directory, it is scanned non-recursively
// for *.json files that parse as Resources (`metadata.kind`,
// `metadata.name`, and non-null `spec`); other JSON files are silently
// ignored, since the directory may contain overlays or unrelated config.
func Discover(path string) ([]LoadedResource, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.IsDir() {
		r, err := loadResource(path)
		if err != nil {
			return nil, err
		}
		if err := validateResourceShape(r, path); err != nil {
			return nil, err
		}
		return []LoadedResource{r}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var out []LoadedResource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fp := filepath.Join(path, e.Name())
		r, err := loadResource(fp)
		if err != nil {
			continue // not valid JSON / not parseable as Resource; skip quietly
		}
		if r.GetMetadata().GetKind() == "" || r.GetMetadata().GetName() == "" || r.GetSpec() == nil {
			continue // not a Resource shape; skip quietly
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetMetadata().GetName() < out[j].GetMetadata().GetName()
	})
	return out, nil
}

func loadResource(path string) (LoadedResource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LoadedResource{}, fmt.Errorf("reading %s: %w", path, err)
	}
	r := &veilv1.Resource{}
	if err := resourceJSON.Unmarshal.Unmarshal(data, r); err != nil {
		return LoadedResource{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	abs, _ := filepath.Abs(path)
	return LoadedResource{Resource: r, Path: abs}, nil
}

func validateResourceShape(r LoadedResource, path string) error {
	var missing []string
	if r.GetMetadata().GetKind() == "" {
		missing = append(missing, "metadata.kind")
	}
	if r.GetMetadata().GetName() == "" {
		missing = append(missing, "metadata.name")
	}
	if r.GetSpec() == nil {
		missing = append(missing, "spec")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: not a resource — missing %s", path, strings.Join(missing, ", "))
	}
	return nil
}

func renderResource(r LoadedResource, root string, opts Options) (RenderedResource, error) {
	kindName := r.GetMetadata().GetKind()
	resourceName := r.GetMetadata().GetName()

	kindPath, ok := opts.RegistryKinds[kindName]
	if !ok {
		return RenderedResource{}, fmt.Errorf("kind %q not found in any loaded registry", kindName)
	}

	compiled, err := loadCompiledKind(kindPath)
	if err != nil {
		return RenderedResource{}, err
	}

	mergedSpec, err := applyOverlays(r, opts.Variables)
	if err != nil {
		return RenderedResource{}, fmt.Errorf("overlays: %w", err)
	}

	schemaPath := filepath.Join(filepath.Dir(kindPath), "kind.schema.json")
	specSchema, err := loadSpecSubschema(schemaPath)
	if err != nil {
		return RenderedResource{}, fmt.Errorf("loading spec schema: %w", err)
	}
	applySchemaDefaults(mergedSpec, specSchema)

	// Build the post-overlay resource that downstream code (validator +
	// hook ctx) operates on: clone the original, replace its spec with the
	// merged+defaulted result, and drop overlays since they've already
	// been applied.
	resolved, err := resolveResource(r.Resource, mergedSpec)
	if err != nil {
		return RenderedResource{}, fmt.Errorf("building resolved resource: %w", err)
	}

	if err := validateResource(schemaPath, resolved); err != nil {
		return RenderedResource{}, fmt.Errorf("schema validation: %w", err)
	}

	// Promote the flat source map to the identity → File structure that
	// flows through the hook pipeline. Identity starts as the declared
	// source path; hooks may remap the destination via File.setOutputPath
	// without changing identity.
	bundle := make(hook.Bundle, len(compiled.Sources))
	for k, v := range compiled.Sources {
		bundle[k] = hook.File{Path: k, Content: v}
	}

	resourceMap, err := resourceToMap(resolved)
	if err != nil {
		return RenderedResource{}, fmt.Errorf("encoding resource for hook ctx: %w", err)
	}

	ctx := map[string]any{
		"resource": resourceMap,
		"vars":     opts.Variables,
		"root":     root,
	}
	for _, h := range compiled.Hooks.Render {
		newBundle, err := invokeHook(h, kindName, resourceName, ctx, bundle)
		if err != nil {
			return RenderedResource{}, fmt.Errorf("hook %s: %w", h.Name, err)
		}
		bundle = newBundle
	}

	outDir := filepath.Join(opts.OutDir, resourceName)
	files, err := writeBundle(outDir, bundle)
	if err != nil {
		return RenderedResource{}, err
	}

	return RenderedResource{
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
	data, err := resourceJSON.Marshal.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// invokeHook constructs a Hook from compiled code, calls RenderHook, and
// always closes the underlying runtime. Emits a "running hook" log line
// before the call and "hook completed" (or "hook failed") after — all
// scoped with kind, resource name, and hook name so a multi-resource
// render is traceable. The hook's own `console.log` calls flow through
// the same scoped slog logger; warn/error calls are additionally
// surfaced on the user's terminal via interact.Default() so they're
// visible without --debug.
func invokeHook(h CompiledHook, kindName, resourceName string, ctx any, bundle hook.Bundle) (hook.Bundle, error) {
	logger := slog.Default().With("kind", kindName, "resource", resourceName, "hook", h.Name)

	logger.Info("running hook")
	start := time.Now()

	display := func(level, msg string) {
		p := interact.Default()
		switch level {
		case "warn":
			p.Warnf("WARN [%s/%s/%s] %s", kindName, resourceName, h.Name, msg)
		case "error":
			p.Errorf("ERROR [%s/%s/%s] %s", kindName, resourceName, h.Name, msg)
		}
	}

	hk, err := hook.New(h.Content, hook.WithLogger(logger), hook.WithDisplay(display))
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

func loadCompiledKind(path string) (*CompiledKind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading compiled kind %s: %w", path, err)
	}
	var ck CompiledKind
	if err := json.Unmarshal(data, &ck); err != nil {
		return nil, fmt.Errorf("parsing compiled kind %s: %w", path, err)
	}
	return &ck, nil
}

// applyOverlays compiles and evaluates each overlay's CEL match expression
// against the resolved variables. Every matching overlay's spec is deep-merged
// into the base spec in declaration order.
func applyOverlays(r LoadedResource, vars map[string]any) (map[string]any, error) {
	baseSpec := r.GetSpec().AsMap()
	overlays := r.GetMetadata().GetOverlays()
	if len(overlays) == 0 {
		return baseSpec, nil
	}

	env, err := cel.NewEnv(
		cel.Variable("vars", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("building CEL env: %w", err)
	}

	activation := map[string]any{"vars": vars}
	result := baseSpec
	for _, ov := range overlays {
		ast, iss := env.Compile(ov.GetMatch())
		if iss.Err() != nil {
			return nil, fmt.Errorf("overlay %q: %w", ov.GetMatch(), iss.Err())
		}
		prg, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("overlay %q: %w", ov.GetMatch(), err)
		}
		out, _, err := prg.Eval(activation)
		if err != nil {
			return nil, fmt.Errorf("overlay %q: %w", ov.GetMatch(), err)
		}
		if !isTrue(out) {
			continue
		}

		overlayPath := ov.GetFile()
		if !filepath.IsAbs(overlayPath) {
			overlayPath = filepath.Join(filepath.Dir(r.Path), overlayPath)
		}
		data, err := os.ReadFile(overlayPath)
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

func isTrue(v ref.Val) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(types.Bool); ok {
		return bool(b)
	}
	return false
}
