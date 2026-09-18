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

// TestBlockLabelsMatchTerraform pins the label counts against the ones
// Terraform's own configFileSchema declares. Getting one wrong is silent
// and ugly: too few labels and a block header is emitted as an
// attribute, too many and a body is read as a label.
func (s *HCLSuite) TestBlockLabelsMatchTerraform() {
	// From terraform/internal/configs/parser_config.go.
	want := map[string]int{
		"terraform": 0, "locals": 0, "moved": 0, "removed": 0, "import": 0,
		"provider": 1, "variable": 1, "output": 1, "module": 1, "check": 1,
		"resource": 2, "data": 2, "ephemeral": 2, "action": 2,
	}
	s.Equal(want, terraformBlockLabels)

	// provisioner is the tempting mistake: it is a block, but nested
	// inside a resource rather than top-level.
	s.NotContains(terraformBlockLabels, "provisioner")
}

// TestEveryBlockTypeRoundTrips walks the whole table, so a block type
// added to it is exercised rather than merely declared.
func (s *HCLSuite) TestEveryBlockTypeRoundTrips() {
	for blockType, labels := range terraformBlockLabels {
		s.Run(blockType, func() {
			header := blockType
			for i := 0; i < labels; i++ {
				header += ` "l` + string(rune('0'+i)) + `"`
			}
			src := header + " {\n  field = \"v\"\n}\n"

			j, err := HCLToJSON([]byte(src), "main.tf")
			s.Require().NoError(err)
			out, err := JSONToHCL(j)
			s.Require().NoError(err)
			again, err := HCLToJSON(out, "main.tf")
			s.Require().NoError(err)
			s.JSONEq(string(j), string(again), "%s should survive a round trip", blockType)
		})
	}
}
