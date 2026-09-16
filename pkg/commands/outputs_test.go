package commands

import (
	"os"
	"path/filepath"

	"github.com/vercel/veil/pkg/hook"
	"github.com/vercel/veil/pkg/output"
)

func (s *RenderSuite) TestOutputsRemoveNeedsNoProjectAndRequiresExactIdentity() {
	root, err := filepath.EvalSymlinks(s.root)
	s.Require().NoError(err)
	out := filepath.Join(root, "out")
	s.Require().NoError(output.Publish(out, []output.Root{{
		Kind: "@platform/service", Name: "deleted", Bundle: hook.Bundle{"file": {Path: "file", Content: "generated"}},
	}}, output.Options{}))
	_, err = s.run("outputs", "remove", "--out", out, "--kind", "service", "--name", "deleted")
	s.Require().Error(err)
	s.FileExists(filepath.Join(out, "deleted/file"))
	_, err = s.run("outputs", "remove", "--out", out, "--kind", "@platform/service", "--name", "deleted")
	s.Require().NoError(err)
	s.NoFileExists(filepath.Join(out, "deleted/file"))
}

func (s *RenderSuite) TestAdoptCannotEnableOwnershipImplicitly() {
	_, err := s.run("render", "missing.yaml", "--adopt")
	s.Require().Error(err)
	s.Contains(err.Error(), "--adopt requires --managed")
}

func (s *RenderSuite) TestManagedFailedRootDoesNotPublishSuccessfulSibling() {
	s.writeConfig(`{"registries":{"":"./registry.json"},"resource_discovery":{"paths":["*.resource.json"]}}`)
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "registry.json"), []byte(`{"kinds":{"worker":{"name":"worker","path":"kind.json","schema":"kind.schema.json"}}}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "kind.json"), []byte(`{"name":"worker","sources":[{"path":"file","contents":"generated"}]}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "kind.schema.json"), []byte(`{"type":"object"}`), 0644))
	s.Require().NoError(os.WriteFile(filepath.Join(s.root, "good.resource.json"), []byte(`{"metadata":{"kind":"worker","name":"good"},"spec":{}}`), 0644))
	root, err := filepath.EvalSymlinks(s.root)
	s.Require().NoError(err)
	out := filepath.Join(root, "out")
	_, err = s.run("render", "good.resource.json", "missing.resource.json", "--managed", "--out", out)
	s.Require().Error(err)
	s.NoDirExists(out)
}
