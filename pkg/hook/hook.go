// Package hook is the Go-side adapter for a veil hook: a compiled TS/JS
// module that participates in lifecycle events during `veil render`.
//
// Each lifecycle point is a method on the Hook interface. When a compiled
// hook does not define the corresponding function (e.g. `renderHook`), the
// Go method is a no-op that returns the input state unchanged.
package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/fastschema/qjs"
	"github.com/go-sourcemap/sourcemap"
	"github.com/goccy/go-json"
)

// Defaults applied when no corresponding Option is passed to New.
const (
	DefaultTimeout            = 30 * time.Second
	DefaultMemoryLimit        = 128 * 1024 * 1024 // 128 MiB
	DefaultHTTPTimeoutMs      = 10_000            // 10 s per request
	DefaultHTTPMaxResponseLen = 10 * 1024 * 1024  // 10 MiB
)

// HTTPConfig shapes the http.request binding installed for each hook.
// Zero values mean: allow all hosts, use DefaultHTTPTimeoutMs, use
// DefaultHTTPMaxResponseLen.
type HTTPConfig struct {
	AllowedHosts     []string
	DefaultTimeoutMs int
	MaxResponseBytes int
}

// NOTE on runaway hooks: the underlying QuickJS/wazero stack in qjs v0.0.6
// cannot interrupt a pure-wasm tight loop (host-call-based context checks
// don't see loops that never yield). What we do instead:
//
//   - Wall-clock timeout via goroutine + time.After. On fire, RenderHook
//     returns a clean error immediately; the eval goroutine is *abandoned*
//     and keeps using its OS thread until the script naturally completes.
//   - The Hook is marked stuck — subsequent RenderHook calls fail fast and
//     Close is a no-op (rt.Close on an in-flight runtime hangs).
//   - Memory limit — most runaway bugs also runaway-allocate and hit this
//     limit first, producing a clean throw before the timeout fires.
//
// For a one-shot CLI this is fine: the process exits after the render and
// the OS reclaims the stuck thread. If you need something more robust for
// long-running / multi-tenant usage, run renders in killable subprocesses.

// File is one entry in the per-instance FS threaded through the hook
// pipeline. The identity key under which it sits in the bundle map is
// stable across hooks; Path is the destination (defaults to the identity),
// Content is the file contents, and Deleted is a tombstone flag that
// survives across hooks — when true the final writer skips this entry but
// downstream hooks can still observe it via File.isDeleted().
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Deleted bool   `json:"deleted,omitempty"`
}

// Bundle is the shape of the state passed to and returned from a hook. Keys
// are identity strings — typically the declared source path. Values carry
// the current destination path and content.
type Bundle map[string]File

// fsFactoryJS installs the per-runtime globals used by the RenderHook call
// script: __veilMakeFS (wraps a raw bundle in an FS with typed accessors),
// __veilMethodSuffix (path→accessor name; must match methodSuffixForPath
// in pkg/commands), and a `console` polyfill that buffers log entries into
// __veilLogs for the host to drain after each hook invocation.
const fsFactoryJS = `
function __veilMethodSuffix(p) {
  if (typeof p !== 'string') return '';
  if (p.startsWith('./')) p = p.slice(2);
  var out = '';
  var cap = true;
  for (var i = 0; i < p.length; i++) {
    var c = p[i];
    if (c === '/' || c === '.' || c === '-' || c === '_' || c === ' ') {
      cap = true;
      continue;
    }
    if (cap) { out += c.toUpperCase(); cap = false; }
    else { out += c; }
  }
  return out;
}

function __veilMakeFile(entry) {
  return {
    getContent: function() { return entry.content; },
    setContent: function(c) { entry.content = String(c); },
    getPath: function() { return entry.path; },
    setOutputPath: function(p) { entry.path = String(p); },
    isDeleted: function() { return !!entry.deleted; },
    setDeleted: function(v) { entry.deleted = !!v; }
  };
}

function __veilMakeFS(initial) {
  // Normalize: accept either the structured form { key: {path, content, deleted?} }
  // or a legacy flat form { key: "content" } (path defaults to the key).
  var entries = {};
  for (var k in initial) {
    if (!Object.prototype.hasOwnProperty.call(initial, k)) continue;
    var v = initial[k];
    if (typeof v === 'string') {
      entries[k] = { path: k, content: v, deleted: false };
    } else {
      entries[k] = {
        path: typeof v.path === 'string' ? v.path : k,
        content: typeof v.content === 'string' ? v.content : '',
        deleted: !!v.deleted
      };
    }
  }

  var fs = {
    get: function(path) {
      if (!Object.prototype.hasOwnProperty.call(entries, path)) return undefined;
      return __veilMakeFile(entries[path]);
    },
    add: function(path, content) {
      if (typeof path !== 'string' || !path) throw new Error('fs.add: path must be a non-empty string');
      if (Object.prototype.hasOwnProperty.call(entries, path)) {
        throw new Error('fs.add: path ' + JSON.stringify(path) + ' already exists');
      }
      entries[path] = { path: path, content: String(content == null ? '' : content), deleted: false };
      return __veilMakeFile(entries[path]);
    },
    delete: function(path) {
      if (Object.prototype.hasOwnProperty.call(entries, path)) entries[path].deleted = true;
    },
    keys: function() { return Object.keys(entries); },
    getAll: function() {
      var out = [];
      for (var k in entries) {
        if (Object.prototype.hasOwnProperty.call(entries, k)) {
          out.push(__veilMakeFile(entries[k]));
        }
      }
      return out;
    },
    toJSON: function() {
      var out = {};
      for (var k in entries) {
        if (!Object.prototype.hasOwnProperty.call(entries, k)) continue;
        var e = entries[k];
        var obj = { path: e.path, content: e.content };
        if (e.deleted) obj.deleted = true;
        out[k] = obj;
      }
      return out;
    }
  };

  var ks = Object.keys(entries);
  for (var i = 0; i < ks.length; i++) {
    var key = ks[i];
    var suffix = __veilMethodSuffix(key);
    if (!suffix) continue;
    fs['get' + suffix] = (function(k) { return function() { return __veilMakeFile(entries[k]); }; })(key);
  }
  return fs;
}

// ---- console polyfill ---------------------------------------------------
var __veilLogs = [];
function __veilFormatArg(a) {
  if (a === undefined) return 'undefined';
  if (a === null) return 'null';
  if (typeof a === 'object') {
    try { return JSON.stringify(a); } catch (e) { return String(a); }
  }
  return String(a);
}
function __veilLog(level, args) {
  var parts = [];
  for (var i = 0; i < args.length; i++) parts.push(__veilFormatArg(args[i]));
  __veilLogs.push({ level: level, message: parts.join(' ') });
}
globalThis.console = {
  log:   function() { __veilLog('info',  arguments); },
  info:  function() { __veilLog('info',  arguments); },
  warn:  function() { __veilLog('warn',  arguments); },
  error: function() { __veilLog('error', arguments); },
  debug: function() { __veilLog('debug', arguments); }
};
`

// Script fragments around the two JSON substitution points. Split so we
// can pre-size a strings.Builder and write the marshalled ctx/bundle
// bytes directly (avoiding the intermediate string(bytes) copies that
// fmt.Sprintf forces).
//
// The wrapper is an async IIFE so hook authors can write either sync or
// async `renderHook` — if the hook returns a Promise we await it
// transparently. The eval is invoked with FlagAsync so QuickJS returns
// the resolved value (not the Promise) to Go. Errors come exclusively
// through uncaught throws.
const (
	renderHookScriptPrefix = `await (async () => {
  __veilLogs.length = 0;
  if (!__veilMod || !__veilMod.default || typeof __veilMod.default.render !== 'function') {
    return JSON.stringify({ logs: __veilLogs });
  }
  const __ctx = `
	// Between the JSON-parsed ctx and the FS construction we splice in the
	// host APIs from __veilHost — read-only std/os proxies and the fetch
	// polyfill. They can't round-trip through JSON so we attach them as
	// properties post-parse.
	renderHookScriptMiddle = `;
  __ctx.std = globalThis.__veilHost.std;
  __ctx.os = globalThis.__veilHost.os;
  __ctx.fetch = globalThis.__veilHost.fetch;
  __ctx.env = globalThis.__veilHost.env;
  const __fs = __veilMakeFS(`
	renderHookScriptSuffix = `);
  let __res = __veilMod.default.render(__ctx, __fs);
  if (__res && typeof __res.then === 'function') __res = await __res;
  const __final = __res == null ? __fs : __res;
  return JSON.stringify({ fs: __final, logs: __veilLogs });
})()`
)

// Option configures a Hook.
type Option func(*options)

type options struct {
	timeout     time.Duration
	memoryLimit int
	logger      *slog.Logger
	display     func(level, msg string)
	http        HTTPConfig
	env         map[string]string
}

// WithTimeout bounds a single RenderHook call. When the timeout fires the
// call returns an error immediately; the eval goroutine is abandoned (see
// the NOTE at the top of this file) and the Hook is marked stuck so
// subsequent calls and Close fail fast without touching the runtime.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithMemoryLimit caps the QuickJS runtime's memory allocation in bytes.
// Allocations beyond the limit surface as a throw from JS.
func WithMemoryLimit(n int) Option { return func(o *options) { o.memoryLimit = n } }

// WithLogger routes `console.log` / warn / error / info / debug calls from
// the hook's JS to the supplied slog.Logger. Messages are emitted after
// the hook returns, preserving order within a single invocation. Defaults
// to slog.Default().
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithDisplay supplies a callback invoked once per `console.*` call from
// the hook, *in addition to* the slog logger. The level is one of
// "debug", "info", "warn", "error". Use this when the slog logger is
// going somewhere the user can't see (e.g. a rolling log file) and you
// want to surface specific levels — most commonly warn/error — through
// a separate channel like the terminal printer.
func WithDisplay(fn func(level, msg string)) Option {
	return func(o *options) { o.display = fn }
}

// WithHTTP configures the http.request binding exposed to the hook. Zero
// values within HTTPConfig take their documented defaults.
func WithHTTP(cfg HTTPConfig) Option { return func(o *options) { o.http = cfg } }

// WithEnv supplies the resolved environment variables a hook is allowed
// to read. The map is exposed to the hook on `ctx.env` (and `globalThis.env`)
// as a frozen object — only the keys passed here are visible. Callers
// are expected to have already validated against the hook's declared
// `access.env` list (the runner does this in pre-flight).
func WithEnv(env map[string]string) Option {
	return func(o *options) { o.env = env }
}

// Hook is the Go-side abstraction for a veil hook. Every lifecycle method
// is safe to call; methods whose underlying JS function is not defined
// return the input unchanged.
//
// Hook wraps a long-lived QuickJS runtime — callers must invoke Close when
// they are finished with it to free the associated Wasm memory.
type Hook interface {
	// RenderHook runs the hook's `renderHook(ctx, fs)` function. If the JS
	// module has no `renderHook` defined, the input bundle is returned
	// unchanged.
	RenderHook(ctx any, bundle Bundle) (Bundle, error)

	// Close releases the underlying runtime resources. Safe to call more
	// than once.
	Close() error
}

// New creates a Hook backed by compiled JS code (IIFE form emitted by
// `veil build` with GlobalName="__veilMod"). The code is evaluated once at
// construction so the `__veilMod` global is ready for subsequent lifecycle
// calls on the same runtime.
func New(code string, opts ...Option) (Hook, error) {
	cfg := options{
		timeout:     DefaultTimeout,
		memoryLimit: DefaultMemoryLimit,
		logger:      slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	rt, err := qjs.New(qjs.Option{MemoryLimit: cfg.memoryLimit})
	if err != nil {
		return nil, fmt.Errorf("creating runtime: %w", err)
	}

	smap := extractInlineSourcemap(code)

	val, err := rt.Eval("hook.js", qjs.Code(code))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("evaluating hook code: %w", rewriteErr(err, smap))
	}
	val.Free()

	// Install the FS factory + console polyfill used by the RenderHook
	// call script.
	fsVal, err := rt.Eval("veil-fs.js", qjs.Code(fsFactoryJS))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing fs factory: %w", err)
	}
	fsVal.Free()

	// Bind the Go-side fetch handler. installFetch sets it as a temporary
	// __veilFetch global that the host-namespace lockdown step closes over
	// and then deletes — hooks themselves never see __veilFetch.
	if err := installFetch(rt, cfg); err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing fetch binding: %w", err)
	}

	// Build the read-only std/os proxies, wrap fetch as a Promise-returning
	// polyfill, bundle them into globalThis.__veilHost, and delete the raw
	// std/os/__veilFetch globals. After this, the only surface hook code
	// can reach is what we splice into ctx.std/ctx.os/ctx.fetch per call.
	hostVal, err := rt.Eval("veil-host.js", qjs.Code(hostNamespaceJS))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing host namespace: %w", err)
	}
	hostVal.Free()

	// Attach the resolved env map onto __veilHost.env, frozen so hook
	// code can't mutate it. Only keys the hook declared in `access.env`
	// (and that the host actually has set) reach this point.
	envJSON, err := json.Marshal(cfg.env)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("encoding env map: %w", err)
	}
	envScript := "globalThis.__veilHost.env = Object.freeze(" + string(envJSON) + ");"
	envVal, err := rt.Eval("veil-env.js", qjs.Code(envScript))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing env: %w", err)
	}
	envVal.Free()

	return &jsHook{rt: rt, cfg: cfg, sourcemap: smap}, nil
}

// rewriteErr returns err with any `hook.js:line:col` references in its
// message replaced with the original source location, when an inline
// sourcemap is available.
func rewriteErr(err error, c *sourcemap.Consumer) error {
	if err == nil || c == nil {
		return err
	}
	rewritten := rewriteStackTrace(err.Error(), c)
	if rewritten == err.Error() {
		return err
	}
	return errors.New(rewritten)
}

// fetchRequestOptions is the JSON shape handed to the Go binding by the
// JS fetch polyfill. It collects everything we need to issue the HTTP
// request on the Go side.
type fetchRequestOptions struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// fetchResponseShape is the JSON structure returned to the polyfill; it's
// then normalized into a Web-Fetch-shaped Response object on the JS side.
type fetchResponseShape struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// installFetch binds `__veilFetch(optsJSON) string` as a temporary global.
// The host-namespace lockdown script closes over it to expose fetch on
// hook contexts, then deletes the global. Enforces the allowlist, timeout,
// and response-size cap configured via WithHTTP.
func installFetch(rt *qjs.Runtime, cfg options) error {
	timeoutMs := cfg.http.DefaultTimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = DefaultHTTPTimeoutMs
	}
	maxBytes := cfg.http.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultHTTPMaxResponseLen
	}
	allowedHosts := cfg.http.AllowedHosts
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	fn, err := qjs.FuncToJS(rt.Context(), func(optsJSON string) (string, error) {
		var opts fetchRequestOptions
		if err := json.Unmarshal([]byte(optsJSON), &opts); err != nil {
			return "", fmt.Errorf("fetch: invalid options: %w", err)
		}
		if opts.URL == "" {
			return "", errors.New("fetch: url is required")
		}

		u, err := url.Parse(opts.URL)
		if err != nil {
			return "", fmt.Errorf("fetch: invalid url: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("fetch: only http/https are allowed (got %q)", u.Scheme)
		}
		if len(allowedHosts) > 0 && !slices.Contains(allowedHosts, u.Host) {
			return "", fmt.Errorf("fetch: host %q not in allowlist", u.Host)
		}

		method := strings.ToUpper(opts.Method)
		if method == "" {
			method = http.MethodGet
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()

		var body io.Reader
		if opts.Body != "" {
			body = strings.NewReader(opts.Body)
		}
		req, err := http.NewRequestWithContext(ctx, method, opts.URL, body)
		if err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		for k, v := range opts.Headers {
			req.Header.Set(k, v)
		}

		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Warn("fetch failed", "method", method, "url", opts.URL, "err", err.Error())
			return "", fmt.Errorf("fetch: %w", err)
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if err != nil {
			return "", fmt.Errorf("fetch: reading body: %w", err)
		}
		if len(raw) > maxBytes {
			return "", fmt.Errorf("fetch: response exceeds %d bytes", maxBytes)
		}

		headers := make(map[string]string, len(resp.Header))
		for k := range resp.Header {
			headers[k] = resp.Header.Get(k)
		}

		logger.Info("fetch",
			"method", method, "url", opts.URL,
			"status", resp.StatusCode,
			"duration", time.Since(start).String(),
			"bytes", len(raw))

		out, _ := json.Marshal(fetchResponseShape{
			Status:  resp.StatusCode,
			Headers: headers,
			Body:    string(raw),
		})
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping Go function: %w", err)
	}

	global := rt.Context().Global()
	defer global.Free()
	global.SetPropertyStr("__veilFetch", fn)
	return nil
}

// hostNamespaceJS is the final init step. It closes over the raw
// std/os/__veilFetch globals, exposes only the read-only / Promise-shaped
// proxies on globalThis.__veilHost, and replaces the originals so hook
// code can't reach the dangerous bindings. The per-call render script
// splices the proxies onto ctx.std / ctx.os / ctx.fetch.
//
// `globalThis.std` is replaced with the same loadFile/getenv pair, and
// `globalThis.fetch` is set to the polyfill — both are common enough in
// hook one-liners (and stock Web Fetch usage) that having them as globals
// matches developer expectations. `globalThis.os` and the
// `globalThis.__veilFetch` Go binding are deleted; hooks reach the
// read-only os via ctx, and there's no reason to expose the raw binding.
//
// Intentional gaps vs. spec-compliant fetch:
//   - No AbortController/AbortSignal support (timeout is configured
//     host-side via hook.HTTPConfig).
//   - `resp.headers` is a duck-typed object (get/has/forEach/iterable),
//     not a real Headers instance — fine for typical usage.
//   - Body types: strings only; no FormData/Blob/ReadableStream.
//   - No streaming: only `.text()` and `.json()`.
const hostNamespaceJS = `
(function() {
  var nativeStd = globalThis.std;
  var nativeOs = globalThis.os;
  var nativeFetch = globalThis.__veilFetch;

  function findHeader(headers, name) {
    var want = String(name).toLowerCase();
    for (var k in headers) {
      if (Object.prototype.hasOwnProperty.call(headers, k) && k.toLowerCase() === want) {
        return headers[k];
      }
    }
    return null;
  }

  function fetchPolyfill(input, init) {
    init = init || {};
    var url, method, headers, body;
    if (typeof input === 'string') {
      url = input;
    } else if (input && typeof input === 'object') {
      url = input.url != null ? String(input.url) : String(input);
      method = input.method;
      headers = input.headers;
      body = input.body;
    } else {
      url = String(input);
    }
    method = init.method || method || 'GET';
    headers = Object.assign({}, headers || {}, init.headers || {});
    body = init.body != null ? init.body : body;

    var resp = JSON.parse(nativeFetch(JSON.stringify({
      url: url, method: method, headers: headers,
      body: body != null ? String(body) : undefined
    })));
    var responseHeaders = {
      get: function(n) { return findHeader(resp.headers, n); },
      has: function(n) { return findHeader(resp.headers, n) !== null; },
      forEach: function(fn) { for (var k in resp.headers) fn(resp.headers[k], k); }
    };
    return Promise.resolve({
      status: resp.status,
      statusText: '',
      ok: resp.status >= 200 && resp.status < 300,
      url: url,
      headers: responseHeaders,
      text: function() { return Promise.resolve(resp.body); },
      json: function() { return Promise.resolve(JSON.parse(resp.body)); }
    });
  }

  var stdProxy = {
    loadFile: function(path) { return nativeStd.loadFile(path); },
    getenv:   function(name) { return nativeStd.getenv(name); }
  };
  var osProxy = {
    readdir:  function(p) { return nativeOs.readdir(p); },
    stat:     function(p) { return nativeOs.stat(p); },
    lstat:    function(p) { return nativeOs.lstat(p); },
    realpath: function(p) { return nativeOs.realpath(p); },
    readlink: function(p) { return nativeOs.readlink(p); },
    getcwd:   function()  { return nativeOs.getcwd(); },
    get platform() { return nativeOs.platform; }
  };

  globalThis.__veilHost = { std: stdProxy, os: osProxy, fetch: fetchPolyfill };

  // Replace globalThis.std with the read-only proxy (same object as
  // ctx.std). The full QuickJS std module is gone but loadFile / getenv
  // remain for the common "read a file" / "read an env var" one-liners.
  globalThis.std = stdProxy;

  // Expose fetch as a global so hook code reads like normal Web Fetch
  // (most snippets / docs assume a global fetch). Same polyfill as
  // ctx.fetch.
  globalThis.fetch = fetchPolyfill;

  delete globalThis.os;
  delete globalThis.__veilFetch;
})();
`

type jsHook struct {
	rt        *qjs.Runtime
	cfg       options
	sourcemap *sourcemap.Consumer
	closed    bool
	stuck     bool // set after a timeout; rt must not be touched again
}

func (h *jsHook) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	if h.stuck {
		// An eval goroutine is still holding the runtime; closing it
		// from here would hang (rt.Close waits on in-flight wasm).
		// Abandon the runtime — process exit will reclaim.
		return nil
	}
	h.rt.Close()
	return nil
}

type hookResult struct {
	FS   Bundle     `json:"fs,omitempty"`
	Logs []logEntry `json:"logs,omitempty"`
}

type logEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (h *jsHook) RenderHook(ctx any, bundle Bundle) (Bundle, error) {
	if h.closed {
		return nil, errors.New("hook: RenderHook called after Close")
	}
	if h.stuck {
		return nil, errors.New("hook: runtime abandoned after prior timeout")
	}

	ctxJSON, err := json.Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("marshalling ctx: %w", err)
	}
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshalling bundle: %w", err)
	}

	var b strings.Builder
	b.Grow(len(renderHookScriptPrefix) + len(ctxJSON) + len(renderHookScriptMiddle) + len(bundleJSON) + len(renderHookScriptSuffix))
	b.WriteString(renderHookScriptPrefix)
	b.Write(ctxJSON)
	b.WriteString(renderHookScriptMiddle)
	b.Write(bundleJSON)
	b.WriteString(renderHookScriptSuffix)
	script := b.String()

	// Run the eval on a goroutine so we can timeout the wall clock. When
	// the timeout fires we report back immediately and abandon the
	// goroutine — the eval keeps running until the script naturally
	// terminates. See the NOTE at top of file.
	//
	// FlagAsync makes QuickJS await the top-level Promise (our async IIFE)
	// and hand back the resolved value, so hooks can be sync or async
	// without any caller-side plumbing.
	type outcome struct {
		raw string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		val, err := h.rt.Eval("render.js", qjs.Code(script), qjs.FlagAsync())
		if err != nil {
			done <- outcome{err: err}
			return
		}
		raw := val.String()
		val.Free()
		done <- outcome{raw: raw}
	}()

	var res outcome
	select {
	case res = <-done:
	case <-time.After(h.cfg.timeout):
		// Timeout fired. The eval goroutine keeps running on its OS
		// thread because qjs/wazero cannot interrupt a pure-wasm tight
		// loop (see NOTE at top of file for the full story). That leak
		// is acceptable here: `veil render` is a one-shot CLI, the
		// caller is about to surface this error and exit, and process
		// teardown reclaims the thread. Mark the Hook stuck so any
		// further RenderHook calls and Close fail fast without touching
		// the in-flight runtime.
		h.stuck = true
		return nil, fmt.Errorf("hook exceeded %s timeout", h.cfg.timeout)
	}

	if res.err != nil {
		return nil, fmt.Errorf("invoking renderHook: %w", rewriteErr(res.err, h.sourcemap))
	}

	var result hookResult
	if err := json.Unmarshal([]byte(res.raw), &result); err != nil {
		return nil, fmt.Errorf("parsing hook result: %w (raw: %s)", err, res.raw)
	}

	h.emitLogs(result.Logs)

	if result.FS == nil {
		return bundle, nil
	}
	return result.FS, nil
}

func (h *jsHook) emitLogs(logs []logEntry) {
	if len(logs) == 0 {
		return
	}
	for _, l := range logs {
		if h.cfg.logger != nil {
			switch l.Level {
			case "debug":
				h.cfg.logger.Debug(l.Message)
			case "warn":
				h.cfg.logger.Warn(l.Message)
			case "error":
				h.cfg.logger.Error(l.Message)
			default:
				h.cfg.logger.Info(l.Message)
			}
		}
		if h.cfg.display != nil {
			h.cfg.display(l.Level, l.Message)
		}
	}
}
