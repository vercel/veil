package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

	"github.com/vercel/veil/pkg/embeds"
)

type InitSuite struct {
	suite.Suite
	root string
}

func TestInitSuite(t *testing.T) {
	suite.Run(t, new(InitSuite))
}

func (s *InitSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.T().Chdir(s.root)
}

func (s *InitSuite) run(args ...string) (string, error) {
	var buf bytes.Buffer
	app := NewApp()
	app.Writer = &buf
	app.ErrWriter = &buf
	err := app.Run(context.Background(), append([]string{"veil"}, args...))
	return buf.String(), err
}

func (s *InitSuite) TestInitWritesBareVeilJSON() {
	out, err := s.run("init")
	s.Require().NoError(err, out)

	veilJSON := filepath.Join(s.root, "veil.json")
	s.FileExists(veilJSON)

	data, err := os.ReadFile(veilJSON)
	s.Require().NoError(err)

	var got map[string]any
	s.Require().NoError(json.Unmarshal(data, &got))
	s.Equal(embeds.VeilConfigDefinitionSchemaURL, got["$schema"])
	s.Equal([]any{}, got["kinds"])
	s.Equal(map[string]any{"": "./public/r/registry.json"}, got["registries"])
}

func (s *InitSuite) TestInitFailsIfVeilJSONExists() {
	veilJSON := filepath.Join(s.root, "veil.json")
	s.Require().NoError(os.WriteFile(veilJSON, []byte("{}"), 0644))

	_, err := s.run("init")
	s.Require().Error(err)
	s.Contains(err.Error(), "already exists")
}

func (s *InitSuite) TestInitInSubdirOfExistingProjectStillCreates() {
	// Ancestor has veil.json; running `veil init` in a subdir should
	// still create a fresh veil.json in cwd. The auto-init path used by
	// `veil new kind` skips when an ancestor exists, but `veil init` is
	// an explicit user request and should always honor cwd.
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "veil.json"), []byte("{}"), 0644))
	sub := filepath.Join(s.root, "nested")
	s.Require().NoError(os.MkdirAll(sub, 0755))
	s.T().Chdir(sub)

	_, err := s.run("init")
	s.Require().NoError(err)
	s.FileExists(filepath.Join(sub, "veil.json"))
}
