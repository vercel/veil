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
	"github.com/vercel/veil/pkg/bundle"
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
	// Threaded through to the hook context as `ctx.root` and handed to
	// each hook runtime as its filesystem root (QuickJS mounts it at "/"),
	// so `ctx.std` / `ctx.os` paths resolve against the project root
	// regardless of where the user invoked `veil render` from. Passed
	// straight to the runtime — never via the host process's working
	// directory — so renders are safe to run concurrently. If empty,
	// falls back to the caller's CWD.
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

	// The project root is handed to each hook runtime as its filesystem
	// root (see invokeHook → hook.WithCwd), so hook-side std/os paths
	// resolve against it without the host process ever changing its own
	// working directory — which is what lets renders run concurrently.
	root := opts.Root
	if root == "" {
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

	// Apply local overrides before any hook runs so hooks see the
	// user's content. `frozen` records the override paths whose
	// skip_hooks flag is set — those get re-stamped after the pipeline
	// finishes so any hook mutations to them are discarded.
	frozen, err := applyOverrides(opts.FS, r, bundle)
	if err != nil {
		return nil, fmt.Errorf("applying overrides: %w", err)
	}

	resourceMap, err := resourceToMap(resolved)
	if err != nil {
		return nil, fmt.Errorf("encoding resource for hook ctx: %w", err)
	}

	ctx := map[string]any{
		"resource": resourceMap,
		"path":     r.Path,
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
		newBundle, err := applyDependency(logger, bundle, dep, kindName, resourceName, r.Path, resourceMap, root, opts)
		if err != nil {
			return nil, fmt.Errorf("dependency %s/%s: %w", dep.GetKind(), dep.GetName(), err)
		}
		bundle = newBundle
	}

	// Resource-level render hooks: paths declared inline on the resource
	// yaml under metadata.hooks.render. Compiled on demand at render
	// time (no kind.json entry — they belong to the resource). Run
	// after the kind's render + dependents so they see the fully
	// kind-rendered bundle.
	resourceHooks := r.RenderHooks
	if len(resourceHooks) > 0 {
		logger.Info("running resource hooks", "count", len(resourceHooks))
		resourceDir := path.Dir(r.Path)
		for _, def := range resourceHooks {
			compiled, err := compileResourceHook(opts.FS, resourceDir, def.GetPath(), def.GetAccess())
			if err != nil {
				return nil, fmt.Errorf("resource hook %s: %w", def.GetPath(), err)
			}
			newBundle, err := invokeHook(logger, compiled, kindName, resourceName, ctx, bundle)
			if err != nil {
				return nil, fmt.Errorf("resource hook %s: %w", def.GetPath(), err)
			}
			bundle = newBundle
		}
	}

	// Kind post_render hooks: the kind's final normalization pass.
	// Same RenderHook contract as `render`, but runs after resource
	// hooks so the kind can react to whatever resource customizations
	// produced (consistent formatting, key ordering, banner stamping,
	// etc.). Validates run after this so they see the normalized
	// output.
	postRenderHooks := kind.GetHooks().GetPostRender()
	if len(postRenderHooks) > 0 {
		logger.Info("running post_render hooks", "count", len(postRenderHooks))
		for _, h := range postRenderHooks {
			newBundle, err := invokeHook(logger, h, kindName, resourceName, ctx, bundle)
			if err != nil {
				return nil, fmt.Errorf("post_render hook %s: %w", h.GetName(), err)
			}
			bundle = newBundle
		}
	}

	// Validation hooks: run after every other lifecycle point. Every
	// hook runs regardless of failures, the runner snapshots away its
	// FS/ctx mutations, and the aggregated issue list fails the
	// render at the end if any error-severity issues come back.
	validateHooks := kind.GetHooks().GetValidate()
	if len(validateHooks) > 0 {
		issues, err := runValidateHooks(logger, validateHooks, kindName, resourceName, ctx, bundle)
		if err != nil {
			return nil, fmt.Errorf("validate: %w", err)
		}
		if report := formatValidationReport(kindName, resourceName, issues); report != "" {
			return nil, errors.New(report)
		}
	}

	// Re-stamp every skip_hooks override so the rendered output is the
	// user's bytes verbatim, regardless of what the pipeline did to the
	// in-memory copy.
	for path, content := range frozen {
		f, ok := bundle[path]
		if !ok {
			// Override was for a path that isn't in the bundle anymore
			// (deleted by a hook). Re-introduce it under the same key
			// so the user's content still lands in the output.
			bundle[path] = hook.File{Path: path, Content: content}
			continue
		}
		f.Content = content
		f.Deleted = false
		bundle[path] = f
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

// applyOverrides resolves every metadata.overrides entry on r against
// the project FS and stamps the override's bytes onto the matching
// bundle entry. The override path is resolved relative to the resource
// file's directory (or used as-is when absolute). Returns the set of
// override paths whose `skip_hooks` flag is set, mapped to their
// content — callers re-stamp those after the hook pipeline runs so
// any in-flight mutations are discarded.
func applyOverrides(fsys fs.FS, r *resource.Resource, bundle hook.Bundle) (map[string]string, error) {
	overrides := r.GetMetadata().GetOverrides()
	if len(overrides) == 0 {
		return nil, nil
	}
	resourceDir := path.Dir(r.Path)
	frozen := make(map[string]string)
	for i, ov := range overrides {
		source := ov.GetSource()
		if source == "" {
			return nil, fmt.Errorf("overrides[%d]: source is required", i)
		}
		if _, ok := bundle[source]; !ok {
			return nil, fmt.Errorf("overrides[%d]: source %q is not declared by the kind", i, source)
		}
		ovPath := ov.GetPath()
		if ovPath == "" {
			return nil, fmt.Errorf("overrides[%d] (%s): path is required", i, source)
		}
		// Resolve relative paths against the resource file's directory.
		// Absolute paths (e.g. "/etc/...") are passed through but io/fs
		// requires forward slashes and no leading slash, so trim it.
		resolved := ovPath
		if !path.IsAbs(resolved) {
			resolved = path.Join(resourceDir, resolved)
		}
		resolved = strings.TrimPrefix(resolved, "/")
		data, err := fs.ReadFile(fsys, resolved)
		if err != nil {
			return nil, fmt.Errorf("overrides[%d] (%s): reading %s: %w", i, source, ovPath, err)
		}
		entry := bundle[source]
		entry.Content = string(data)
		bundle[source] = entry
		if ov.GetSkipHooks() {
			frozen[source] = string(data)
		}
	}
	return frozen, nil
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
func applyDependency(parent *slog.Logger, bundle hook.Bundle, dep *veilv1.Dependency, consumerKind, consumerName, consumerPath string, consumerMap map[string]any, root string, opts *Options) (hook.Bundle, error) {
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
		"path":     consumerPath,
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

// compileResourceHook bundles a resource-level hook on the fly. Paths
// are resolved relative to the resource file's directory (resourceDir).
// The compiled Hook carries the access info copied from the
// definition so the runner can pre-flight env access the same way it
// does for kind-bundled hooks.
func compileResourceHook(fsys fs.FS, resourceDir, hookPath string, access *veilv1.HookAccess) (*veilv1.Hook, error) {
	entrypoint := hookPath
	if !path.IsAbs(entrypoint) {
		entrypoint = path.Join(resourceDir, entrypoint)
	}
	entrypoint = strings.TrimPrefix(entrypoint, "/")
	code, err := bundle.Bundle(entrypoint, fsys, &bundle.Options{
		Minify:     true,
		GlobalName: "__veilMod",
	})
	if err != nil {
		return nil, fmt.Errorf("bundling %s: %w", hookPath, err)
	}
	return &veilv1.Hook{
		Name:    hookPath,
		Content: code,
		Access:  access,
	}, nil
}

// runValidateHooks invokes every validate hook on the kind, collecting
// each one's reported issues plus any thrown errors. A throw from one
// hook does not abort the loop — the runner records it as an
// error-severity issue and keeps going so the user sees every problem
// at once.
func runValidateHooks(parent *slog.Logger, hooks []*veilv1.Hook, kindName, resourceName string, ctx any, bdl hook.Bundle) ([]hook.ValidationIssue, error) {
	parent.Info("running validate hooks", "count", len(hooks))
	var issues []hook.ValidationIssue
	for _, h := range hooks {
		hookIssues, err := invokeValidateHook(parent, h, kindName, resourceName, ctx, bdl)
		if err != nil {
			// Aggregate the throw as a single error-severity issue,
			// then keep iterating so subsequent hooks still get a
			// chance to report.
			issues = append(issues, hook.ValidationIssue{
				Message:  fmt.Sprintf("validate hook %s threw: %s", h.GetName(), err),
				Severity: "error",
			})
			continue
		}
		issues = append(issues, hookIssues...)
	}
	return issues, nil
}

// formatValidationReport returns a single aggregated message
// describing every error-severity issue across the validate hooks.
// Warning-severity issues are surfaced through the interactive
// printer (so they're visible to the user) but do not cause the
// render to fail. Returns "" when there are no error-severity issues.
func formatValidationReport(kindName, resourceName string, issues []hook.ValidationIssue) string {
	if len(issues) == 0 {
		return ""
	}
	var errs, warns []hook.ValidationIssue
	for _, i := range issues {
		if i.Severity == "warning" {
			warns = append(warns, i)
		} else {
			errs = append(errs, i)
		}
	}
	if len(warns) > 0 {
		p := interact.Default()
		for _, w := range warns {
			loc := ""
			if w.Path != "" {
				loc = " [" + w.Path + "]"
			}
			p.Warnf("WARN [%s/%s] validate%s: %s", kindName, resourceName, loc, w.Message)
		}
	}
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation issue(s):\n", len(errs))
	for _, e := range errs {
		loc := ""
		if e.Path != "" {
			loc = " [" + e.Path + "]"
		}
		fmt.Fprintf(&b, "  - %s: %s%s\n", kindName+"/"+resourceName, e.Message, loc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// invokeValidateHook is the validate-lifecycle twin of invokeHook —
// same construction + env resolution, but it calls ValidateHook
// instead and returns the issue list. Any FS or ctx mutations the
// hook makes inside the runtime are not read back out: the only
// observable output is the issues slice.
// cwdFromCtx pulls the project root out of a hook context map. Every
// context built in this package carries "root" (the directory housing
// veil.json), which the hook runtime mounts as its filesystem root.
func cwdFromCtx(ctx any) string {
	if m, ok := ctx.(map[string]any); ok {
		if root, ok := m["root"].(string); ok {
			return root
		}
	}
	return ""
}

func invokeValidateHook(parent *slog.Logger, h *veilv1.Hook, kindName, resourceName string, ctx any, bdl hook.Bundle) ([]hook.ValidationIssue, error) {
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

	logger.Info("running validate hook")
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
		hook.WithCwd(cwdFromCtx(ctx)),
	)
	if err != nil {
		logger.Error("validate hook failed", "stage", "init", "duration", time.Since(start).String(), "err", err.Error())
		return nil, err
	}
	defer hk.Close()

	issues, err := hk.ValidateHook(ctx, bdl)
	if err != nil {
		logger.Error("validate hook failed", "stage", "validate", "duration", time.Since(start).String(), "err", err.Error())
		return nil, err
	}
	logger.Info("validate hook completed", "duration", time.Since(start).String(), "issues", len(issues))
	return issues, nil
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
		hook.WithCwd(cwdFromCtx(ctx)),
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
		// reads "services/api/staging.json". Source format is detected
		// from the overlay path — .yaml/.yml overlays go through the
		// yaml.v3 decoder, others through encoding/json.
		overlayPath := path.Join(path.Dir(r.Path), filepath.ToSlash(ov.GetFile()))
		var overlayDoc struct {
			Spec map[string]any `json:"spec" yaml:"spec"`
		}
		if err := protoencode.ReadFS(fsys, overlayPath, &overlayDoc); err != nil {
			return nil, fmt.Errorf("reading overlay %s: %w", overlayPath, err)
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
	var root map[string]any
	if err := registry.ReadResource(schemaPath, &root); err != nil {
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
	var schemaDoc any
	if err := registry.ReadResource(schemaPath, &schemaDoc); err != nil {
		return fmt.Errorf("reading schema %s: %w", schemaPath, err)
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
//
// outDir is created lazily — only when a file actually lands inside it.
// A kind whose layout hook reroutes every file via `../foo.yaml` (so the
// final write lands one level up) is common, and creating outDir upfront
// in that case leaves an empty orphan subdirectory next to the real
// output. After the write finishes, any outDir that's still empty is
// removed.
func writeBundle(outDir string, bundle hook.Bundle) ([]string, error) {
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

	// If every file routed itself out of outDir (via setOutputPath with
	// a `../`-prefixed destination), the only thing that landed there
	// is whatever per-file MkdirAll incidentally created — at most the
	// outDir itself. Remove it if it's empty so callers don't see an
	// orphan directory next to their real output.
	if entries, err := os.ReadDir(outDir); err == nil && len(entries) == 0 {
		_ = os.Remove(outDir)
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
