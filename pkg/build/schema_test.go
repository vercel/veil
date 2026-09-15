package build

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type SourceSchemaSuite struct {
	suite.Suite
}

func TestSourceSchemaSuite(t *testing.T) {
	suite.Run(t, new(SourceSchemaSuite))
}

const replicasSchema = `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"],"additionalProperties":false}`

func (s *SourceSchemaSuite) TestAcceptsConformingContents() {
	s.NoError(ValidateSourceContents("sources/app.json", []byte(`{"replicas":3}`), []byte(replicasSchema)))
	s.NoError(ValidateSourceContents("sources/app.yaml", []byte("replicas: 3\n"), []byte(replicasSchema)))
}

// TestRejectsViolations covers each way a source can fail its schema,
// and checks the message names the source rather than leaking the
// in-memory schema URI.
func (s *SourceSchemaSuite) TestRejectsViolations() {
	for name, contents := range map[string]string{
		"wrong type":     `{"replicas":"three"}`,
		"missing field":  `{}`,
		"extra field":    `{"replicas":3,"nope":1}`,
		"not an object":  `[]`,
		"malformed JSON": `{"replicas":`,
	} {
		s.Run(name, func() {
			err := ValidateSourceContents("sources/app.json", []byte(contents), []byte(replicasSchema))
			s.Require().Error(err)
			s.NotContains(err.Error(), "mem://")
		})
	}
}

func (s *SourceSchemaSuite) TestRejectsUnusableSchema() {
	err := ValidateSourceContents("sources/app.json", []byte(`{}`), []byte(`{"type":`))
	s.Require().Error(err)
	s.Contains(err.Error(), "invalid JSON")
}

// TestYAMLDecidedByExtension pins the rule build shares with render's
// typed File accessors: the codec comes from the extension, and the
// check is case-insensitive.
func (s *SourceSchemaSuite) TestYAMLDecidedByExtension() {
	doc, err := ParseSourceContents("sources/app.YAML", []byte("replicas: 3\n"))
	s.Require().NoError(err)
	s.Equal(map[string]any{"replicas": 3}, doc)

	// The same bytes are not JSON, so a .json extension must fail.
	_, err = ParseSourceContents("sources/app.json", []byte("replicas: 3\n"))
	s.Require().Error(err)
}
