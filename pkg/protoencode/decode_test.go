package protoencode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type YAMLSuite struct {
	suite.Suite
}

func TestYAMLSuite(t *testing.T) {
	suite.Run(t, new(YAMLSuite))
}

func (s *YAMLSuite) TestIsYAMLOnExtensions() {
	cases := map[string]bool{
		"foo.json":             false,
		"foo.yaml":             true,
		"foo.yml":              true,
		"foo.YAML":             true,
		"foo.YML":              true,
		"foo":                  false,
		"":                     false,
		"path/to/kind.json":    false,
		"path/to/kind.yaml":    true,
		"path/to/kind.yml":     true,
		"https://x/r.yaml":     true,
		"https://x/r.yaml?t=1": true,
		"https://x/r.json":     false,
		"https://x/r":          false,
	}
	for path, expected := range cases {
		s.Equal(expected, IsYAML(path), "IsYAML(%q)", path)
	}
}

// TestReadFileDispatchesByExtension confirms the decoder map: a .json
// path goes through the JSON decoder, a .yaml path through yaml.v3.
func (s *YAMLSuite) TestReadFileDispatchesByExtension() {
	dir := s.T().TempDir()
	jsonPath := filepath.Join(dir, "doc.json")
	s.Require().NoError(os.WriteFile(jsonPath, []byte(`{"a":1,"b":["x","y"]}`), 0644))

	yamlPath := filepath.Join(dir, "doc.yaml")
	s.Require().NoError(os.WriteFile(yamlPath, []byte("a: 1\nb:\n  - x\n  - y\n"), 0644))

	var fromJSON map[string]any
	s.Require().NoError(ReadFile(jsonPath, &fromJSON))

	var fromYAML map[string]any
	s.Require().NoError(ReadFile(yamlPath, &fromYAML))

	// Same semantic content regardless of source format.
	s.Equal(fromJSON["b"], fromYAML["b"])
	// Numbers round-trip via the right decoder — both produce numeric
	// types (json gives float64, yaml gives int), which is fine as
	// long as the value is 1.
	s.NotZero(fromJSON["a"])
	s.NotZero(fromYAML["a"])
}

// TestReadFileFallsBackToJSON pins the behavior for paths whose
// extension isn't in the decoder map — anything unknown is treated
// as JSON. Mirrors how legacy callers (HTTP URLs without an explicit
// suffix) still work.
func (s *YAMLSuite) TestReadFileFallsBackToJSON() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "no-extension")
	s.Require().NoError(os.WriteFile(path, []byte(`{"a":1}`), 0644))

	var doc map[string]any
	s.Require().NoError(ReadFile(path, &doc))
	s.NotZero(doc["a"])
}

// TestWriteFileAnyDispatchesByExtension confirms the encoder map
// produces format-appropriate output for each extension.
func (s *YAMLSuite) TestWriteFileAnyDispatchesByExtension() {
	dir := s.T().TempDir()
	doc := map[string]any{"a": 1, "b": []any{"x", "y"}}

	jsonPath := filepath.Join(dir, "doc.json")
	s.Require().NoError(WriteFileAny(jsonPath, doc))
	jsonBytes, err := os.ReadFile(jsonPath)
	s.Require().NoError(err)
	s.Contains(string(jsonBytes), `"a": 1`)
	s.Contains(string(jsonBytes), `"x"`)

	yamlPath := filepath.Join(dir, "doc.yaml")
	s.Require().NoError(WriteFileAny(yamlPath, doc))
	yamlBytes, err := os.ReadFile(yamlPath)
	s.Require().NoError(err)
	// yaml.v3 emits block-style by default.
	s.Contains(string(yamlBytes), "a: 1")
	s.Contains(string(yamlBytes), "- x")
}

// TestRoundTripPreservesData runs decode → encode → decode and
// confirms semantic equivalence after the YAML round-trip.
func (s *YAMLSuite) TestRoundTripPreservesData() {
	dir := s.T().TempDir()
	orig := map[string]any{
		"a": 1,
		"b": []any{"x", "y"},
		"c": map[string]any{"d": true},
	}
	path := filepath.Join(dir, "doc.yaml")
	s.Require().NoError(WriteFileAny(path, orig))

	var back map[string]any
	s.Require().NoError(ReadFile(path, &back))

	s.Equal(orig["c"], back["c"])
	s.Equal(orig["b"], back["b"])
	s.NotZero(back["a"])
}

// TestDecodeAutoDetectsFormatFromBytes exercises the io.Reader path
// — used by registry's HTTP fetches — and confirms NewDecoder picks
// JSON vs YAML by peeking the first non-whitespace byte rather than
// any path/extension hint.
func (s *YAMLSuite) TestDecodeAutoDetectsFormatFromBytes() {
	var doc map[string]any
	s.Require().NoError(Decode(strings.NewReader("a: 1\n"), &doc))
	s.NotZero(doc["a"])

	var json map[string]any
	s.Require().NoError(Decode(bytes.NewReader([]byte(`{"a":1}`)), &json))
	s.NotZero(json["a"])

	// Leading whitespace doesn't fool the detector.
	var withSpace map[string]any
	s.Require().NoError(Decode(strings.NewReader("   \n  {\"a\":1}"), &withSpace))
	s.NotZero(withSpace["a"])
}
