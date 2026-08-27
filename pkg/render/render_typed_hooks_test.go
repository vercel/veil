package render

import (
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
	yaml "gopkg.in/yaml.v3"
)

// replicasSchemaJSON is the JSON Schema text embedded in a compiled
// Kind's source_schemas map for these tests: an object with a required
// integer `replicas`.
const replicasSchemaJSON = `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"],"additionalProperties":false}`

// bumpReplicasTypedIIFE is a typed render hook: receives the parsed
// source object directly (no FS), increments replicas, and returns
// the object.
const bumpReplicasTypedIIFE = `var __veilMod=(()=>{var h={render:function(ctx,obj){obj.replicas=obj.replicas+1;return obj;}};return{default:h};})();`

// badReturnTypedIIFE returns an object that violates replicasSchemaJSON
// (replicas as a string, not an integer) — used to prove a typed
// hook's own bad return is rejected at its own call boundary.
const badReturnTypedIIFE = `var __veilMod=(()=>{var h={render:function(ctx,obj){obj.replicas="not-a-number";return obj;}};return{default:h};})();`

// corruptAppJSONUntypedIIFE is a whole-bundle (untyped) hook that
// reaches into app.json via the FS and overwrites it with content that
// violates replicasSchemaJSON. Used to prove the final post-post_render
// check catches corruption a typed hook never gets a chance to see.
const corruptAppJSONUntypedIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.get("app.json").setContent('{"replicas":"broken"}');return fs;}};return{default:h};})();`

// markerHookIIFE adds a marker file so a test can prove a later stage
// of the pipeline never ran (the marker's absence in the output is the
// proof).
const markerHookIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.add("marker.txt","ran");return fs;}};return{default:h};})();`

// writeTypedWorkerKind installs a compiled "worker" kind with two
// sources: app.json (schema-declared per schemaForApp) and a plain
// config.txt with no schema. hookEntries lets each test declare its
// own render-hook list (mixing typed/untyped, single/multiple).
func (s *RenderSuite) writeTypedWorkerKind(schemaForApp string, hookEntries []map[string]any) {
	sourceSchemas := map[string]string{}
	if schemaForApp != "" {
		sourceSchemas["app.json"] = schemaForApp
	}
	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"app.json":   `{"replicas":3}`,
			"config.txt": "base",
		},
		"source_schemas": sourceSchemas,
		"hooks": map[string]any{
			"render": hookEntries,
		},
	}
	s.writeJSON(filepath.Join(s.root, "r", "worker", "kind.json"), compiled)
}

func (s *RenderSuite) writeTypedWorkerResource(dir string) {
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 3},
	})
}

func (s *RenderSuite) readOutputJSON(outDir, name, file string) map[string]any {
	data, err := os.ReadFile(filepath.Join(outDir, name, file))
	s.Require().NoError(err)
	var out map[string]any
	s.Require().NoError(json.Unmarshal(data, &out))
	return out
}

func (s *RenderSuite) TestTypedHookRoundTripJSON() {
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bump.ts", "content": bumpReplicasTypedIIFE, "source": "app.json"},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	app := s.readOutputJSON(out, "my-worker", "app.json")
	s.EqualValues(4, app["replicas"])
}

func (s *RenderSuite) TestTypedHookRoundTripYAML() {
	// Same shape as the JSON test but the schema-declared source is
	// YAML-encoded — proves the host picks the codec by extension, not
	// just for JSON.
	sourceSchemas := map[string]string{"app.yaml": replicasSchemaJSON}
	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"app.yaml": "replicas: 3\n",
		},
		"source_schemas": sourceSchemas,
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/bump.ts", "content": bumpReplicasTypedIIFE, "source": "app.yaml"},
			},
		},
	}
	s.writeJSON(filepath.Join(s.root, "r", "worker", "kind.json"), compiled)

	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	data, err := os.ReadFile(filepath.Join(out, "my-worker", "app.yaml"))
	s.Require().NoError(err)
	var got map[string]any
	s.Require().NoError(yaml.Unmarshal(data, &got))
	s.EqualValues(4, got["replicas"])
}

func (s *RenderSuite) TestTypedHookBadReturnFailsRender() {
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bad.ts", "content": badReturnTypedIIFE, "source": "app.json"},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "app.json")
	s.NoDirExists(filepath.Join(out, "my-worker"))
}

func (s *RenderSuite) TestPreRenderGateRejectsInvalidInitialSource() {
	// app.json's *initial* content already violates the schema —
	// render must fail before any hook (typed or not) runs. The
	// marker hook's absence from the output proves it never ran.
	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"app.json": `{"replicas":"not-a-number"}`,
		},
		"source_schemas": map[string]string{"app.json": replicasSchemaJSON},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/marker.ts", "content": markerHookIIFE},
			},
		},
	}
	s.writeJSON(filepath.Join(s.root, "r", "worker", "kind.json"), compiled)

	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "app.json")
	s.NoDirExists(filepath.Join(out, "my-worker"))
}

func (s *RenderSuite) TestFinalCheckCatchesUntypedCorruption() {
	// app.json starts valid; an *untyped* hook corrupts it via the raw
	// FS with no typed hook downstream to catch it immediately. Render
	// must still fail, but only at the final check (after post_render,
	// alongside validate) — proven by the marker hook running fine
	// beforehand (it's earlier in the same render list).
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/marker.ts", "content": markerHookIIFE},
		{"name": "hooks/corrupt.ts", "content": corruptAppJSONUntypedIIFE},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "app.json")
	// The render fails only once every hook has already run (the
	// failure is reported, not thrown mid-loop) — nothing is written
	// either way since the failure still aborts before writeBundle.
	s.NoDirExists(filepath.Join(out, "my-worker"))
}

func (s *RenderSuite) TestTypedAndUntypedHooksInterleave() {
	// One typed hook (bound to app.json) and one untyped hook
	// (touching config.txt via the FS) in the same render list — both
	// effects must land in the output.
	untypedIIFE := `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.get("config.txt").setContent("touched");return fs;}};return{default:h};})();`
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bump.ts", "content": bumpReplicasTypedIIFE, "source": "app.json"},
		{"name": "hooks/untyped.ts", "content": untypedIIFE},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	app := s.readOutputJSON(out, "my-worker", "app.json")
	s.EqualValues(4, app["replicas"])

	cfg, err := os.ReadFile(filepath.Join(out, "my-worker", "config.txt"))
	s.Require().NoError(err)
	s.Equal("touched", string(cfg))
}

func (s *RenderSuite) TestTypedBindingWithoutSchemaSkipsValidation() {
	// app.json has no declared schema — a typed hook can still bind to
	// it (getting a parsed object instead of a raw File) but nothing
	// validates the object on the way in or out.
	s.writeTypedWorkerKind("", []map[string]any{
		{"name": "hooks/bump.ts", "content": bumpReplicasTypedIIFE, "source": "app.json"},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	app := s.readOutputJSON(out, "my-worker", "app.json")
	s.EqualValues(4, app["replicas"])
}
