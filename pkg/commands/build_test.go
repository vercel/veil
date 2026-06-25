package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/embeds"
)

type BuildSuite struct {
	suite.Suite
	root string
}

func TestBuildSuite(t *testing.T) {
	suite.Run(t, new(BuildSuite))
}

func (s *BuildSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": [],
  "registries": { "": "./public/r/registry.json" }
}
`), 0644))
	s.T().Chdir(s.root)
}

func (s *BuildSuite) run(args ...string) (string, error) {
	var buf bytes.Buffer
	app := NewApp()
	app.Writer = &buf
	app.ErrWriter = &buf
	err := app.Run(context.Background(), append([]string{"veil"}, args...))
	return buf.String(), err
}

func (s *BuildSuite) readJSON(path string) map[string]any {
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	var out map[string]any
	s.Require().NoError(json.Unmarshal(data, &out))
	return out
}

func (s *BuildSuite) TestBuildEmitsCompiledKindAndSchema() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	outDir := filepath.Join(s.root, "public", "r")
	s.Require().NoError(os.RemoveAll(outDir))

	_, err = s.run("build")
	s.Require().NoError(err)

	s.FileExists(filepath.Join(outDir, "worker", "kind.json"))
	s.FileExists(filepath.Join(outDir, "worker", "kind.schema.json"))
	s.FileExists(filepath.Join(outDir, "registry.json"))

	registry := s.readJSON(filepath.Join(outDir, "registry.json"))
	s.Equal(embeds.RegistrySchemaURL, registry["$schema"])
	kinds, ok := registry["kinds"].(map[string]any)
	s.Require().True(ok)
	worker, ok := kinds["worker"].(map[string]any)
	s.Require().True(ok)
	s.Equal("worker", worker["name"])
	s.Equal("./worker/kind.json", worker["path"])
	s.Equal("./worker/kind.schema.json", worker["schema"])

	compiled := s.readJSON(filepath.Join(outDir, "worker", "kind.json"))
	s.Equal(embeds.KindSchemaURL, compiled["$schema"])
	s.Equal("worker", compiled["name"])

	sources, ok := compiled["sources"].(map[string]any)
	s.Require().True(ok)
	s.Equal("This is a source file for worker.\n", sources["sources/source.txt"])

	hooksObj, ok := compiled["hooks"].(map[string]any)
	s.Require().True(ok)
	renderHooks, ok := hooksObj["render"].([]any)
	s.Require().True(ok)
	s.Len(renderHooks, 1)
	first := renderHooks[0].(map[string]any)
	s.Equal("hooks/src/hello-world.ts", first["name"])
	content, ok := first["content"].(string)
	s.Require().True(ok)
	s.Contains(content, "__veilMod")  // IIFE global set
	s.Contains(content, "render")     // hook method preserved
	s.NotContains(content, "// TODO") // comment stripped
}

func (s *BuildSuite) TestBuildSchemasOnlyEmitsSchemaWithoutRegistry() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	outDir := filepath.Join(s.root, "public", "r")
	s.Require().NoError(os.RemoveAll(outDir))

	_, err = s.run("build", "--schemas-only")
	s.Require().NoError(err)

	// Only the schema lands — no compiled kind.json body, no registry index.
	s.FileExists(filepath.Join(outDir, "worker", "kind.schema.json"))
	s.NoFileExists(filepath.Join(outDir, "worker", "kind.json"))
	s.NoFileExists(filepath.Join(outDir, "registry.json"))
}

func (s *BuildSuite) TestBuildRegeneratesTypesBeforeBundling() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Break veil-types.ts so bundling should fail if regen isn't happening.
	typesPath := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "veil-types.ts")
	s.Require().NoError(os.WriteFile(typesPath, []byte("// corrupted\n"), 0644))

	_, err = s.run("build")
	s.Require().NoError(err)

	// The file should have been rewritten with the current types.
	types, err := os.ReadFile(typesPath)
	s.Require().NoError(err)
	s.Contains(string(types), "WorkerSpec")
}

// TestBuildRegeneratesResourceHookTypes proves `veil build` refreshes the
// veil-types.ts next to a resource's render hooks — not just kind hooks —
// so resource-hook authors track the kind schema. A second resource with
// no render hooks is left untouched (nothing to type).
func (s *BuildSuite) TestBuildRegeneratesResourceHookTypes() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Declare discovery so `veil build` can find resources under svc/.
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": ["./.veil/kinds/worker/kind.json"],
  "registries": { "": "./public/r/registry.json" },
  "resource_discovery": { "paths": ["svc/*/production.json"] }
}`), 0644))

	// A worker resource we'll attach a render hook to.
	hookedDir := filepath.Join(s.root, "svc", "my-api")
	s.Require().NoError(os.MkdirAll(hookedDir, 0755))
	hookedPath := filepath.Join(hookedDir, "production.json")
	s.Require().NoError(os.WriteFile(hookedPath, []byte(`{
  "metadata": { "kind": "worker", "name": "my-api" },
  "spec": {}
}`), 0644))

	// The scaffolder writes the initial resource veil-types.ts; corrupt it
	// so a stale copy would survive if build didn't regenerate it.
	_, err = s.run("new", "hook", "fix-thing", "--resource", hookedPath)
	s.Require().NoError(err)
	typesPath := filepath.Join(hookedDir, "hooks", "veil-types.ts")
	s.Require().FileExists(typesPath)
	s.Require().NoError(os.WriteFile(typesPath, []byte("// corrupted\n"), 0644))

	// A second worker resource with NO render hooks — must be skipped.
	bareDir := filepath.Join(s.root, "svc", "bare")
	s.Require().NoError(os.MkdirAll(bareDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(bareDir, "production.json"), []byte(`{
  "metadata": { "kind": "worker", "name": "bare" },
  "spec": {}
}`), 0644))

	_, err = s.run("build")
	s.Require().NoError(err)

	// The hooked resource's types are regenerated from the worker schema.
	types, err := os.ReadFile(typesPath)
	s.Require().NoError(err)
	s.Contains(string(types), "WorkerSpec")

	// The hook-less resource never gets a hooks/veil-types.ts.
	s.NoFileExists(filepath.Join(bareDir, "hooks", "veil-types.ts"))
}

// TestBuildEmitsTypesPackage proves a kind whose `kinds` entry carries an
// `import` (the object form) emits its types into the shared package at
// generators.types.output_dir — host.ts once, a per-kind module importing it,
// a package.json with name + exports — and wires the kind's hooks/package.json
// to depend on it, instead of an inline per-hook veil-types.ts.
func (s *BuildSuite) TestBuildEmitsTypesPackage() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// A hooks/package.json the build can wire the types dependency into
	// (mirrors the per-kind hooks package every real kind ships).
	hooksPkg := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "package.json")
	s.Require().NoError(os.WriteFile(hooksPkg, []byte(`{
  "name": "@platform/worker-hooks",
  "version": "0.0.0",
  "private": true,
  "devDependencies": {
    "kubernetes-types": "^1.30.0"
  }
}
`), 0644))

	// Opt the worker kind into the shared types package via the object form.
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": [
    { "path": "./.veil/kinds/worker/kind.json", "import": { "name": "@platform/veil-types/worker", "value": "workspace:*" } }
  ],
  "registries": { "": "./public/r/registry.json" },
  "generators": { "types": { "output_dir": "./types" } }
}`), 0644))

	// The types package is owned by the repo — veil manages only its
	// `exports`, so its package.json (name/version/private) must pre-exist.
	// Use a non-default version to prove veil leaves it untouched.
	s.Require().NoError(os.MkdirAll(filepath.Join(s.root, "types"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "types", "package.json"), []byte(`{
  "name": "@platform/veil-types",
  "version": "1.2.3",
  "private": true
}
`), 0644))

	// --no-typecheck: the scaffolded hook still imports ./veil-types and the
	// workspace package isn't installed in the test tree, so we assert
	// emission rather than resolution (that's covered against a real repo).
	_, err = s.run("build", "--no-typecheck")
	s.Require().NoError(err)

	typesDir := filepath.Join(s.root, "types")

	// host.ts holds the shared, kind-independent declarations once.
	host, err := os.ReadFile(filepath.Join(typesDir, "host.ts"))
	s.Require().NoError(err)
	hostStr := string(host)
	s.Contains(hostStr, "export interface Std")
	s.Contains(hostStr, "export interface Resource<")
	s.Contains(hostStr, "export interface RegistryVariables")
	s.Contains(hostStr, "export type ValidationResult")

	// The kind module imports the shared half from ./host and holds only the
	// per-kind types — the shared types are not re-declared.
	mod, err := os.ReadFile(filepath.Join(typesDir, "worker.ts"))
	s.Require().NoError(err)
	modStr := string(mod)
	s.Contains(modStr, "from './host'")
	s.Contains(modStr, "export interface WorkerSpec")
	s.Contains(modStr, "export interface RenderHook")
	s.NotContains(modStr, "export interface Std")
	s.NotContains(modStr, "export interface RegistryVariables")

	// package.json: veil added exports for ./host and the kind subpath while
	// leaving the repo-owned name/version untouched (veil names nothing).
	pkg := s.readJSON(filepath.Join(typesDir, "package.json"))
	s.Equal("@platform/veil-types", pkg["name"])
	s.Equal("1.2.3", pkg["version"]) // not overwritten with 0.0.0
	exports, ok := pkg["exports"].(map[string]any)
	s.Require().True(ok)
	s.Equal("./host.ts", exports["./host"])
	s.Equal("./worker.ts", exports["./worker"])

	// The hooks package.json gained the types dependency in devDependencies,
	// preserving the pre-existing entry.
	hp := s.readJSON(hooksPkg)
	dev, ok := hp["devDependencies"].(map[string]any)
	s.Require().True(ok)
	s.Equal("workspace:*", dev["@platform/veil-types"])
	s.Equal("^1.30.0", dev["kubernetes-types"])

	// No inline per-hook veil-types.ts for an import kind — the stale one
	// scaffolding wrote is removed.
	s.NoFileExists(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "veil-types.ts"))
}

// TestBuildTypesPackageIsIdempotent proves a second build produces
// byte-identical package artifacts — including the consuming hooks
// package.json — so re-running `veil build` never churns the tree. This
// exercises the hand-rolled package.json encoder (top-level order preserved,
// dependency maps stable) and the ensureTypesDep already-present no-op.
func (s *BuildSuite) TestBuildTypesPackageIsIdempotent() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	hooksPkg := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "package.json")
	s.Require().NoError(os.WriteFile(hooksPkg, []byte(`{
  "name": "@platform/worker-hooks",
  "version": "0.0.0",
  "private": true,
  "devDependencies": {
    "kubernetes-types": "^1.30.0"
  }
}
`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": [
    { "path": "./.veil/kinds/worker/kind.json", "import": { "name": "@platform/veil-types/worker", "value": "workspace:*" } }
  ],
  "registries": { "": "./public/r/registry.json" },
  "generators": { "types": { "output_dir": "./types" } }
}`), 0644))
	s.Require().NoError(os.MkdirAll(filepath.Join(s.root, "types"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "types", "package.json"), []byte(`{
  "name": "@platform/veil-types",
  "version": "0.0.0",
  "private": true
}
`), 0644))

	read := func(p string) string {
		b, err := os.ReadFile(p)
		s.Require().NoError(err)
		return string(b)
	}

	_, err = s.run("build", "--no-typecheck")
	s.Require().NoError(err)
	typesDir := filepath.Join(s.root, "types")
	pkg1 := read(filepath.Join(typesDir, "package.json"))
	host1 := read(filepath.Join(typesDir, "host.ts"))
	mod1 := read(filepath.Join(typesDir, "worker.ts"))
	hooks1 := read(hooksPkg)

	_, err = s.run("build", "--no-typecheck")
	s.Require().NoError(err)
	s.Equal(pkg1, read(filepath.Join(typesDir, "package.json")), "types package.json must be byte-stable")
	s.Equal(host1, read(filepath.Join(typesDir, "host.ts")), "host.ts must be byte-stable")
	s.Equal(mod1, read(filepath.Join(typesDir, "worker.ts")), "kind module must be byte-stable")
	s.Equal(hooks1, read(hooksPkg), "consuming hooks package.json must be byte-stable (no dep churn)")
}

// TestAddDepPreservesSpecialCharsAndOrder proves wiring the types dependency
// into an existing package.json leaves unrelated fields byte-faithful: &, &&,
// and > in npm scripts / repository URLs are NOT HTML-escaped, and the
// top-level key order is preserved (not reordered alphabetically).
func (s *BuildSuite) TestAddDepPreservesSpecialCharsAndOrder() {
	pkg := filepath.Join(s.root, "package.json")
	s.Require().NoError(os.WriteFile(pkg, []byte(`{
  "name": "@platform/worker-hooks",
  "scripts": {
    "build": "tsc -b && eslint .",
    "clean": "rm -rf dist > /dev/null"
  },
  "repository": "https://example.com/r?path=hooks&ref=main",
  "devDependencies": {
    "kubernetes-types": "^1.30.0"
  }
}
`), 0644))

	s.Require().NoError(addDepToPackageJSON(pkg, "@platform/veil-types", "workspace:*"))

	got, err := os.ReadFile(pkg)
	s.Require().NoError(err)
	gs := string(got)
	s.Contains(gs, `"@platform/veil-types": "workspace:*"`)
	s.Contains(gs, "tsc -b && eslint .")
	s.Contains(gs, "rm -rf dist > /dev/null")
	s.Contains(gs, "?path=hooks&ref=main")
	s.NotContains(gs, "&amp;")
	s.NotContains(gs, "&gt;")
	// Top-level key order preserved (not reordered alphabetically).
	s.Less(strings.Index(gs, `"name"`), strings.Index(gs, `"scripts"`))
	s.Less(strings.Index(gs, `"repository"`), strings.Index(gs, `"devDependencies"`))
}

// TestRegisterKindPreservesObjectEntries proves `veil new kind` can append to a
// project whose `kinds` already uses the object form ({path, import}) — the
// object entries are preserved verbatim, dedupe is by path, and the new kind is
// appended as a bare path string.
func (s *BuildSuite) TestRegisterKindPreservesObjectEntries() {
	cfg := filepath.Join(s.root, "veil.json")
	s.Require().NoError(os.WriteFile(cfg, []byte(`{
  "kinds": [
    { "path": "./a/kind.json", "import": { "name": "@p/veil-types/a", "value": "workspace:*" } },
    "./b/kind.json"
  ],
  "registries": { "": "./public/r/registry.json" }
}`), 0644))

	s.Require().NoError(registerKindInVeilJSON(cfg, "./c/kind.json", "", ""))
	out := s.readJSON(cfg)
	kinds, ok := out["kinds"].([]any)
	s.Require().True(ok)
	s.Require().Len(kinds, 3)
	obj, ok := kinds[0].(map[string]any)
	s.Require().True(ok)
	s.Equal("./a/kind.json", obj["path"])
	s.Equal("./b/kind.json", kinds[1])
	s.Equal("./c/kind.json", kinds[2])

	// Idempotent: re-registering an existing path (object or string) is a no-op.
	s.Require().NoError(registerKindInVeilJSON(cfg, "./a/kind.json", "", ""))
	s.Require().NoError(registerKindInVeilJSON(cfg, "./b/kind.json", "", ""))
	s.Require().Len(s.readJSON(cfg)["kinds"].([]any), 3)
}

// TestResolveTypesPackageValidation pins the up-front config guards: an import
// with no output_dir, two kinds colliding on a module subpath, and a missing
// or mismatched repo-owned package.json are all rejected rather than silently
// producing a broken or lossy tree.
func (s *BuildSuite) TestResolveTypesPackageValidation() {
	mk := func(name string, imp *veilv1.KindImport) *config.Kind {
		return &config.Kind{KindDefinition: &veilv1.KindDefinition{Name: name}, Import: imp}
	}
	withTypes := &veilv1.Generators{Types: &veilv1.Types{OutputDir: "./types"}}

	// import set but output_dir unset -> error.
	_, err := resolveTypesPackage(&config.Registry{
		Root:       s.root,
		Generators: &veilv1.Generators{},
		Kinds:      []*config.Kind{mk("a", &veilv1.KindImport{Name: "@p/veil-types/a", Value: "workspace:*"})},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "output_dir is unset")

	// two kinds colliding on the same subpath -> error.
	_, err = resolveTypesPackage(&config.Registry{
		Root:       s.root,
		Generators: withTypes,
		Kinds: []*config.Kind{
			mk("a", &veilv1.KindImport{Name: "@p/veil-types/shared", Value: "workspace:*"}),
			mk("b", &veilv1.KindImport{Name: "@p/veil-types/shared", Value: "workspace:*"}),
		},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "subpath")

	// distinct subpaths, but the repo-owned package.json is missing -> error.
	distinct := &config.Registry{
		Root:       s.root,
		Generators: withTypes,
		Kinds: []*config.Kind{
			mk("a", &veilv1.KindImport{Name: "@p/veil-types/a", Value: "workspace:*"}),
			mk("b", &veilv1.KindImport{Name: "@p/veil-types/b", Value: "workspace:*"}),
		},
	}
	_, err = resolveTypesPackage(distinct)
	s.Require().Error(err)
	s.Contains(err.Error(), "no package.json")

	typesDir := filepath.Join(s.root, "types")
	s.Require().NoError(os.MkdirAll(typesDir, 0755))
	pkgPath := filepath.Join(typesDir, "package.json")

	// package.json names a different package than the imports -> error.
	s.Require().NoError(os.WriteFile(pkgPath, []byte(`{ "name": "@other/types" }`), 0644))
	_, err = resolveTypesPackage(distinct)
	s.Require().Error(err)
	s.Contains(err.Error(), "does not match")

	// matching name -> resolves; veil reads the name, it does not set it.
	s.Require().NoError(os.WriteFile(pkgPath, []byte(`{ "name": "@p/veil-types", "version": "9.9.9" }`), 0644))
	tp, err := resolveTypesPackage(distinct)
	s.Require().NoError(err)
	s.Require().NotNil(tp)
	s.Equal("@p/veil-types", tp.name)
}

// TestNewKindUsesPackageWhenConfigured proves `veil new kind` scaffolds in
// package mode when generators.types is set — the hook imports the shared
// package (not a local veil-types.ts), a hooks/package.json carries the
// workspace dep, the kind registers in object form, and the kind's module is
// emitted into the package and exported.
func (s *BuildSuite) TestNewKindUsesPackageWhenConfigured() {
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": [],
  "registries": { "": "./public/r/registry.json" },
  "generators": { "types": { "output_dir": "./types" } }
}`), 0644))
	// repo-owned types package.json (veil never names it)
	s.Require().NoError(os.MkdirAll(filepath.Join(s.root, "types"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "types", "package.json"), []byte(`{
  "name": "@scratch/veil",
  "version": "0.0.0",
  "private": true
}`), 0644))
	// make the package resolvable so the build's typecheck (if a compiler is
	// present) finds @scratch/veil/<kind>
	s.Require().NoError(os.MkdirAll(filepath.Join(s.root, "node_modules", "@scratch"), 0755))
	s.Require().NoError(os.Symlink(filepath.Join(s.root, "types"), filepath.Join(s.root, "node_modules", "@scratch", "veil")))

	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// registered in object form with the import
	cfg := s.readJSON(filepath.Join(s.root, "veil.json"))
	kinds, ok := cfg["kinds"].([]any)
	s.Require().True(ok)
	s.Require().Len(kinds, 1)
	entry, ok := kinds[0].(map[string]any)
	s.Require().True(ok, "kind should be registered in object form")
	imp, ok := entry["import"].(map[string]any)
	s.Require().True(ok)
	s.Equal("@scratch/veil/worker", imp["name"])
	s.Equal("workspace:*", imp["value"])

	// hook imports the package; no inline veil-types.ts
	hook, err := os.ReadFile(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "hello-world.ts"))
	s.Require().NoError(err)
	s.Contains(string(hook), "from '@scratch/veil/worker'")
	s.NoFileExists(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "veil-types.ts"))

	// hooks/package.json carries the workspace dep
	hp := s.readJSON(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "package.json"))
	dev, ok := hp["devDependencies"].(map[string]any)
	s.Require().True(ok)
	s.Equal("workspace:*", dev["@scratch/veil"])

	// the kind's module is emitted + exported; repo-owned name untouched
	s.FileExists(filepath.Join(s.root, "types", "worker.ts"))
	pkg := s.readJSON(filepath.Join(s.root, "types", "package.json"))
	s.Equal("@scratch/veil", pkg["name"])
	exports, ok := pkg["exports"].(map[string]any)
	s.Require().True(ok)
	s.Equal("./worker.ts", exports["./worker"])
}

func (s *BuildSuite) TestBuildTypesFileEmitsEnumUnion() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": ["./.veil/kinds/worker/kind.json"],
  "registries": { "": "./public/r/registry.json" },
  "variables": {
    "env":      { "type": "string", "enum": ["dev", "staging", "prod"], "default": "dev" },
    "replicas": { "type": "number", "enum": [1, 3, 5] }
  }
}`), 0644))

	_, err = s.run("build")
	s.Require().NoError(err)

	types, err := os.ReadFile(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "veil-types.ts"))
	s.Require().NoError(err)
	ts := string(types)
	s.Contains(ts, `env: "dev" | "staging" | "prod";`)
	s.Contains(ts, `replicas: 1 | 3 | 5;`)
}

func (s *BuildSuite) TestBuildTypesFileIncludesVariables() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": ["./.veil/kinds/worker/kind.json"],
  "registries": { "": "./public/r/registry.json" },
  "variables": {
    "env":      { "type": "string", "default": "dev", "description": "Target deployment environment." },
    "replicas": { "type": "number", "default": 3 },
    "debug":    { "type": "bool", "default": false, "description": "Enable verbose logging.\nForwarded to transforms via ctx.vars." }
  }
}`), 0644))

	_, err = s.run("build")
	s.Require().NoError(err)

	types, err := os.ReadFile(filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "veil-types.ts"))
	s.Require().NoError(err)
	ts := string(types)
	s.Contains(ts, "export interface RegistryVariables {")
	s.Contains(ts, "env: string;")
	s.Contains(ts, "replicas: number;")
	s.Contains(ts, "debug: boolean;")
	s.Contains(ts, "vars: RegistryVariables;")

	// Single-line description → /** … */ on one line.
	s.Contains(ts, "/** Target deployment environment. */")
	// Multi-line description → JSDoc block with * gutter.
	s.Contains(ts, "/**\n   * Enable verbose logging.\n   * Forwarded to transforms via ctx.vars.\n   */")
	// Variable with no description has no comment directly preceding its field.
	s.NotContains(ts, "/** */") // sanity: never emit empty comments
	s.Contains(ts, "export interface RenderHook {")
	s.Contains(ts, "render(ctx: RenderHookContext")
}

func (s *BuildSuite) TestBuildEmbedsVariablesInCompiledKind() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Add variables to veil.json.
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": ["./.veil/kinds/worker/kind.json"],
  "registries": { "": "./public/r/registry.json" },
  "variables": {
    "env": { "type": "string", "default": "dev" },
    "region": { "type": "string" }
  }
}`), 0644))

	_, err = s.run("build")
	s.Require().NoError(err)

	compiled := s.readJSON(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
	vars, ok := compiled["variables"].(map[string]any)
	s.Require().True(ok)

	env := vars["env"].(map[string]any)
	s.Equal("string", env["type"])
	s.Equal("dev", env["default"])

	region := vars["region"].(map[string]any)
	s.Equal("string", region["type"])
	_, hasDefault := region["default"]
	s.False(hasDefault)
}

// TestBuildAcceptsYAMLSchemaAndKind exercises the full source-side
// YAML ingestion path: kind.yaml + schema.yaml authored together,
// referenced from veil.yaml, all run through `veil build`. The
// compiled output is always JSON regardless of source format.
func (s *BuildSuite) TestBuildAcceptsYAMLSchemaAndKind() {
	altRoot := filepath.Join(s.root, "yaml-project")
	kindDir := filepath.Join(altRoot, ".veil", "kinds", "svc")
	s.Require().NoError(os.MkdirAll(filepath.Join(kindDir, "sources"), 0755))
	s.Require().NoError(os.MkdirAll(filepath.Join(kindDir, "hooks", "src"), 0755))

	schemaYAML := `type: object
properties:
  replicas:
    type: integer
`
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "schema.yaml"), []byte(schemaYAML), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "sources", "deploy.yaml"),
		[]byte("kind: Deployment\n"), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "hooks", "src", "noop.ts"), []byte(
		"import type { FS, RenderHook, RenderHookContext } from './veil-types';\nconst h: RenderHook = { render(ctx: RenderHookContext, fs: FS) { return fs; } };\nexport default h;\n",
	), 0644))

	kindYAML := `name: svc
sources:
  - ./sources/deploy.yaml
hooks:
  render:
    - path: ./hooks/src/noop.ts
schema: ./schema.yaml
`
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "kind.yaml"), []byte(kindYAML), 0644))

	veilYAML := `kinds:
  - ./.veil/kinds/svc/kind.yaml
registries:
  "": ./public/r/registry.json
`
	s.Require().NoError(os.WriteFile(filepath.Join(altRoot, "veil.yaml"), []byte(veilYAML), 0644))

	out := filepath.Join(s.root, "dist-yaml")
	_, err := s.run("build", "--config", filepath.Join(altRoot, "veil.yaml"), "--out", out)
	s.Require().NoError(err)

	// Compiled output is always JSON, even though the inputs were YAML.
	s.FileExists(filepath.Join(out, "svc", "kind.json"))
	s.FileExists(filepath.Join(out, "svc", "kind.schema.json"))

	compiled := s.readJSON(filepath.Join(out, "svc", "kind.json"))
	s.Equal("svc", compiled["name"])

	schema := s.readJSON(filepath.Join(out, "svc", "kind.schema.json"))
	props := schema["properties"].(map[string]any)
	spec := props["spec"].(map[string]any)
	specProps := spec["properties"].(map[string]any)
	replicas := specProps["replicas"].(map[string]any)
	s.Equal("integer", replicas["type"])
}

func (s *BuildSuite) TestBuildHonorsConfigAndOutFlags() {
	altRoot := filepath.Join(s.root, "custom-root")
	kindDir := filepath.Join(altRoot, ".veil", "kinds", "svc")
	s.Require().NoError(os.MkdirAll(filepath.Join(kindDir, "sources"), 0755))
	s.Require().NoError(os.MkdirAll(filepath.Join(kindDir, "hooks", "src"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "schema.json"), []byte(`{"type":"object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "sources", "deploy.yaml"), []byte("kind: Deployment\n"), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "hooks", "src", "noop.ts"), []byte(
		"import type { FS, RenderHook, RenderHookContext } from './veil-types';\nconst h: RenderHook = { render(ctx: RenderHookContext, fs: FS) { return fs; } };\nexport default h;\n",
	), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "kind.json"), []byte(`{
  "name": "svc",
  "sources": ["./sources/deploy.yaml"],
  "hooks": {"render": [{"path": "./hooks/src/noop.ts"}]},
  "schema": "./schema.json"
}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(altRoot, "veil.json"), []byte(`{"kinds": ["./.veil/kinds/svc/kind.json"], "registries": { "": "./public/r/registry.json" }}`), 0644))

	out := filepath.Join(s.root, "dist")
	_, err := s.run("build", "--config", filepath.Join(altRoot, "veil.json"), "--out", out)
	s.Require().NoError(err)

	s.FileExists(filepath.Join(out, "svc", "kind.json"))
	s.FileExists(filepath.Join(out, "svc", "kind.schema.json"))
	s.FileExists(filepath.Join(out, "registry.json"))

	compiled := s.readJSON(filepath.Join(out, "svc", "kind.json"))
	sources := compiled["sources"].(map[string]any)
	s.Equal("kind: Deployment\n", sources["sources/deploy.yaml"])
}

func (s *BuildSuite) TestBuildFailsOnTypeError() {
	if _, err := s.lookPath("tsc", "tsgo"); err != nil {
		s.T().Skip("no tsc/tsgo on PATH")
	}

	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Rewrite hello-world.ts with a type error: treat Bundle as a number.
	badTS := `import type { Bundle, Hook, HookContext } from './veil-types';
const helloWorld: Hook = {
  renderHook(ctx: HookContext, bundle: Bundle) {
    const n: number = bundle;
    return { bundle };
  },
};
export default helloWorld;
`
	path := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "hello-world.ts")
	s.Require().NoError(os.WriteFile(path, []byte(badTS), 0644))

	_, err = s.run("build")
	s.Require().Error(err)
	s.Contains(err.Error(), "typecheck failed")
}

func (s *BuildSuite) TestBuildSkipsTypecheckWithFlag() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	badTS := `import type { Bundle, Hook, HookContext } from './veil-types';
const helloWorld: Hook = {
  renderHook(ctx: HookContext, bundle: Bundle) {
    const n: number = bundle;
    return { bundle };
  },
};
export default helloWorld;
`
	path := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "hello-world.ts")
	s.Require().NoError(os.WriteFile(path, []byte(badTS), 0644))

	_, err = s.run("build", "--no-typecheck")
	s.Require().NoError(err, "build should succeed when typecheck is skipped")
}

func (s *BuildSuite) lookPath(bins ...string) (string, error) {
	for _, b := range bins {
		if p, err := exec.LookPath(b); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("none of %v on PATH", bins)
}

func (s *BuildSuite) TestBuildFailsOnMissingSource() {
	kindDir := filepath.Join(s.root, ".veil", "kinds", "svc")
	s.Require().NoError(os.MkdirAll(kindDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "schema.json"), []byte(`{"type":"object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "kind.json"), []byte(`{
  "name": "svc",
  "sources": ["./sources/missing.yaml"],
  "schema": "./schema.json"
}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{
  "kinds": ["./.veil/kinds/svc/kind.json"],
  "registries": { "": "./public/r/registry.json" }
}`), 0644))

	_, err := s.run("build")
	s.Require().Error(err)
	s.Contains(err.Error(), "missing.yaml")
}
