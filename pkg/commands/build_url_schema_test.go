package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/protoencode"
	"github.com/vercel/veil/pkg/schemaload"
	"github.com/vercel/veil/pkg/vfs"
)

type URLSchemaBuildSuite struct {
	suite.Suite
	root string
}

func TestURLSchemaBuildSuite(t *testing.T) {
	suite.Run(t, new(URLSchemaBuildSuite))
}

func (s *URLSchemaBuildSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.T().Chdir(s.root)
}

func (s *URLSchemaBuildSuite) writeFile(path, content string) {
	abs := filepath.Join(s.root, path)
	s.Require().NoError(os.MkdirAll(filepath.Dir(abs), 0755))
	s.Require().NoError(os.WriteFile(abs, []byte(content), 0644))
}

func (s *URLSchemaBuildSuite) run(args ...string) error {
	var output bytes.Buffer
	app := NewApp()
	app.Writer = &output
	app.ErrWriter = &output
	return app.Run(context.Background(), append([]string{"veil"}, args...))
}

func (s *URLSchemaBuildSuite) fixture(ref string) {
	s.writeFile("veil.json", `{
  "kinds": ["./alpha/kind.json", "./beta/kind.json"],
  "registries": {"": "./public/r/registry.json"},
  "resource_discovery": {"paths": ["resources/*.json"]}
}`)
	for _, name := range []string{"alpha", "beta"} {
		kind := map[string]any{
			"name":    name,
			"schema":  ref,
			"sources": []map[string]any{{"path": "./sources/app.json", "schema": ref}},
		}
		if name == "alpha" {
			kind["hooks"] = map[string]any{"render": []map[string]any{{"path": "./hooks/src/render.ts"}}}
		} else {
			kind["hooks"] = map[string]any{"dependents": []map[string]any{{
				"kind": "alpha", "paths": []string{"./hooks/src/dependent.ts"}, "params_path": ref,
			}}}
		}
		data, err := json.Marshal(kind)
		s.Require().NoError(err)
		s.writeFile(name+"/kind.json", string(data))
		s.writeFile(name+"/sources/app.json", `{"replicas":1}`)
	}
	s.writeFile("alpha/hooks/src/render.ts", `import type { RenderHook } from './veil-types';
export default { render(ctx, fs) {
  fs.getSourcesAppJson().setContent({ replicas: ctx.resource.spec.replicas });
  return fs;
} } satisfies RenderHook;
`)
	s.writeFile("beta/hooks/src/dependent.ts", `import type { AlphaDependentHook } from './veil-types';
export default { render(ctx, fs) {
  const file = fs.getSourcesAppJson();
  file.setContent({ replicas: file.getContent().replicas + ctx.params.replicas });
  return fs;
} } satisfies AlphaDependentHook;
`)
	s.writeFile("resources/alpha.json", `{
  "metadata":{"kind":"alpha","name":"app"},
  "spec":{"replicas":4},
  "dependencies":[{"kind":"beta","name":"backend","params":{"replicas":3}}]
}`)
	s.writeFile("resources/beta.json", `{"metadata":{"kind":"beta","name":"backend"},"spec":{"replicas":2}}`)
}

func (s *URLSchemaBuildSuite) TestBuildDeduplicatesAllSchemaFieldsAndRendersOffline() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"],"additionalProperties":false,"description":"fetch %d"}`, requests.Load())
	}))
	s.T().Cleanup(server.Close)
	ref := server.URL + "/shared.schema.json?version=1"
	s.fixture(ref)

	_, err := config.Load(filepath.Join(s.root, "veil.json"))
	s.Require().NoError(err)
	_, err = config.Discover(filepath.Join(s.root, "alpha", "hooks"))
	s.Require().NoError(err)
	_ = NewApp()
	s.Equal(int32(0), requests.Load(), "config discovery and CLI setup must not fetch schemas")

	for buildNumber := int32(1); buildNumber <= 2; buildNumber++ {
		s.Require().NoError(s.run("build"))
		s.Equal(buildNumber, requests.Load(), "every schema consumer shares one fetch per build")
		for _, name := range []string{"alpha", "beta"} {
			var compiled veilv1.Kind
			s.Require().NoError(protoencode.ReadFile(filepath.Join(s.root, "public", "r", name, "kind.json"), &compiled))
			schema := compiled.SourceSchemas["sources/app.json"]
			s.Contains(schema, fmt.Sprintf(`"description":"fetch %d"`, buildNumber))
			var raw map[string]any
			s.Require().NoError(json.Unmarshal([]byte(schema), &raw))
			s.Equal("object", raw["type"])
			if name == "beta" {
				s.Require().Len(compiled.GetHooks().GetDependents(), 1)
				s.JSONEq(schema, compiled.GetHooks().GetDependents()[0].ParamsSchema)
			}
			var resourceSchema map[string]any
			s.Require().NoError(protoencode.ReadFile(filepath.Join(s.root, "public", "r", name, "kind.schema.json"), &resourceSchema))
			properties := resourceSchema["properties"].(map[string]any)
			s.Equal(raw, properties["spec"])
			types, err := os.ReadFile(filepath.Join(s.root, name, "hooks", "src", "veil-types.ts"))
			s.Require().NoError(err)
			s.Contains(string(types), "export interface Shared {")
			s.Contains(string(types), "replicas: number;")
			s.Contains(string(types), "getSourcesAppJson(): File<Shared>;")
			s.NotContains(string(types), "?version")
			if name == "beta" {
				s.Contains(string(types), "export interface AlphaShared {")
			}
		}
	}

	server.Close()
	s.Require().NoError(s.run("render", "resources/alpha.json", "--out", "rendered"))
	data, err := os.ReadFile(filepath.Join(s.root, "rendered", "app", "sources", "app.json"))
	s.Require().NoError(err)
	s.JSONEq(`{"replicas":7}`, string(data))
	s.Equal(int32(2), requests.Load())
}

func (s *URLSchemaBuildSuite) TestPipelineRestoresLoadersAndRefetchesOnReuse() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"]}`)
	}))
	defer server.Close()
	s.fixture(server.URL + "/shared.schema.json")
	reg, err := config.Load(filepath.Join(s.root, "veil.json"))
	s.Require().NoError(err)
	previous := schemaload.New(context.Background())
	reg.Kinds[0].SchemaLoader = previous
	for i := int32(1); i <= 2; i++ {
		_, err = runBuildPipeline(context.Background(), reg, vfs.NewMem(), buildPipelineOpts{})
		s.Require().NoError(err)
		s.Equal(i, requests.Load())
		s.Same(previous, reg.Kinds[0].SchemaLoader)
		s.Nil(reg.Kinds[1].SchemaLoader)
	}
	reg.Kinds[0].Schema = server.URL + "/missing.schema.json#fragment"
	_, err = runBuildPipeline(context.Background(), reg, vfs.NewMem(), buildPipelineOpts{})
	s.Require().Error(err)
	s.Same(previous, reg.Kinds[0].SchemaLoader)
	s.Nil(reg.Kinds[1].SchemaLoader)
}

func (s *URLSchemaBuildSuite) TestSourceSchemaURLRequiresFilename() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"type":"object"}`)
	}))
	defer server.Close()
	for _, suffix := range []string{"", "/", "/schemas/", "/.schema.json"} {
		s.Run(suffix, func() {
			s.fixture(server.URL + suffix + "?format=json")
			err := s.run("build", "--no-typecheck")
			s.Require().Error(err)
			s.Contains(err.Error(), "no usable filename")
		})
	}
}

func (s *URLSchemaBuildSuite) TestSourceSchemaStillRequiresJSON() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "type: object\nproperties:\n  replicas:\n    type: integer\n")
	}))
	defer server.Close()
	s.fixture(server.URL + "/shared.schema.yaml")
	err := s.run("build", "--no-typecheck")
	s.Require().Error(err)
	s.Contains(err.Error(), "invalid JSON")
}
