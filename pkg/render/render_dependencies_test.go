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
	s.Equal("target=rate-limit-exceeded consumer=my-service", string(fromDynamo))
}

// TestDependencyCycleAppliesEachEdgeOnceWithoutInfiniteLoop covers the
// cycle-safety half of the BFS walk: alpha depends on beta and beta
// depends back on alpha. ctx.consumer is always the render root
// (alpha/a1), so alpha's own dependents list must accept its own kind
// to allow the back-edge — beta's back-edge into alpha is checked
// against the root's kind, not beta's. The walk must apply each real
// edge's hooks exactly once and terminate instead of looping forever
// re-visiting the same pair.
func (s *RenderSuite) TestDependencyCycleAppliesEachEdgeOnceWithoutInfiniteLoop() {
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
	rendered, err := s.renderKind("alpha", "a1", dir, out)
	s.Require().NoError(err)
	s.Equal("a1", rendered.Name)

	fromBeta, err := os.ReadFile(filepath.Join(out, "a1", "from-beta.txt"))
	s.Require().NoError(err)
	s.Equal("target=b1 consumer=a1", string(fromBeta))

	fromAlpha, err := os.ReadFile(filepath.Join(out, "a1", "from-alpha.txt"))
	s.Require().NoError(err)
	s.Equal("target=a1 consumer=a1", string(fromAlpha))
}

// TestDiamondDependencyWithAgreeingIndirectParamsAppliesHookOnce covers
// the half of PLAT-8324's policy that isn't a conflict at all: a target
// with two independent indirect edges (root depends on both
// branch/b1 and branch/c1; both branches depend on the same
// leaf/d1) that happen to agree on params. leaf/d1 is visited exactly
// once for the whole walk — both for node expansion and for dependent
// hook application — so agreeing params never need arbitration and the
// hook fires once, not once per incoming edge.
func (s *RenderSuite) TestDiamondDependencyWithAgreeingIndirectParamsAppliesHookOnce() {
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
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "shared"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "c1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-branch", "name": "c1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "shared"}},
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

	viaShared, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-shared.txt"))
	s.Require().NoError(err)
	s.Equal("target=d1 consumer=r1 param=shared", string(viaShared))
}

// TestConflictingIndirectDependencyParamsIsHardError covers the other
// half of PLAT-8324's policy: two indirect edges into the same target
// (root depends on both branch/b1 and branch/c1; both branches depend
// on the same leaf/d1) disagreeing on params, with no direct
// declaration from the root to arbitrate. There's no arbiter for two
// unrelated packages disagreeing about how a shared target should be
// depended on, so this is a hard render-time error rather than
// silently firing the hook twice or picking one edge's params over the
// other's.
func (s *RenderSuite) TestConflictingIndirectDependencyParamsIsHardError() {
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
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "b1"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "c1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-branch", "name": "c1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": "c1"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("diamond-root", "r1", dir, out)
	s.Require().Error(err)
	s.Contains(err.Error(), "diamond-leaf/d1")
	s.Contains(err.Error(), "conflicting params")

	s.NoFileExists(filepath.Join(out, "r1", "from-leaf-via-b1.txt"))
	s.NoFileExists(filepath.Join(out, "r1", "from-leaf-via-c1.txt"))
}

// TestRootDirectDependencyOverridesConflictingTransitiveParams is the
// direct regression test for PLAT-8324's "root wins" half: the render
// root declares override-target directly with its own params, and also
// reaches the same target transitively through override-package with
// different params. The root's own direct declaration is authoritative
// — the target's dependent hook fires once, with the root's params,
// and the package's conflicting indirect params never take effect (and
// never need to agree with the root's).
func (s *RenderSuite) TestRootDirectDependencyOverridesConflictingTransitiveParams() {
	s.writeSimpleKind("override-root")
	s.writeDependentKind("override-package", "override-root", noopDependentHookIIFE)
	s.writeDependentKind("override-target", "override-root", dependentHookIIFEKeyedByParam("from-target", "tag"))
	s.reloadRegistryWithKinds("override-root", "override-package", "override-target")

	dir := filepath.Join(s.root, "ovr")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{
		"metadata": map[string]any{"kind": "override-root", "name": "r1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "override-package", "name": "p1", "params": map[string]any{}},
			{"kind": "override-target", "name": "t1", "params": map[string]any{"tag": "root"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "p1.json"), map[string]any{
		"metadata": map[string]any{"kind": "override-package", "name": "p1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "override-target", "name": "t1", "params": map[string]any{"tag": "indirect"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "t1.json"), map[string]any{
		"metadata": map[string]any{"kind": "override-target", "name": "t1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	rendered, err := s.renderKind("override-root", "r1", dir, out)
	s.Require().NoError(err)
	s.Equal("r1", rendered.Name)

	viaRoot, err := os.ReadFile(filepath.Join(out, "r1", "from-target-via-root.txt"))
	s.Require().NoError(err)
	s.Equal("target=t1 consumer=r1 param=root", string(viaRoot))

	s.NoFileExists(filepath.Join(out, "r1", "from-target-via-indirect.txt"))
}
