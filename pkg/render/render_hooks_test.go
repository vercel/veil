package render

import (
	"os"
	"path/filepath"
)

// Pre-bundled validate-hook IIFEs. Mirror the helloHookIIFE pattern in
// render_test.go — keeping these inline avoids running esbuild in tests
// just to exercise the validate runner.

// passValidateIIFE returns no issues — render should succeed.
const passValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){return [];}};return{default:h};})();`

// issueValidateIIFE returns a single issue — render should fail with
// that message included in the aggregated report.
const issueValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){return [{message:"replicas must be even",path:"spec.replicas"}];}};return{default:h};})();`

// multiIssueValidateIIFE returns three issues in one hook to confirm
// the runner expands an array into multiple report lines.
const multiIssueValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){return [{message:"a"},{message:"b"},{message:"c"}];}};return{default:h};})();`

// warnValidateIIFE returns a warning-severity issue — render should
// succeed; the warning surfaces through the interactive printer.
const warnValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){return [{message:"only a warning",severity:"warning"}];}};return{default:h};})();`

// throwValidateIIFE throws — runner should treat the throw as one
// error-severity issue but keep iterating across other hooks.
const throwValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){throw new Error("kaboom");}};return{default:h};})();`

// mutateValidateIIFE attempts to mutate the bundle and ctx; the runner
// must discard both mutations. Returns one issue so the test can assert
// the issue is reported (proving the hook actually ran) while also
// asserting the mutation never lands in the rendered output.
const mutateValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){fs.add("evil.txt","BAD");ctx.resource.metadata.name="hijacked";return [{message:"mutation attempted"}];}};return{default:h};})();`

// stringValidateIIFE returns a bare string to confirm the runner
// promotes it to a single error-severity issue.
const stringValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){return "compact form";}};return{default:h};})();`

// writeWorkerKind overwrites the kind.json so the test can declare
// validate hooks alongside the SetupTest-installed render hook. The
// pre-bundled render hook stays the same so the rendered files keep
// matching the assertions in TestHappyPathRendersBundle.
func (s *RenderSuite) writeWorkerKind(validateContent string) {
	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"config.txt": "base",
		},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/hello-world.ts", "content": helloHookIIFE},
			},
			"validate": []map[string]any{
				{"name": "hooks/validate.ts", "content": validateContent},
			},
		},
	}
	s.writeJSON(filepath.Join(s.root, "r", "worker", "kind.json"), compiled)
}

// writeWorkerKindMultiValidate is the same idea but with two validate
// hooks so the runner can be exercised across multiple invocations.
func (s *RenderSuite) writeWorkerKindMultiValidate(contents ...string) {
	entries := make([]map[string]any, 0, len(contents))
	for i, c := range contents {
		entries = append(entries, map[string]any{
			"name":    "hooks/validate-" + string(rune('a'+i)) + ".ts",
			"content": c,
		})
	}
	compiled := map[string]any{
		"name": "worker",
		"sources": map[string]string{
			"config.txt": "base",
		},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"name": "hooks/hello-world.ts", "content": helloHookIIFE},
			},
			"validate": entries,
		},
	}
	s.writeJSON(filepath.Join(s.root, "r", "worker", "kind.json"), compiled)
}

func (s *RenderSuite) writeMinimalResource(dir string) {
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{"kind": "worker", "name": "my-worker"},
		"spec":     map[string]any{"replicas": 3},
	})
}

// --- validate hook tests -------------------------------------------------

func (s *RenderSuite) TestValidateHookPassesWhenNoIssues() {
	s.writeWorkerKind(passValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)
	s.FileExists(filepath.Join(out, "my-worker", "greeting.txt"))
}

func (s *RenderSuite) TestValidateHookFailsRenderOnIssue() {
	s.writeWorkerKind(issueValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "replicas must be even")
	s.Contains(err.Error(), "spec.replicas")
}

func (s *RenderSuite) TestValidateHookExpandsMultiIssueArray() {
	s.writeWorkerKind(multiIssueValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err)
	s.Contains(err.Error(), ": a")
	s.Contains(err.Error(), ": b")
	s.Contains(err.Error(), ": c")
}

func (s *RenderSuite) TestValidateHookWarningDoesNotFail() {
	s.writeWorkerKind(warnValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)
	s.FileExists(filepath.Join(out, "my-worker", "greeting.txt"))
}

func (s *RenderSuite) TestValidateHookThrowsTreatedAsIssue() {
	s.writeWorkerKind(throwValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "threw")
	s.Contains(err.Error(), "kaboom")
}

func (s *RenderSuite) TestValidateHookMutationsAreDiscarded() {
	s.writeWorkerKind(mutateValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err) // the mutation-attempt issue
	s.Contains(err.Error(), "mutation attempted")

	// The fs.add the hook called must not have made it to disk.
	s.NoFileExists(filepath.Join(s.root, "out", "my-worker", "evil.txt"))
	// The greeting still has the original resource name — ctx mutation
	// was discarded too.
	greeting, readErr := os.ReadFile(filepath.Join(s.root, "out", "my-worker", "greeting.txt"))
	if readErr == nil {
		// (won't exist if the render aborted on the issue, which is
		// what we expect — but if a future change made warnings the
		// default this still guards against the rename leaking)
		s.NotContains(string(greeting), "hijacked")
	}
}

func (s *RenderSuite) TestValidateHookStringReturnIsPromoted() {
	s.writeWorkerKind(stringValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "compact form")
}

func (s *RenderSuite) TestValidateHooksRunAllOnFailure() {
	// First throws, second returns an issue. Both should appear in
	// the aggregated report — the throw must not abort the loop.
	s.writeWorkerKindMultiValidate(throwValidateIIFE, issueValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.writeMinimalResource(dir)

	_, err := s.renderWorker(dir, filepath.Join(s.root, "out"), nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "kaboom")
	s.Contains(err.Error(), "replicas must be even")
}

// --- resource hook tests -------------------------------------------------

// resourceHookTS is a tiny TS file written into the resource directory
// so the render-time bundler has something to compile. The hook adds a
// marker file proving it ran. No imports — keeps the bundler call simple
// (no need to materialize the veil-types.ts type stubs).
const resourceHookTS = `export default {
  render: function(ctx, fs) {
    fs.add("from-resource-hook.txt", "ran-after-kind: " + ctx.resource.metadata.name);
    return fs;
  }
};
`

func (s *RenderSuite) TestResourceHookRunsAfterKindRender() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "hook.ts"), []byte(resourceHookTS), 0644))

	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"hooks": map[string]any{
				"render": []map[string]any{
					{"path": "./hook.ts"},
				},
			},
		},
		"spec": map[string]any{"replicas": 3},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	marker, err := os.ReadFile(filepath.Join(out, "my-worker", "from-resource-hook.txt"))
	s.Require().NoError(err)
	s.Equal("ran-after-kind: my-worker", string(marker))

	// Kind render hook still ran first — config.txt has the prefix it
	// stamps.
	cfg, err := os.ReadFile(filepath.Join(out, "my-worker", "config.txt"))
	s.Require().NoError(err)
	s.Equal("my-worker:base", string(cfg))
}

// observingResourceHookTS reads a kind-stamped file. If it sees the
// prefix the kind hook added, kind-render must have run before this
// resource hook — exactly the ordering we promise.
const observingResourceHookTS = `export default {
  render: function(ctx, fs) {
    var f = fs.get("config.txt");
    var prefixedByKind = f && f.getContent().indexOf("my-worker:") === 0;
    fs.add("ordering-check.txt", prefixedByKind ? "kind-ran-first" : "kind-did-not-run");
    return fs;
  }
};
`

func (s *RenderSuite) TestResourceHookSeesKindRenderOutput() {
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "hook.ts"), []byte(observingResourceHookTS), 0644))

	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"hooks": map[string]any{
				"render": []map[string]any{{"path": "./hook.ts"}},
			},
		},
		"spec": map[string]any{"replicas": 3},
	})

	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)

	ord, err := os.ReadFile(filepath.Join(out, "my-worker", "ordering-check.txt"))
	s.Require().NoError(err)
	s.Equal("kind-ran-first", string(ord))
}

// --- pipeline ordering: validate runs after resource hooks --------------

// resourceMarkerHookTS adds a file that a subsequent validate hook
// observes. If validate sees it, validate ran after the resource hook
// — which is the documented order.
const resourceMarkerHookTS = `export default {
  render: function(ctx, fs) {
    fs.add("from-resource.txt", "yes");
    return fs;
  }
};
`

// observeMarkerValidateIIFE: returns one issue iff the resource-hook's
// marker file is present in the FS at validate time.
const observeMarkerValidateIIFE = `var __veilMod=(()=>{var h={validate:function(ctx,fs){var f=fs.get("from-resource.txt");if(!f||f.isDeleted())return [{message:"resource hook did not run before validate"}];return [{message:"resource-hook-seen-at-validate",severity:"warning"}];}};return{default:h};})();`

func (s *RenderSuite) TestValidateRunsAfterResourceHooks() {
	s.writeWorkerKind(observeMarkerValidateIIFE)
	dir := filepath.Join(s.root, "svc")
	s.Require().NoError(os.MkdirAll(dir, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "marker.ts"), []byte(resourceMarkerHookTS), 0644))

	s.writeJSON(filepath.Join(dir, "my-worker.json"), map[string]any{
		"metadata": map[string]any{
			"kind": "worker",
			"name": "my-worker",
			"hooks": map[string]any{
				"render": []map[string]any{{"path": "./marker.ts"}},
			},
		},
		"spec": map[string]any{"replicas": 3},
	})

	// validate hook reports a warning ("resource-hook-seen-at-validate")
	// when it can see the resource hook's output — render must succeed
	// (warnings don't fail) and the marker must be on disk.
	out := filepath.Join(s.root, "out")
	_, err := s.renderWorker(dir, out, nil)
	s.Require().NoError(err)
	s.FileExists(filepath.Join(out, "my-worker", "from-resource.txt"))
}
