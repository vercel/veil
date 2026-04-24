package variables

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

	"github.com/vercel/veil/pkg/config"
)

type ResolveSuite struct {
	suite.Suite
}

func TestResolveSuite(t *testing.T) {
	suite.Run(t, new(ResolveSuite))
}

func (s *ResolveSuite) decl(t config.VariableType, def any) config.Variable {
	v := config.Variable{Type: t}
	if def != nil {
		raw, err := json.Marshal(def)
		s.Require().NoError(err)
		v.Default = raw
	}
	return v
}

func (s *ResolveSuite) TestCLIFlagBeatsEnvAndDefault() {
	decls := map[string]config.Variable{
		"env": s.decl(config.VariableTypeString, "dev"),
	}
	env := map[string]string{"VEIL_VAR_ENV": "staging"}
	out, err := Resolve(decls, []string{"env=prod"}, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("prod", out["env"])
}

func (s *ResolveSuite) TestEnvBeatsDefault() {
	decls := map[string]config.Variable{
		"env": s.decl(config.VariableTypeString, "dev"),
	}
	env := map[string]string{"VEIL_VAR_ENV": "staging"}
	out, err := Resolve(decls, nil, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("staging", out["env"])
}

func (s *ResolveSuite) TestDefaultWhenNotProvided() {
	decls := map[string]config.Variable{
		"env":      s.decl(config.VariableTypeString, "dev"),
		"replicas": s.decl(config.VariableTypeNumber, 3),
		"debug":    s.decl(config.VariableTypeBool, false),
	}
	out, err := Resolve(decls, nil, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal("dev", out["env"])
	s.Equal(float64(3), out["replicas"])
	s.Equal(false, out["debug"])
}

func (s *ResolveSuite) TestRequiredMissingErrors() {
	decls := map[string]config.Variable{
		"region": s.decl(config.VariableTypeString, nil),
	}
	_, err := Resolve(decls, nil, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), `required variable "region"`)
	s.Contains(err.Error(), "VEIL_VAR_REGION")
}

func (s *ResolveSuite) TestCoerceNumberAndBool() {
	decls := map[string]config.Variable{
		"replicas": s.decl(config.VariableTypeNumber, nil),
		"debug":    s.decl(config.VariableTypeBool, nil),
	}
	out, err := Resolve(decls, []string{"replicas=5", "debug=true"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal(float64(5), out["replicas"])
	s.Equal(true, out["debug"])
}

func (s *ResolveSuite) TestCoerceFailsForBadNumber() {
	decls := map[string]config.Variable{
		"replicas": s.decl(config.VariableTypeNumber, nil),
	}
	_, err := Resolve(decls, []string{"replicas=lots"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "expected number")
}

func (s *ResolveSuite) TestUnknownCLIVarRejected() {
	decls := map[string]config.Variable{
		"env": s.decl(config.VariableTypeString, "dev"),
	}
	_, err := Resolve(decls, []string{"regiion=iad1"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not declared")
}

func (s *ResolveSuite) TestUnknownEnvVarIgnored() {
	decls := map[string]config.Variable{
		"env": s.decl(config.VariableTypeString, "dev"),
	}
	env := map[string]string{"VEIL_VAR_UNRELATED": "x"}
	out, err := Resolve(decls, nil, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("dev", out["env"])
}

func (s *ResolveSuite) TestInvalidCLIPair() {
	decls := map[string]config.Variable{
		"env": s.decl(config.VariableTypeString, "dev"),
	}
	_, err := Resolve(decls, []string{"novalue"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "expected name=value")
}

func (s *ResolveSuite) TestEnumAcceptsAllowedString() {
	decls := map[string]config.Variable{
		"env": {
			Type: config.VariableTypeString,
			Enum: []json.RawMessage{json.RawMessage(`"dev"`), json.RawMessage(`"staging"`), json.RawMessage(`"prod"`)},
		},
	}
	out, err := Resolve(decls, []string{"env=staging"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal("staging", out["env"])
}

func (s *ResolveSuite) TestEnumRejectsUnlistedString() {
	decls := map[string]config.Variable{
		"env": {
			Type: config.VariableTypeString,
			Enum: []json.RawMessage{json.RawMessage(`"dev"`), json.RawMessage(`"prod"`)},
		},
	}
	_, err := Resolve(decls, []string{"env=staging"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not in declared enum")
}

func (s *ResolveSuite) TestEnumAppliesToEnvSource() {
	decls := map[string]config.Variable{
		"env": {
			Type: config.VariableTypeString,
			Enum: []json.RawMessage{json.RawMessage(`"dev"`), json.RawMessage(`"prod"`)},
		},
	}
	env := map[string]string{"VEIL_VAR_ENV": "qa"}
	_, err := Resolve(decls, nil, mapEnv(env))
	s.Require().Error(err)
	s.Contains(err.Error(), "VEIL_VAR_ENV")
}

func (s *ResolveSuite) TestEnumAcceptsNumber() {
	decls := map[string]config.Variable{
		"replicas": {
			Type: config.VariableTypeNumber,
			Enum: []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`3`), json.RawMessage(`5`)},
		},
	}
	out, err := Resolve(decls, []string{"replicas=3"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal(float64(3), out["replicas"])

	_, err = Resolve(decls, []string{"replicas=2"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not in declared enum")
}

func (s *ResolveSuite) TestValueWithEqualsSign() {
	decls := map[string]config.Variable{
		"conn": s.decl(config.VariableTypeString, nil),
	}
	out, err := Resolve(decls, []string{"conn=host=db;port=5432"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal("host=db;port=5432", out["conn"])
}

func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}
