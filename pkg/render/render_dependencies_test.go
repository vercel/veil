package render

import (
	"os"
	"path/filepath"

	"github.com/vercel/veil/pkg/registry"
)

// dependentHookIIFE returns a pre-bundled dependent hook that stamps a
// marker file recording which target/consumer pair it ran for — proof
// that a given (target, consumer) hop actually fired, and with what
// ctx.self/ctx.consumer the runtime handed it.
func dependentHookIIFE(markerFile string) string {
	return `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.add("` + markerFile + `","target="+ctx.self.metadata.name+" consumer="+ctx.consumer.metadata.name);return fs;}};return{default:h};})();`
}

// dependentHookIIFEKeyedByParam returns a pre-bundled dependent hook
// that stamps a marker file per invocation, named after one of the
// edge's own params rather than a fixed path. ctx.consumer is always
// the render root (see applyDependencies in render.go) — identical
// for every edge into a shared target — so params are the only
// signal that still varies per edge; a target reached through more
// than one incoming edge (a diamond dependency) writes one such file
// per edge instead of one shared file the second firing would
// silently overwrite — proof that each edge's hook ran independently
// rather than the target's node-level dedup suppressing repeat edges.
func dependentHookIIFEKeyedByParam(prefix, paramKey string) string {
	return `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.add("` + prefix + `-via-"+ctx.params.` + paramKey + `+".txt","target="+ctx.self.metadata.name+" consumer="+ctx.consumer.metadata.name+" param="+ctx.params.` + paramKey + `);return fs;}};return{default:h};})();`
}

// noopDependentHookIIFE is a dependent hook that satisfies a required
// (target kind, consumer kind) pairing without writing anything —
// used where a test needs an edge to be valid but has nothing to
// assert about that edge itself.
const noopDependentHookIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){return fs;}};return{default:h};})();`

// permissiveDependencySchema is a minimal kind.schema.json with no
// additionalProperties:false at the top level, so a resource of this
// kind may carry a `dependencies` array without the fixture needing to
// mirror the real build-time discriminated-union schema.
var permissiveDependencySchema = map[string]any{
	"$schema":  "https://json-schema.org/draft/2020-12/schema",
	"type":     "object",
	"required": []string{"metadata", "spec"},
	"properties": map[string]any{
		"metadata": map[string]any{"type": "object"},
		"spec":     map[string]any{"type": "object"},
	},
}

// writeSimpleKind writes a compiled kind with no hooks at all — used
// for the root of a dependency walk that only needs to declare
// `dependencies`, not accept any itself.
func (s *RenderSuite) writeSimpleKind(name string) {
	kindDir := filepath.Join(s.root, "r", name)
	s.Require().NoError(os.MkdirAll(kindDir, 0755))
	s.writeJSON(filepath.Join(kindDir, "kind.json"), map[string]any{
		"name":    name,
		"sources": compiledSources(nil, nil),
	})
	s.writeJSON(filepath.Join(kindDir, "kind.schema.json"), permissiveDependencySchema)
}

// writeDependentKind writes a compiled kind (no render hooks of its
// own) that accepts dependents from consumerKind, running hookContent
// against the consumer's bundle. Used to build the target side of a
// dependency edge without dragging in the full kind fixture used by
// the "worker" suite-wide setup.
func (s *RenderSuite) writeDependentKind(name, consumerKind, hookContent string) {
	kindDir := filepath.Join(s.root, "r", name)
	s.Require().NoError(os.MkdirAll(kindDir, 0755))

	compiled := map[string]any{
		"name":    name,
		"sources": compiledSources(nil, nil),
		"hooks": map[string]any{
			"dependents": []map[string]any{
				{
					"kind": consumerKind,
					"hooks": []map[string]any{
						{"name": "hooks/inject.ts", "content": hookContent},
					},
					"params_schema": "{}",
				},
			},
		},
	}
	s.writeJSON(filepath.Join(kindDir, "kind.json"), compiled)
	s.writeJSON(filepath.Join(kindDir, "kind.schema.json"), permissiveDependencySchema)
}

// reloadRegistryWithKinds rewrites registry.json to cover the
// suite-wide "worker" kind plus every kind name passed in (each
// written via writeDependentKind), then reloads s.registry so the
// render pipeline can see them.
func (s *RenderSuite) reloadRegistryWithKinds(extraKinds ...string) {
	kinds := map[string]any{
		"worker": map[string]any{
			"name":   "worker",
			"path":   "./worker/kind.json",
			"schema": "./worker/kind.schema.json",
		},
	}
	for _, k := range extraKinds {
		kinds[k] = map[string]any{
			"name":   k,
			"path":   "./" + k + "/kind.json",
			"schema": "./" + k + "/kind.schema.json",
		}
	}
	regJSON := filepath.Join(s.root, "r", "registry.json")
	s.writeJSON(regJSON, map[string]any{"kinds": kinds})
	reg, err := registry.Load([]registry.Reference{{Path: regJSON}})
	s.Require().NoError(err)
	s.registry = reg
}

// renderKind is renderWorker's generalization for kinds other than
// "worker" — the dependency tests need three cooperating kinds, not
// just the suite-wide one.
func (s *RenderSuite) renderKind(kind, name, dir, outDir string) (*RenderedResource, error) {
	fsys, cat := s.catalogFor(dir)
	return Render(&Options{
		Kind:      kind,
		Name:      name,
		OutDir:    outDir,
		FS:        fsys,
		Catalog:   cat,
		Variables: map[string]any{},
	})
}

// TestMultiHopDependencyFollowsForwarding is the scenario from
// PLAT-8321: a service depends on package/api-rate-limits, which itself
// depends on dynamo-table/rate-limit-exceeded. The table's dependent
// hook reaches the service only because the package forwards that edge
// — the package is declaring the table part of what it offers. Run with
// forward off, the same graph leaves the service untouched by the
// table, which is the whole point of the flag.
func (s *RenderSuite) TestMultiHopDependencyFollowsForwarding() {
	s.writeSimpleKind("service")
	s.writeDependentKind("package", "service", dependentHookIIFE("from-package.txt"))
	s.writeDependentKind("dynamo-table", "service", dependentHookIIFE("from-dynamo.txt"))
	s.reloadRegistryWithKinds("service", "package", "dynamo-table")

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-service.json"), map[string]any{
		"metadata": map[string]any{"kind": "service", "name": "my-service"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "package", "name": "api-rate-limits", "params": map[string]any{}},
		},
	})
	s.writeJSON(filepath.Join(dir, "api-rate-limits.json"), map[string]any{
		"metadata": map[string]any{"kind": "package", "name": "api-rate-limits"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "dynamo-table", "name": "rate-limit-exceeded", "params": map[string]any{}, "forward": true},
		},
	})
	s.writeJSON(filepath.Join(dir, "rate-limit-exceeded.json"), map[string]any{
		"metadata": map[string]any{"kind": "dynamo-table", "name": "rate-limit-exceeded"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	rendered, err := s.renderKind("service", "my-service", dir, out)
	s.Require().NoError(err)
	s.Equal("my-service", rendered.Name)

	fromPackage, err := os.ReadFile(filepath.Join(out, "my-service", "from-package.txt"))
	s.Require().NoError(err)
	s.Equal("target=api-rate-limits consumer=my-service", string(fromPackage))

	// Only present because the package forwards its table.
	fromDynamo, err := os.ReadFile(filepath.Join(out, "my-service", "from-dynamo.txt"))
	s.Require().NoError(err)
	s.Equal("target=rate-limit-exceeded consumer=my-service", string(fromDynamo))
}

// TestUnforwardedDependencyStaysPrivate is the other half: the same
// three-resource chain with forwarding off. The service gets the
// package's hooks and nothing else — the table the package happens to
// use is none of the service's business.
func (s *RenderSuite) TestUnforwardedDependencyStaysPrivate() {
	s.writeSimpleKind("service")
	s.writeDependentKind("package", "service", dependentHookIIFE("from-package.txt"))
	s.writeDependentKind("dynamo-table", "service", dependentHookIIFE("from-dynamo.txt"))
	s.reloadRegistryWithKinds("service", "package", "dynamo-table")

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-service.json"), map[string]any{
		"metadata": map[string]any{"kind": "service", "name": "my-service"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "package", "name": "api-rate-limits", "params": map[string]any{}},
		},
	})
	s.writeJSON(filepath.Join(dir, "api-rate-limits.json"), map[string]any{
		"metadata": map[string]any{"kind": "package", "name": "api-rate-limits"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "dynamo-table", "name": "rate-limit-exceeded", "params": map[string]any{}},
		},
	})
	s.writeJSON(filepath.Join(dir, "rate-limit-exceeded.json"), map[string]any{
		"metadata": map[string]any{"kind": "dynamo-table", "name": "rate-limit-exceeded"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("service", "my-service", dir, out)
	s.Require().NoError(err)

	s.FileExists(filepath.Join(out, "my-service", "from-package.txt"))
	s.NoFileExists(filepath.Join(out, "my-service", "from-dynamo.txt"))
}

// TestDependencyCycleIsRejected covers the cycle case: alpha depends on
// beta and beta depends back on alpha. The catalog links the whole
// graph up front, so the loop is caught there — the render fails before
// any hook runs, naming the chain that closes it rather than looping or
// silently applying an arbitrary subset of the edges.
func (s *RenderSuite) TestDependencyCycleIsRejected() {
	s.writeDependentKind("alpha", "alpha", dependentHookIIFE("from-alpha.txt"))
	s.writeDependentKind("beta", "alpha", dependentHookIIFE("from-beta.txt"))
	s.reloadRegistryWithKinds("alpha", "beta")

	dir := filepath.Join(s.root, "cyc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "a1.json"), map[string]any{
		"metadata":     map[string]any{"kind": "alpha", "name": "a1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "beta", "name": "b1", "params": map[string]any{}}},
	})
	s.writeJSON(filepath.Join(dir, "b1.json"), map[string]any{
		"metadata":     map[string]any{"kind": "beta", "name": "b1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "alpha", "name": "a1", "params": map[string]any{}}},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("alpha", "a1", dir, out)
	s.Require().Error(err)
	s.Contains(err.Error(), "dependency cycle")
	s.Contains(err.Error(), "alpha/a1 -> beta/b1 -> alpha/a1")
	// Nothing was rendered — the failure precedes every hook.
	s.NoDirExists(filepath.Join(out, "a1"))
}

// TestDiamondDependencyFiresTargetHookOncePerIncomingEdge covers a
// target reached by two forwarded edges: root depends on branch/b1 and
// branch/c1, and both branches forward the same leaf/d1. The leaf is
// resolved once, but its dependent hook fires once per incoming edge —
// deduping targets is about not resolving one twice, not about dropping
// an edge. ctx.consumer is the render root for both firings, identical
// either way, so the two are distinguished by their own params, the one
// thing that still varies per edge.
func (s *RenderSuite) TestDiamondDependencyFiresTargetHookOncePerIncomingEdge() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeDependentKind("diamond-leaf", "diamond-root", dependentHookIIFEKeyedByParam("from-leaf", "tag"))
	s.reloadRegistryWithKinds("diamond-root", "diamond-branch", "diamond-leaf")

	dir := filepath.Join(s.root, "dmd")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-root", "name": "r1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "diamond-branch", "name": "b1", "params": map[string]any{}},
			{"kind": "diamond-branch", "name": "c1", "params": map[string]any{}},
		},
	})
	s.writeJSON(filepath.Join(dir, "b1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-branch", "name": "b1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "b1"}, "forward": true},
		},
	})
	s.writeJSON(filepath.Join(dir, "c1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-branch", "name": "c1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "c1"}, "forward": true},
		},
	})
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	rendered, err := s.renderKind("diamond-root", "r1", dir, out)
	s.Require().NoError(err)
	s.Equal("r1", rendered.Name)

	// Both edges into d1 must have run: one marker file per incoming
	// edge, not one shared file the second firing silently overwrote.
	viaB, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-b1.txt"))
	s.Require().NoError(err)
	s.Equal("target=d1 consumer=r1 param=b1", string(viaB))

	viaC, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-c1.txt"))
	s.Require().NoError(err)
	s.Equal("target=d1 consumer=r1 param=c1", string(viaC))
}
