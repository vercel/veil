package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"
	"gopkg.in/yaml.v3"

	"github.com/vercel/veil/pkg/embeds"
)

func encodeYAMLForTest(v any) ([]byte, error) {
	return yaml.Marshal(v)
}

func decodeYAMLForTest(data []byte, into any) error {
	return yaml.Unmarshal(data, into)
}

type NewSuite struct {
	suite.Suite
	root string
}

func TestNewSuite(t *testing.T) {
	suite.Run(t, new(NewSuite))
}

func (s *NewSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.T().Chdir(s.root)
}

func (s *NewSuite) run(args ...string) (string, error) {
	var buf bytes.Buffer
	app := NewApp()
	app.Writer = &buf
	app.ErrWriter = &buf
	err := app.Run(context.Background(), append([]string{"veil"}, args...))
	return buf.String(), err
}

func (s *NewSuite) TestNewKindAutoInitsVeilJSONAndScaffoldsAllFiles() {
	_, err := os.Stat(filepath.Join(s.root, "veil.json"))
	s.Require().True(os.IsNotExist(err))

	out, err := s.run("new", "kind", "worker")
	s.Require().NoError(err, out)

	s.FileExists(filepath.Join(s.root, "veil.json"))
	kindDir := filepath.Join(s.root, ".veil", "kinds", "worker")
	s.FileExists(filepath.Join(kindDir, "kind.json"))
	s.FileExists(filepath.Join(kindDir, "schema.json"))
	s.FileExists(filepath.Join(kindDir, "sources", "source.txt"))
	s.FileExists(filepath.Join(kindDir, "hooks", "src", "hello-world.ts"))

	sourceBlurb, err := os.ReadFile(filepath.Join(kindDir, "sources", "source.txt"))
	s.Require().NoError(err)
	s.Equal("This is a source file for worker.\n", string(sourceBlurb))

	hello, err := os.ReadFile(filepath.Join(kindDir, "hooks", "src", "hello-world.ts"))
	s.Require().NoError(err)
	s.Contains(string(hello), "const helloWorld: RenderHook")
	s.Contains(string(hello), "render(ctx: RenderHookContext, fs: FS): FS {")
	s.Contains(string(hello), "return fs;")
	s.Contains(string(hello), "from './veil-types'")

	types, err := os.ReadFile(filepath.Join(kindDir, "hooks", "src", "veil-types.ts"))
	s.Require().NoError(err)
	s.Contains(string(types), "WorkerSpec")
	s.Contains(string(types), "RenderHookContext")
	s.Contains(string(types), "interface RegistryVariables {")
	s.Contains(string(types), "vars: RegistryVariables")
	s.Contains(string(types), "root: string;")
	s.Contains(string(types), "std: Std;")
	s.Contains(string(types), "os: Os;")
	s.Contains(string(types), "fetch: Fetch;")
	s.Contains(string(types), "export type Fetch =")
	s.Contains(string(types), "resource: Resource<WorkerSpec, Dependency>;")
	s.Contains(string(types), "export interface Resource<Spec, Deps = never> {")
	s.Contains(string(types), "export type Dependency = never;")
	s.Contains(string(types), "export interface Metadata {")
	s.Contains(string(types), "kind: string;")
	s.Contains(string(types), "name: string;")
	s.Contains(string(types), "overrides?: Override[];")
	// Overlays are resolved into spec before hooks run; not surfaced
	// as a Metadata field or as a TS interface.
	s.NotContains(string(types), "overlays?:")
	s.NotContains(string(types), "interface Overlay ")
	// Removed-from-Std write/exec surfaces should never reappear.
	s.NotContains(string(types), "writeFile")
	s.NotContains(string(types), "interface StdFile")
	s.NotContains(string(types), "mkdir(")
	s.NotContains(string(types), "rename(")
	s.NotContains(string(types), "interface Http ")
	s.NotContains(string(types), "http.request")
	s.Contains(string(types), "interface RenderHook {")
	s.Contains(string(types), "render(ctx: RenderHookContext, fs: FS)")
	// Old names should be gone.
	s.NotContains(string(types), "interface Hook {")
	s.NotContains(string(types), "renderHook?")
	s.Contains(string(types), "interface File {")
	s.Contains(string(types), "getContent(): string;")
	s.Contains(string(types), "setContent(content: string): void;")
	s.Contains(string(types), "getPath(): string;")
	s.Contains(string(types), "setOutputPath(path: string): void;")
	s.Contains(string(types), "isDeleted(): boolean;")
	s.Contains(string(types), "setDeleted(deleted: boolean): void;")
	s.Contains(string(types), "interface FS {")
	s.Contains(string(types), "getSourcesSourceTxt(): File;")
	s.Contains(string(types), "add(path: string, content: string): File;")
	s.Contains(string(types), "get(path: string): File | undefined;")
	s.Contains(string(types), "getAll(): File[];")
	s.Contains(string(types), "delete(path: string): void;")

	kind := s.readJSON(filepath.Join(kindDir, "kind.json"))
	s.Equal(embeds.KindDefinitionSchemaURL, kind["$schema"])
	s.Equal("worker", kind["name"])
	s.Equal("./schema.json", kind["schema"])
	s.Equal([]any{"./sources/source.txt"}, kind["sources"])
	hooksField, ok := kind["hooks"].(map[string]any)
	s.Require().True(ok)
	s.Equal([]any{map[string]any{"path": "./hooks/src/hello-world.ts"}}, hooksField["render"])

	veil := s.readJSON(filepath.Join(s.root, "veil.json"))
	s.Equal(embeds.VeilConfigDefinitionSchemaURL, veil["$schema"])
	s.Equal([]any{"./.veil/kinds/worker/kind.json"}, veil["kinds"])
	s.Equal(map[string]any{"": "./public/r/registry.json"}, veil["registries"])

	s.FileExists(filepath.Join(s.root, "public", "r", "worker", "kind.schema.json"))
	s.FileExists(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
	s.FileExists(filepath.Join(s.root, "public", "r", "registry.json"))

	compiled := s.readJSON(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
	s.Equal(embeds.KindSchemaURL, compiled["$schema"])
	s.Equal("worker", compiled["name"])

	registry := s.readJSON(filepath.Join(s.root, "public", "r", "registry.json"))
	s.Equal(embeds.RegistrySchemaURL, registry["$schema"])
	sources, ok := compiled["sources"].(map[string]any)
	s.Require().True(ok)
	s.Equal("This is a source file for worker.\n", sources["sources/source.txt"])
	compiledHooks, ok := compiled["hooks"].(map[string]any)
	s.Require().True(ok)
	renderHooks, ok := compiledHooks["render"].([]any)
	s.Require().True(ok)
	s.Len(renderHooks, 1)
	first, ok := renderHooks[0].(map[string]any)
	s.Require().True(ok)
	s.Equal("hooks/src/hello-world.ts", first["name"])
	content, ok := first["content"].(string)
	s.Require().True(ok)
	s.NotEmpty(content)
	s.NotContains(content, "\n  ") // minified — no 2-space indent
}

func (s *NewSuite) TestNewKindHonorsGeneratorsKindsDir() {
	veilJSON := filepath.Join(s.root, "veil.json")
	s.Require().NoError(os.WriteFile(veilJSON, []byte(`{
		"kinds": [],
		"registries": { "": "./public/r/registry.json" },
		"generators": { "kinds_dir": "./platform/kinds" }
	}`), 0644))

	out, err := s.run("new", "kind", "worker")
	s.Require().NoError(err, out)

	customDir := filepath.Join(s.root, "platform", "kinds", "worker")
	s.FileExists(filepath.Join(customDir, "kind.json"))
	s.FileExists(filepath.Join(customDir, "schema.json"))
	s.FileExists(filepath.Join(customDir, "hooks", "src", "hello-world.ts"))

	defaultDir := filepath.Join(s.root, ".veil", "kinds", "worker")
	_, err = os.Stat(defaultDir)
	s.True(os.IsNotExist(err), "scaffold must not fall back to .veil/kinds when generators.kinds_dir is set")

	veil := s.readJSON(veilJSON)
	s.Equal([]any{"./platform/kinds/worker/kind.json"}, veil["kinds"])

	s.FileExists(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
}

func (s *NewSuite) TestNewKindReusesExistingVeilJSON() {
	veilJSON := filepath.Join(s.root, "veil.json")
	s.Require().NoError(os.WriteFile(veilJSON, []byte(`{"kinds": [], "registries": { "": "./public/r/registry.json" }}`), 0644))

	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	_, err = s.run("new", "kind", "cron")
	s.Require().NoError(err)

	veil := s.readJSON(veilJSON)
	s.Equal([]any{"./.veil/kinds/worker/kind.json", "./.veil/kinds/cron/kind.json"}, veil["kinds"])
}

func (s *NewSuite) TestNewKindRejectsDuplicate() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "kind", "worker")
	s.Require().Error(err)
	s.Contains(err.Error(), "already exists")
}

func (s *NewSuite) TestNewKindRejectsInvalidName() {
	_, err := s.run("new", "kind", "Bad Name")
	s.Require().Error(err)
	s.Contains(err.Error(), "kind name")
}

func (s *NewSuite) TestNewHookAppendsToKind() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "hook", "annotate", "--kind", "worker")
	s.Require().NoError(err)

	hookPath := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "annotate.ts")
	s.FileExists(hookPath)

	contents, err := os.ReadFile(hookPath)
	s.Require().NoError(err)
	s.Contains(string(contents), "const annotate: RenderHook")
	s.Contains(string(contents), "from './veil-types'")

	kind := s.readJSON(filepath.Join(s.root, ".veil", "kinds", "worker", "kind.json"))
	hooksField, ok := kind["hooks"].(map[string]any)
	s.Require().True(ok)
	s.Equal([]any{
		map[string]any{"path": "./hooks/src/hello-world.ts"},
		map[string]any{"path": "./hooks/src/annotate.ts"},
	}, hooksField["render"])
}

func (s *NewSuite) TestNewHookRequiresKindOrResourceFlagOrAutoDetect() {
	// cwd is the empty temp dir — no kind file, no resource file → auto-detect
	// has nothing to grab onto, so the command errors with a message asking
	// the user to disambiguate.
	out, err := s.run("new", "hook", "annotate")
	s.Require().Error(err, out)
	s.Contains(err.Error(), "--kind or --resource")
}

func (s *NewSuite) TestNewHookRejectsBothKindAndResourceFlags() {
	_, err := s.run("new", "hook", "annotate", "--kind", "worker", "--resource", "./foo.yaml")
	s.Require().Error(err)
	s.Contains(err.Error(), "mutually exclusive")
}

func (s *NewSuite) TestNewHookAppendsToResource() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	_, err = s.run("new", "resource", "my-worker", "--kind", "worker")
	s.Require().NoError(err)

	resourcePath := filepath.Join(s.root, "my-worker.json")

	_, err = s.run("new", "hook", "annotate", "--resource", resourcePath)
	s.Require().NoError(err)

	hookPath := filepath.Join(s.root, "hooks", "annotate.ts")
	s.FileExists(hookPath)
	contents, err := os.ReadFile(hookPath)
	s.Require().NoError(err)
	s.Contains(string(contents), "const annotate: RenderHook")

	// veil-types.ts must land next to the hook so the import resolves.
	typesPath := filepath.Join(s.root, "hooks", "veil-types.ts")
	s.FileExists(typesPath)
	types, err := os.ReadFile(typesPath)
	s.Require().NoError(err)
	s.Contains(string(types), "export interface WorkerSpec")
	s.Contains(string(types), "export interface RenderHook")
	s.Contains(string(types), "export interface ValidateHook")

	res := s.readJSON(resourcePath)
	meta := res["metadata"].(map[string]any)
	hooks := meta["hooks"].(map[string]any)
	s.Equal([]any{map[string]any{"path": "./hooks/annotate.ts"}}, hooks["render"])
}

func (s *NewSuite) TestNewHookAppendsToResourceWithExistingHooks() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	_, err = s.run("new", "resource", "my-worker", "--kind", "worker")
	s.Require().NoError(err)

	resourcePath := filepath.Join(s.root, "my-worker.json")

	// Seed an existing resource hook so we can confirm append (not replace).
	res := s.readJSON(resourcePath)
	meta := res["metadata"].(map[string]any)
	meta["hooks"] = map[string]any{
		"render": []any{map[string]any{"path": "./hooks/first.ts"}},
	}
	s.writeJSON(resourcePath, res)

	_, err = s.run("new", "hook", "second", "--resource", resourcePath)
	s.Require().NoError(err)

	res = s.readJSON(resourcePath)
	meta = res["metadata"].(map[string]any)
	hooks := meta["hooks"].(map[string]any)
	s.Equal([]any{
		map[string]any{"path": "./hooks/first.ts"},
		map[string]any{"path": "./hooks/second.ts"},
	}, hooks["render"])
}

func (s *NewSuite) TestNewHookAutoDetectsResourceFromCwd() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Drop the resource into a dedicated subdir so cwd has exactly one
	// resource file — the auto-detect single-candidate path.
	svcDir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(svcDir, 0755))
	_, err = s.run("new", "resource", "api", "--kind", "worker", "--out", filepath.Join(svcDir, "api.json"))
	s.Require().NoError(err)

	s.T().Chdir(svcDir)
	_, err = s.run("new", "hook", "annotate")
	s.Require().NoError(err)

	hookPath := filepath.Join(svcDir, "hooks", "annotate.ts")
	s.FileExists(hookPath)

	res := s.readJSON(filepath.Join(svcDir, "api.json"))
	hooks := res["metadata"].(map[string]any)["hooks"].(map[string]any)
	s.Equal([]any{map[string]any{"path": "./hooks/annotate.ts"}}, hooks["render"])
}

func (s *NewSuite) TestNewHookAutoDetectsKindFromCwd() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	kindDir := filepath.Join(s.root, ".veil", "kinds", "worker")
	s.T().Chdir(kindDir)

	_, err = s.run("new", "hook", "annotate")
	s.Require().NoError(err)

	hookPath := filepath.Join(kindDir, "hooks", "src", "annotate.ts")
	s.FileExists(hookPath)
}

func (s *NewSuite) TestNewHookRegeneratesResourceVeilTypesOnSecondInvocation() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)
	_, err = s.run("new", "resource", "my-worker", "--kind", "worker")
	s.Require().NoError(err)

	resourcePath := filepath.Join(s.root, "my-worker.json")

	_, err = s.run("new", "hook", "first", "--resource", resourcePath)
	s.Require().NoError(err)

	// Overwrite the on-disk veil-types.ts with garbage; the second
	// `new hook` invocation should regenerate it (and the rest of the
	// pipeline should still leave the file in a valid state).
	typesPath := filepath.Join(s.root, "hooks", "veil-types.ts")
	s.Require().NoError(os.WriteFile(typesPath, []byte("// stale"), 0644))

	_, err = s.run("new", "hook", "second", "--resource", resourcePath)
	s.Require().NoError(err)

	types, err := os.ReadFile(typesPath)
	s.Require().NoError(err)
	s.Contains(string(types), "export interface WorkerSpec")
	s.NotContains(string(types), "// stale")
}

func (s *NewSuite) TestNewHookAutoDetectRefusesAmbiguousResources() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	svcDir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(svcDir, 0755))
	_, err = s.run("new", "resource", "alpha", "--kind", "worker", "--out", filepath.Join(svcDir, "alpha.json"))
	s.Require().NoError(err)
	_, err = s.run("new", "resource", "beta", "--kind", "worker", "--out", filepath.Join(svcDir, "beta.json"))
	s.Require().NoError(err)

	s.T().Chdir(svcDir)
	_, err = s.run("new", "hook", "annotate")
	s.Require().Error(err)
	s.Contains(err.Error(), "multiple resource files")
}

func (s *NewSuite) TestNewHookRejectsUnknownKind() {
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{"kinds": [], "registries": { "": "./public/r/registry.json" }}`), 0644))

	_, err := s.run("new", "hook", "annotate", "--kind", "missing")
	s.Require().Error(err)
	s.Contains(err.Error(), "not found in registry")
}

func (s *NewSuite) TestNewResourceScaffoldsFile() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "resource", "my-worker", "--kind", "worker")
	s.Require().NoError(err)

	resourcePath := filepath.Join(s.root, "my-worker.json")
	s.FileExists(resourcePath)

	res := s.readJSON(resourcePath)
	expectedSchema := filepath.ToSlash(filepath.Join("public", "r", "worker", "kind.schema.json"))
	s.Equal(expectedSchema, res["$schema"])

	meta, ok := res["metadata"].(map[string]any)
	s.Require().True(ok)
	s.Equal("worker", meta["kind"])
	s.Equal("my-worker", meta["name"])

	spec, ok := res["spec"].(map[string]any)
	s.Require().True(ok)
	s.Empty(spec)
}

func (s *NewSuite) TestNewResourceWithOutFlag() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	subDir := filepath.Join(s.root, "services", "alpha")
	out := filepath.Join(subDir, "alpha.json")

	_, err = s.run("new", "resource", "alpha", "--kind", "worker", "--out", out)
	s.Require().NoError(err)

	s.FileExists(out)
	res := s.readJSON(out)
	// Schema path is relative to the output file's directory.
	s.Equal("../../public/r/worker/kind.schema.json", res["$schema"])
	meta := res["metadata"].(map[string]any)
	s.Equal("alpha", meta["name"])
}

func (s *NewSuite) TestNewResourceRejectsUnknownKind() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "resource", "thing", "--kind", "missing")
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "missing"`)
}

func (s *NewSuite) TestNewResourceRejectsExistingFile() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "resource", "thing", "--kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "resource", "thing", "--kind", "worker")
	s.Require().Error(err)
	s.Contains(err.Error(), "already exists")
}

func (s *NewSuite) TestNewResourceRequiresKindFlag() {
	_, err := s.run("new", "resource", "thing")
	s.Require().Error(err)
}

func (s *NewSuite) TestNewResourceRejectsInvalidName() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	_, err = s.run("new", "resource", "Bad Name", "--kind", "worker")
	s.Require().Error(err)
	s.Contains(err.Error(), "resource name")
}

func (s *NewSuite) TestNewHookRollsBackOnBuildFailure() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Corrupt the kind by referencing a hook that doesn't exist on disk.
	// The follow-up build will fail when validateKind stat's it.
	kindJSONPath := filepath.Join(s.root, ".veil", "kinds", "worker", "kind.json")
	s.Require().NoError(os.WriteFile(kindJSONPath, []byte(`{
  "name": "worker",
  "sources": ["./sources/source.txt"],
  "hooks": {"render": [{"path": "./hooks/src/hello-world.ts"}, {"path": "./hooks/src/missing.ts"}]},
  "schema": "./schema.json"
}`), 0644))

	before, err := os.ReadFile(kindJSONPath)
	s.Require().NoError(err)

	_, err = s.run("new", "hook", "annotate", "--kind", "worker")
	s.Require().Error(err)

	annotatePath := filepath.Join(s.root, ".veil", "kinds", "worker", "hooks", "src", "annotate.ts")
	_, statErr := os.Stat(annotatePath)
	s.True(os.IsNotExist(statErr), "annotate.ts should have been rolled back")

	after, err := os.ReadFile(kindJSONPath)
	s.Require().NoError(err)
	s.Equal(string(before), string(after), "kind.json should be restored to pre-scaffold contents")
}

func (s *NewSuite) readJSON(path string) map[string]any {
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	var out map[string]any
	s.Require().NoError(json.Unmarshal(data, &out))
	return out
}

func (s *NewSuite) writeJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(path, data, 0644))
}

// TestNewHookPreservesYAMLKindFormat verifies that when the kind file
// was authored as YAML, `veil new hook` reads it as YAML, mutates,
// and writes it back as YAML — not silently converted to JSON.
func (s *NewSuite) TestNewHookPreservesYAMLKindFormat() {
	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// Rewrite the scaffolded JSON kind as YAML, drop the JSON file so
	// the loader picks up the YAML variant.
	kindDir := filepath.Join(s.root, ".veil", "kinds", "worker")
	jsonPath := filepath.Join(kindDir, "kind.json")
	jsonData, err := os.ReadFile(jsonPath)
	s.Require().NoError(err)

	var raw map[string]any
	s.Require().NoError(json.Unmarshal(jsonData, &raw))
	// Drop $schema — yaml.v3 has no opinion about the value, but
	// keeping it would survive the round-trip and only adds noise.
	delete(raw, "$schema")

	yamlBytes, err := encodeYAMLForTest(raw)
	s.Require().NoError(err)
	yamlPath := filepath.Join(kindDir, "kind.yaml")
	s.Require().NoError(os.WriteFile(yamlPath, yamlBytes, 0644))
	s.Require().NoError(os.Remove(jsonPath))

	// Point veil.json at the YAML file.
	veilPath := filepath.Join(s.root, "veil.json")
	veil := s.readJSON(veilPath)
	veil["kinds"] = []any{"./.veil/kinds/worker/kind.yaml"}
	veilOut, err := json.MarshalIndent(veil, "", "  ")
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(veilPath, append(veilOut, '\n'), 0644))

	_, err = s.run("new", "hook", "annotate", "--kind", "worker")
	s.Require().NoError(err)

	// kind.yaml still exists, kind.json was not resurrected.
	s.FileExists(yamlPath)
	_, statErr := os.Stat(jsonPath)
	s.True(os.IsNotExist(statErr), "kind.json should not have been recreated")

	// kind.yaml must be parseable as YAML and have the new hook.
	roundTripped, err := os.ReadFile(yamlPath)
	s.Require().NoError(err)
	var reparsed map[string]any
	s.Require().NoError(decodeYAMLForTest(roundTripped, &reparsed))
	hooks := reparsed["hooks"].(map[string]any)
	render := hooks["render"].([]any)
	s.Require().Len(render, 2)
	s.Equal("./hooks/src/hello-world.ts", render[0].(map[string]any)["path"])
	s.Equal("./hooks/src/annotate.ts", render[1].(map[string]any)["path"])
}

// TestNewKindPreservesYAMLVeilConfig verifies that when veil.yaml is
// the project config, `veil new kind` rewrites it as YAML rather than
// silently swapping in a veil.json.
func (s *NewSuite) TestNewKindPreservesYAMLVeilConfig() {
	veilYAML := "kinds: []\nregistries:\n  \"\": ./public/r/registry.json\n"
	veilPath := filepath.Join(s.root, "veil.yaml")
	s.Require().NoError(os.WriteFile(veilPath, []byte(veilYAML), 0644))

	_, err := s.run("new", "kind", "worker")
	s.Require().NoError(err)

	// veil.yaml is still the project config; no veil.json appeared.
	s.FileExists(veilPath)
	_, statErr := os.Stat(filepath.Join(s.root, "veil.json"))
	s.True(os.IsNotExist(statErr), "veil.json should not have been created")

	roundTripped, err := os.ReadFile(veilPath)
	s.Require().NoError(err)
	var reparsed map[string]any
	s.Require().NoError(decodeYAMLForTest(roundTripped, &reparsed))
	s.Equal([]any{"./.veil/kinds/worker/kind.json"}, reparsed["kinds"])
}
