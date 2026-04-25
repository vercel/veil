package hook

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/vercel/veil/pkg/bundle"
)

type HookSuite struct {
	suite.Suite
}

func TestHookSuite(t *testing.T) {
	suite.Run(t, new(HookSuite))
}

func (s *HookSuite) compile(src string) string {
	root := fstest.MapFS{
		"hook.ts": &fstest.MapFile{Data: []byte(src)},
	}
	code, err := bundle.Bundle("hook.ts", root, bundle.Options{GlobalName: "__veilMod"})
	s.Require().NoError(err)
	return code
}

func (s *HookSuite) TestRenderHookRoundTrip() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("out.txt", "hello " + ctx.name);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	ctx := map[string]any{"name": "world", "spec": map[string]any{}, "vars": map[string]any{}}
	bundle := Bundle{"existing.txt": File{Path: "existing.txt", Content: "kept"}}

	result, err := hk.RenderHook(ctx, bundle)
	s.Require().NoError(err)
	s.Equal("kept", result["existing.txt"].Content)
	s.Equal("hello world", result["out.txt"].Content)
	s.Equal("out.txt", result["out.txt"].Path)
}

func (s *HookSuite) TestRenderHookNoOpWhenUndefined() {
	code := s.compile(`
const h = {}; // no render
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"x": File{Path: "x", Content: "y"}}
	result, err := hk.RenderHook(map[string]any{"name": "n"}, bundle)
	s.Require().NoError(err)
	s.Equal(bundle, result)
}

func (s *HookSuite) TestRenderHookPropagatesThrow() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    throw new Error("boom");
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err)
	s.Contains(err.Error(), "boom")
}

func (s *HookSuite) TestRenderHookErrorIncludesSourceMappedPosition() {
	// The runtime error happens at line 4 of hook.ts ("calling undefined as
	// a function"). After source-map rewriting the stack should reference
	// hook.ts, not just the bundled hook.js position.
	code := s.compile(`
const h = {
  render(ctx, fs) {
    const broken = undefined;
    broken();
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err)
	s.Contains(err.Error(), "hook.ts:", "stack should be rewritten to point at the original .ts source, got: %s", err.Error())
}

func (s *HookSuite) TestWithDisplayReceivesEveryConsoleCall() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    console.log("a log");
    console.warn("a warn");
    console.error("an error");
    return fs;
  }
};
export default h;
`)

	type entry struct{ level, msg string }
	var got []entry
	hk, err := New(code, WithDisplay(func(level, msg string) {
		got = append(got, entry{level, msg})
	}))
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)
	s.Equal([]entry{
		{"info", "a log"},
		{"warn", "a warn"},
		{"error", "an error"},
	}, got)
}

func (s *HookSuite) TestRenderHookGetAllReturnsEveryFile() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("added.txt", "added");
    fs.delete("doomed.txt");
    const summary = fs.getAll()
      .map(f => f.getPath() + (f.isDeleted() ? ":deleted" : ":live"))
      .sort()
      .join(",");
    fs.add("summary.txt", summary);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{
		"a.txt":      File{Path: "a.txt", Content: "A"},
		"b.txt":      File{Path: "b.txt", Content: "B"},
		"doomed.txt": File{Path: "doomed.txt", Content: "X"},
	}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.Equal("a.txt:live,added.txt:live,b.txt:live,doomed.txt:deleted", result["summary.txt"].Content)
}

func (s *HookSuite) TestRenderHookSupportsAsync() {
	code := s.compile(`
const h = {
  async render(ctx, fs) {
    const val = await Promise.resolve("async-ok");
    fs.add("marker.txt", val);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)
	s.Equal("async-ok", result["marker.txt"].Content)
}

func (s *HookSuite) TestRenderHookGeneratedAccessorReadsContent() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    const file = fs.getSourcesSourceTxt();
    fs.add("echo.txt", file.getContent().toUpperCase());
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"./sources/source.txt": File{Path: "./sources/source.txt", Content: "hello"}}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.Equal("HELLO", result["echo.txt"].Content)
}

func (s *HookSuite) TestSetOutputPathPreservesIdentity() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.getSourcesDeploymentYaml().setOutputPath("kubernetes/deployment.yaml");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"./sources/deployment.yaml": File{Path: "./sources/deployment.yaml", Content: "kind: Deployment"}}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)

	entry, ok := result["./sources/deployment.yaml"]
	s.Require().True(ok, "identity key must be preserved across setOutputPath")
	s.Equal("kubernetes/deployment.yaml", entry.Path)
	s.Equal("kind: Deployment", entry.Content)
}

func (s *HookSuite) TestSetContent() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.getSourcesSourceTxt().setContent("updated");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"./sources/source.txt": File{Path: "./sources/source.txt", Content: "original"}}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.Equal("updated", result["./sources/source.txt"].Content)
}

func (s *HookSuite) TestAddCollidingIdentityErrors() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("foo.yaml", "first");
    fs.add("foo.yaml", "second");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err)
	s.Contains(err.Error(), "already exists")
}

func (s *HookSuite) TestDeleteIsSoftAndObservable() {
	h1Code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.delete("./sources/source.txt");
    return fs;
  }
};
export default h;
`)
	h2Code := s.compile(`
const h = {
  render(ctx, fs) {
    const file = fs.getSourcesSourceTxt();
    fs.add("report.txt", file.isDeleted() ? "source was deleted" : "source is live");
    return fs;
  }
};
export default h;
`)

	hk1, err := New(h1Code)
	s.Require().NoError(err)
	defer hk1.Close()
	hk2, err := New(h2Code)
	s.Require().NoError(err)
	defer hk2.Close()

	bundle := Bundle{"./sources/source.txt": File{Path: "./sources/source.txt", Content: "hi"}}
	mid, err := hk1.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.True(mid["./sources/source.txt"].Deleted)

	final, err := hk2.RenderHook(map[string]any{}, mid)
	s.Require().NoError(err)
	s.Equal("source was deleted", final["report.txt"].Content)
}

func (s *HookSuite) TestSetDeletedMarksAndRestores() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    const file = fs.getSourcesSourceTxt();
    file.setDeleted(true);
    fs.add("mid.txt", file.isDeleted() ? "dead" : "alive");
    file.setDeleted(false);
    fs.add("end.txt", file.isDeleted() ? "dead" : "alive");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"./sources/source.txt": File{Path: "./sources/source.txt", Content: "hi"}}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.Equal("dead", result["mid.txt"].Content)
	s.Equal("alive", result["end.txt"].Content)
	s.False(result["./sources/source.txt"].Deleted, "setDeleted(false) restores the entry")
}

func (s *HookSuite) TestIsDeletedDefaultsFalse() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("status.txt", fs.getSourcesSourceTxt().isDeleted() ? "yes" : "no");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	bundle := Bundle{"./sources/source.txt": File{Path: "./sources/source.txt", Content: "hi"}}
	result, err := hk.RenderHook(map[string]any{}, bundle)
	s.Require().NoError(err)
	s.Equal("no", result["status.txt"].Content)
}

func (s *HookSuite) TestRenderHookVoidReturnFallsBackToFS() {
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("out.txt", "mutated");
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)
	s.Equal("mutated", result["out.txt"].Content)
}

func (s *HookSuite) TestRenderHookSupportsMultipleCalls() {
	// Each call gets a fresh runtime — closure state does not persist, but
	// the Hook itself remains reusable for independent invocations.
	code := s.compile(`
const h = {
  render(ctx, fs) {
    fs.add("out.txt", ctx.name);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	r1, err := hk.RenderHook(map[string]any{"name": "a"}, Bundle{})
	s.Require().NoError(err)
	s.Equal("a", r1["out.txt"].Content)

	r2, err := hk.RenderHook(map[string]any{"name": "b"}, Bundle{})
	s.Require().NoError(err)
	s.Equal("b", r2["out.txt"].Content)
}

func (s *HookSuite) TestRenderHookAfterCloseErrors() {
	code := s.compile(`
const h = { render(ctx, fs) { return fs; } };
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	s.Require().NoError(hk.Close())

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err)
	s.Contains(err.Error(), "after Close")
}

func (s *HookSuite) TestCloseIsIdempotent() {
	code := s.compile(`const h = {}; export default h;`)
	hk, err := New(code)
	s.Require().NoError(err)
	s.Require().NoError(hk.Close())
	s.Require().NoError(hk.Close())
}

func (s *HookSuite) TestRenderHookTimeout() {
	// Finite-but-slow loop: spins for ~1s, timeout at 100ms. Using a
	// finite upper bound lets the leaked eval goroutine eventually
	// complete so the test process can exit cleanly.
	code := s.compile(`
const h = {
  render(ctx, fs) {
    const end = Date.now() + 1000;
    while (Date.now() < end) {}
    return fs;
  }
};
export default h;
`)

	hk, err := New(code, WithTimeout(100*time.Millisecond))
	s.Require().NoError(err)
	// Explicitly do NOT defer hk.Close() — once stuck, Close is a no-op
	// by design; skipping it avoids any doubt about test cleanup order.

	start := time.Now()
	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	elapsed := time.Since(start)
	s.Require().Error(err)
	s.Contains(err.Error(), "timeout")
	s.Less(elapsed, 500*time.Millisecond, "timeout fires without waiting for the script to finish")

	// Subsequent calls on a stuck hook fail fast — does not touch the
	// in-flight runtime.
	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err)
	s.Contains(err.Error(), "abandoned")

	// Close on a stuck hook is a no-op.
	s.Require().NoError(hk.Close())
}

func (s *HookSuite) TestFetchPolyfill() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("POST", r.Method)
		s.Equal("application/json", r.Header.Get("Content-Type"))
		w.Header().Set("X-Echo", "howdy")
		_, _ = w.Write([]byte(`{"greeting":"hi"}`))
	}))
	defer srv.Close()

	code := s.compile(`
const h = {
  async render(ctx, fs) {
    const resp = await ctx.fetch(ctx.vars.endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ msg: "pong" })
    });
    const json = await resp.json();
    fs.add("ok.txt", String(resp.ok));
    fs.add("status.txt", String(resp.status));
    fs.add("echo.txt", resp.headers.get("X-Echo") ?? "<missing>");
    fs.add("greeting.txt", json.greeting);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{"vars": map[string]any{"endpoint": srv.URL}}, Bundle{})
	s.Require().NoError(err)
	s.Equal("true", result["ok.txt"].Content)
	s.Equal("200", result["status.txt"].Content)
	s.Equal("howdy", result["echo.txt"].Content)
	s.Equal("hi", result["greeting.txt"].Content)
}

func (s *HookSuite) TestFetchAllowlistRejectsOtherHosts() {
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer allowed.Close()
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer blocked.Close()

	allowedHost := mustHost(allowed.URL)
	code := s.compile(`
const h = {
  async render(ctx, fs) {
    const ok = await ctx.fetch(ctx.vars.ok);
    fs.add("allowed.txt", await ok.text());
    try {
      await ctx.fetch(ctx.vars.blocked);
      fs.add("blocked.txt", "UNEXPECTEDLY SUCCEEDED");
    } catch (e) {
      fs.add("blocked.txt", String(e));
    }
    return fs;
  }
};
export default h;
`)

	hk, err := New(code, WithHTTP(HTTPConfig{AllowedHosts: []string{allowedHost}}))
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{
		"vars": map[string]any{
			"ok":      allowed.URL,
			"blocked": blocked.URL,
		},
	}, Bundle{})
	s.Require().NoError(err)
	s.Equal("ok", result["allowed.txt"].Content)
	s.Contains(result["blocked.txt"].Content, "not in allowlist")
}

func (s *HookSuite) TestFetchTimeoutFromHostConfig() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	code := s.compile(`
const h = {
  async render(ctx, fs) {
    try {
      await ctx.fetch(ctx.vars.url);
      fs.add("result.txt", "no-throw");
    } catch (e) {
      fs.add("result.txt", String(e));
    }
    return fs;
  }
};
export default h;
`)

	hk, err := New(code, WithHTTP(HTTPConfig{DefaultTimeoutMs: 50}))
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{"vars": map[string]any{"url": srv.URL}}, Bundle{})
	s.Require().NoError(err)
	body := result["result.txt"].Content
	s.True(
		contains(body, "deadline exceeded") || contains(body, "Client.Timeout") || contains(body, "context"),
		"expected timeout-shaped error, got: %s", body,
	)
}

func (s *HookSuite) TestFetchRejectsNonHTTPScheme() {
	code := s.compile(`
const h = {
  async render(ctx, fs) {
    try {
      await ctx.fetch("file:///etc/passwd");
      fs.add("out.txt", "UNEXPECTEDLY SUCCEEDED");
    } catch (e) {
      fs.add("out.txt", String(e));
    }
    return fs;
  }
};
export default h;
`)

	hk, err := New(code)
	s.Require().NoError(err)
	defer hk.Close()

	result, err := hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)
	s.Contains(result["out.txt"].Content, "http/https")
}

func contains(s, sub string) bool { return indexAt(s, sub, 0) >= 0 }

func mustHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u.Host
}

func (s *HookSuite) TestMemoryLimitCatchesRunaway() {
	// 1 MiB limit; an allocating loop blows past it quickly and throws.
	code := s.compile(`
const h = {
  render(ctx, fs) {
    const chunks = [];
    while (true) chunks.push("x".repeat(1024 * 64));
  }
};
export default h;
`)

	hk, err := New(code, WithMemoryLimit(1*1024*1024))
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().Error(err, "runaway allocation must hit the memory limit")
}

func (s *HookSuite) TestConsoleLogRoutedThroughLogger() {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	code := s.compile(`
const h = {
  render(ctx, fs) {
    console.log("info from hook", { foo: "bar" }, 42);
    console.warn("warn text");
    console.error("error text");
    console.debug("debug text");
    return fs;
  }
};
export default h;
`)

	hk, err := New(code, WithLogger(logger))
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)

	out := buf.String()
	// JSON handler emits `"msg":"..."` with internal quotes escaped.
	s.Contains(out, `"level":"INFO"`)
	s.Contains(out, `info from hook`)
	s.Contains(out, `{\"foo\":\"bar\"} 42`)
	s.Contains(out, `"level":"WARN"`)
	s.Contains(out, `warn text`)
	s.Contains(out, `"level":"ERROR"`)
	s.Contains(out, `error text`)
	s.Contains(out, `"level":"DEBUG"`)
	s.Contains(out, `debug text`)
}

func (s *HookSuite) TestLogsBufferDoesNotLeakAcrossCalls() {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	code := s.compile(`
let n = 0;
const h = {
  render(ctx, fs) {
    n++;
    console.log("call " + n);
    return fs;
  }
};
export default h;
`)

	hk, err := New(code, WithLogger(logger))
	s.Require().NoError(err)
	defer hk.Close()

	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)
	_, err = hk.RenderHook(map[string]any{}, Bundle{})
	s.Require().NoError(err)

	s.Equal(1, bytesCount(buf.String(), "call 1"), "first call's log should not replay on the second")
	s.Equal(1, bytesCount(buf.String(), "call 2"))
}

func bytesCount(s, sub string) int {
	if sub == "" {
		return 0
	}
	count := 0
	for i := 0; ; {
		j := indexAt(s, sub, i)
		if j < 0 {
			return count
		}
		count++
		i = j + len(sub)
	}
}

func indexAt(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
