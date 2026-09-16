package commands

import (
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/tsc"
)

func (s *BuildSuite) dependentSourceFixture(packageMode bool) string {
	write := func(path, body string) {
		s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0755))
		s.Require().NoError(os.WriteFile(path, []byte(body), 0644))
	}
	service := filepath.Join(s.root, "kinds", "service")
	table := filepath.Join(s.root, "kinds", "table")
	write(filepath.Join(service, "kind.json"), `{"name":"service","sources":[],"schema":"schema.json"}`)
	write(filepath.Join(service, "schema.json"), `{"type":"object","properties":{}}`)
	write(filepath.Join(table, "schema.json"), `{"type":"object","properties":{}}`)
	write(filepath.Join(table, "params.json"), `{"type":"object","properties":{}}`)
	write(filepath.Join(table, "grant.schema.json"), `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"]}`)
	write(filepath.Join(table, "grant.json"), `{"replicas":1}`)
	write(filepath.Join(table, "kind.json"), `{"name":"table","sources":[],"schema":"schema.json","hooks":{"dependents":[{"kind":"service","paths":["hooks/src/grant.ts"],"params_path":"params.json","sources":[{"path":"./grant.json","schema":"grant.schema.json"}]}]}}`)
	imp := "./veil-types"
	cfg := map[string]any{"kinds": []any{"kinds/service/kind.json", "kinds/table/kind.json"}, "registries": map[string]string{"": "public/r/registry.json"}}
	if packageMode {
		imp = "@test/types/table"
		cfg["kinds"] = []any{map[string]any{"path": "kinds/service/kind.json", "import": map[string]string{"name": "@test/types/service", "value": "workspace:*"}}, map[string]any{"path": "kinds/table/kind.json", "import": map[string]string{"name": "@test/types/table", "value": "workspace:*"}}}
		cfg["generators"] = map[string]any{"types": map[string]string{"output_dir": "types"}}
		write(filepath.Join(s.root, "types", "package.json"), `{"name":"@test/types","version":"0.0.0","private":true}`)
		s.Require().NoError(os.MkdirAll(filepath.Join(s.root, "node_modules", "@test"), 0755))
		s.Require().NoError(os.Symlink(filepath.Join(s.root, "types"), filepath.Join(s.root, "node_modules", "@test", "types")))
		write(filepath.Join(table, "hooks", "package.json"), `{"name":"table-hooks","private":true}`)
		write(filepath.Join(service, "hooks", "package.json"), `{"name":"service-hooks","private":true}`)
	}
	data, err := json.Marshal(cfg)
	s.Require().NoError(err)
	write(filepath.Join(s.root, "veil.json"), string(data))
	hook := filepath.Join(table, "hooks", "src", "grant.ts")
	write(hook, `import type { ServiceDependentHook } from '`+imp+`';
const hook: ServiceDependentHook = { render(ctx, fs) {
 const f=ctx.sources.getGrantJson(); const doc=f.getContent();
 const replicas: number=doc.replicas; f.setContent({replicas:replicas+1});
 // @ts-expect-error dependent templates never widen the consumer FS
 fs.getGrantJson();
 return fs;
}}; export default hook;`)
	return hook
}

func (s *BuildSuite) TestDependentSourceTypesInlineAndPackage() {
	checker := tsc.Find()
	if checker == nil {
		s.T().Skip("no tsc/tsgo on PATH")
	}
	for _, mode := range []bool{false, true} {
		s.Run(map[bool]string{false: "inline", true: "package"}[mode], func() {
			hook := s.dependentSourceFixture(mode)
			_, err := s.run("build")
			s.Require().NoError(err)
			s.Require().NoError(checker.Check([]string{hook}))
			raw, err := os.ReadFile(hook)
			s.Require().NoError(err)
			// An invalid typed write must be rejected, not silently any-typed.
			bad := append(raw, []byte("\nimport type { ServiceDependentHookContext } from '"+map[bool]string{false: "./veil-types", true: "@test/types/table"}[mode]+"';\ndeclare const ctx: ServiceDependentHookContext; ctx.sources.getGrantJson().setContent({replicas:'bad'});\n")...)
			s.Require().NoError(os.WriteFile(hook, bad, 0644))
			s.Error(checker.Check([]string{hook}))
		})
	}
}

func (s *BuildSuite) TestDependentSourceTypeCollisionFailsWithoutTypechecker() {
	s.dependentSourceFixture(false)
	service := filepath.Join(s.root, "kinds", "service")
	s.Require().NoError(os.WriteFile(filepath.Join(service, "source-grant.schema.json"), []byte(`{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"]}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(service, "app.json"), []byte(`{"replicas":1}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(service, "kind.json"), []byte(`{"name":"service","sources":[{"path":"app.json","schema":"source-grant.schema.json"}],"schema":"schema.json"}`), 0644))
	_, err := s.run("build", "--no-typecheck")
	s.Require().Error(err)
	s.Contains(err.Error(), "ServiceSourceGrant")
	s.Contains(err.Error(), "service")
	s.Contains(err.Error(), "table")
	s.Contains(err.Error(), "app.json")
	s.Contains(err.Error(), "grant.json")
}

func (s *BuildSuite) TestDependentSourcesCompiledWithoutOriginalDirectory() {
	s.dependentSourceFixture(false)
	_, err := s.run("build", "--no-typecheck")
	s.Require().NoError(err)
	s.Require().NoError(os.RemoveAll(filepath.Join(s.root, "kinds")))
	reg, err := registry.Load([]registry.Reference{{Path: filepath.Join(s.root, "public", "r", "registry.json")}})
	s.Require().NoError(err)
	kind, err := reg.LoadKind("table")
	s.Require().NoError(err)
	source := kind.DependentSources["service"][0]
	s.NoError(source.Validate(map[string]any{"replicas": 2}))
	s.Error(source.Validate(map[string]any{"replicas": "bad"}))
	s.Equal("./grant.json", source.GetPath())
}

func (s *BuildSuite) TestDependentSourceSchemaRejectedAtBuild() {
	s.dependentSourceFixture(false)
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "kinds", "table", "grant.json"), []byte(`{"replicas":"bad"}`), 0644))
	_, err := s.run("build", "--no-typecheck")
	s.Require().Error(err)
	s.Contains(err.Error(), "replicas")
}

func (s *BuildSuite) TestDependentSourceDeclarationsValidate() {
	s.dependentSourceFixture(false)
	path := filepath.Join(s.root, "kinds", "table", "kind.json")
	raw := s.readJSON(path)
	dep := raw["hooks"].(map[string]any)["dependents"].([]any)[0].(map[string]any)
	for _, sources := range []any{[]any{"grant.json", "grant.json"}, []any{map[string]any{"path": "grant.txt", "schema": "grant.schema.json"}}, []any{map[string]any{"path": "grant.json", "schema": "missing.json"}}} {
		dep["sources"] = sources
		body, err := json.Marshal(raw)
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(path, body, 0644))
		_, err = config.LoadKind(path)
		s.Error(err)
	}
}

func (s *BuildSuite) TestOverrideDiscoversAndCopiesDependencyTemplates() {
	s.dependentSourceFixture(false)
	cfg := s.readJSON(filepath.Join(s.root, "veil.json"))
	cfg["resource_discovery"] = map[string]any{"paths": []string{"resources/*.json"}}
	body, err := json.Marshal(cfg)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), body, 0644))
	_, err = s.run("build", "--no-typecheck")
	s.Require().NoError(err)
	dir := filepath.Join(s.root, "resources")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	deps := []map[string]any{}
	for _, name := range []string{"alpha", "beta"} {
		deps = append(deps, map[string]any{"kind": "table", "name": name, "params": map[string]any{}})
		raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"kind": "table", "name": name}, "spec": map[string]any{}})
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(filepath.Join(dir, name+".json"), raw, 0644))
	}
	root := filepath.Join(dir, "one.json")
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"kind": "service", "name": "one"}, "spec": map[string]any{}, "dependencies": deps})
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(root, raw, 0644))
	first := registry.DependencySourceID("table", "alpha", "./grant.json")
	second := registry.DependencySourceID("table", "beta", "./grant.json")
	listing, err := s.run("override", root)
	s.Require().NoError(err)
	s.Contains(listing, first)
	s.Contains(listing, second)
	_, err = s.run("override", root, first, second)
	s.Require().NoError(err)
	for _, id := range []string{first, second} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(id)))
		s.Require().NoError(err)
		s.JSONEq(`{"replicas":1}`, string(data))
	}
	resource := s.readJSON(root)
	overrides := resource["metadata"].(map[string]any)["overrides"].([]any)
	s.Len(overrides, 2)
}
