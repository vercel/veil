package e2e

import (
	"os"
	"path/filepath"
)

func (s *E2ESuite) TestDependentSourcesManagedLifecycle() {
	dir, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	files := map[string]string{
		"veil.json": `{
  "kinds": ["kinds/service/kind.json", "kinds/table/kind.json"],
  "registries": {"": "public/r/registry.json"},
  "resource_discovery": {"paths": ["resources/*.json"]}
}`,
		"kinds/service/kind.json": `{
  "name": "service", "schema": "schema.json", "sources": ["app.txt"]
}`,
		"kinds/service/schema.json": `{"type":"object","additionalProperties":false}`,
		"kinds/service/app.txt":     "service application\n",
		"kinds/table/kind.json": `{
  "name": "table", "schema": "schema.json",
  "hooks": {"dependents": [{
    "kind": "service", "paths": ["hooks/src/grant.ts"],
    "params_path": "params.json", "sources": ["grant.txt"]
  }]}
}`,
		"kinds/table/schema.json": `{"type":"object","additionalProperties":false}`,
		"kinds/table/params.json": `{
  "type":"object", "properties":{"access":{"type":"string"}},
  "required":["access"], "additionalProperties":false
}`,
		"kinds/table/grant.txt": "access template",
		"kinds/table/hooks/src/grant.ts": `import type { ServiceDependentHook } from './veil-types';
const grant: ServiceDependentHook = {
  render(ctx, fs) {
    const file = ctx.sources.getGrantTxt();
    file.setContent(file.getContent() + ':' + ctx.self.metadata.name + ':' + ctx.consumer.metadata.name + ':' + ctx.params.access);
    file.setOutputPath('grants/' + ctx.self.metadata.name + '.txt');
    return fs;
  }
};
export default grant;
`,
		"resources/alpha.json": `{"metadata":{"kind":"table","name":"alpha"},"spec":{}}`,
		"resources/beta.json":  `{"metadata":{"kind":"table","name":"beta"},"spec":{}}`,
		"resources/one.json": `{
  "metadata":{"kind":"service","name":"one"}, "spec":{},
  "dependencies":[
    {"kind":"table","name":"alpha","params":{"access":"read"}},
    {"kind":"table","name":"beta","params":{"access":"write"}}
  ]
}`,
		"resources/two.json": `{
  "metadata":{"kind":"service","name":"two"}, "spec":{},
  "dependencies":[
    {"kind":"table","name":"alpha","params":{"access":"write"}},
    {"kind":"table","name":"beta","params":{"access":"read"}}
  ]
}`,
	}
	for name, body := range files {
		s.write(dir, name, body)
	}
	stdout, err := s.runIn(dir, "build")
	s.Require().NoError(err, stdout)
	s.Require().NoError(os.RemoveAll(filepath.Join(dir, "kinds")))
	s.write(dir, "veil.json", `{
  "registries":{"":"public/r/registry.json"},
  "resource_discovery":{"paths":["resources/*.json"]}
}`)
	out := filepath.Join(dir, "out")
	s.write(dir, "out/one/notes.txt", "handwritten\n")
	stdout, err = s.runIn(dir, "render", "resources/one.json", "resources/two.json", "--out", out, "--managed", "--quiet")
	s.Require().NoError(err, stdout)
	s.Equal("access template:alpha:one:read", s.read(out, "one", "grants/alpha.txt"))
	s.Equal("access template:beta:one:write", s.read(out, "one", "grants/beta.txt"))
	s.Equal("access template:alpha:two:write", s.read(out, "two", "grants/alpha.txt"))
	s.Equal("access template:beta:two:read", s.read(out, "two", "grants/beta.txt"))

	s.write(dir, "resources/one.json", `{
  "metadata":{"kind":"service","name":"one"}, "spec":{},
  "dependencies":[{"kind":"table","name":"beta","params":{"access":"write"}}]
}`)
	stdout, err = s.runIn(dir, "render", "resources/one.json", "--out", out, "--managed", "--quiet")
	s.Require().NoError(err, stdout)
	s.NoFileExists(filepath.Join(out, "one", "grants", "alpha.txt"))
	s.Equal("access template:beta:one:write", s.read(out, "one", "grants/beta.txt"))
	s.Equal("service application\n", s.read(out, "one", "app.txt"))
	s.Equal("handwritten\n", s.read(out, "one", "notes.txt"))
	s.Equal("access template:alpha:two:write", s.read(out, "two", "grants/alpha.txt"))
	s.Equal("access template:beta:two:read", s.read(out, "two", "grants/beta.txt"))

	stdout, err = s.runIn(dir, "render", "resources/one.json", "--out", out, "--managed", "--quiet")
	s.Require().NoError(err, stdout)
	s.NoFileExists(filepath.Join(out, "one", "grants", "alpha.txt"))
	s.Equal("access template:beta:one:write", s.read(out, "one", "grants/beta.txt"))
}
