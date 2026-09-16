package render

import (
	"os"
	"path/filepath"

	"github.com/vercel/veil/pkg/output"
)

func (s *RenderSuite) TestManagedDynamicFilesRenameRemoveAndFailedCompute() {
	root, err := filepath.EvalSymlinks(s.root)
	s.Require().NoError(err)
	out := filepath.Join(root, "out")
	kindDir := filepath.Join(s.root, "r", "worker")
	s.writeJSON(filepath.Join(kindDir, "kind.json"), map[string]any{
		"name":    "worker",
		"sources": compiledSources(nil, nil),
		"hooks": map[string]any{"render": []map[string]any{{
			"name":    "dynamic.ts",
			"content": `var __veilMod=(()=>{var h={render(ctx,fs){if(ctx.vars.fail){throw new Error("deliberate failure");}if(ctx.vars.destination){fs.add("dynamic","generated");fs.get("dynamic").setOutputPath(ctx.vars.destination);}return fs;}};return{default:h};})();`,
		}}},
	})
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 1},
	})
	fsys, catalog := s.catalogFor(dir)
	opts := &Options{Kind: "worker", Name: "my-worker", OutDir: out, FS: fsys, Catalog: catalog}
	publish := func(destination string) {
		opts.Variables = map[string]any{"destination": destination}
		computed, err := Compute(opts)
		s.Require().NoError(err)
		s.Require().NoError(output.Publish(out, []output.Root{{Kind: computed.Kind, Name: computed.Name, Bundle: computed.Bundle}}, output.Options{}))
	}
	// Computation alone must not publish even dynamically added files.
	opts.Variables = map[string]any{"destination": "old.yaml"}
	_, err = Compute(opts)
	s.Require().NoError(err)
	s.NoDirExists(out)
	publish("old.yaml")
	data, err := os.ReadFile(filepath.Join(out, "my-worker/old.yaml"))
	s.Require().NoError(err)
	s.Equal("generated", string(data))
	publish("old.yaml")
	publish("new.yaml")
	s.NoFileExists(filepath.Join(out, "my-worker/old.yaml"))
	s.FileExists(filepath.Join(out, "my-worker/new.yaml"))
	manifest, err := os.ReadFile(filepath.Join(out, ".veil/manifest.json"))
	s.Require().NoError(err)
	opts.Variables = map[string]any{"fail": true}
	_, err = Compute(opts)
	s.Require().Error(err)
	s.FileExists(filepath.Join(out, "my-worker/new.yaml"))
	after, err := os.ReadFile(filepath.Join(out, ".veil/manifest.json"))
	s.Require().NoError(err)
	s.Equal(manifest, after)
	publish("")
	s.NoFileExists(filepath.Join(out, "my-worker/new.yaml"))
	s.Require().NoError(output.Remove(out, "worker", "my-worker"))
}
