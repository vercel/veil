package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type RenderSuite struct {
	suite.Suite
	root string
}

func TestRenderSuite(t *testing.T) {
	suite.Run(t, new(RenderSuite))
}

func (s *RenderSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.T().Chdir(s.root)
}

func (s *RenderSuite) writeConfig(body string) {
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte(body), 0644))
}

func (s *RenderSuite) run(args ...string) (string, error) {
	var buf bytes.Buffer
	app := NewApp()
	app.Writer = &buf
	app.ErrWriter = &buf
	err := app.Run(context.Background(), append([]string{"veil"}, args...))
	return buf.String(), err
}

func (s *RenderSuite) TestRenderFailsOnMissingRequiredVariable() {
	s.writeConfig(`{
  "kinds": [],
  "variables": { "region": { "type": "string" } }
}`)

	_, err := s.run("render", ".")
	s.Require().Error(err)
	s.Contains(err.Error(), `required variable "region"`)
}

func (s *RenderSuite) TestRenderPassesVariableResolutionWithCLIFlag() {
	s.writeConfig(`{
  "kinds": [],
  "variables": { "region": { "type": "string" } }
}`)

	// With --var provided, variable resolution succeeds; the next failure
	// is "resource at . not in catalog" which proves we got past variable
	// resolution without complaining about region.
	_, err := s.run("render", ".", "--var", "region=iad1")
	s.Require().Error(err)
	s.NotContains(err.Error(), "region")
	s.Contains(err.Error(), "not in catalog")
}

func (s *RenderSuite) TestRenderRejectsBadTypeCoercion() {
	s.writeConfig(`{
  "kinds": [],
  "variables": { "replicas": { "type": "number" } }
}`)

	_, err := s.run("render", ".", "--var", "replicas=lots")
	s.Require().Error(err)
	s.Contains(err.Error(), "expected number")
}

func (s *RenderSuite) TestRenderResolvesFromEnv() {
	s.writeConfig(`{
  "kinds": [],
  "variables": { "region": { "type": "string" } }
}`)

	s.T().Setenv("VEIL_VAR_REGION", "iad1")
	// Variable resolution succeeds via env; subsequent error is the
	// expected "no resource at path" since this test doesn't write one.
	_, err := s.run("render", ".")
	s.Require().Error(err)
	s.NotContains(err.Error(), "region")
	s.Contains(err.Error(), "not in catalog")
}

func (s *RenderSuite) TestRenderFailsOnBadRegistryPath() {
	s.writeConfig(`{ "kinds": [] }`)
	_, err := s.run("render", ".", "--registry", "/nonexistent/registry.json")
	s.Require().Error(err)
	s.Contains(err.Error(), "registry")
}

func (s *RenderSuite) TestRenderFailsOnDuplicateKindAcrossRegistries() {
	s.writeConfig(`{ "kinds": [] }`)

	regA := filepath.Join(s.root, "a", "registry.json")
	regB := filepath.Join(s.root, "b", "registry.json")
	s.Require().NoError(os.MkdirAll(filepath.Dir(regA), 0755))
	s.Require().NoError(os.MkdirAll(filepath.Dir(regB), 0755))
	entry := `{"name":"svc","path":"./svc/kind.json","schema":"./svc/kind.schema.json"}`
	s.Require().NoError(os.WriteFile(regA, []byte(`{"kinds": {"svc": `+entry+`}}`), 0644))
	s.Require().NoError(os.WriteFile(regB, []byte(`{"kinds": {"svc": `+entry+`}}`), 0644))

	_, err := s.run("render", ".", "--registry", regA, "--registry", regB)
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "svc" provided by multiple registries`)
}

func (s *RenderSuite) TestRenderLoadsAliasedRegistriesFromVeilJSON() {
	regPath := filepath.Join(s.root, "vendor", "registry.json")
	s.Require().NoError(os.MkdirAll(filepath.Dir(regPath), 0755))
	s.Require().NoError(os.WriteFile(regPath, []byte(`{"kinds": {}}`), 0644))

	s.writeConfig(`{
  "kinds": [],
  "registries": { "acme": "./vendor/registry.json" }
}`)

	// Aliased registries load successfully; no resource in catalog yet so
	// the render call still bottoms out at the catalog lookup.
	_, err := s.run("render", ".")
	s.Require().Error(err)
	s.Contains(err.Error(), "not in catalog")
}

func (s *RenderSuite) TestRenderClearErrorWhenPathDoesNotExist() {
	s.writeConfig(`{ "kinds": [], "registries": {} }`)

	_, err := s.run("render", "missing/path.json")
	s.Require().Error(err)
	s.Contains(err.Error(), "no file at")
	s.Contains(err.Error(), "missing/path.json")
}

func (s *RenderSuite) TestRenderRejectsBareKindWhenDefaultAliasMissing() {
	regPath := filepath.Join(s.root, "vendor", "registry.json")
	s.Require().NoError(os.MkdirAll(filepath.Dir(regPath), 0755))
	s.Require().NoError(os.WriteFile(regPath, []byte(`{"kinds": {}}`), 0644))

	s.writeConfig(`{
  "kinds": [],
  "registries": { "derp": "./vendor/registry.json" },
  "resource_discovery": { "paths": ["resources/*.json"] }
}`)

	resourcesDir := filepath.Join(s.root, "resources")
	s.Require().NoError(os.MkdirAll(resourcesDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(resourcesDir, "thing.json"), []byte(`{
  "metadata": { "kind": "service", "name": "thing" },
  "spec": {}
}`), 0644))

	_, err := s.run("render", "./resources/thing.json")
	s.Require().Error(err)
	s.Contains(err.Error(), `registry alias "" is not configured`)
}

func (s *RenderSuite) TestRenderRejectsResourceWithUnknownAlias() {
	regPath := filepath.Join(s.root, "vendor", "registry.json")
	s.Require().NoError(os.MkdirAll(filepath.Dir(regPath), 0755))
	s.Require().NoError(os.WriteFile(regPath, []byte(`{"kinds": {}}`), 0644))

	s.writeConfig(`{
  "kinds": [],
  "registries": { "acme": "./vendor/registry.json" },
  "resource_discovery": { "paths": ["resources/*.json"] }
}`)

	resourcesDir := filepath.Join(s.root, "resources")
	s.Require().NoError(os.MkdirAll(resourcesDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(resourcesDir, "thing.json"), []byte(`{
  "metadata": { "kind": "unknown/service", "name": "thing" },
  "spec": {}
}`), 0644))

	_, err := s.run("render", "./resources/thing.json")
	s.Require().Error(err)
	s.Contains(err.Error(), `registry alias "unknown" is not configured`)
}
