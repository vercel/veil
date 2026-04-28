package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type RegistrySuite struct {
	suite.Suite
	root string
}

func TestRegistrySuite(t *testing.T) {
	suite.Run(t, new(RegistrySuite))
}

func (s *RegistrySuite) SetupTest() {
	s.root = s.T().TempDir()
}

// writeRegistry writes a minimal compiled registry.json plus a stub
// kind.json + kind.schema.json for each declared kind, so LoadKind
// resolves end-to-end. Returns the absolute path to the registry.json.
func (s *RegistrySuite) writeRegistry(subdir string, kinds ...string) string {
	dir := filepath.Join(s.root, subdir)
	s.Require().NoError(os.MkdirAll(dir, 0755))

	regKinds := "{"
	for i, k := range kinds {
		if i > 0 {
			regKinds += ","
		}
		kindDir := filepath.Join(dir, k)
		s.Require().NoError(os.MkdirAll(kindDir, 0755))
		s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "kind.json"), []byte(`{"name":"`+k+`"}`), 0644))
		s.Require().NoError(os.WriteFile(filepath.Join(kindDir, "kind.schema.json"), []byte(`{"type":"object"}`), 0644))
		regKinds += `"` + k + `":{"name":"` + k + `","path":"./` + k + `/kind.json","schema":"./` + k + `/kind.schema.json"}`
	}
	regKinds += "}"

	regPath := filepath.Join(dir, "registry.json")
	s.Require().NoError(os.WriteFile(regPath, []byte(`{"kinds":`+regKinds+`}`), 0644))
	return regPath
}

func (s *RegistrySuite) TestLoadKindResolvesDefaultAlias() {
	regPath := s.writeRegistry("default", "service")

	r, err := Load([]Reference{{Path: regPath}})
	s.Require().NoError(err)

	loaded, err := r.LoadKind("service")
	s.Require().NoError(err)
	s.Equal("service", loaded.GetName())
}

func (s *RegistrySuite) TestLoadKindResolvesAliasedReference() {
	defaultReg := s.writeRegistry("default", "service")
	acmeReg := s.writeRegistry("acme", "service")

	r, err := Load([]Reference{
		{Alias: "", Path: defaultReg},
		{Alias: "acme", Path: acmeReg},
	})
	s.Require().NoError(err)

	defaultLoaded, err := r.LoadKind("service")
	s.Require().NoError(err)
	s.Contains(defaultLoaded.SchemaPath, "default/service/kind.schema.json")

	acmeLoaded, err := r.LoadKind("acme/service")
	s.Require().NoError(err)
	s.Contains(acmeLoaded.SchemaPath, "acme/service/kind.schema.json")
}

func (s *RegistrySuite) TestLoadKindAcceptsAtPrefixedAliases() {
	regPath := s.writeRegistry("scoped", "service")

	r, err := Load([]Reference{{Alias: "@acme", Path: regPath}})
	s.Require().NoError(err)

	loaded, err := r.LoadKind("@acme/service")
	s.Require().NoError(err)
	s.Contains(loaded.SchemaPath, "scoped/service/kind.schema.json")
}

func (s *RegistrySuite) TestLoadKindErrorsOnUnknownAlias() {
	regPath := s.writeRegistry("default", "service")

	r, err := Load([]Reference{{Path: regPath}})
	s.Require().NoError(err)

	_, err = r.LoadKind("missing/service")
	s.Require().Error(err)
	s.Contains(err.Error(), `registry alias "missing" is not configured`)
}

func (s *RegistrySuite) TestLoadKindErrorsOnUnknownKindWithinAlias() {
	regPath := s.writeRegistry("acme", "service")

	r, err := Load([]Reference{{Alias: "acme", Path: regPath}})
	s.Require().NoError(err)

	_, err = r.LoadKind("acme/cron")
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "acme/cron" not found`)
}

func (s *RegistrySuite) TestLoadRejectsDuplicateKindWithinSameAlias() {
	regA := s.writeRegistry("a", "service")
	regB := s.writeRegistry("b", "service")

	_, err := Load([]Reference{
		{Alias: "", Path: regA},
		{Alias: "", Path: regB},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "service" provided by multiple registries`)
}

func (s *RegistrySuite) TestLoadAllowsSameKindAcrossDifferentAliases() {
	regA := s.writeRegistry("a", "service")
	regB := s.writeRegistry("b", "service")

	r, err := Load([]Reference{
		{Alias: "", Path: regA},
		{Alias: "acme", Path: regB},
	})
	s.Require().NoError(err)

	defaultLoaded, err := r.LoadKind("service")
	s.Require().NoError(err)
	s.Contains(defaultLoaded.SchemaPath, "/a/service/kind.schema.json")

	acmeLoaded, err := r.LoadKind("acme/service")
	s.Require().NoError(err)
	s.Contains(acmeLoaded.SchemaPath, "/b/service/kind.schema.json")
}

func (s *RegistrySuite) TestParseRefAcceptsValidShapes() {
	cases := []struct {
		ref         string
		alias, name string
	}{
		{"service", "", "service"},
		{"acme/service", "acme", "service"},
		{"acme/service-with-dashes", "acme", "service-with-dashes"},
		{"@scope/service", "@scope", "service"},
		{"my-org_42/service", "my-org_42", "service"},
	}
	for _, tc := range cases {
		alias, name, err := ParseRef(tc.ref)
		s.Require().NoError(err, tc.ref)
		s.Equal(tc.alias, alias, tc.ref)
		s.Equal(tc.name, name, tc.ref)
	}
}

func (s *RegistrySuite) TestParseRefRejectsMalformed() {
	cases := []string{
		"/service",
		"acme/",
		"/",
	}
	for _, tc := range cases {
		_, _, err := ParseRef(tc)
		s.Require().Error(err, tc)
	}
}
