package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"

	"github.com/vercel/veil/pkg/registry"
)

type RenderSuite struct {
	suite.Suite
	root     string
	registry registry.Registry
}

func TestRenderSuite(t *testing.T) {
	suite.Run(t, new(RenderSuite))
}

// helloHookIIFE is a pre-bundled IIFE hook that prepends the resource
// name to every source's content and adds a greeting.txt. Tests that use
// it don't need esbuild.
const helloHookIIFE = `var __veilMod=(()=>{var h={render(ctx,fs){var n=ctx.resource.metadata.name;var ks=fs.keys();for(var i=0;i<ks.length;i++){var f=fs.get(ks[i]);f.setContent(n+":"+f.getContent());}fs.add("greeting.txt","hello, "+n);return fs;}};return{default:h};})();`

func (s *RenderSuite) SetupTest() {
	s.root = s.T().TempDir()

	// Lay down a minimal compiled registry with one kind "worker".
	kindDir := filepath.Join(s.root, "r", "worker")
	s.Require().NoError(os.MkdirAll(kindDir, 0755))

	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"config.txt": "base",
		},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/hello-world.ts", "content": helloHookIIFE},
			},
		},
	}
	s.writeJSON(filepath.Join(kindDir, "kind.json"), compiled)

	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"metadata", "spec"},
		"properties": map[string]any{
			"metadata": map[string]any{"type": "object"},
			"spec": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"replicas": map[string]any{"type": "integer", "minimum": 1},
				},
			},
		},
	}
	s.writeJSON(filepath.Join(kindDir, "kind.schema.json"), schema)

	regJSON := filepath.Join(s.root, "r", "registry.json")
	s.writeJSON(regJSON, map[string]any{
		"kinds": map[string]any{
			"worker": map[string]any{
				"name":   "worker",
				"path":   "./worker/kind.json",
				"schema": "./worker/kind.schema.json",
			},
		},
	})
	reg, err := registry.FromIndex([]string{regJSON})
	s.Require().NoError(err)
	s.registry = reg
}

func (s *RenderSuite) writeJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(path, data, 0644))
}

func (s *RenderSuite) TestHappyPathRendersBundle() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 3},
	})

	out := filepath.Join(s.root, "out")
	result, err := Render(Options{
		Dir:       dir,
		OutDir:    out,
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().NoError(err)
	s.Require().Len(result.Rendered, 1)

	greeting, err := os.ReadFile(filepath.Join(out, "my-worker", "greeting.txt"))
	s.Require().NoError(err)
	s.Equal("hello, my-worker", string(greeting))

	cfg, err := os.ReadFile(filepath.Join(out, "my-worker", "config.txt"))
	s.Require().NoError(err)
	s.Equal("my-worker:base", string(cfg))
}

func (s *RenderSuite) TestOverlayMergesMatchingSpec() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))

	// Overlay file gets merged when var.env == 'staging'.
	s.writeJSON(filepath.Join(dir, "staging.json"), map[string]any{
		"spec": map[string]any{"replicas": 1},
	})
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"overlays": []map[string]any{
				{"match": "vars.env == 'staging'", "file": "./staging.json"},
			},
		},
		"spec": map[string]any{"replicas": 3},
	})

	out := filepath.Join(s.root, "out")
	_, err := Render(Options{
		Dir:       dir,
		OutDir:    out,
		Registry:  s.registry,
		Variables: map[string]any{"env": "staging"},
	})
	s.Require().NoError(err)
	// Bundle is produced; the fact that it succeeded and schema.spec.replicas
	// validates `{type:integer,minimum:1}` both pre- and post-overlay is the
	// signal. Also ensure non-matching overlay is skipped (see next test).
	s.FileExists(filepath.Join(out, "my-worker", "greeting.txt"))
}

func (s *RenderSuite) TestOverlaySkippedWhenMatchFalse() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))

	// Overlay would violate the schema (replicas: 0 breaks minimum:1), but
	// match is false so it shouldn't be applied.
	s.writeJSON(filepath.Join(dir, "staging.json"), map[string]any{
		"spec": map[string]any{"replicas": 0},
	})
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"overlays": []map[string]any{
				{"match": "vars.env == 'staging'", "file": "./staging.json"},
			},
		},
		"spec": map[string]any{"replicas": 3},
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{"env": "production"}, // not staging
	})
	s.Require().NoError(err)
}

func (s *RenderSuite) TestSchemaValidationFailure() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 0}, // violates minimum:1
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "schema validation")
	// Error message should not leak the in-memory schema URL.
	s.NotContains(err.Error(), "mem://schema")
}

func (s *RenderSuite) TestSchemaValidationCatchesMissingRequiredField() {
	// Replace SetupTest's schema with one that requires `replicas`.
	kindDir := filepath.Join(s.root, "r", "worker")
	s.writeJSON(filepath.Join(kindDir, "kind.schema.json"), map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"metadata", "spec"},
		"properties": map[string]any{
			"metadata": map[string]any{"type": "object"},
			"spec": map[string]any{
				"type":     "object",
				"required": []string{"replicas"},
				"properties": map[string]any{
					"replicas": map[string]any{"type": "integer"},
				},
			},
		},
	})

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{}, // missing required "replicas"
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "schema validation")
	s.Contains(err.Error(), "replicas")
}

func (s *RenderSuite) TestSchemaValidationCatchesWrongType() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": "lots"}, // type mismatch
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "schema validation")
	s.Contains(err.Error(), "replicas")
}

func (s *RenderSuite) TestSchemaValidationAfterOverlayMerge() {
	// Overlay flips replicas from a valid value to an invalid one — the
	// merged spec is what gets validated, not the on-disk spec, so the
	// error must surface.
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "bad-overlay.json"), map[string]any{
		"spec": map[string]any{"replicas": 0},
	})
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"overlays": []map[string]any{
				{"match": "vars.env == 'staging'", "file": "./bad-overlay.json"},
			},
		},
		"spec": map[string]any{"replicas": 3}, // valid until overlay applies
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{"env": "staging"},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "schema validation")
}

func (s *RenderSuite) TestUnknownKindErrors() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "x.json"), map[string]any{
		"metadata": map[string]any{"kind": "unknown", "name": "x"},
		"spec":     map[string]any{},
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), `kind "unknown" not found`)
}

func (s *RenderSuite) TestSetOutputPathRoutesToNewLocation() {
	// Hook relocates the source via setOutputPath; final writer must land
	// it at the new path, not the original identity.
	routingHook := `var __veilMod=(()=>{var h={render(ctx,fs){var f=fs.get("config.txt");f.setOutputPath("kubernetes/config.txt");return fs;}};return{default:h};})();`

	kindDir := filepath.Join(s.root, "r", "worker")
	s.writeJSON(filepath.Join(kindDir, "kind.json"), map[string]any{
		"name":    "worker",
		"sources": map[string]string{"config.txt": "base"},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/route.ts", "content": routingHook},
			},
		},
	})

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 1},
	})

	out := filepath.Join(s.root, "out")
	_, err := Render(Options{
		Dir:       dir,
		OutDir:    out,
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().NoError(err)

	s.FileExists(filepath.Join(out, "my-worker", "kubernetes", "config.txt"))
	s.NoFileExists(filepath.Join(out, "my-worker", "config.txt"))
}

func (s *RenderSuite) TestDeleteSkipsOutput() {
	// Hook deletes the only source; final output directory has nothing.
	deleteHook := `var __veilMod=(()=>{var h={render(ctx,fs){fs.delete("config.txt");return fs;}};return{default:h};})();`

	kindDir := filepath.Join(s.root, "r", "worker")
	s.writeJSON(filepath.Join(kindDir, "kind.json"), map[string]any{
		"name":    "worker",
		"sources": map[string]string{"config.txt": "base"},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/delete.ts", "content": deleteHook},
			},
		},
	})

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 1},
	})

	out := filepath.Join(s.root, "out")
	_, err := Render(Options{
		Dir:       dir,
		OutDir:    out,
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().NoError(err)

	// config.txt must not land; the directory exists but is empty.
	s.NoFileExists(filepath.Join(out, "my-worker", "config.txt"))
}

func (s *RenderSuite) TestPathCollisionErrors() {
	// Two sources rerouted to the same destination — path collision.
	collideHook := `var __veilMod=(()=>{var h={render(ctx,fs){fs.add("a","A");fs.add("b","B");fs.get("a").setOutputPath("same.txt");fs.get("b").setOutputPath("same.txt");return fs;}};return{default:h};})();`

	kindDir := filepath.Join(s.root, "r", "worker")
	s.writeJSON(filepath.Join(kindDir, "kind.json"), map[string]any{
		"name":    "worker",
		"sources": map[string]string{},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/collide.ts", "content": collideHook},
			},
		},
	})

	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 1},
	})

	_, err := Render(Options{
		Dir:       dir,
		OutDir:    filepath.Join(s.root, "out"),
		Registry:  s.registry,
		Variables: map[string]any{},
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "path collision")
}

func (s *RenderSuite) TestDiscoverySkipsNonInstances() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))

	// overlay-style file (has spec but no metadata.kind/name) — should be skipped
	s.writeJSON(filepath.Join(dir, "overlay.json"), map[string]any{
		"spec": map[string]any{"replicas": 2},
	})
	// valid instance
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 3},
	})

	instances, err := Discover(dir)
	s.Require().NoError(err)
	s.Require().Len(instances, 1)
	s.Equal("my-worker", instances[0].Metadata.Name)
}
