package render

import (
	"encoding/json"
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

// TestDiamondWithAgreeingParamsFiresOnce covers a target reached down
// two branches of a diamond: both forward leaf/d1 and both pass the same
// params, so there is nothing to settle and the hook runs once.
func (s *RenderSuite) TestDiamondWithAgreeingParamsFiresOnce() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeDependentKind("diamond-leaf", "diamond-root", dependentHookIIFEKeyedByParam("from-leaf", "tag"))
	s.reloadRegistryWithKinds("diamond-root", "diamond-branch", "diamond-leaf")

	dir := s.writeDiamond(map[string]string{"b1": "shared", "c1": "shared"})
	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("diamond-root", "r1", dir, out)
	s.Require().NoError(err)

	body, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-shared.txt"))
	s.Require().NoError(err)
	s.Equal("target=d1 consumer=r1 param=shared", string(body))
}

// TestDiamondWithConflictingParamsIsAnError is the case with no arbiter:
// two branches forward the same leaf with different params and the root
// declares no dependency on it, so there is no basis to prefer either.
// Applying both would wire the root up twice; picking one silently would
// come down to declaration order.
func (s *RenderSuite) TestDiamondWithConflictingParamsIsAnError() {
	s.writeSimpleKind("diamond-root")
	s.writeDependentKind("diamond-branch", "diamond-root", noopDependentHookIIFE)
	s.writeDependentKind("diamond-leaf", "diamond-root", dependentHookIIFEKeyedByParam("from-leaf", "tag"))
	s.reloadRegistryWithKinds("diamond-root", "diamond-branch", "diamond-leaf")

	dir := s.writeDiamond(map[string]string{"b1": "from-b1", "c1": "from-c1"})
	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("diamond-root", "r1", dir, out)
	s.Require().Error(err)
	s.Contains(err.Error(), "diamond-leaf/d1")
	s.Contains(err.Error(), "conflicting params")
	s.Contains(err.Error(), "tag=from-b1")
	s.Contains(err.Error(), "tag=from-c1")
	s.NoDirExists(filepath.Join(out, "r1"))
}

// writeDiamond lays down root r1 depending on two branches, each
// forwarding leaf d1 with the tag given for that branch.
func (s *RenderSuite) writeDiamond(tagByBranch map[string]string) string {
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
	for _, branch := range []string{"b1", "c1"} {
		s.writeJSON(filepath.Join(dir, branch+".json"), map[string]any{
			"metadata": map[string]any{"kind": "diamond-branch", "name": branch},
			"spec":     map[string]any{},
			"dependencies": []map[string]any{
				{"kind": "diamond-leaf", "name": "d1", "params": map[string]any{"tag": tagByBranch[branch]}, "forward": true},
			},
		})
	}
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "diamond-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})
	return dir
}

// TestRootDirectDependencyWinsOverForwardedParams covers a target the
// render root reaches two ways: declared directly, and inherited from
// something it depends on that forwards the same target. Each edge
// carries its own params.
//
// Firing the dependent hook once per edge wires the root up twice with
// two different configurations — and which one a hook that sets a fixed
// key ends up with depends on the order the dependencies happen to be
// listed in. The root's own declaration is the one it asked for, so it
// wins, and the hook runs once.
func (s *RenderSuite) TestRootDirectDependencyWinsOverForwardedParams() {
	s.writeSimpleKind("dup-root")
	s.writeDependentKind("dup-mid", "dup-root", noopDependentHookIIFE)
	s.writeDependentKind("dup-leaf", "dup-root", dependentHookIIFEKeyedByParam("from-leaf", "tag"))
	s.reloadRegistryWithKinds("dup-root", "dup-mid", "dup-leaf")

	dir := filepath.Join(s.root, "dup")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{
		"metadata": map[string]any{"kind": "dup-root", "name": "r1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "dup-mid", "name": "m1", "params": map[string]any{}},
			{"kind": "dup-leaf", "name": "d1", "params": map[string]any{"tag": "direct"}},
		},
	})
	s.writeJSON(filepath.Join(dir, "m1.json"), map[string]any{
		"metadata": map[string]any{"kind": "dup-mid", "name": "m1"},
		"spec":     map[string]any{},
		"dependencies": []map[string]any{
			{"kind": "dup-leaf", "name": "d1", "params": map[string]any{"tag": "forwarded"}, "forward": true},
		},
	})
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "dup-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderKind("dup-root", "r1", dir, out)
	s.Require().NoError(err)

	direct, err := os.ReadFile(filepath.Join(out, "r1", "from-leaf-via-direct.txt"))
	s.Require().NoError(err, "the root's own params should be the ones applied")
	s.Contains(string(direct), "param=direct")
	s.NoFileExists(filepath.Join(out, "r1", "from-leaf-via-forwarded.txt"),
		"the forwarded params must not also be applied — one target, one firing")
}

// selfFSDependentHookIIFE copies a file out of the target kind's own FS
// into the consumer's bundle — the shape of a database handing a service
// the IAM policy that grants it access.
const selfFSDependentHookIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){
  var t=ctx.selfFS.get("assets/policy.tf");
  if(!t) throw new Error("selfFS should carry the target kind's own files");
  fs.add("terraform/"+ctx.self.metadata.name+".tf", String(t.getContent()).replace("NAME",ctx.self.metadata.name));
  return fs;}};return{default:h};})();`

// TestDependentHookReadsItsOwnFilesThroughSelfFS covers ctx.selfFS: a
// dependent hook runs against the consumer's bundle, but the file it
// wants belongs to its own kind.
func (s *RenderSuite) TestDependentHookReadsItsOwnFilesThroughSelfFS() {
	s.writeSimpleKind("selffs-root")
	s.writeDependentKind("selffs-leaf", "selffs-root", selfFSDependentHookIIFE)

	// Give the target kind an asset to hand over.
	kindPath := filepath.Join(s.root, "r", "selffs-leaf", "kind.json")
	raw, err := os.ReadFile(kindPath)
	s.Require().NoError(err)
	var kind map[string]any
	s.Require().NoError(json.Unmarshal(raw, &kind))
	kind["files"] = []map[string]any{
		{"path": "assets/policy.tf", "contents": "policy for NAME", "render": false},
	}
	s.writeJSON(kindPath, kind)
	s.reloadRegistryWithKinds("selffs-root", "selffs-leaf")

	dir := filepath.Join(s.root, "sf")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{
		"metadata":     map[string]any{"kind": "selffs-root", "name": "r1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "selffs-leaf", "name": "d1"}},
	})
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "selffs-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	_, err = s.renderKind("selffs-root", "r1", dir, out)
	s.Require().NoError(err)

	body, err := os.ReadFile(filepath.Join(out, "r1", "terraform", "d1.tf"))
	s.Require().NoError(err, "the hook should have copied its own file into the consumer")
	s.Equal("policy for d1", string(body))

	// The target's asset is not itself part of the consumer's output —
	// only what the hook explicitly added.
	s.NoFileExists(filepath.Join(out, "r1", "assets", "policy.tf"))
}

// TestSelfFSWritesDoNotEscape pins that selfFS is a reading surface: the
// runner takes back only the consumer's FS, so a hook scribbling on its
// own kind's files changes nothing anywhere.
func (s *RenderSuite) TestSelfFSWritesDoNotEscape() {
	hook := `var __veilMod=(()=>{var h={render:function(ctx,fs){
	  var t=ctx.selfFS.get("assets/policy.tf");
	  t.setContent("scribbled");
	  ctx.selfFS.add("assets/extra.tf", "should go nowhere");
	  fs.add("terraform/"+ctx.self.metadata.name+".tf", String(t.getContent()));
	  return fs;}};return{default:h};})();`
	s.writeSimpleKind("escape-root")
	s.writeDependentKind("escape-leaf", "escape-root", hook)

	kindPath := filepath.Join(s.root, "r", "escape-leaf", "kind.json")
	raw, err := os.ReadFile(kindPath)
	s.Require().NoError(err)
	var kind map[string]any
	s.Require().NoError(json.Unmarshal(raw, &kind))
	kind["files"] = []map[string]any{
		{"path": "assets/policy.tf", "contents": "original", "render": false},
	}
	s.writeJSON(kindPath, kind)
	s.reloadRegistryWithKinds("escape-root", "escape-leaf")

	dir := filepath.Join(s.root, "esc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "r1.json"), map[string]any{
		"metadata":     map[string]any{"kind": "escape-root", "name": "r1"},
		"spec":         map[string]any{},
		"dependencies": []map[string]any{{"kind": "escape-leaf", "name": "d1"}},
	})
	s.writeJSON(filepath.Join(dir, "d1.json"), map[string]any{
		"metadata": map[string]any{"kind": "escape-leaf", "name": "d1"},
		"spec":     map[string]any{},
	})

	out := filepath.Join(s.root, "out")
	_, err = s.renderKind("escape-root", "r1", dir, out)
	s.Require().NoError(err)

	// The hook saw its own write within the hook...
	body, err := os.ReadFile(filepath.Join(out, "r1", "terraform", "d1.tf"))
	s.Require().NoError(err)
	s.Equal("scribbled", string(body))
	// ...and nothing it did to selfFS reached the output.
	s.NoFileExists(filepath.Join(out, "r1", "assets", "extra.tf"))
	s.NoDirExists(filepath.Join(out, "r1", "assets"))
}
