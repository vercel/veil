package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

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
