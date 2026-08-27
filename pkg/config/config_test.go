package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
)

type DiscoverSuite struct {
	suite.Suite
}

func TestDiscoverSuite(t *testing.T) {
	suite.Run(t, new(DiscoverSuite))
}

// writeBareVeilJSON writes a minimal veil.json at the given root directory
// and returns its path. Most tests use this to set up a project root.
func (s *DiscoverSuite) writeVeilJSON(root, body string) string {
	path := filepath.Join(root, "veil.json")
	s.Require().NoError(os.WriteFile(path, []byte(body), 0644))
	return path
}

// stockRegistries is the minimal valid registries map appended to most
// test fixtures so the proto's `registries: required` constraint passes
// without each test having to spell it out. The path is a placeholder —
// config.Load doesn't try to fetch registry contents at load time.
const stockRegistries = `"registries": { "": "./registry.json" }`

func (s *DiscoverSuite) TestFindsBareVeilJSON() {
	root := s.T().TempDir()
	nested := filepath.Join(root, "services", "api")
	s.Require().NoError(os.MkdirAll(nested, 0755))
	s.writeVeilJSON(root, `{"kinds":[], `+stockRegistries+`}`)

	reg, err := Discover(nested)
	s.Require().NoError(err)
	s.Equal(root, reg.Root, "Root is the directory housing veil.json")
}

func (s *DiscoverSuite) TestFindsVeilJSONFromNestedDirectory() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	nested := filepath.Join(root, "services", "api")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.MkdirAll(nested, 0755))

	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	reg, err := Discover(nested)
	s.Require().NoError(err)

	s.Equal(root, reg.Root)
	s.Require().Len(reg.Kinds, 1)
	k := reg.Kinds[0]
	s.Equal("service", k.Name)
	s.Equal([]string{"./sources/deployment.yaml"}, k.SourcePaths())
	s.Equal(kindsDir, k.Dir)
}

func (s *DiscoverSuite) TestErrorsWhenNoVeilJSON() {
	dir := s.T().TempDir()
	_, err := Discover(dir)
	s.Error(err)
}

func (s *DiscoverSuite) TestErrorsOnMissingKindFile() {
	root := s.T().TempDir()
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/missing.json"], `+stockRegistries+`}`)
	_, err := Discover(root)
	s.Error(err)
}

func (s *DiscoverSuite) TestErrorsWhenKindMissingName() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "bad.json"), []byte(`{
		"sources": ["./deployment.yaml"]
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/bad.json"], `+stockRegistries+`}`)
	_, err := Discover(root)
	s.Error(err)
}

func (s *DiscoverSuite) TestLoadsVariablesWithDefaults() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": {
			"env": { "type": "string", "default": "dev" },
			"region": { "type": "string" },
			"replicas": { "type": "number", "default": 3 },
			"debug": { "type": "bool", "default": false }
		}
	}`)

	reg, err := Load(path)
	s.Require().NoError(err)
	s.Require().Len(reg.Variables, 4)

	env := reg.Variables["env"]
	s.Equal(veilv1.VariableType_string, env.Type)
	s.True(HasDefault(env))
	defVal, err := ParsedDefault(env)
	s.Require().NoError(err)
	s.Equal("dev", defVal)

	region := reg.Variables["region"]
	s.False(HasDefault(region))

	replicas := reg.Variables["replicas"]
	rv, err := ParsedDefault(replicas)
	s.Require().NoError(err)
	s.Equal(float64(3), rv)

	debug := reg.Variables["debug"]
	dv, err := ParsedDefault(debug)
	s.Require().NoError(err)
	s.Equal(false, dv)
}

func (s *DiscoverSuite) TestLoadsCliVersion() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"cli_version": "v1.4.0"
	}`)

	reg, err := Load(path)
	s.Require().NoError(err)
	s.Equal("v1.4.0", reg.CliVersion)
}

func (s *DiscoverSuite) TestCliVersionDefaultsEmptyWhenUnset() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{"kinds": [], `+stockRegistries+`}`)

	reg, err := Load(path)
	s.Require().NoError(err)
	s.Empty(reg.CliVersion)
}

func (s *DiscoverSuite) TestRejectsUnknownVariableType() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": { "x": { "type": "object" } }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `variable "x"`)
	s.Contains(err.Error(), `"string"`)
}

func (s *DiscoverSuite) TestAcceptsEnumOnStringAndNumber() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": {
			"env":      { "type": "string", "enum": ["dev", "staging", "prod"], "default": "dev" },
			"replicas": { "type": "number", "enum": [1, 3, 5] }
		}
	}`)
	reg, err := Load(path)
	s.Require().NoError(err)
	env := reg.Variables["env"]
	vals, err := ParsedEnum(env)
	s.Require().NoError(err)
	s.Equal([]any{"dev", "staging", "prod"}, vals)
}

func (s *DiscoverSuite) TestRejectsEnumOnBool() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": { "debug": { "type": "bool", "enum": [true, false] } }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "enum is not supported for bool")
}

func (s *DiscoverSuite) TestRejectsDefaultNotInEnum() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": {
			"env": { "type": "string", "enum": ["dev", "prod"], "default": "qa" }
		}
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "default")
	s.Contains(err.Error(), "enum")
}

func (s *DiscoverSuite) TestKindVariablesMergeIntoRegistry() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/service.schema.json",
		"variables": {
			"replicas": { "type": "number", "default": 3 }
		}
	}`), 0644))
	path := s.writeVeilJSON(root, `{
		"kinds": ["./.veil/kinds/service.json"],
		`+stockRegistries+`,
		"variables": {
			"env": { "type": "string", "default": "dev" }
		}
	}`)

	reg, err := Load(path)
	s.Require().NoError(err)
	s.Require().Len(reg.Variables, 2)

	env := reg.Variables["env"]
	s.Equal(veilv1.VariableType_string, env.Type)

	replicas := reg.Variables["replicas"]
	s.Equal(veilv1.VariableType_number, replicas.Type)
	rv, err := ParsedDefault(replicas)
	s.Require().NoError(err)
	s.Equal(float64(3), rv)
}

func (s *DiscoverSuite) TestKindVariablesConflictWithVeilJSON() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/service.schema.json",
		"variables": {
			"env": { "type": "string" }
		}
	}`), 0644))
	path := s.writeVeilJSON(root, `{
		"kinds": ["./.veil/kinds/service.json"],
		`+stockRegistries+`,
		"variables": {
			"env": { "type": "string", "default": "dev" }
		}
	}`)

	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `variable "env"`)
	s.Contains(err.Error(), "veil.json")
	s.Contains(err.Error(), `kind "service"`)
}

func (s *DiscoverSuite) TestKindVariablesConflictAcrossKinds() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/service.schema.json",
		"variables": {
			"region": { "type": "string", "default": "us-east-1" }
		}
	}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "worker.json"), []byte(`{
		"name": "worker",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/worker.schema.json",
		"variables": {
			"region": { "type": "string" }
		}
	}`), 0644))
	path := s.writeVeilJSON(root, `{
		"kinds": ["./.veil/kinds/service.json", "./.veil/kinds/worker.json"],
		`+stockRegistries+`
	}`)

	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `variable "region"`)
	s.Contains(err.Error(), `kind "service"`)
	s.Contains(err.Error(), `kind "worker"`)
}

func (s *DiscoverSuite) TestKindVariableValidationErrorsMentionKind() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {"render": [{"path": "./hooks/inject-env.ts"}]},
		"schema": "./schemas/service.schema.json",
		"variables": {
			"replicas": { "type": "number", "default": "three" }
		}
	}`), 0644))
	path := s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "service"`)
	s.Contains(err.Error(), `variable "replicas"`)
	s.Contains(err.Error(), "expected number")
}

func (s *DiscoverSuite) TestRejectsDefaultTypeMismatch() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		`+stockRegistries+`,
		"variables": { "replicas": { "type": "number", "default": "three" } }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `variable "replicas"`)
	s.Contains(err.Error(), "expected number")
}

func (s *DiscoverSuite) TestAcceptsRenderHookStringShorthand() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {
			"render": [
				"./hooks/inject-env.ts",
				{ "path": "./hooks/inject-image.ts" },
				"./hooks/inject-probes.ts"
			]
		},
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	reg, err := Load(filepath.Join(root, "veil.json"))
	s.Require().NoError(err)
	s.Require().Len(reg.Kinds, 1)

	render := reg.Kinds[0].RenderHooks()
	s.Require().Len(render, 3)
	s.Equal("./hooks/inject-env.ts", render[0].GetPath())
	s.Nil(render[0].GetAccess())
	s.Equal("./hooks/inject-image.ts", render[1].GetPath())
	s.Equal("./hooks/inject-probes.ts", render[2].GetPath())
}

func (s *DiscoverSuite) TestAcceptsValidRegistryAliases() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": {
			"": "./public/r/registry.json",
			"acme": "./vendor/acme.json",
			"@scope": "./vendor/scoped.json",
			"my-org_42": "./vendor/org.json"
		}
	}`)
	_, err := Load(path)
	s.Require().NoError(err)
}

func (s *DiscoverSuite) TestRejectsAliasStartingWithDot() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { ".local": "./registry.json" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `.local`)
	s.Contains(err.Error(), "pattern")
}

func (s *DiscoverSuite) TestRejectsAliasContainingSlash() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { "foo/bar": "./registry.json" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "foo/bar")
	s.Contains(err.Error(), "pattern")
}

func (s *DiscoverSuite) TestRejectsAliasContainingColon() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { "scheme:thing": "./registry.json" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "scheme:thing")
	s.Contains(err.Error(), "pattern")
}

func (s *DiscoverSuite) TestAcceptsValidRegistryLocations() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": {
			"":      "./public/r/registry.json",
			"abs":   "/abs/path/to/registry.json",
			"http":  "http://example.com/registry.json",
			"https": "https://example.com/path/to/registry.json"
		}
	}`)
	_, err := Load(path)
	s.Require().NoError(err)
}

func (s *DiscoverSuite) TestRejectsRegistryLocationWithUnsupportedScheme() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { "remote": "ftp://example.com/registry.json" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "remote")
	s.Contains(err.Error(), "pattern")
}

func (s *DiscoverSuite) TestRejectsRegistryLocationFileScheme() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { "local": "file:///etc/registry.json" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "pattern")
}

func (s *DiscoverSuite) TestRejectsEmptyRegistryLocation() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"registries": { "blank": "" }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "blank")
}

// TestLoadsVeilYAML exercises the YAML ingestion path: a project rooted
// at veil.yaml is discovered the same way as one rooted at veil.json,
// and a kind declared in kind.yaml loads through the same code path.
func (s *DiscoverSuite) TestLoadsVeilYAML() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))

	kindYAML := `name: service
sources:
  - ./sources/deployment.yaml
hooks:
  render:
    - path: ./hooks/inject-env.ts
schema: ./schemas/service.schema.json
`
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.yaml"), []byte(kindYAML), 0644))

	veilYAML := `kinds:
  - ./.veil/kinds/service.yaml
registries:
  "": ./registry.json
`
	configPath := filepath.Join(root, "veil.yaml")
	s.Require().NoError(os.WriteFile(configPath, []byte(veilYAML), 0644))

	reg, err := Discover(root)
	s.Require().NoError(err)
	s.Equal(configPath, reg.ConfigPath, "ConfigPath echoes back the on-disk file")
	s.Equal(root, reg.Root)
	s.Require().Len(reg.Kinds, 1)

	k := reg.Kinds[0]
	s.Equal("service", k.Name)
	s.Equal([]string{"./sources/deployment.yaml"}, k.SourcePaths())
	s.Equal(filepath.Join(kindsDir, "service.yaml"), k.Path)
	s.Equal(kindsDir, k.Dir)
	render := k.RenderHooks()
	s.Require().Len(render, 1)
	s.Equal("./hooks/inject-env.ts", render[0].GetPath())
}

// TestVeilJSONWinsOverYAMLAtSameDir documents the precedence rule:
// when both veil.json and veil.yaml exist in the same directory, JSON
// wins (matches the historical behavior + scaffolder default).
func (s *DiscoverSuite) TestVeilJSONWinsOverYAMLAtSameDir() {
	root := s.T().TempDir()
	jsonPath := s.writeVeilJSON(root, `{"kinds":[], `+stockRegistries+`}`)
	s.Require().NoError(os.WriteFile(filepath.Join(root, "veil.yaml"),
		[]byte("kinds: []\nregistries:\n  \"\": ./registry.json\n"), 0644))

	reg, err := Discover(root)
	s.Require().NoError(err)
	s.Equal(jsonPath, reg.ConfigPath)
}

func (s *DiscoverSuite) TestAcceptsRenderHookObjectWithAccess() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": ["./sources/deployment.yaml"],
		"hooks": {
			"render": [
				{
					"path": "./hooks/inject-env.ts",
					"access": {
						"env": [{"name": "API_KEY", "description": "auth token"}]
					}
				}
			]
		},
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	reg, err := Load(filepath.Join(root, "veil.json"))
	s.Require().NoError(err)

	render := reg.Kinds[0].RenderHooks()
	s.Require().Len(render, 1)
	s.Equal("./hooks/inject-env.ts", render[0].GetPath())
	envs := render[0].GetAccess().GetEnv()
	s.Require().Len(envs, 1)
	s.Equal("API_KEY", envs[0].GetName())
	s.Equal("auth token", envs[0].GetDescription())
}

func (s *DiscoverSuite) TestAcceptsSourceObjectWithSchema() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.MkdirAll(filepath.Join(kindsDir, "schemas"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "schemas", "deployment.schema.json"), []byte(`{"type": "object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": [
			"./sources/plain.yaml",
			{ "path": "./sources/deployment.yaml", "schema": "./schemas/deployment.schema.json" }
		],
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	reg, err := Load(filepath.Join(root, "veil.json"))
	s.Require().NoError(err)
	s.Require().Len(reg.Kinds, 1)

	k := reg.Kinds[0]
	s.Equal([]string{"./sources/plain.yaml", "./sources/deployment.yaml"}, k.SourcePaths())
	defs := k.SourceDefs()
	s.Require().Len(defs, 2)
	s.Equal("", defs[0].GetSchema())
	s.Equal("./schemas/deployment.schema.json", defs[1].GetSchema())
}

func (s *DiscoverSuite) TestRejectsSourceWithMissingSchemaFile() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": [
			{ "path": "./sources/deployment.yaml", "schema": "./schemas/missing.schema.json" }
		],
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	_, err := Load(filepath.Join(root, "veil.json"))
	s.Require().Error(err)
	s.Contains(err.Error(), "missing.schema.json")
}

func (s *DiscoverSuite) TestRejectsSchemaDeclaredSourceWithUnsupportedExtension() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.MkdirAll(filepath.Join(kindsDir, "schemas"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "schemas", "deployment.schema.json"), []byte(`{"type":"object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": [
			{ "path": "./sources/deployment.conf", "schema": "./schemas/deployment.schema.json" }
		],
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	_, err := Load(filepath.Join(root, "veil.json"))
	s.Require().Error(err)
	s.Contains(err.Error(), "deployment.conf")
	s.Contains(err.Error(), ".json, .yaml, or .yml")
}

func (s *DiscoverSuite) TestAcceptsSchemaDeclaredSourceWithSupportedExtensions() {
	root := s.T().TempDir()
	kindsDir := filepath.Join(root, ArtifactsDir, "kinds")
	s.Require().NoError(os.MkdirAll(kindsDir, 0755))
	s.Require().NoError(os.MkdirAll(filepath.Join(kindsDir, "schemas"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "schemas", "deployment.schema.json"), []byte(`{"type":"object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(kindsDir, "service.json"), []byte(`{
		"name": "service",
		"sources": [
			{ "path": "./sources/a.json", "schema": "./schemas/deployment.schema.json" },
			{ "path": "./sources/b.yaml", "schema": "./schemas/deployment.schema.json" },
			{ "path": "./sources/c.yml", "schema": "./schemas/deployment.schema.json" }
		],
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"], `+stockRegistries+`}`)

	reg, err := Load(filepath.Join(root, "veil.json"))
	s.Require().NoError(err)
	s.Require().Len(reg.Kinds, 1)
}
