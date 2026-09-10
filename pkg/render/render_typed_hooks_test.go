package render

import (
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
	yaml "gopkg.in/yaml.v3"
)

// replicasSchemaJSON is embedded in a compiled Kind's source_schemas
// map: an object with a required integer `replicas`.
const replicasSchemaJSON = `{"type":"object","properties":{"replicas":{"type":"integer"}},"required":["replicas"],"additionalProperties":false}`

// bumpReplicasViaAccessorIIFE reaches app.json through its generated
// typed accessor: getContent() hands back the parsed object,
// setContent(obj) validates and writes it back.
const bumpReplicasViaAccessorIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){var f=fs.getAppJson();var o=f.getContent();o.replicas=o.replicas+1;f.setContent(o);return fs;}};return{default:h};})();`

// badWriteViaAccessorIIFE writes a value violating replicasSchemaJSON
// through the typed accessor — the write should be rejected synchronously.
const badWriteViaAccessorIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){var f=fs.getAppJson();var o=f.getContent();o.replicas="not-a-number";f.setContent(o);return fs;}};return{default:h};})();`

// corruptViaEscapeHatchIIFE reaches app.json through the fs.get()
// escape hatch (not the typed accessor) and writes content that
// violates replicasSchemaJSON — proves the escape hatch is validated
// too, synchronously.
const corruptViaEscapeHatchIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.get("app.json").setContent('{"replicas":"broken"}');return fs;}};return{default:h};})();`

// markerHookIIFE adds a marker file so a test can prove a later stage
// never ran (its absence in the output is the proof).
const markerHookIIFE = `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.add("marker.txt","ran");return fs;}};return{default:h};})();`

// writeTypedWorkerKind installs a "worker" kind with app.json
// (schema-declared per schemaForApp) and a plain config.txt.
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

func (s *RenderSuite) TestSchemaTypedAccessorRoundTripJSON() {
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bump.ts", "content": bumpReplicasViaAccessorIIFE},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	app := s.readOutputJSON(out, "my-worker", "app.json")
	s.EqualValues(4, app["replicas"])
}

func (s *RenderSuite) TestSchemaTypedAccessorRoundTripYAML() {
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
				{"name": "hooks/bump.ts", "content": `var __veilMod=(()=>{var h={render:function(ctx,fs){var f=fs.getAppYaml();var o=f.getContent();o.replicas=o.replicas+1;f.setContent(o);return fs;}};return{default:h};})();`},
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

func (s *RenderSuite) TestSchemaTypedAccessorRejectsInvalidWrite() {
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bad.ts", "content": badWriteViaAccessorIIFE},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "hooks/bad.ts")
	s.Contains(err.Error(), "replicas")
	s.NoDirExists(filepath.Join(out, "my-worker"))
}

func (s *RenderSuite) TestPreRenderGateRejectsInvalidInitialSource() {
	// app.json's *initial* content already violates the schema —
	// render must fail before any hook runs. The marker hook's
	// absence from the output proves it never ran.
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

// TestEscapeHatchCorruptionCaughtImmediately proves enforcement isn't
// limited to the typed accessor — a bad fs.get() write fails the
// render right at that hook, with no deferred checkpoint.
func (s *RenderSuite) TestEscapeHatchCorruptionCaughtImmediately() {
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/marker.ts", "content": markerHookIIFE},
		{"name": "hooks/corrupt.ts", "content": corruptViaEscapeHatchIIFE},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "hooks/corrupt.ts")
	s.Contains(err.Error(), "replicas")
	s.NoDirExists(filepath.Join(out, "my-worker"))
}

func (s *RenderSuite) TestTypedAccessorAndUntypedHookInterleave() {
	// One hook uses the typed accessor for app.json; another touches a
	// different, schema-less file (config.txt) via the plain escape
	// hatch — both in the same render list, both effects must land.
	untypedIIFE := `var __veilMod=(()=>{var h={render:function(ctx,fs){fs.get("config.txt").setContent("touched");return fs;}};return{default:h};})();`
	s.writeTypedWorkerKind(replicasSchemaJSON, []map[string]any{
		{"name": "hooks/bump.ts", "content": bumpReplicasViaAccessorIIFE},
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

func (s *RenderSuite) TestSourceWithoutSchemaSkipsValidation() {
	// app.json has no declared schema — the generated accessor still
	// exists (every declared source gets one), but getContent/
	// setContent never validate and always deal in the raw string.
	s.writeTypedWorkerKind("", []map[string]any{
		{"name": "hooks/bump.ts", "content": `var __veilMod=(()=>{var h={render:function(ctx,fs){var f=fs.getAppJson();f.setContent('{"replicas":"whatever, no schema"}');return fs;}};return{default:h};})();`},
	})
	dir := filepath.Join(s.root, "svc")
	s.writeTypedWorkerResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	data, err := os.ReadFile(filepath.Join(out, "my-worker", "app.json"))
	s.Require().NoError(err)
	s.Equal(`{"replicas":"whatever, no schema"}`, string(data))
}
