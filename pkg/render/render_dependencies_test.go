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

// dependentHookIIFEKeyedByConsumer returns a pre-bundled dependent hook
// that stamps a marker file per invocation, named after the immediate
// consumer rather than a fixed path. A target reached through more
// than one incoming edge (a diamond dependency) writes one such file
// per edge instead of one shared file the second firing would
// silently overwrite — proof that each edge's hook ran independently
// rather than the target's node-level dedup suppressing repeat edges.
func dependentHookIIFEKeyedByConsumer(prefix string) string {
	return `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.add("` + prefix + `-via-"+ctx.consumer.metadata.name+".txt","target="+ctx.self.metadata.name+" consumer="+ctx.consumer.metadata.name);return fs;}};return{default:h};})();`
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
		"sources": map[string]string{},
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
		"sources": map[string]string{},
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
		Registry:  s.registry,
		Catalog:   cat,
		Variables: map[string]any{},
	})
}

// TestMultiHopDependencyAppliesTransitiveDependentHooks is the direct
// regression test for the scenario in PLAT-8321: a service depends on
// package/api-rate-limits, which itself depends on
// dynamo-table/rate-limit-exceeded. The dynamo-table dependent hook
// must fire against the service's bundle even though the service never
// declares dynamo-table as a direct dependency — veil's core walks the
// full transitive graph, not just the root's own `dependencies` list.
func (s *RenderSuite) TestMultiHopDependencyAppliesTransitiveDependentHooks() {
	s.writeSimpleKind("service")
	s.writeDependentKind("package", "service", dependentHookIIFE("from-package.txt"))
	s.writeDependentKind("dynamo-table", "package", dependentHookIIFE("from-dynamo.txt"))
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
	rendered, err := s.renderKind("service", "my-service", dir, out)
	s.Require().NoError(err)
	s.Equal("my-service", rendered.Name)

	fromPackage, err := os.ReadFile(filepath.Join(out, "my-service", "from-package.txt"))
	s.Require().NoError(err)
	s.Equal("target=api-rate-limits consumer=my-service", string(fromPackage))

	// This file only exists if the walk continues past the service's
	// direct dependency into the package's own dependency on
	// dynamo-table — the regression this test guards against.
	fromDynamo, err := os.ReadFile(filepath.Join(out, "my-service", "from-dynamo.txt"))
	s.Require().NoError(err)
	s.Equal("target=rate-limit-exceeded consumer=api-rate-limits", string(fromDynamo))
}

// TestDependencyCycleAppliesEachEdgeOnceWithoutInfiniteLoop covers the
// cycle-safety half of the BFS walk: alpha depends on beta and beta
// depends back on alpha, and both kinds accept the other as a
// consumer. The walk must apply each real edge's hooks exactly once
// and terminate instead of looping forever re-visiting the same pair.
func (s *RenderSuite) TestDependencyCycleAppliesEachEdgeOnceWithoutInfiniteLoop() {
	s.writeDependentKind("alpha", "beta", dependentHookIIFE("from-alpha.txt"))
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
	rendered, err := s.renderKind("alpha", "a1", dir, out)
	s.Require().NoError(err)
	s.Equal("a1", rendered.Name)

	fromBeta, err := os.ReadFile(filepath.Join(out, "a1", "from-beta.txt"))
	s.Require().NoError(err)
	s.Equal("target=b1 consumer=a1", string(fromBeta))

	fromAlpha, err := os.ReadFile(filepath.Join(out, "a1", "from-alpha.txt"))
	s.Require().NoError(err)
	s.Equal("target=a1 consumer=b1", string(fromAlpha))
}

// TestDiamondDependencyFiresTargetHookOncePerIncomingEdge covers the
// other half of the multi-hop semantic the cycle test doesn't reach:
// a target with two independent incoming edges (root depends on both
// branch/b1 and branch/c1; both branches depend on the same
// leaf/d1). leaf/d1 is resolved once (the walk is visit-once for node
// expansion), but its dependent hook must still fire once per
// incoming edge — the visited-set dedup is about not re-expanding a
// node's own dependencies, not about suppressing repeat edges into
// it.
func (s *RenderSuite) TestDiamondDependencyFiresTargetHookOncePerIncomingEdge() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeDependentKind("diamond-leaf", "diamond-branch", dependentHookIIFEKeyedByConsumer("from-leaf"))
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
		"metadata":     map[string]any{"kind": "diamond-branch", "name": "b1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{}}},
	})
	s.writeJSON(filepath.Join(dir, "c1.json"), map[string]any{
		"metadata":     map[string]any{"kind": "diamond-branch", "name": "c1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{}}},
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
	s.Equal("target=d1 consumer=b1", string(viaB))

	viaC, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-c1.txt"))
	s.Require().NoError(err)
	s.Equal("target=d1 consumer=c1", string(viaC))
}
