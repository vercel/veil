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

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/build"
	"github.com/vercel/veil/pkg/bundle"
	"github.com/vercel/veil/pkg/codec"
	"github.com/vercel/veil/pkg/hook"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/resource"
	"github.com/vercel/veil/pkg/vfs"
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

	// FS is the read-only project filesystem, rooted at the directory
	// housing veil.json. Used to read overlay files, schemas, and any
	// other auxiliary content the render pipeline pulls in. CLI passes
	// reg.FS(); tests may wrap an fstest.MapFS.
	//
	// Its Root is threaded through to the hook context as `ctx.root` and
	// handed to each hook runtime as its filesystem root (QuickJS mounts
	// it at "/"), so `ctx.std` / `ctx.os` paths resolve against the
	// project root regardless of where the user invoked `veil render`
	// from. Passed straight to the runtime — never via the host
	// process's working directory — so renders are safe to run
	// concurrently. When Root is empty, it falls back to the caller's CWD.
	FS vfs.FS

	// Catalog resolves the entry-point resource and any dependency
	// targets by (kind, name). Built from the same FS, lazy and
	// cached.
	Catalog resource.Catalog

	// Variables is the resolved map of input variable values, keyed by
	// name. Each variable's stringified value is what an overlay's `if`
	// regex matches against; hooks receive the same map as `ctx.vars`.
	Variables map[string]any
}

// RenderedResource describes one successfully rendered resource.
type RenderedResource struct {
	// Kind retains the registry-qualified reference used for root ownership.
	Kind   string
	Bundle hook.Bundle
	Name   string
	OutDir string
	Files  []string
}

// Render renders the resource identified by (opts.Kind, opts.Name).
// The catalog supplies the entry-point resource and any dependency
// targets it reaches. Overlays and dependent hooks fan out from this
// single starting point.
func Render(opts *Options) (*RenderedResource, error) {
	rendered, err := Compute(opts)
	if err != nil {
		return nil, err
	}
	files, err := writeBundle(rendered.OutDir, rendered.Bundle)
	if err != nil {
		return nil, err
	}
	rendered.Files = files
	return rendered, nil
}

// Compute runs the complete render pipeline without publishing any files.
// Its result can be collected with other roots for one managed publication.
func Compute(opts *Options) (*RenderedResource, error) {
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
	root := opts.FS.Root()
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

	// The catalog resolved this when it loaded the resource.
	loaded := r.Kind
	kind := loaded.Kind

	logger.Debug("applying overlays", "count", len(r.GetMetadata().GetOverlays()))
	mergedSpec, err := applyOverlays(opts.FS, r, opts.Variables)
	if err != nil {
		return nil, fmt.Errorf("overlays: %w", err)
	}

	applySchemaDefaults(mergedSpec, loaded.SpecSchema)

	// Build the post-overlay resource that downstream code (validator +
	// hook ctx) operates on: clone the original, replace its spec with the
	// merged+defaulted result, and drop overlays since they've already
	// been applied.
	resolved, err := resolveResource(r.Resource, mergedSpec)
	if err != nil {
		return nil, fmt.Errorf("building resolved resource: %w", err)
	}

	// The map form of the resolved resource feeds both schema validation
	// and the hook ctx — encode it once.
	resourceMap, err := resourceToMap(resolved)
	if err != nil {
		return nil, fmt.Errorf("encoding resource: %w", err)
	}

	logger.Debug("validating spec against schema")
	if err := loaded.Validate(resourceMap); err != nil {
		return nil, fmt.Errorf("schema validation: %w", err)
	}

	// Promote the compiled sources to the identity → File structure that
	// flows through the hook pipeline. Identity starts as the declared
	// source path; hooks may remap the destination via File.setOutputPath
	// without changing identity.
	bundle := make(hook.Bundle, len(kind.Sources))
	for _, src := range loaded.Sources {
		bundle[src.GetPath()] = sourceFile(src, src.GetPath())
	}
	targets, order, err := groupDependencies(r, opts)
	if err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}
	if err := instantiateDependencies(bundle, kindName, targets, order); err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}

	// Apply local overrides before any hook runs so hooks see the
	// user's content. `frozen` records the override paths whose
	// skip_hooks flag is set — those get re-stamped after the pipeline
	// finishes so any hook mutations to them are discarded.
	frozen, err := applyOverrides(opts.FS, r, bundle)
	if err != nil {
		return nil, fmt.Errorf("applying overrides: %w", err)
	}

	// Pre-render schema gate: every schema-declared source's initial
	// content must parse and validate before any hook runs. Checked in
	// one pass and reported together, same as the validate lifecycle.
	if report := formatValidationReport(kindName, resourceName, validateSchemaSources(bundle)); report != "" {
		return nil, errors.New(report)
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
		newBundle, err := invokeHook(logger, h, r, ctx, bundle, opts.Catalog)
		if err != nil {
			return nil, fmt.Errorf("hook %s: %w", h.GetName(), err)
		}
		bundle = newBundle
	}

	if deps := resolved.GetDependencies(); len(deps) > 0 {
		logger.Info("resolving dependency graph", "declared_dependencies", len(deps))
	}
	newBundle, err := applyDependencies(logger, bundle, r, resolved, resourceMap, root, opts, targets, order)
	if err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}
	bundle = newBundle

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
			newBundle, err := invokeHook(logger, compiled, r, ctx, bundle, opts.Catalog)
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
			newBundle, err := invokeHook(logger, h, r, ctx, bundle, opts.Catalog)
			if err != nil {
				return nil, fmt.Errorf("post_render hook %s: %w", h.GetName(), err)
			}
			bundle = newBundle
		}
	}

	// Validation hooks: run after every other lifecycle point. Every
	// hook runs regardless of failures, the runner discards its
	// FS/ctx mutations, and the aggregated issue list fails the render
	// at the end if any error-severity issues come back. Schema-declared
	// sources are already valid at this point — every access validated
	// synchronously as it happened.
	var issues []hook.ValidationIssue
	if validateHooks := kind.GetHooks().GetValidate(); len(validateHooks) > 0 {
		hookIssues, err := runValidateHooks(logger, validateHooks, r, ctx, bundle, opts.Catalog)
		if err != nil {
			return nil, fmt.Errorf("validate: %w", err)
		}
		issues = append(issues, hookIssues...)
	}
	if report := formatValidationReport(kindName, resourceName, issues); report != "" {
		return nil, errors.New(report)
	}

	// Re-stamp every skip_hooks override so the rendered output is the
	// user's bytes verbatim, regardless of what the pipeline did to the
	// in-memory copy.
	for path, original := range frozen {
		f, ok := bundle[path]
		if !ok {
			f = original
		}
		f.Content = original.Content
		f.Deleted = false
		bundle[path] = f
	}

	outDir := filepath.Join(opts.OutDir, resourceName)
	files := make([]string, 0, len(bundle))
	for key, file := range bundle {
		if file.Deleted {
			continue
		}
		destination := file.Path
		if destination == "" {
			destination = key
		}
		files = append(files, destination)
	}
	sort.Strings(files)

	return &RenderedResource{
		Kind:   kindName,
		Bundle: bundle,
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
func applyOverrides(fsys vfs.FS, r *resource.Resource, bundle hook.Bundle) (map[string]hook.File, error) {
	overrides := r.GetMetadata().GetOverrides()
	if len(overrides) == 0 {
		return nil, nil
	}
	resourceDir := path.Dir(r.Path)
	frozen := make(map[string]hook.File)
	for i, ov := range overrides {
		source := ov.GetSource()
		if source == "" {
			return nil, fmt.Errorf("overrides[%d]: source is required", i)
		}
		if _, ok := bundle[source]; !ok {
			return nil, fmt.Errorf("overrides[%d]: source %q is not present in the root's sources", i, source)
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
			frozen[source] = entry
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

// resourceToMap flattens a Resource into a generic map, suitable for
// embedding in the hook ctx (which round-trips through goccy/go-json).
// codec.Convert marshals via protojson so the map is keyed by proto
// field names.
func resourceToMap(r *veilv1.Resource) (map[string]any, error) {
	var out map[string]any
	if err := codec.Convert(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// depNode is one visited node in the transitive dependency walk: the
// resource's identity plus its resolved (overlay + schema-defaulted)
// form and map encoding, cached so a target reached through multiple
// consumer paths is only loaded and resolved once.
type depNode struct {
	// res is the resource as loaded: its kind, name, path and compiled
	// kind all hang off it, so the node keeps no copies.
	res *resource.Resource
	// resolved is res with overlays and schema defaults applied —
	// computed once per node and reused across every edge reaching it.
	resolved    *veilv1.Resource
	resourceMap map[string]any
}

func depNodeID(kind, name string) string { return registry.DependencySourceID(kind, name, "") }

// applyDependencies applies the dependent hooks of every resource the
// render root depends on, against the root's own bundle.
//
// The set is already decided: the catalog resolved each resource's
// effective dependencies — its own declared edges plus whatever those
// targets forward up — so this is one pass over a flat list, not a
// graph traversal. A service that depends on a package sees the
// package's table only when the package forwards it.
//
// At every edge the "consumer" the target's hooks see is the render
// root, never an intermediate that forwarded the edge: the only bundle
// a dependent hook can mutate is the root's, so ctx.consumer names the
// resource that bundle belongs to. A target kind's `dependents` list is
// therefore keyed by the render-root kinds that can reach it.
func applyDependencies(parent *slog.Logger, bundle hook.Bundle, rootRes *resource.Resource, rootResolved *veilv1.Resource, rootMap map[string]any, root string, opts *Options, targets map[string]*depTarget, order []string) (hook.Bundle, error) {
	deps := rootRes.Dependencies
	if len(deps) == 0 {
		return bundle, nil
	}
	rootNode := &depNode{res: rootRes, resolved: rootResolved, resourceMap: rootMap}

	for _, id := range order {
		t := targets[id]
		logger := parent.With("dep_kind", t.node.res.GetMetadata().GetKind(), "dep_name", t.node.res.GetMetadata().GetName())
		newBundle, err := applyDependentHooks(logger, bundle, t, rootNode, root, opts)
		if err != nil {
			return nil, fmt.Errorf("dependency %s: %w", id, err)
		}
		bundle = newBundle
	}
	parent.Info("applied dependency graph", "targets", len(order), "edges", len(deps))
	return bundle, nil
}

// depTarget is one resource the root depends on, however many edges
// reach it, together with the single params object that applies.
type depTarget struct {
	node    *depNode
	params  *veilv1.Dependency
	direct  bool
	entry   *veilv1.DependentHook
	sources map[string]string
}

// instantiateDependencies adds all templates before overrides without running
// hooks. The prepared grouping is reused at the dependent lifecycle point.
func instantiateDependencies(bdl hook.Bundle, consumer string, targets map[string]*depTarget, order []string) error {
	for _, id := range order {
		target := targets[id]
		res := target.node.res
		for _, d := range res.Kind.GetHooks().GetDependents() {
			if d.GetKind() == consumer {
				target.entry = d
				break
			}
		}
		if target.entry == nil {
			return fmt.Errorf("target kind %q does not list %q as a valid consumer", res.GetMetadata().GetKind(), consumer)
		}
		target.sources = make(map[string]string)
		for _, src := range res.Kind.DependentSources[consumer] {
			key := registry.DependencySourceID(res.GetMetadata().GetKind(), res.GetMetadata().GetName(), src.GetPath())
			if _, exists := bdl[key]; exists {
				return fmt.Errorf("source identity collision: root source %q and dependency %s template %q", key, id, src.GetPath())
			}
			bdl[key] = sourceFile(src, key)
			target.sources[src.GetPath()] = key
		}
	}
	return nil
}

func sourceFile(src *registry.LoadedSource, identity string) hook.File {
	file := hook.File{Path: identity, Content: src.GetContents(), Type: hook.ContentPlaintext}
	if src.Validator != nil {
		file.Type = sourceContentType(src.GetPath())
		file.MustValidate = true
		file.ValidateContent = func(contents string) error {
			doc, err := build.ParseSourceContents(src.GetPath(), []byte(contents))
			if err != nil {
				return fmt.Errorf("source %q: parsing: %w", identity, err)
			}
			if err := src.Validate(doc); err != nil {
				return fmt.Errorf("source %q: %w", identity, err)
			}
			return nil
		}
	}
	return file
}

// groupDependencies collapses the root's effective edges to one entry
// per target and settles which params apply, returning the targets and
// the order to visit them in.
//
// A target can be reached more than once — declared directly and also
// inherited from something that forwards it, or inherited down two
// branches of a diamond. Firing its hooks once per edge would wire the
// root up several times with different configurations, and which one
// survived would come down to the order the dependencies happen to be
// listed in. So:
//
//   - the root's own declaration wins outright; no inherited edge has to
//     agree with it, since the root asked for it explicitly;
//   - otherwise every inherited edge must agree, and a disagreement with
//     no direct declaration to arbitrate is an error rather than a coin
//     flip.
func groupDependencies(rootRes *resource.Resource, opts *Options) (map[string]*depTarget, []string, error) {
	declared := make(map[*veilv1.Dependency]bool, len(rootRes.GetDependencies()))
	for _, d := range rootRes.GetDependencies() {
		declared[d] = true
	}
	directByTarget := make(map[string]*veilv1.Dependency)
	for _, dep := range rootRes.Dependencies {
		if declared[dep.Dependency] {
			id := depNodeID(dep.Resource.GetMetadata().GetKind(), dep.Resource.GetMetadata().GetName())
			if existing := directByTarget[id]; existing != nil && !proto.Equal(existing.GetParams(), dep.GetParams()) {
				return nil, nil, fmt.Errorf("%s: declares %s twice with different params — remove one of them", rootRes.Path, id)
			}
			directByTarget[id] = dep.Dependency
		}
	}

	targets := map[string]*depTarget{}
	var order []string
	for _, dep := range rootRes.Dependencies {
		target := dep.Resource
		kind, name := target.GetMetadata().GetKind(), target.GetMetadata().GetName()
		id := depNodeID(kind, name)
		direct := declared[dep.Dependency]
		selected := dep.Dependency
		if own := directByTarget[id]; own != nil {
			selected, direct = own, true
		}

		existing, seen := targets[id]
		if !seen {
			// Resolving a target (overlays + schema defaults) is the
			// expensive half, so it happens once however many edges
			// reach it.
			resolved, err := resolveTargetResource(target, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("dependency %s: resolving target: %w", id, err)
			}
			resolvedMap, err := resourceToMap(resolved)
			if err != nil {
				return nil, nil, fmt.Errorf("dependency %s: encoding target: %w", id, err)
			}
			targets[id] = &depTarget{
				node:   &depNode{res: target, resolved: resolved, resourceMap: resolvedMap},
				params: selected,
				direct: direct,
			}
			order = append(order, id)
			continue
		}

		switch {
		case direct && !existing.direct:
			// The root's own declaration displaces anything inherited.
			existing.params, existing.direct = selected, true
		case existing.direct && !direct:
			// Already settled by the root's declaration.
		case proto.Equal(existing.params.GetParams(), selected.GetParams()):
			// Both say the same thing, so there is nothing to settle.
		case direct && existing.direct:
			return nil, nil, fmt.Errorf(
				"%s: declares %s twice with different params — remove one of them",
				rootRes.Path, id)
		default:
			return nil, nil, fmt.Errorf(
				"%s: inherits %s along more than one path with conflicting params (%s vs %s), "+
					"and declares no dependency on it to settle which applies — "+
					"declare it directly, or make the forwarding resources agree",
				rootRes.Path, id,
				paramsSummary(existing.params.GetParams()), paramsSummary(dep.GetParams()))
		}
	}
	return targets, order, nil
}

// paramsSummary renders a params struct for an error message, with keys
// in a stable order.
func paramsSummary(params *structpb.Struct) string {
	if params == nil || len(params.GetFields()) == 0 {
		return "{}"
	}
	m := params.AsMap()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// applyDependentHooks invokes the grouped target prepared before overrides.
func applyDependentHooks(parent *slog.Logger, bundle hook.Bundle, target *depTarget, consumer *depNode, root string, opts *Options) (hook.Bundle, error) {
	paramsMap := map[string]any{}
	if p := target.params.GetParams(); p != nil {
		paramsMap = p.AsMap()
	}
	depCtx := map[string]any{
		"self":          target.node.resourceMap,
		"consumer":      consumer.resourceMap,
		"path":          consumer.res.Path,
		"params":        paramsMap,
		"vars":          opts.Variables,
		"root":          root,
		"__veilSources": target.sources,
	}

	for _, h := range target.entry.GetHooks() {
		newBundle, err := invokeHook(parent, h, target.node.res, depCtx, bundle, opts.Catalog)
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
	applySchemaDefaults(mergedSpec, r.Kind.SpecSchema)
	return resolveResource(r.Resource, mergedSpec)
}

// isYAMLSourcePath reports whether path's extension marks it as
// YAML-encoded (vs. JSON), the same detection applyOverlays already
// uses for overlay files.
func isYAMLSourcePath(p string) bool {
	return codec.IsYAML(p)
}

// validateSchemaSources parses and validates every schema-declared
// source's current content against its schema, one error-severity
// ValidationIssue per failure. This is the pre-render gate — checked
// against the initial bundle, before any hook runs. Corruption
// introduced mid-render is instead caught as it happens (every
// getContent/setContent validates synchronously, see WithSchemaValidate)
// so there's no separate check needed after hooks run.
func validateSchemaSources(bdl hook.Bundle) []hook.ValidationIssue {
	var issues []hook.ValidationIssue
	paths := make([]string, 0, len(bdl))
	for p := range bdl {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		file := bdl[p]
		if file.ValidateContent == nil {
			continue
		}
		if err := file.ValidateContent(file.Content); err != nil {
			issues = append(issues, hook.ValidationIssue{Path: p, Message: err.Error(), Severity: "error"})
		}
	}
	return issues
}

// compileResourceHook bundles a resource-level hook on the fly. Paths
// are resolved relative to the resource file's directory (resourceDir).
// The compiled Hook carries the access info copied from the
// definition so the runner can pre-flight env access the same way it
// does for kind-bundled hooks.
func compileResourceHook(fsys vfs.FS, resourceDir, hookPath string, access *veilv1.HookAccess) (*veilv1.Hook, error) {
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
func runValidateHooks(parent *slog.Logger, hooks []*veilv1.Hook, res *resource.Resource, ctx any, bdl hook.Bundle, catalog resource.Catalog) ([]hook.ValidationIssue, error) {
	parent.Info("running validate hooks", "count", len(hooks))
	var issues []hook.ValidationIssue
	for _, h := range hooks {
		hookIssues, err := invokeValidateHook(parent, h, res, ctx, bdl, catalog)
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

// sourceContentType maps a source path to the encoding its bytes use,
// the same extension rule build and the pre-render gate follow.
func sourceContentType(p string) hook.ContentType {
	if isYAMLSourcePath(p) {
		return hook.ContentYAML
	}
	return hook.ContentJSON
}

// sourceValidator resolves by stable bundle identity, never output destination
// or the identity of whichever target owns the executing hook.
func sourceValidator(bdl hook.Bundle) func(kind, name, path, contents string) error {
	return func(_, _, path, contents string) error {
		if validate := bdl[path].ValidateContent; validate != nil {
			return validate(contents)
		}
		return nil
	}
}

// invokeValidateHook is the validate-lifecycle twin of invokeHook —
// same construction + env resolution, but it calls ValidateHook
// instead and returns the issue list. Any FS or ctx mutations the
// hook makes inside the runtime are not read back out: the only
// observable output is the issues slice.
func invokeValidateHook(parent *slog.Logger, h *veilv1.Hook, res *resource.Resource, ctx any, bdl hook.Bundle, catalog resource.Catalog) ([]hook.ValidationIssue, error) {
	kindName := res.GetMetadata().GetKind()
	resourceName := res.GetMetadata().GetName()
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
		hook.WithResource(kindName, resourceName),
		hook.WithSourceValidator(sourceValidator(bdl)),
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
// the user's terminal via interact.Default(). Each bundle entry carries
// its declaring schema independently of the hook's target identity.
func invokeHook(parent *slog.Logger, h *veilv1.Hook, res *resource.Resource, ctx any, bundle hook.Bundle, catalog resource.Catalog) (hook.Bundle, error) {
	kindName := res.GetMetadata().GetKind()
	resourceName := res.GetMetadata().GetName()
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
		hook.WithResource(kindName, resourceName),
		hook.WithSourceValidator(sourceValidator(bundle)),
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
func applyOverlays(fsys vfs.FS, r *resource.Resource, vars map[string]any) (map[string]any, error) {
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
		if err := codec.ReadFS(fsys, overlayPath, &overlayDoc); err != nil {
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
		path = filepath.Clean(path)
		if prev, ok := usedPaths[path]; ok {
			return nil, fmt.Errorf("path collision: identities %q and %q both resolve to %q", prev, id, path)
		}
		usedPaths[path] = id
		pathsOut = append(pathsOut, path)

	}
	// Preflight every collision before the first write, including a file
	// occupying another entry's parent directory.
	for destination, id := range usedPaths {
		for parent := filepath.Dir(destination); parent != "." && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if prev, ok := usedPaths[parent]; ok {
				return nil, fmt.Errorf("path collision: identities %q and %q resolve to file/directory paths %q and %q", prev, id, parent, destination)
			}
		}
	}
	for _, destination := range pathsOut {
		fullPath := filepath.Join(outDir, destination)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", filepath.Dir(fullPath), err)
		}
		if err := os.WriteFile(fullPath, []byte(bundle[usedPaths[destination]].Content), 0644); err != nil {
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
