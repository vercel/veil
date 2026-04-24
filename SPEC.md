# Veil

Veil is a CLI tool written in Go for tracking, transforming, and rendering arbitrary deployment configuration
(Kubernetes, Envoy, Terraform, Ansible, etc.). Some foundational tenets:

1. It is not a template renderer
2. It is local-first
3. All abstractions should have an escape hatch to the underlying configuration
4. Hooks > embedded logic

# Influences

Veil borrows design ideas from tools that already solved related problems well:

- **[shadcn](https://ui.shadcn.com/)** — the `veil build` command and registry architecture are inspired by
  `shadcn build`: a local source tree is compiled into a static, self-describing registry that downstream
  consumers fetch by URL. `public/r/` is the default output path for the same reason.
- **[Kubernetes](https://kubernetes.io/)** — the resource model (a `metadata`/`spec` envelope where
  `metadata.kind` names the schema that validates `spec`) is lifted directly from Kubernetes. Resources are
  declarative JSON documents that reference a kind, and kinds are the unit of extension. The broader shape
  also follows K8s: resources are authored and owned by individual teams, scattered across the repo
  alongside the services they describe, while a central engine (`veil`) discovers, validates, and
  coordinates them at render time.
- **[Terraform](https://developer.hashicorp.com/terraform)** — the hook plugin system takes its cue from
  Terraform providers: third-party code runs inside a sandboxed, out-of-process runtime (QuickJS/wasm in
  our case, gRPC-over-stdio in Terraform's) with a narrow, host-controlled API. Hooks are the extension
  point for everything veil itself doesn't know how to do.
- **[Rails](https://rubyonrails.org/)** — the `veil new` generators (`veil new kind`, `veil new hook`, …)
  are modeled on `rails generate`: a single command scaffolds a complete, conventionally-placed set of
  files (source, schema, hook, registry entry) so getting started is a one-liner instead of a setup
  checklist.

# Why not templates

Templates are often implemented via company-specific IDL (JSON, YAML, TOML, etc.) that acts as the data which is then
applied to a template file.

1. Tight coupling of the "state" (the contents of the file) and the "logic" (template logic). This makes testing
   templates significantly harder.
2. They're significantly less readable when there are tons of {{if ...}} {{end}} blocks everywhere (in the simplest
   case).
3. IDLs force you to abstract the entire resource meaning it's very brittle and all underlying fields must be exposed in
   the above IDL.
4. IDLs are not well understood by agents. Best case scenario you have a well-documented, source of truth JSON schema
   that all instantiations reference via the `$schema` field but this is almost never the case.

# Core concepts

## Discovery

Veil recursively searches upward from the current working directory to find a `.veil/` folder. Inside it,
`veil.json` declares the project configuration:

```json
{
  "resources": [
    "./resources/service.json",
    "./resources/cron.json"
  ]
}
```

The `resources` field is a list of relative or absolute paths to resource definition files. Relative paths are
resolved from the `.veil/` directory.

## Variables

`veil.json` can declare named input variables — scalars that are substituted into overlay CEL expressions at
render time:

```json
{
  "kinds": ["./kinds/service/kind.json"],
  "variables": {
    "env":      { "type": "string", "enum": ["dev", "staging", "prod"], "default": "dev", "description": "Target deployment environment." },
    "region":   { "type": "string", "description": "Cloud region for this render (e.g. iad1)." },
    "replicas": { "type": "number", "enum": [1, 3, 5], "default": 3 },
    "debug":    { "type": "bool",   "default": false }
  }
}
```

Each variable has a `type` of `string`, `number`, or `bool`, an optional `default`, an optional
`description`, and an optional `enum` (allowed only for `string` and `number` — a bool is already its own
two-value enum). A variable with no default is **required** — `veil render` fails if it is not provided.

- **Descriptions** render as JSDoc comments on the corresponding field of `RegistryVariables` in
  `veil-types.ts`.
- **Enums** narrow the generated TS type: `{"type":"string","enum":["dev","staging","prod"]}` becomes
  `env: "dev" | "staging" | "prod";` instead of `env: string;`. Values supplied via `--var` or
  `VEIL_VAR_*` are rejected at render time if they're not in the enum. A declared `default` must also be
  in the enum — otherwise the config fails to load.

Values are resolved in precedence order (highest first):

1. `--var name=value` (repeatable) passed to `veil render`. `--variable` is an accepted alias.
2. `VEIL_VAR_<NAME>` environment variable (name uppercased).
3. The declared `default`.

String values from `--var` or env are coerced to the declared type — `"3"` → `3` for `number`, `"true"` →
`true` for `bool`. Coercion failures and missing required variables produce an error identifying the variable
and both ways to supply it.

Inside overlay CEL expressions, resolved values are accessible under the `vars` namespace (named
`vars` rather than `var` because `var` is a CEL reserved word):

```cel
vars.env == 'staging'
vars.replicas > 1 && vars.debug
```

## Registry

The set of resource definitions declared in `veil.json` forms the **registry**. Each resource definition
describes a type of deployable unit (e.g. `service`, `cron`, `consumer`, `bucket`, `sql`).

## Resource definition

A resource definition is a JSON file in `.veil/resources/` with the following fields:

```json
{
  "name": "service",
  "sources": [
    "./sources/service/deployment.yaml",
    "./sources/service/hpa.yaml"
  ],
  "hooks": {
    "render": [
      "./hooks/inject-env-var.ts"
    ]
  },
  "schema": "./schemas/service.schema.json"
}
```

All types are defined as protobuf messages (`proto/veil/v1/`) with `buf.validate` constraints and generated as
both Go code and JSON Schemas. The JSON schemas are embedded in the CLI binary via `//go:embed`.

### `sources`

A set of source configuration files that make up the resource. These are the raw config files — Kubernetes manifests,
Terraform HCL, Envoy configs, etc. They are the starting point that hooks operate on.

Sources are format-agnostic. Veil does not parse or understand their contents; it treats them as opaque files that
are passed through the hook pipeline and ultimately rendered to disk.

### `hooks`

An object grouping hook code files (TS/JS) by lifecycle point. Each lifecycle key holds an ordered
list of hook files, and each file exports an interface specific to that lifecycle. Today only
`render` exists; new lifecycle points will be added as additional fields so kind.json can grow
without breaking changes.

Cross-resource hooks — where a target kind injects into a consumer's render output — live separately
under the kind's `dependents` declaration rather than as another `hooks` lifecycle, because they need
to be scoped per consumer kind. See [Dependencies](#dependencies).

```json
"hooks": {
  "render": ["./hooks/inject-env-var.ts", "./hooks/annotate.ts"]
}
```

Files under `hooks.render` export a `RenderHook`:

```ts
export interface File {
  getContent(): string;
  setContent(content: string): void;
  getPath(): string;
  setOutputPath(path: string): void;
}

export interface FS {
  // Generated per declared source — strongly typed handles:
  getSourcesDeploymentYaml(): File;
  // ...

  // Escape hatches for dynamic files:
  get(key: string): File | undefined;
  getAll(): File[];
  add(path: string, content: string): File;
  delete(key: string): void;
  keys(): string[];
}

export interface RenderHook {
  render(ctx: RenderHookContext, fs: FS): FS | void | Promise<FS | void>;
}
```

- **RenderHookContext**: contains `resource` (metadata + merged spec), `vars`, `root`, and the host
  APIs `std` / `os` / `fetch`.
- **FS**: holds the file state for this render. Each declared source gets a typed, generated accessor so
  renames in `kind.json` surface as TypeScript errors at build time. `./sources/deployment.yaml` produces
  `fs.getSourcesDeploymentYaml(): File`.
- **File**: a handle to one entry. `getContent`/`setContent` read/write contents; `getPath`/`setOutputPath`
  read/rewrite the destination path on disk. **Identity is stable** — calling `setOutputPath` changes where
  the file *lands* without changing the FS key, so downstream hooks still reach it via the same typed getter.

The generated accessor algorithm: strip the leading `./`, split on `/`, `.`, `-`, `_`, and space, then
PascalCase-concatenate and prefix with `get`. `./sources/my-file.json` → `getSourcesMyFileJson`.

Render hooks are invoked in declaration order. A hook may mutate the passed-in FS and return it,
return a replacement FS, return nothing (implicit passthrough), or `throw` to abort. The pipeline is
`(RenderHookContext, FS) → (FS, Error?)`.

At final write time, veil walks the bundle and writes each entry's contents to `<outDir>/<instance>/<file.path>`.
If two entries resolve to the same destination path, that's a hard error.

### `schema`

A JSON Schema file that defines the shape of the `spec` field for this resource. This is the **source** schema —
it only describes the resource-specific data (e.g. `port`, `replicas`, `image`).

`veil resource gen` takes this source schema and produces:

1. A **generated JSON Schema** that wraps the spec schema with the standard `metadata` envelope (the
   `Resource` structure from the protobuf definitions). Consumers reference this via `$schema` in their
   resource files for validation and autocompletion.
2. A **`veil-types.ts`** file containing TypeScript interfaces generated from each resource's spec schema.
   Hooks can import these types for type-safe access to the resource data.

Output is written to `.veil/resource-schemas/` by default (configurable via `--out`).

### `dependents`

Optional. Declares which consumer kinds may depend on this kind, the per-consumer parameter shapes,
and the hooks that run when that consumer renders. See [Dependencies](#dependencies) for the full
design.

```json
"dependents": [
  {
    "kind": "service",
    "hooks": ["./dependents/service/inject-env.ts"],
    "params_path": "./dependents/service/params.json"
  }
]
```

Each entry binds a consumer kind to (a) one or more hook files and (b) a JSON Schema for the `params`
object the consumer must supply. Paths are resolved relative to the `kind.json` file. Listing a
consumer kind with an empty `hooks` array is a build error.

## Resource

A consumer creates a JSON file in their service directory that instantiates a kind — that file **is** the
resource. Every resource has the top-level fields `metadata` and `spec`, and may optionally include a
peer `dependencies` array.

### `metadata`

Common to all resources. Contains:

- **`kind`** (required): The name of the kind this resource instantiates. Must match a kind present in one
  of the loaded registries.
- **`name`** (required): The name of this resource.
- **`overlays`**: A list of conditional overlays. Each overlay has a [CEL](https://github.com/google/cel-spec)
  expression and a path to an overlay file. The path is resolved relative to the resource file declaring
  the overlay (or used as-is when absolute). When the CEL expression evaluates to `true` for the current
  render context, the overlay file is loaded and its contents are merged into the resource's spec before
  hooks run.
- **`overrides`**: A list of ejected source files. Each entry is an object with `source` (the path from the
  resource definition's `sources`) and `path` (the local path to the override file). By default, `veil eject`
  places the file at `.veil/<resource>/overrides/<filename>`, but the user can specify a custom path.

### `spec`

The consumer's custom data. This is validated against the JSON Schema defined by the resource definition's
`schema` field. The schema from the resource definition becomes the schema for the `spec` field in the resource
instance's JSON Schema.

### `dependencies`

Optional list of declared dependencies on other resources. Each entry has `kind`, `name`, and `params`;
`params` is typed against the schema declared by the target kind's matching `dependents` entry. See
[Dependencies](#dependencies) for the full design.

### Example

```json
{
  "$schema": "../.veil/resource-schemas/service.schema.json",
  "metadata": {
    "kind": "service",
    "name": "api-feature-flags",
    "overlays": [
      {
        "match": "vars.env == 'staging'",
        "file": "./staging.json"
      },
      {
        "match": "vars.env == 'production' && vars.region == 'iad1'",
        "file": "./iad1.json"
      }
    ]
  },
  "spec": {
    "port": 2000,
    "replicas": 3,
    "image": "my-service:latest",
    "env": {
      "LOG_LEVEL": "info"
    }
  }
}
```

An overlay file (e.g. `staging.json`) contains a partial `spec` that is merged into the base instance:

```json
{
  "spec": {
    "replicas": 1,
    "env": {
      "LOG_LEVEL": "debug"
    }
  }
}
```

## Override

Overrides are an escape hatch for cases where a consumer needs deep control of a source file. Rather than trying
to express everything through hooks, a consumer can **eject** a source file — getting a local copy that
completely replaces the hooked output for that file during render.

Overrides are tracked in `metadata.overrides`:

```json
{
  "metadata": {
    "name": "api-feature-flags",
    "overrides": [
      {
        "source": "./sources/service/deployment.yaml",
        "path": "./.veil/service/overrides/deployment.yaml"
      }
    ]
  }
}
```

`source` identifies which file in the resource definition is being replaced. `path` points to the local copy.
During render, veil seeds the initial FS with the contents of the file at `path` instead of the resource
definition's source — the override file becomes the **starting point** that the hook pipeline operates on.
Render hooks and dependent hooks still run normally; an override just shifts the baseline they mutate.

# Dependencies

Resources can declare **dependencies** on other resources. The depended-on resource (the *target*)
authoritatively controls how consumers talk to it: when a consumer declares a dependency on a target,
target-owned hooks run after the consumer's render and inject whatever the consumer needs to reach the
target — env vars, mounts, role policies, etc.

This inverts the usual integration pattern. Instead of every service hand-rolling the wiring for a
bucket, the bucket kind ships the wiring code once and every consumer just declares the dependency
with a typed parameter object. Because the target owns the hook, breaking changes to how it's
consumed surface at the target's repo, not in N consumer repos.

## Consumer side: `dependencies`

A resource declares its dependencies as a top-level field, peer to `metadata` and `spec`:

```json
{
  "$schema": "../../../public/r/service/kind.schema.json",
  "metadata": {
    "kind": "service",
    "name": "api-feature-flags"
  },
  "dependencies": [
    {
      "kind": "bucket",
      "name": "eddie",
      "params": { "action": "read" }
    }
  ],
  "spec": { ... }
}
```

Each entry has:

- **`kind`** — the kind being depended on.
- **`name`** — the name of the target resource. The pair `(kind, name)` must resolve to a resource in
  the catalog at render time.
- **`params`** — an arbitrary object whose shape is dictated by the target kind. The generated
  `kind.schema.json` validates `params` against the target's declared schema (a discriminated union
  keyed on `kind`), and `veil-types.ts` exposes typed `params` shapes per `(target, consumer)` pair.

## Target side: `dependents`

A kind that wants to be dependable declares the consumer kinds it accepts in its `kind.json`:

```json
{
  "name": "bucket",
  "sources": [...],
  "schema": "./schema.json",
  "hooks": { "render": [...] },
  "dependents": [
    {
      "kind": "service",
      "hooks": ["./dependents/service/inject-env.ts"],
      "params_path": "./dependents/service/params.json"
    },
    {
      "kind": "worker",
      "hooks": ["./dependents/worker/inject-stream.ts"],
      "params_path": "./dependents/worker/params.json"
    }
  ]
}
```

Each entry has:

- **`kind`** — the consumer kind that may depend on this resource.
- **`hooks`** — at least one hook file that runs when a resource of `kind` depends on this one. Hooks
  run in declaration order. An empty `hooks` array is a build error.
- **`params_path`** — JSON Schema describing the `params` object the consumer must supply. Paths are
  resolved relative to the `kind.json` file.

Because hooks are registered per consumer kind, each hook receives a concretely typed `consumer` and
`params` — no union narrowing inside the hook body.

## `dependent` hooks

Each dependent hook exports a `DependentHook`:

```ts
export interface DependentHook {
  render(ctx: DependentHookContext, fs: FS): FS | void | Promise<FS | void>;
}

export interface DependentHookContext {
  self: TargetCtx;       // the target kind's resolved resource (e.g. BucketCtx)
  consumer: ConsumerCtx; // the consumer kind's resolved resource (e.g. ServiceCtx)
  params: Params;        // the consumer-supplied params, typed per params_path
  vars: RegistryVariables;
  root: string;
  std: Std; os: Os; fetch: Fetch;
}
```

The `fs` argument is the **consumer's** filesystem after all of the consumer's render hooks have
completed. A dependent hook can only read and mutate the consumer's FS — it has no handle to its
own. This is deliberate: dependent hooks express how to *plug into* the target, not how to construct
the target.

Example — a bucket injects env vars into a service that depends on it:

```ts
import type { DependentHook, DependentHookContext, FS } from './veil-types';
import type { Deployment } from 'kubernetes-types/apps/v1';
import { load, dump } from 'js-yaml';
import { appContainer } from '../../shared/k8s';

const injectBucketEnv: DependentHook = {
  render(ctx: DependentHookContext, fs: FS): FS {
    const { self, params } = ctx;
    const file = fs.getSourcesAppYaml();
    const app = load(file.getContent()) as Deployment;
    const container = appContainer(app);
    container.env ??= [];
    const prefix = self.metadata.name.toUpperCase().replace(/-/g, '_');
    container.env.push({ name: `${prefix}_BUCKET_URL`, value: self.spec.url });
    container.env.push({ name: `${prefix}_BUCKET_ACTION`, value: params.action });
    file.setContent(dump(app));
    return fs;
  },
};

export default injectBucketEnv;
```

## Render-time execution

After a resource's render hooks finish, veil iterates `dependencies` in declaration order. For each
entry:

1. Resolve `(kind, name)` to a target resource in the catalog. Missing targets are a hard error.
2. Find the target kind's `dependents` entry matching the consumer's kind. A target that doesn't list
   the consumer's kind as allowed is a hard error.
3. Run each registered dependent hook against the consumer's FS, in declaration order.

Render hooks cannot observe state injected by dependent hooks — the lifecycles are strictly ordered
(overrides → render → dependents → write). Cycles (A depends on B, B depends on A, …) are detected at
render time and produce a hard error.

## Build-time integration

`veil build` walks all kinds in the registry. For each consumer kind C, it collects every kind T that
lists C in its `dependents` and writes a discriminated union into C's emitted `kind.schema.json`:

```json
"dependencies": {
  "type": "array",
  "items": {
    "oneOf": [
      { "properties": { "kind": {"const": "bucket"}, "params": { /* bucket→service params */ } } },
      { "properties": { "kind": {"const": "queue"},  "params": { /* queue→service params */ } } }
    ]
  }
}
```

The same pass extends the consumer's `veil-types.ts` with concrete `params` shapes per
`(target, consumer)` pair so consumer hooks (and `dependencies[].params` literals in resource files)
are fully typed. Dependent hook files are bundled the same way render hooks are (esbuild → minified
JS embedded in the compiled `kind.json`). The compiled kind carries its full `dependents` table, so
`veil render` needs only the registry plus the catalog of resource files to wire dependencies up.

# Script runtime

Hooks written in TS/JS are executed in a two-stage pipeline:

1. **Bundling** — [esbuild's Go API](https://pkg.go.dev/github.com/evanw/esbuild/pkg/api) (pure Go, no CGO)
   resolves all imports, transpiles TypeScript, and produces a single bundled JavaScript file. The runtime
   accepts an `fs.FS` as the root filesystem for module resolution:
   - **Relative imports** (`./utils`, `../lib`) are resolved against the importer's directory within the FS.
   - **Bare specifiers** (`lodash`, `zod`) are resolved from `node_modules/` within the FS, following Node's
     resolution algorithm (walking up directories, reading `package.json` `module`/`main` fields).
   - **TypeScript types** (e.g. importing from `veil-types.ts`) are erased at bundle time — the import resolves
     so bundling succeeds, but the type-only code is stripped from the output.

2. **Execution** — The bundled JavaScript is executed via [QuickJS-NG](https://github.com/quickjs-ng/quickjs)
   (ES2023) using the [fastschema/qjs](https://github.com/fastschema/qjs) Go module, which runs QuickJS inside
   [Wazero](https://wazero.io/) (pure Go WebAssembly runtime — no CGO).

**Sandboxing**: By design, user code has no filesystem or network access. All I/O flows through the `Context` and
`FS` interfaces provided by the host. Per-hook memory is capped (default 128 MiB, configurable), and each
`RenderHook.render` call has a wall-clock timeout (default 30s, configurable). On timeout, `veil render` logs the
error and returns — the underlying eval goroutine is *abandoned* because qjs/wazero cannot interrupt a
pure-wasm tight loop cleanly; the process exit reclaims it. In practice: the user sees an error and the
CLI exits. `veil build` also runs `tsc --noEmit --strict` on every hook as an upfront defensive check.

### Host APIs exposed to hooks

Every host capability hooks can reach hangs off `RenderHookContext` — `ctx.std`, `ctx.os`, and
`ctx.fetch`. `globalThis.os` and the underlying `__veilFetch` Go binding are deleted before hook
code runs.

Two host APIs are also exposed as globals so hook code reads naturally:

- `globalThis.std` is the same read-only proxy as `ctx.std`. A hook can write
  `std.loadFile("data.txt")` or `std.getenv("REGION")` without unpacking the context. It is *not*
  the full QuickJS-NG `std` module — every other binding (`open`, `popen`, `tmpfile`, `printf`,
  `puts`, write modes, …) has been stripped.
- `globalThis.fetch` is the same Web Fetch polyfill as `ctx.fetch`. Most existing snippets and
  docs assume a global `fetch`, so this matches developer expectations.

#### `ctx.std` and `ctx.os` — read-only host filesystem

Hooks can **read** files, list directories, and stat — they cannot write, create, delete, rename, exec,
or otherwise mutate host state. The `std` and `os` namespaces are minimal read-only proxies built over
QuickJS-NG's modules of the same name; the underlying write/popen/tmpfile/exec bindings are stripped.

```ts
const data = ctx.std.loadFile("config/env.yaml");        // string | null
const region = ctx.std.getenv("AWS_REGION");              // string | undefined
const [names, err] = ctx.os.readdir("manifests");         // [string[], number]
const [stats]      = ctx.os.stat("manifests/svc.yaml");   // [Stats, number]
```

Paths resolve against the project root (the directory housing `veil.json`) — the render pipeline
chdir's into root before invoking hooks, so plain relative strings like `"data.txt"` are what hooks
should use. The full surface is declared as `Std` and `Os` in the generated `veil-types.ts`.

If a hook needs to *produce* a file, it should do so via `fs.add(path, content)` so the file flows
through the rest of the pipeline and lands in the render output — not via direct host I/O.

#### `ctx.fetch` — Web Fetch HTTP client

```ts
const resp = await ctx.fetch(`${base}/status`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(payload),
});
if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
const data = await resp.json();
```

Because hook execution is synchronous under the hood, the Promise returned by `ctx.fetch` resolves
immediately when the request completes; `await` reads naturally.

**Scope vs. the Web standard.** Our polyfill covers what the vast majority of real hook code needs.
Known gaps:

- No `AbortController` / `AbortSignal`. Timeout is configured host-side via `hook.HTTPConfig`.
- `resp.headers` is a duck-typed object (`get`/`has`/`forEach`) rather than a real `Headers`
  instance — no `entries()` / `Symbol.iterator`.
- Body types: strings only; no `FormData`/`Blob`/`ReadableStream`.
- No streaming — only `.text()` and `.json()`.

**Operator controls** (via `hook.HTTPConfig` on the Go side): allowlist of hosts (exact match; empty = all
allowed), default per-request timeout (10s), max response body size (10 MiB). Only `http://` and `https://`
URLs are accepted; other schemes are rejected.

Every successful request is logged via the hook's `slog.Logger` with `method`, `url`, `status`, `duration`,
and `bytes`. Failures log at WARN.

**Non-determinism caveat.** Enabling network access inside hooks means two `veil render` runs can produce
different output from the same inputs. If reproducibility matters, either avoid `ctx.fetch` or cache
responses to a source file that gets declared on the kind.

**`RenderHookContext.root`** is the absolute path of the project root (the directory housing
`veil.json`) — provided for display / logging; `ctx.std` and `ctx.os` already resolve paths against it.

Hooks may be `async` — if `render` returns a Promise, the host awaits it transparently before handing
the result to the next hook. Under the hood the runtime is still single-threaded and synchronous, so
`await` is essentially a syntactic convenience for chaining sync APIs (like the `fetch` polyfill). Errors
flow exclusively through thrown `Error` instances (or Promise rejections) — there is no structured error
return shape; `throw new Error("...")` is canonical.

`console.log` / `.info` / `.warn` / `.error` / `.debug` calls inside a hook are captured and emitted via the
host's `slog` logger, tagged with the kind and hook names so output is traceable across multi-instance
renders. Complex arguments are JSON-stringified.

# Protobuf & schemas

All core types (`VeilConfig`, `Kind`, `Resource`, `Metadata`, `Overlay`, `Override`) are defined
as protobuf messages in `proto/veil/v1/` with `buf.validate` constraints. The `Makefile` runs a generation
pipeline:

1. `buf generate` — produces Go code (`api/go/`) and JSON Schemas (`api/jsonschema/`)
2. `scripts/deref-jsonschema/` — post-processes the JSON Schemas: dereferences all `$ref` pointers, simplifies
   enum representations, cleans filenames (e.g. `veil.v1.ResourceDef.schema.bundle.json` → `ResourceDef.schema.json`)
3. Cleaned schemas are copied to `pkg/embeds/jsonschema/` and embedded in the binary via `//go:embed`

The embedded schemas are available via `veil schema {config,kind,resource,metadata}`.

# CLI commands

## `veil gen`

Runs hooks on a resource and generates the output files. (Not yet implemented.)

## `veil build`

Compiles every kind in the registry into a self-contained JSON document and writes a top-level
`registry.json` that indexes them. Output layout under `--out` (default `<.veil dir>/r/`):

```
<out>/
├── registry.json                      # { "kinds": { "<name>": {
│                                      #     "name": "<name>",
│                                      #     "path": "./<name>/kind.json",
│                                      #     "schema": "./<name>/kind.schema.json"
│                                      # } } }
└── <name>/
    ├── kind.json                      # compiled CompiledKind: name, sources (embedded), hooks
    │                                  # (bundled + minified), variables (declarations)
    └── kind.schema.json               # composite resource schema (metadata + spec envelope)
```

The pipeline runs in two passes. Single-kind: validate the definition, regenerate
`hooks/veil-types.ts`, run `tsc --noEmit --strict` (or `tsgo`) against the hooks dir if a TypeScript
compiler is on PATH, bundle each hook (esbuild, minified — both render hooks and any
`dependents[].hooks`). Cross-kind: for every consumer kind, collect every target kind that lists it
in `dependents` and emit the discriminated `dependencies` schema (and corresponding TS types) into
the consumer's `kind.schema.json` and `veil-types.ts`. After both passes succeed for every kind, the
per-kind `kind.json` files and the top-level `registry.json` are written.

Flags:

- `--config` — path to `veil.json`. Defaults to the nearest `.veil/veil.json` (searched upward from cwd).
- `--out` — output directory. Defaults to `<veil.json dir>/r`.
- `--no-typecheck` — skip the `tsc`/`tsgo` invocation.

## `veil render <path>`

The primary command. Renders deployment configuration. The required `<path>` positional is either a
single resource file or a directory of resources — directories are scanned non-recursively for `*.json`
files matching the Resource shape; a file argument must parse as a Resource or the command errors.

1. Discover resources at `<path>` (JSON files with `metadata.kind`, `metadata.name`, and `spec`)
2. Load compiled kinds from every configured **registry** (see below)
3. Evaluate `metadata.overlays` — for each overlay whose CEL `match` expression is true, merge the overlay's
   `spec` into the base `spec`
4. Validate the merged `spec` against the kind's schema (which also validates `dependencies[].params`
   against the discriminated schema baked in at build time)
5. Load the `sources` (already embedded in the compiled `kind.json`) into an initial `FS`, then apply any
   `metadata.overrides` — each entry replaces the corresponding source's contents in the FS so the hook
   pipeline operates on the override as its starting point
6. Apply `hooks.render` in order (calling each `RenderHook.render`), threading the FS through the pipeline
7. Apply `dependencies` — for each entry, resolve the target, find its matching `dependents` hook(s),
   and run them against the consumer's FS. See [Dependencies](#dependencies)
8. Write the final files to disk

### Registries

`veil render` discovers compiled kinds by loading one or more `registry.json` files. Sources are consulted
in precedence order (first match wins):

1. `--registry <path>` (repeatable) on the CLI
2. `VEIL_REGISTRY` environment variable (colon-separated list of paths)
3. The `registries` field in `veil.json` (paths resolved relative to the `veil.json`'s directory)
4. Implicit `<.veil dir>/r/registry.json` — auto-discovered when present, so local `veil build` output is
   picked up without extra configuration

A kind name collision across loaded registries is a hard error.

## `veil eject <target> <source-filename>`

Ejects a source file from a resource definition for local override.

- `<target>`: The resource file (or directory containing it) to update.
- `<source-filename>`: The filename from the resource definition's `sources` to eject.

The command:

1. Resolves the resource definition referenced by `<target>`
2. Copies `<source-filename>` from the resource definition's `sources` into
   `.veil/<resource>/overrides/<filename>` (or a user-specified path)
3. Adds a `{source, path}` entry to `metadata.overrides` in the target resource

Example:

```
$ veil eject ./service.json sources/service/deployment.yaml
# copies .veil/resources/sources/service/deployment.yaml → .veil/service/overrides/deployment.yaml
# adds {"source": "./sources/service/deployment.yaml", "path": "./.veil/service/overrides/deployment.yaml"}
#   to metadata.overrides in ./service.json
```

To place the override at a custom path:

```
$ veil eject ./service.json sources/service/deployment.yaml --out ./custom/deployment.yaml
```

After ejecting, the developer owns that file entirely. It is used as the starting point for the hook
pipeline in place of the resource definition's source.

## `veil validate`

Runs schema validation without writing output files. Useful in CI to catch errors early.

## `veil schema <type>`

Prints the embedded JSON Schema for a veil type to stdout. Available subcommands:

- `veil schema config` — `.veil/veil.json` schema
- `veil schema resource-def` — resource definition schema
- `veil schema resource` — resource schema (metadata + spec)
- `veil schema metadata` — metadata schema (name, overlays, overrides)
