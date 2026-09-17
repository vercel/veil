package codec

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type HCLSuite struct {
	suite.Suite
}

func TestHCLSuite(t *testing.T) {
	suite.Run(t, new(HCLSuite))
}

const terraformSample = `resource "aws_iam_policy" "db" {
  name   = "orders-db-access"
  count  = 2
  tags   = { env = var.environment, team = "infra" }
  policy = jsonencode({ Version = "2012-10-17" })
}

variable "region" {
  type    = string
  default = "us-east-1"
}

locals {
  prefix = "acme"
}
`

// TestParseMatchesTerraformJSONShape pins the object a hook actually
// sees: blocks nested by type and label, bodies in arrays, expressions
// left unevaluated as interpolation strings.
func (s *HCLSuite) TestParseMatchesTerraformJSONShape() {
	out, err := HCLToJSON([]byte(terraformSample), "main.tf")
	s.Require().NoError(err)
	j := string(out)

	s.Contains(j, `"resource":{"aws_iam_policy":{"db":[{`, "two labels, then a body in a list")
	s.Contains(j, `"env":"${var.environment}"`, "a reference is not evaluated")
	s.Contains(j, `"team":"infra"`, "a literal stays a literal")
	s.Contains(j, `"locals":[{"prefix":"acme"}]`, "a zero-label block still nests as a body list")
}

// TestRoundTripIsStable is the property that matters for a hook that
// reads, edits and writes back: the second pass has to agree with the
// first, or an untouched file would drift on every render.
func (s *HCLSuite) TestRoundTripIsStable() {
	first, err := HCLToJSON([]byte(terraformSample), "main.tf")
	s.Require().NoError(err)

	rendered, err := JSONToHCL(first)
	s.Require().NoError(err)

	second, err := HCLToJSON(rendered, "main.tf")
	s.Require().NoError(err)
	s.JSONEq(string(first), string(second))

	// And a third pass changes nothing further.
	again, err := JSONToHCL(second)
	s.Require().NoError(err)
	s.Equal(string(rendered), string(again))
}

// TestExpressionsAreWrittenUnquoted is the half a naive encoder gets
// wrong: an interpolation nested inside a map would otherwise come out
// escaped as "$${...}", a literal rather than the reference it was.
func (s *HCLSuite) TestExpressionsAreWrittenUnquoted() {
	out, err := HCLToJSON([]byte(terraformSample), "main.tf")
	s.Require().NoError(err)
	rendered, err := JSONToHCL(out)
	s.Require().NoError(err)
	hcl := string(rendered)

	s.Contains(hcl, "env  = var.environment", "nested in a map")
	s.Contains(hcl, "type    = string", "as a bare attribute")
	s.Contains(hcl, "policy = jsonencode(", "a function call")
	s.NotContains(hcl, "$${", "nothing should come out escaped")
}

// TestLiteralsThatLookLikeExpressionsStayQuoted guards the unquoting:
// a string is only unwrapped when what is inside really parses as an
// expression, so a value that merely resembles one cannot produce a
// broken file.
func (s *HCLSuite) TestLiteralsThatLookLikeExpressionsStayQuoted() {
	rendered, err := JSONToHCL([]byte(`{"locals":[{"weird":"${not a valid expr!!}","plain":"hello"}]}`))
	s.Require().NoError(err)
	hcl := string(rendered)
	s.Contains(hcl, `plain = "hello"`)
	s.NotContains(hcl, "weird = not a valid expr", "it must not be unwrapped into a broken expression")
	// It stays a literal, escaped so HCL does not read it as one, and the
	// result parses. Note the escape is then visible on the way back —
	// the parser reports the template as written rather than the string
	// it denotes — so a literal of this exact shape is the one thing the
	// round trip does not return unchanged. Real expressions, which is
	// what "${...}" almost always is, are unaffected.
	s.Contains(hcl, `weird = "$${not a valid expr!!}"`)
	back, err := HCLToJSON(rendered, "out.tf")
	s.Require().NoError(err)
	s.Contains(string(back), `"weird":"$${not a valid expr!!}"`)
}

func (s *HCLSuite) TestRejectsMalformedInput() {
	_, err := HCLToJSON([]byte(`resource "x" {`), "main.tf")
	s.Require().Error(err)
	s.Contains(err.Error(), "parsing HCL")

	_, err = JSONToHCL([]byte(`["not an object"]`))
	s.Require().Error(err)
	s.Contains(err.Error(), "top level must be an object")
}
