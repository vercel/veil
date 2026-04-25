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

func (s *DiscoverSuite) TestFindsBareVeilJSON() {
	root := s.T().TempDir()
	nested := filepath.Join(root, "services", "api")
	s.Require().NoError(os.MkdirAll(nested, 0755))
	s.writeVeilJSON(root, `{"kinds":[]}`)

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
		"hooks": {"render": ["./hooks/inject-env.ts"]},
		"schema": "./schemas/service.schema.json"
	}`), 0644))
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/service.json"]}`)

	reg, err := Discover(nested)
	s.Require().NoError(err)

	s.Equal(root, reg.Root)
	s.Require().Len(reg.Kinds, 1)
	k := reg.Kinds[0]
	s.Equal("service", k.Name)
	s.Equal([]string{"./sources/deployment.yaml"}, k.Sources)
	s.Equal(kindsDir, k.Dir)
}

func (s *DiscoverSuite) TestErrorsWhenNoVeilJSON() {
	dir := s.T().TempDir()
	_, err := Discover(dir)
	s.Error(err)
}

func (s *DiscoverSuite) TestErrorsOnMissingKindFile() {
	root := s.T().TempDir()
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/missing.json"]}`)
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
	s.writeVeilJSON(root, `{"kinds": ["./.veil/kinds/bad.json"]}`)
	_, err := Discover(root)
	s.Error(err)
}

func (s *DiscoverSuite) TestLoadsVariablesWithDefaults() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
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

func (s *DiscoverSuite) TestRejectsUnknownVariableType() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
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
		"variables": {
			"env": { "type": "string", "enum": ["dev", "prod"], "default": "qa" }
		}
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), "default")
	s.Contains(err.Error(), "enum")
}

func (s *DiscoverSuite) TestRejectsDefaultTypeMismatch() {
	root := s.T().TempDir()
	path := s.writeVeilJSON(root, `{
		"kinds": [],
		"variables": { "replicas": { "type": "number", "default": "three" } }
	}`)
	_, err := Load(path)
	s.Require().Error(err)
	s.Contains(err.Error(), `variable "replicas"`)
	s.Contains(err.Error(), "expected number")
}
