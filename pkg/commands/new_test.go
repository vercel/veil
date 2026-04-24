package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"
)

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
	s.Contains(string(types), "resource: Resource<WorkerSpec>;")
	s.Contains(string(types), "export interface Resource<Spec> {")
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
	s.Equal("worker", kind["name"])
	s.Equal("./schema.json", kind["schema"])
	s.Equal([]any{"./sources/source.txt"}, kind["sources"])
	hooksField, ok := kind["hooks"].(map[string]any)
	s.Require().True(ok)
	s.Equal([]any{"./hooks/src/hello-world.ts"}, hooksField["render"])

	veil := s.readJSON(filepath.Join(s.root, "veil.json"))
	s.Equal([]any{"./.veil/kinds/worker/kind.json"}, veil["kinds"])

	s.FileExists(filepath.Join(s.root, "public", "r", "worker", "kind.schema.json"))
	s.FileExists(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
	s.FileExists(filepath.Join(s.root, "public", "r", "registry.json"))

	compiled := s.readJSON(filepath.Join(s.root, "public", "r", "worker", "kind.json"))
	s.Equal("worker", compiled["name"])
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

func (s *NewSuite) TestNewKindReusesExistingVeilJSON() {
	veilJSON := filepath.Join(s.root, "veil.json")
	s.Require().NoError(os.WriteFile(veilJSON, []byte(`{"kinds": []}`), 0644))

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
	s.Equal([]any{"./hooks/src/hello-world.ts", "./hooks/src/annotate.ts"}, hooksField["render"])
}

func (s *NewSuite) TestNewHookRequiresKindFlag() {
	_, err := s.run("new", "hook", "annotate")
	s.Require().Error(err)
}

func (s *NewSuite) TestNewHookRejectsUnknownKind() {
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(`{"kinds": []}`), 0644))

	_, err := s.run("new", "hook", "annotate", "--kind", "missing")
	s.Require().Error(err)
	s.Contains(err.Error(), "not found in registry")
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
  "hooks": {"render": ["./hooks/src/hello-world.ts", "./hooks/src/missing.ts"]},
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
