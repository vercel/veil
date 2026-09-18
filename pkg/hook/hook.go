// Package hook is the Go-side adapter for a veil hook: a compiled TS/JS
// module that participates in lifecycle events during `veil render`.
//
// Each lifecycle point is a method on the Hook interface. When a compiled
// hook does not define the corresponding function (e.g. `renderHook`), the
// Go method is a no-op that returns the input state unchanged.
package hook

import (
	"bytes"
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
	"github.com/vercel/veil/pkg/codec"
	"github.com/vercel/veil/pkg/tfwrite"
	yaml "gopkg.in/yaml.v3"
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
// stable across hooks; Path is the destination (defaults to the
// identity), and Content is the file contents. An entry stays in the
// bundle for every later hook to see however Render moves — dropping a
// file is a matter of not writing it, not of removing it.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`

	// Type is how Content encodes a document. A JSON or YAML entry hands
	// hooks a parsed object from getContent and takes one in setContent;
	// a plaintext entry stays a string on both sides. Empty means
	// plaintext.
	Type ContentType `json:"type,omitempty"`

	// MustValidate marks an entry whose kind declared a schema for it.
	// setContent checks the serialized result against that schema before
	// storing it, so a hook that writes something invalid throws at the
	// call site instead of failing a whole render later.
	MustValidate bool `json:"mustValidate,omitempty"`

	// Render is the one thing that decides whether this entry is
	// written. False for an asset the kind ships purely for its hooks to
	// read, and equally for anything a hook deleted: both are in the FS
	// like any other entry and neither reaches the output. A file a hook
	// creates is output, so it is true.
	//
	// The tombstone is not a second flag. isDeleted and setDeleted read
	// and write this one inverted, so the two can never disagree.
	//
	// Unlike Type and MustValidate this is the hook's to change —
	// setRendered, setOutputPath and delete all move it — so it survives
	// the round trip rather than being re-stamped.
	Render bool `json:"render,omitempty"`
}

// ContentType names how a source's bytes encode a document. Anything
// the JS side does not recognize — including the empty string — is
// treated as plaintext, and the parse/stringify helpers fall back to
// JSON rather than failing.
type ContentType string

const (
	ContentPlaintext ContentType = "plaintext"
	ContentJSON      ContentType = "json"
	ContentYAML      ContentType = "yaml"
	// ContentTerraform hands a hook a TFFile rather than a string or a
	// plain object: a tree that prints back byte-identical where it was
	// not edited. Applies to any .tf a kind declares, schema or no —
	// there is no useful plaintext reading of Terraform.
	ContentTerraform ContentType = "terraform"
)

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

// SourceFile is what a hook holds for one bundle entry. Its shape is
// driven by entry.type: a json/yaml entry traffics in parsed objects and
// caches the parse, a plaintext entry stays a string throughout.
//
// The cache is the reason this is a class rather than a closure — every
// accessor for one path hands back the same instance, so a hook that
// reads a source several times parses it once, and a write invalidates
// what the next read sees.
class SourceFile {
  constructor(entry, kind, resource) {
    this.entry = entry;
    this.kind = kind;
    this.resource = resource;
    this.cachedObject = undefined;
  }

  get typed() {
    return this.entry.type === 'json' || this.entry.type === 'yaml';
  }

  // getContent returns the raw string for a plaintext entry, and the
  // parsed document for a json/yaml one — parsed on first call, served
  // from cachedObject after that.
  getContent() {
    if (!this.typed) return this.entry.content;
    if (this.cachedObject === undefined) {
      this.cachedObject = globalThis.__veilHost.parse(this.entry.content, this.entry.type);
    }
    return this.cachedObject;
  }

  // setContent serializes value in the entry's own encoding, validates
  // the result when the kind declared a schema for this source, and only
  // then stores it. A rejected write leaves the entry untouched.
  //
  // This is the only place an entry's content is ever assigned. A raw
  // string is not a way around it: for a typed source the string is
  // parsed first, so it is checked against the same schema an object
  // would be, and the parse populates the cache so the next read agrees
  // with what was just written.
  setContent(value) {
    if (!this.typed) {
      this.entry.content = String(value);
      return;
    }
    var object = value;
    if (typeof value === 'string') {
      object = globalThis.__veilHost.parse(value, this.entry.type);
    }
    var serialized = globalThis.__veilHost.stringify(object, this.entry.type);
    if (this.entry.mustValidate) {
      globalThis.__veilHost.validateSourceFile(this.kind, this.resource, this.entry.path, serialized);
    }
    this.entry.content = serialized;
    this.cachedObject = object;
  }

  getPath() { return this.entry.path; }

  // Routing a file somewhere is a statement that it should be written,
  // so it renders from here on — otherwise a layout hook would silently
  // move an asset to a destination nothing ever writes.
  setOutputPath(p) {
    this.entry.path = String(p);
    this.setRendered(true);
  }

  isRendered() { return !!this.entry.render; }
  setRendered(v) { this.entry.render = !!v; }

  // The tombstone is this same flag read the other way round. Deleting a
  // file and declining to render one are the same outcome — the entry
  // stays in the FS, and nothing writes it — so they are one piece of
  // state rather than two that could contradict each other.
  isDeleted() { return !this.entry.render; }
  setDeleted(v) { this.entry.render = !v; }
}

// __veilMakeFS wraps a raw bundle in the FS a hook receives. identity is
// {kind, resource} — the resource being rendered, which a SourceFile
// passes back to the host when it validates a write.
function __veilMakeFS(initial, identity) {
  identity = identity || {};
  var entries = {};
  var files = {};
  for (var k in initial) {
    if (!Object.prototype.hasOwnProperty.call(initial, k)) continue;
    var v = initial[k];
    if (typeof v === 'string') {
      entries[k] = { path: k, content: v, type: 'plaintext', mustValidate: false, render: true };
    } else {
      entries[k] = {
        path: typeof v.path === 'string' ? v.path : k,
        content: typeof v.content === 'string' ? v.content : '',
        type: typeof v.type === 'string' ? v.type : 'plaintext',
        mustValidate: !!v.mustValidate,
        render: !!v.render
      };
    }
  }

  // One SourceFile per key, so the parse cache survives across every way
  // of reaching the same entry.
  function fileFor(key) {
    if (!Object.prototype.hasOwnProperty.call(files, key)) {
      files[key] = new SourceFile(entries[key], identity.kind, identity.resource);
    }
    return files[key];
  }

  var fs = {
    get: function(path) {
      if (!Object.prototype.hasOwnProperty.call(entries, path)) return undefined;
      return fileFor(path);
    },
    add: function(path, content) {
      if (typeof path !== 'string' || !path) throw new Error('fs.add: path must be a non-empty string');
      if (Object.prototype.hasOwnProperty.call(entries, path)) {
        throw new Error('fs.add: path ' + JSON.stringify(path) + ' already exists');
      }
      // Producing output is the only reason to add a file.
      entries[path] = { path: path, content: '', type: 'plaintext', mustValidate: false, render: true };
      // Through setContent, not by assigning content here, so every write
      // in the runtime goes down one path.
      var file = fileFor(path);
      file.setContent(content == null ? '' : content);
      return file;
    },
    delete: function(path) {
      if (Object.prototype.hasOwnProperty.call(entries, path)) fileFor(path).setDeleted(true);
    },
    keys: function() { return Object.keys(entries); },
    getAll: function() {
      var out = [];
      for (var k in entries) {
        if (Object.prototype.hasOwnProperty.call(entries, k)) out.push(fileFor(k));
      }
      return out;
    },
    // Only what a hook is allowed to change crosses back: path, content
    // and whether the file renders — which carries the tombstone too,
    // being the same flag. type and mustValidate are host state: the
    // runner re-stamps them from the bundle it sent in, so emitting them
    // here would just be something to tamper with. render is always
    // emitted, never omitted when false, since turning a file off is
    // exactly the change that has to survive the trip.
    toJSON: function() {
      var out = {};
      for (var k in entries) {
        if (!Object.prototype.hasOwnProperty.call(entries, k)) continue;
        var e = entries[k];
        out[k] = { path: e.path, content: e.content, render: !!e.render };
      }
      return out;
    }
  };

  // Generated per-source accessors (getSourcesAppJson()) hand back the
  // same SourceFile as fs.get, so the two views share one parse cache.
  var ks = Object.keys(entries);
  for (var i = 0; i < ks.length; i++) {
    var key = ks[i];
    var suffix = __veilMethodSuffix(key);
    if (!suffix) continue;
    fs['get' + suffix] = (function(k) {
      return function() { return fileFor(k); };
    })(key);
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
  // A dependent hook gets its own kind's files as ctx.selfFS, so it can
  // hand one to the consumer it is wiring up. Nothing is read back from
  // it — only the consumer's FS returns — so this is a reading surface
  // that happens to share the FS shape, typed accessors included.
  if (__ctx.selfFiles) {
    __ctx.selfFS = __veilMakeFS(__ctx.selfFiles, __ctx.selfIdentity || {});
    delete __ctx.selfFiles;
    delete __ctx.selfIdentity;
  }
  const __fs = __veilMakeFS(`
	// splices in the resource identity, __veilMakeFS's 2nd arg — see its
	// doc comment above.
	renderHookScriptMiddle2 = `, `
	renderHookScriptSuffix  = `);
  let __res = __veilMod.default.render(__ctx, __fs);
  if (__res && typeof __res.then === 'function') __res = await __res;
  const __final = __res == null ? __fs : __res;
  return JSON.stringify({ fs: __final, logs: __veilLogs });
})()`
)

// Validate-hook script fragments. Same ctx + fs shape as render, but the
// invoked function is `validate(ctx, fs)` and the return value is
// normalized into a `ValidationIssue[]`. Bundle/ctx mutations are
// ignored — the runner discards everything except the issues array.
//
// Result normalization handles the three accepted return shapes
// (string, ValidationIssue, array of either) plus undefined/null. A
// throw bubbles through QuickJS as a normal error; the Go-side caller
// is responsible for catching and aggregating rather than letting it
// abort the validate loop.
const (
	validateHookScriptPrefix = `await (async () => {
  __veilLogs.length = 0;
  if (!__veilMod || !__veilMod.default || typeof __veilMod.default.validate !== 'function') {
    return JSON.stringify({ issues: [], logs: __veilLogs });
  }
  const __ctx = `
	validateHookScriptMiddle = `;
  __ctx.std = globalThis.__veilHost.std;
  __ctx.os = globalThis.__veilHost.os;
  __ctx.fetch = globalThis.__veilHost.fetch;
  __ctx.env = globalThis.__veilHost.env;
  const __fs = __veilMakeFS(`
	validateHookScriptMiddle2 = `, `
	validateHookScriptSuffix  = `);
  let __res = __veilMod.default.validate(__ctx, __fs);
  if (__res && typeof __res.then === 'function') __res = await __res;
  function __veilNormIssue(x) {
    if (x == null) return null;
    if (typeof x === 'string') return { message: x };
    if (typeof x === 'object') {
      var msg = x.message == null ? '' : String(x.message);
      var out = { message: msg };
      if (x.path != null) out.path = String(x.path);
      if (x.severity != null) out.severity = String(x.severity);
      return out;
    }
    return { message: String(x) };
  }
  var __issues = [];
  if (Array.isArray(__res)) {
    for (var i = 0; i < __res.length; i++) {
      var n = __veilNormIssue(__res[i]);
      if (n) __issues.push(n);
    }
  } else {
    var n = __veilNormIssue(__res);
    if (n) __issues.push(n);
  }
  return JSON.stringify({ issues: __issues, logs: __veilLogs });
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
	cwd         string
	// kind and resource name the resource being rendered. Passed to
	// validateSource so the host can find the source being written.
	kind     string
	resource string
	// validateSource is the schema check setContent calls; see
	// WithSourceValidator.
	validateSource func(kind, resource, path, contents string) error
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

// WithCwd sets the directory the hook's built-in `std`/`os` modules see
// as the filesystem root (QuickJS mounts it at "/"), so a hook reading
// `packages/foo` resolves it under dir. This is passed straight to the
// QuickJS runtime, so it never touches the host process's working
// directory — multiple hooks can run concurrently with different (or the
// same) roots. Defaults to the process working directory when unset.
func WithCwd(dir string) Option { return func(o *options) { o.cwd = dir } }

// WithResource names the resource being rendered. The identity is
// handed to the FS so a SourceFile can tell validateSource which
// resource's source it is writing.
func WithResource(kind, name string) Option {
	return func(o *options) { o.kind, o.resource = kind, name }
}

// WithSourceValidator supplies the schema check setContent runs before
// it stores a value, throwing at the exact call site rather than
// surfacing as a Go error after the fact. It receives the resource's
// kind and name, the source path, and the already-serialized contents —
// enough for the host to find the source and check it, with no schema
// knowledge on this side of the boundary. Only entries whose File has
// MustValidate set reach it.
//
// A plain closure so this package stays decoupled from any JSON-Schema
// library; pkg/render supplies one backed by the resource catalog.
func WithSourceValidator(fn func(kind, resource, path, contents string) error) Option {
	return func(o *options) { o.validateSource = fn }
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

	// ValidateHook runs the hook's `validate(ctx, fs)` function and
	// returns the normalized list of issues the hook reported. If the
	// JS module has no `validate` defined, returns an empty slice.
	// Any mutations the hook makes to ctx or fs are discarded — the
	// only observable output is the issues slice.
	ValidateHook(ctx any, bundle Bundle) ([]ValidationIssue, error)

	// Close releases the underlying runtime resources. Safe to call more
	// than once.
	Close() error
}

// ValidationIssue is one entry in a validate hook's report. Mirrors the
// TypeScript `ValidationIssue` interface generated into veil-types.ts.
type ValidationIssue struct {
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Severity string `json:"severity,omitempty"`
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

	rt, err := qjs.New(qjs.Option{CWD: cfg.cwd, MemoryLimit: cfg.memoryLimit})
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

	// Bind the Go-side host functions (fetch + yaml codec) as
	// temporary globals (__veilFetch, __veilYamlParse,
	// __veilYamlStringify). The host-namespace lockdown step closes
	// over them and deletes the raw bindings.
	if err := installHostFuncs(rt, cfg); err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing host bindings: %w", err)
	}

	// Build the read-only std/os proxies, wrap fetch as a Promise-returning
	// polyfill, bundle them into globalThis.__veilHost, and delete the raw
	// std/os/__veilFetch/__veilYaml* globals. After this, the only surface
	// hook code can reach is what we splice into ctx.std/ctx.os/ctx.fetch
	// per call.
	// The tfwrite classes install before the host namespace, which
	// closes over them and deletes the raw __veilTf* bindings along with
	// the rest.
	tfVal, err := rt.Eval("veil-terraform.js", qjs.Code(terraformClassesJS))
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("installing terraform classes: %w", err)
	}
	tfVal.Free()

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

	// Precompute once; default to {} not null since __veilMakeFS does
	// hasOwnProperty.call(schemaByPath, key), which throws on null.
	identityJSON, err := json.Marshal(struct {
		Kind     string `json:"kind"`
		Resource string `json:"resource"`
	}{cfg.kind, cfg.resource})
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("encoding typed sources: %w", err)
	}

	return &jsHook{rt: rt, cfg: cfg, sourcemap: smap, identityJSON: identityJSON}, nil
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

// installHostFuncs binds every Go-side host function the runtime needs
// (fetch + the YAML codec helpers) onto the QuickJS global as a single
// step. Bundling them avoids a subtle interaction where freeing the
// global handle between separate install passes left subsequent
// SetPropertyStr calls operating on a stale view of the object —
// surfacing later as "TypeError: not an object" when the host-namespace
// IIFE ran.
func installHostFuncs(rt *qjs.Runtime, cfg options) error {
	fetchFn, err := newFetchFunc(rt, cfg)
	if err != nil {
		return fmt.Errorf("wrapping fetch: %w", err)
	}
	parseFn, err := qjs.FuncToJS(rt.Context(), func(s string) (string, error) {
		var v any
		if err := yaml.Unmarshal([]byte(s), &v); err != nil {
			return "", fmt.Errorf("yaml.parse: %w", err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("yaml.parse: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping yaml.parse: %w", err)
	}
	stringifyFn, err := qjs.FuncToJS(rt.Context(), func(jsonStr string) (string, error) {
		var v any
		if err := json.Unmarshal([]byte(jsonStr), &v); err != nil {
			return "", fmt.Errorf("yaml.stringify: %w", err)
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(v); err != nil {
			return "", fmt.Errorf("yaml.stringify: %w", err)
		}
		if err := enc.Close(); err != nil {
			return "", fmt.Errorf("yaml.stringify: %w", err)
		}
		return buf.String(), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping yaml.stringify: %w", err)
	}
	tfParseFn, err := qjs.FuncToJS(rt.Context(), func(s string) (string, error) {
		out, err := codec.HCLToJSON([]byte(s), "hook.tf")
		if err != nil {
			return "", fmt.Errorf("terraform.parse: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping terraform.parse: %w", err)
	}
	tfStringifyFn, err := qjs.FuncToJS(rt.Context(), func(jsonStr string) (string, error) {
		out, err := codec.JSONToHCL([]byte(jsonStr))
		if err != nil {
			return "", fmt.Errorf("terraform.stringify: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping terraform.stringify: %w", err)
	}
	// The tree crosses as data, not as a handle the hook calls back
	// into: parse hands back JSON, print takes the source plus whatever
	// tree came back. The host keeps no per-document state, so there is
	// nothing to leak between hooks and nothing to invalidate.
	tfTreeFn, err := qjs.FuncToJS(rt.Context(), func(src string) (string, error) {
		f, err := tfwrite.Parse([]byte(src), "hook.tf")
		if err != nil {
			return "", fmt.Errorf("terraform.parse: %w", err)
		}
		out, err := f.MarshalTree()
		if err != nil {
			return "", fmt.Errorf("terraform.parse: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping terraform tree parse: %w", err)
	}
	tfPrintFn, err := qjs.FuncToJS(rt.Context(), func(src, tree string) (string, error) {
		f, err := tfwrite.UnmarshalTree([]byte(src), []byte(tree))
		if err != nil {
			return "", fmt.Errorf("terraform.stringify: %w", err)
		}
		// Render rather than String: printing is where a hook that wrote
		// a malformed expression finds out, instead of the broken file
		// reaching the output directory and surfacing at terraform plan.
		out, err := f.Render()
		if err != nil {
			return "", fmt.Errorf("terraform.stringify: %w", err)
		}
		return string(out), nil
	})
	if err != nil {
		return fmt.Errorf("wrapping terraform tree print: %w", err)
	}
	tfExprFn, err := qjs.FuncToJS(rt.Context(), func(jsonValue string) (string, error) {
		var v any
		if err := json.Unmarshal([]byte(jsonValue), &v); err != nil {
			return "", fmt.Errorf("setAttribute: %w", err)
		}
		out, err := codec.HCLExpr(v)
		if err != nil {
			return "", fmt.Errorf("setAttribute: %w", err)
		}
		return out, nil
	})
	if err != nil {
		return fmt.Errorf("wrapping terraform value encoder: %w", err)
	}
	validateFn, err := qjs.FuncToJS(rt.Context(), func(kind, resource, path, contents string) (string, error) {
		if cfg.validateSource == nil {
			return "", nil
		}
		if err := cfg.validateSource(kind, resource, path, contents); err != nil {
			return "", err
		}
		return "", nil
	})
	if err != nil {
		return fmt.Errorf("wrapping source validate: %w", err)
	}

	global := rt.Context().Global()
	defer global.Free()
	global.SetPropertyStr("__veilFetch", fetchFn)
	global.SetPropertyStr("__veilYamlParse", parseFn)
	global.SetPropertyStr("__veilYamlStringify", stringifyFn)
	global.SetPropertyStr("__veilTerraformParse", tfParseFn)
	global.SetPropertyStr("__veilTerraformStringify", tfStringifyFn)
	global.SetPropertyStr("__veilTfTree", tfTreeFn)
	global.SetPropertyStr("__veilTfPrint", tfPrintFn)
	global.SetPropertyStr("__veilTfExpr", tfExprFn)
	global.SetPropertyStr("__veilValidateSource", validateFn)
	return nil
}

// newFetchFunc wraps the HTTP-fetch behavior in a `qjs.FuncToJS` value
// so installHostFuncs can install it alongside the codec helpers. The
// returned function takes an options JSON blob and returns a response
// JSON blob; the JS-side polyfill in hostNamespaceJS rehydrates the
// result into a Web-Fetch-shaped object.
func newFetchFunc(rt *qjs.Runtime, cfg options) (*qjs.Value, error) {
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
		return nil, err
	}
	return fn, nil
}

// hostNamespaceJS is the final init step. It closes over the raw
// std/os/__veilFetch/__veilYaml* globals, exposes only the read-only /
// Promise-shaped proxies on globalThis.__veilHost, and replaces the
// originals so hook
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

  var nativeYamlParse = globalThis.__veilYamlParse;
  var nativeYamlStringify = globalThis.__veilYamlStringify;
  var nativeTF = globalThis.__veilTF;
  var nativeTerraformParse = globalThis.__veilTerraformParse;
  var nativeTerraformStringify = globalThis.__veilTerraformStringify;
  var nativeValidateSource = globalThis.__veilValidateSource;

  var yamlCodec = {
    parse: function(s) {
      if (s == null) throw new Error('std.yaml.parse: input is required');
      return JSON.parse(nativeYamlParse(String(s)));
    },
    stringify: function(value) {
      return nativeYamlStringify(JSON.stringify(value == null ? null : value));
    }
  };

  // Terraform is a configuration language rather than a data format, so
  // the object here is the one Terraform itself defines for .tf.json:
  // the same configuration, spelled as data. Expressions survive as
  // "${...}" strings and are written back unquoted; comments do not
  // survive, having nowhere to live in between.
  var terraformCodec = {
    // parse hands back a TFFile: the same shape tfwrite works with in
    // Go, method for method, over a tree that keeps every node's source
    // span. Editing one and printing it back reprints only what changed.
    parse: function(s) {
      if (s == null) throw new Error('std.terraform.parse: input is required');
      return nativeTF.parse(s);
    },
    // stringify takes either a TFFile or a plain object in Terraform's
    // .tf.json shape. The first prints from the original bytes; the
    // second has none to print from and is generated fresh.
    stringify: function(value) {
      if (value == null) throw new Error('std.terraform.stringify: input is required');
      if (nativeTF.isFile(value)) return value.toString();
      return nativeTerraformStringify(JSON.stringify(value));
    },
    // toObject is the old parse, kept for reading a file as plain data
    // in Terraform's .tf.json shape when a tree is more than the job
    // needs.
    toObject: function(s) {
      if (s == null) throw new Error('std.terraform.toObject: input is required');
      return JSON.parse(nativeTerraformParse(String(s)));
    }
  };

  var stdProxy = {
    loadFile:  function(path) { return nativeStd.loadFile(path); },
    getenv:    function(name) { return nativeStd.getenv(name); },
    yaml:      yamlCodec,
    terraform: terraformCodec
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

  // parse / stringify are the codec pair every SourceFile goes through.
  // Only YAML needs the host: JSON is handled here, because a round trip
  // through the Wasm boundary costs more than QuickJS's own JSON does.
  // An unset or unrecognized type falls back to JSON rather than
  // failing — plaintext entries never reach these.
  function parseByType(text, type) {
    if (type === 'yaml') return JSON.parse(nativeYamlParse(String(text)));
    return JSON.parse(String(text));
  }
  function stringifyByType(value, type) {
    if (type === 'yaml') return nativeYamlStringify(JSON.stringify(value == null ? null : value));
    var out = JSON.stringify(value);
    if (out === undefined) throw new TypeError('cannot serialize value of type ' + typeof value);
    return out;
  }

  globalThis.__veilHost = {
    std: stdProxy,
    os: osProxy,
    fetch: fetchPolyfill,
    parse: parseByType,
    stringify: stringifyByType,
    validateSourceFile: nativeValidateSource
  };

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
  delete globalThis.__veilYamlParse;
  delete globalThis.__veilYamlStringify;
  delete globalThis.__veilTerraformParse;
  delete globalThis.__veilTerraformStringify;
  delete globalThis.__veilTfTree;
  delete globalThis.__veilTfPrint;
  delete globalThis.__veilTfExpr;
  delete globalThis.__veilTF;
  delete globalThis.__veilValidateSource;
})();
`

type jsHook struct {
	rt        *qjs.Runtime
	cfg       options
	sourcemap *sourcemap.Consumer
	// identityJSON is the {kind, resource} pair spliced into every call
	// script. Fixed for the life of the hook, unlike ctx and the bundle.
	identityJSON []byte
	closed       bool
	stuck        bool // set after a timeout; rt must not be touched again
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

type validateHookResult struct {
	Issues []ValidationIssue `json:"issues,omitempty"`
	Logs   []logEntry        `json:"logs,omitempty"`
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
	b.Grow(len(renderHookScriptPrefix) + len(ctxJSON) + len(renderHookScriptMiddle) + len(bundleJSON) + len(renderHookScriptMiddle2) + len(h.identityJSON) + len(renderHookScriptSuffix))
	b.WriteString(renderHookScriptPrefix)
	b.Write(ctxJSON)
	b.WriteString(renderHookScriptMiddle)
	b.Write(bundleJSON)
	b.WriteString(renderHookScriptMiddle2)
	b.Write(h.identityJSON)
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
	return restoreEncoding(bundle, result.FS), nil
}

// restoreEncoding re-stamps Type, MustValidate and Render from the
// bundle that went in. They describe how a file was declared, which is
// fixed for the whole render: whatever a hook does to its own copy stays
// in that hook, rather than carrying into every hook after it — a hook
// cannot promote an asset to output, or demote a source away from it.
// Entries a hook added have no declared file behind them: they are
// untyped, and they render, since producing output is the only reason to
// add one.
func restoreEncoding(in, out Bundle) Bundle {
	for key, file := range out {
		original, existed := in[key]
		if !existed {
			file.Type, file.MustValidate = "", false
		} else {
			file.Type, file.MustValidate = original.Type, original.MustValidate
		}
		out[key] = file
	}
	return out
}

// ValidateHook runs the compiled module's `validate(ctx, fs)` and
// returns the normalized issue list. Mirrors the render path for
// timeout / stuck-runtime handling so a runaway validate hook gets
// the same wall-clock cutoff. Any FS or ctx mutations the hook
// performs only live inside the QuickJS sandbox — the runner reads
// nothing back except the issues array, so reverting state to a
// snapshot isn't necessary.
func (h *jsHook) ValidateHook(ctx any, bundle Bundle) ([]ValidationIssue, error) {
	if h.closed {
		return nil, errors.New("hook: ValidateHook called after Close")
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
	b.Grow(len(validateHookScriptPrefix) + len(ctxJSON) + len(validateHookScriptMiddle) + len(bundleJSON) + len(validateHookScriptMiddle2) + len(h.identityJSON) + len(validateHookScriptSuffix))
	b.WriteString(validateHookScriptPrefix)
	b.Write(ctxJSON)
	b.WriteString(validateHookScriptMiddle)
	b.Write(bundleJSON)
	b.WriteString(validateHookScriptMiddle2)
	b.Write(h.identityJSON)
	b.WriteString(validateHookScriptSuffix)
	script := b.String()

	type outcome struct {
		raw string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		val, err := h.rt.Eval("validate.js", qjs.Code(script), qjs.FlagAsync())
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
		h.stuck = true
		return nil, fmt.Errorf("hook exceeded %s timeout", h.cfg.timeout)
	}

	if res.err != nil {
		return nil, fmt.Errorf("invoking validate: %w", rewriteErr(res.err, h.sourcemap))
	}

	var result validateHookResult
	if err := json.Unmarshal([]byte(res.raw), &result); err != nil {
		return nil, fmt.Errorf("parsing validate result: %w (raw: %s)", err, res.raw)
	}

	h.emitLogs(result.Logs)
	return result.Issues, nil
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
