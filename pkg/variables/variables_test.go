package variables

import (
	"testing"

	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
)

type ResolveSuite struct {
	suite.Suite
}

func TestResolveSuite(t *testing.T) {
	suite.Run(t, new(ResolveSuite))
}

// decl builds a Variable declaration with the given type and (optional)
// default. nil def means "required, no default".
func (s *ResolveSuite) decl(t veilv1.VariableType_Enum, def any) *veilv1.Variable {
	v := &veilv1.Variable{Type: t}
	if def != nil {
		val, err := structpb.NewValue(def)
		s.Require().NoError(err)
		v.Default = val
	}
	return v
}

// enumVar is shorthand for a Variable with an enum but no default.
func (s *ResolveSuite) enumVar(t veilv1.VariableType_Enum, allowed ...any) *veilv1.Variable {
	v := &veilv1.Variable{Type: t}
	v.Enum = make([]*structpb.Value, 0, len(allowed))
	for _, a := range allowed {
		val, err := structpb.NewValue(a)
		s.Require().NoError(err)
		v.Enum = append(v.Enum, val)
	}
	return v
}

func (s *ResolveSuite) TestCLIFlagBeatsEnvAndDefault() {
	decls := map[string]*veilv1.Variable{
		"env": s.decl(veilv1.VariableType_string, "dev"),
	}
	env := map[string]string{"VEIL_VAR_ENV": "staging"}
	out, err := Resolve(decls, []string{"env=prod"}, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("prod", out["env"])
}

func (s *ResolveSuite) TestEnvBeatsDefault() {
	decls := map[string]*veilv1.Variable{
		"env": s.decl(veilv1.VariableType_string, "dev"),
	}
	env := map[string]string{"VEIL_VAR_ENV": "staging"}
	out, err := Resolve(decls, nil, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("staging", out["env"])
}

func (s *ResolveSuite) TestDefaultWhenNotProvided() {
	decls := map[string]*veilv1.Variable{
		"env":      s.decl(veilv1.VariableType_string, "dev"),
		"replicas": s.decl(veilv1.VariableType_number, 3),
		"debug":    s.decl(veilv1.VariableType_bool, false),
	}
	out, err := Resolve(decls, nil, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal("dev", out["env"])
	s.Equal(float64(3), out["replicas"])
	s.Equal(false, out["debug"])
}

func (s *ResolveSuite) TestRequiredMissingErrors() {
	decls := map[string]*veilv1.Variable{
		"region": s.decl(veilv1.VariableType_string, nil),
	}
	_, err := Resolve(decls, nil, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), `required variable "region"`)
	s.Contains(err.Error(), "VEIL_VAR_REGION")
}

func (s *ResolveSuite) TestCoerceNumberAndBool() {
	decls := map[string]*veilv1.Variable{
		"replicas": s.decl(veilv1.VariableType_number, nil),
		"debug":    s.decl(veilv1.VariableType_bool, nil),
	}
	out, err := Resolve(decls, []string{"replicas=5", "debug=true"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal(float64(5), out["replicas"])
	s.Equal(true, out["debug"])
}

func (s *ResolveSuite) TestCoerceFailsForBadNumber() {
	decls := map[string]*veilv1.Variable{
		"replicas": s.decl(veilv1.VariableType_number, nil),
	}
	_, err := Resolve(decls, []string{"replicas=lots"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "expected number")
}

func (s *ResolveSuite) TestUnknownCLIVarRejected() {
	decls := map[string]*veilv1.Variable{
		"env": s.decl(veilv1.VariableType_string, "dev"),
	}
	_, err := Resolve(decls, []string{"regiion=iad1"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not declared")
}

func (s *ResolveSuite) TestUnknownEnvVarIgnored() {
	decls := map[string]*veilv1.Variable{
		"env": s.decl(veilv1.VariableType_string, "dev"),
	}
	env := map[string]string{"VEIL_VAR_UNRELATED": "x"}
	out, err := Resolve(decls, nil, mapEnv(env))
	s.Require().NoError(err)
	s.Equal("dev", out["env"])
}

func (s *ResolveSuite) TestInvalidCLIPair() {
	decls := map[string]*veilv1.Variable{
		"env": s.decl(veilv1.VariableType_string, "dev"),
	}
	_, err := Resolve(decls, []string{"novalue"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "expected name=value")
}

func (s *ResolveSuite) TestEnumAcceptsAllowedString() {
	decls := map[string]*veilv1.Variable{
		"env": s.enumVar(veilv1.VariableType_string, "dev", "staging", "prod"),
	}
	out, err := Resolve(decls, []string{"env=staging"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal("staging", out["env"])
}

func (s *ResolveSuite) TestEnumRejectsUnlistedString() {
	decls := map[string]*veilv1.Variable{
		"env": s.enumVar(veilv1.VariableType_string, "dev", "prod"),
	}
	_, err := Resolve(decls, []string{"env=staging"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not in declared enum")
}

func (s *ResolveSuite) TestEnumAppliesToEnvSource() {
	decls := map[string]*veilv1.Variable{
		"env": s.enumVar(veilv1.VariableType_string, "dev", "prod"),
	}
	env := map[string]string{"VEIL_VAR_ENV": "qa"}
	_, err := Resolve(decls, nil, mapEnv(env))
	s.Require().Error(err)
	s.Contains(err.Error(), "VEIL_VAR_ENV")
}

func (s *ResolveSuite) TestEnumAcceptsNumber() {
	decls := map[string]*veilv1.Variable{
		"replicas": s.enumVar(veilv1.VariableType_number, float64(1), float64(3), float64(5)),
	}
	out, err := Resolve(decls, []string{"replicas=3"}, mapEnv(nil))
	s.Require().NoError(err)
	s.Equal(float64(3), out["replicas"])

	_, err = Resolve(decls, []string{"replicas=2"}, mapEnv(nil))
	s.Require().Error(err)
	s.Contains(err.Error(), "not in declared enum")
}

func (s *ResolveSuite) TestValueWithEqualsSign() {
	decls := map[string]*veilv1.Variable{
		"conn": s.decl(veilv1.VariableType_string, nil),
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
