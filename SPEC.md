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

Veil recursively searches upward from the current working directory to find a `veil.json` file at the
project root. That file declares the project configuration — kinds, variables, optional published
registries to pull in, and where resource files live so they can be cataloged at render time.

```json
{
  "kinds": [
    "./.veil/kinds/service/kind.json",
    "./.veil/kinds/bucket/kind.json"
  ],
  "variables": {
    "env": { "type": "string", "enum": ["dev", "staging", "prod"], "default": "dev" }
  },
  "registries": {},
  "resource_discovery": {
    "paths": [
      "services/**/_deploy/*.json",
      "infra/buckets/*.json"
    ]
  }
}
```

- **`kinds`** — relative or absolute paths to `kind.json` definitions. Relative paths resolve against
  `veil.json`'s directory.
- **`variables`** — see [Variables](#variables).
- **`registries`** — map of alias → path/URL pointing at compiled `registry.json` files to merge in
  at render time (e.g., shared/published registries from outside this repo). The empty-string alias
  (`""`) names the **default registry** — bare kind references like `"service"` resolve there. Named
  aliases can be any non-empty string and are matched verbatim in references: declaring
  `"acme": "./vendor/acme/registry.json"` lets resources reference its kinds as `acme/service`. The
  `@`-scoped convention (`"@scope": "..."` referenced as `@scope/service`) works the same way — the
  alias is treated as opaque text. Registries are fully declarative — `veil` does not auto-discover
  any local build output; if you want `veil build`'s output as the default, declare it explicitly
  (`"": "./public/r/registry.json"`). The `veil new kind` scaffolder pre-populates this entry on a
  fresh project.
- **`resource_discovery.paths`** — see [Resource discovery](#resource-discovery).

## Resource discovery

`resource_discovery.paths` is the list of [doublestar](https://github.com/bmatcuk/doublestar) glob
patterns that tell `veil render` where to find resource files. At render time veil walks the patterns
to build a **catalog** indexed by `(kind, name)` — that catalog is what dependency targets are looked
up against, regardless of which directory the consumer or its targets live in.

```json
"resource_discovery": {
  "paths": [
    "services/**/_deploy/*.json",
    "infra/buckets/*.json"
  ]
}
```

Patterns use `**` to match across directory boundaries. Relative patterns resolve against the
`veil.json` directory; absolute patterns are used as-is. Each match is shallow-parsed (only
`metadata.kind` / `metadata.name` are read) — files that don't have those fields plus a `spec`
(overlays, fragments, schema files, anything else the glob happens to capture) are silently skipped,
so a generous pattern won't break discovery. Duplicate `(kind, name)` pairs across matches are a
hard error.

Catalog entries are loaded **lazily**: each match becomes a `sync.OnceValues`-backed loader keyed
on `(kind, name)` (and on its absolute path), so the proto body of a resource is read at most once
per render even if many other resources depend on it.

## Variables

`veil.json` can declare named input variables — scalars that overlay `if` regex maps match against
at render time:

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

Inside an overlay's `if` block, each entry's key is the variable name and the value is a Go regex
the variable's stringified value must match. Numbers and bools are stringified by `fmt`'s default
formatting (`3` → `"3"`, `false` → `"false"`).

```json
{
  "if": {
    "env":      "^staging$",
    "replicas": "^[3-9]$"
  }
}
```

## Registry

The set of resource definitions declared in `veil.json` forms the **registry**. Each resource definition
describes a type of deployable unit (e.g. `service`, `cron`, `consumer`, `bucket`, `sql`).

## Resource definition

A resource definition is a JSON file at `.veil/kinds/<name>/kind.json` with the following fields:

```json
{
  "name": "service",
  "sources": [
    "./sources/service/deployment.yaml",
    "./sources/service/hpa.yaml"
  ],
  "hooks": {
    "render": [
      { "path": "./hooks/src/inject-env-var.ts" }
    ],
    "dependents": [
      {
        "kind": "service",
        "paths": ["./hooks/src/dependents/service/inject-env.ts"],
        "params_path": "./service.params.json"
      }
    ]
  },
  "schema": "./schema.json"
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
list of hook files, and each file exports an interface specific to that lifecycle.

Two lifecycles exist today:

- **`render`** — runs during the consumer's own `veil render`. Each entry is an object with a
  required `path` (TS/JS file that exports a `RenderHook`) plus optional `access` declaring host
  resources the hook needs (env vars today; filesystem / network later). The string short-form is
  not supported: every entry is `{path, access?}`.
- **`dependents`** — per-consumer hooks that fire when *another* kind declares a dependency on this
  one. Each entry binds a consumer kind to one or more hook file paths and a JSON Schema for the
  params the consumer must supply. See [Dependencies](#dependencies) for the full design.

#### `access` — declared host resources

A render hook entry's optional `access` block tells the runner which host resources the hook needs.
Today only `env` is supported:

```json
{
  "path": "./hooks/src/inject-providers.ts",
  "access": {
    "env": [
      { "name": "DATADOG_API_KEY", "description": "Datadog API key for monitor provisioning" },
      { "name": "DATADOG_APP_KEY", "description": "Datadog application key for monitor provisioning" }
    ]
  }
}
```

Each `env` entry needs both a `name` and a `description`. Before the hook runs, `veil render`
calls `os.LookupEnv` for every declared name. **Missing vars aggregate into a single error** that
prints every name + description so the user can fix all of them in one pass — no piecemeal
re-runs. On success, the runner logs `granting env access` with the var list and the resolved
values reach the hook on `ctx.env` as a frozen `Record<string, string>` containing **only** the
declared keys. Vars the hook didn't declare are not visible regardless of host state.

```json
"hooks": {
  "render": [
    { "path": "./hooks/src/inject-env-var.ts" },
    {
      "path": "./hooks/src/inject-providers.ts",
      "access": {
        "env": [
          { "name": "DATADOG_API_KEY", "description": "Datadog API key for monitor provisioning" }
        ]
      }
    }
  ],
  "dependents": [
    {
      "kind": "service",
      "paths": ["./hooks/src/dependents/service/inject-env.ts"],
      "params_path": "./service.params.json"
    }
  ]
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

- **RenderHookContext**: contains `resource` (metadata + merged spec), `vars`, `root`, the host
  APIs `std` / `os` / `fetch`, and `env` — a frozen `Record<string, string>` containing exactly
  the env vars declared under the hook entry's `access.env` (and only those).
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

## Resource

A consumer creates a JSON file in their service directory that instantiates a kind — that file **is** the
resource. Every resource has the top-level fields `metadata` and `spec`, and may optionally include a
peer `dependencies` array.

### `metadata`

Common to all resources. Contains:

- **`kind`** (required for definitions): The name of the kind this resource instantiates. Must match
  a kind present in one of the loaded registries. Ignored entirely on overlay files (see `file_type`).
- **`name`** (required for definitions): The name of this resource. Ignored on overlay files.
- **`file_type`**: Either `definition` (default) or `overlay`. The default makes existing files —
  every authored resource — work unchanged. Overlay files set `file_type: "overlay"` so the JSON
  schema relaxes the `name`/`kind` requirement; even when an overlay sets those fields, render
  ignores them. An overlay cannot rename its target or reassign its kind. Purely a JSON-schema
  hint; the catalog walker skips overlays automatically because they lack `name`/`kind`/`spec`.
- **`overlays`**: A list of conditional overlays. Each overlay has an `if` map (variable name → Go
  regex) and a path to an overlay file. The path is resolved relative to the resource file declaring
  the overlay (or used as-is when absolute). When every `if` entry's regex matches the corresponding
  variable's stringified value, the overlay file is loaded and its `spec` is merged into the
  resource's `spec` before hooks run. An empty `if` map (or omitted entirely) means the overlay
  always applies.
- **`overrides`**: A list of source files replaced with local copies. Each entry is an object with
  `source` (a path from the resource definition's `sources`), `path` (the local override file,
  resolved relative to the resource file's directory or used as-is when absolute), and optional
  `skip_hooks` (default `false`). `veil override` manages this list — see [Override](#override) for
  semantics and the [CLI section](#veil-override-resource-source) for usage.

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
        "if":   { "env": "^staging$" },
        "file": "./staging.json"
      },
      {
        "if":   { "env": "^production$", "region": "^iad1$" },
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

Overrides are an escape hatch for cases where a consumer needs deep control of a source file. Rather
than trying to express everything through hooks, a consumer can **override** a source file with a
local copy. Overrides are tracked in `metadata.overrides`:

```json
{
  "metadata": {
    "name": "api-feature-flags",
    "overrides": [
      { "source": "sources/app.yaml",     "path": "app.yaml" },
      { "source": "sources/service.yaml", "path": "service.yaml", "skip_hooks": true }
    ]
  }
}
```

Each entry has:

- **`source`** (required) — path to the kind's source being replaced. Must match an entry in the
  kind's `sources` list.
- **`path`** (required) — local override file, resolved relative to the resource file's directory
  (or used as-is when absolute).
- **`skip_hooks`** (optional, default `false`) — controls how the override interacts with the
  hook pipeline.

There are two override modes:

1. **Default (`skip_hooks: false`)** — at render start, veil seeds the bundle entry for `source`
   with the override file's bytes. The hook pipeline (render hooks + every applicable dependent
   hook) then operates on those bytes; whatever the pipeline produces is what gets written. The
   override just shifts the **starting point** the hooks mutate.
2. **`skip_hooks: true`** — same starting-point swap, but veil **re-stamps** the override's bytes
   onto the bundle entry after every hook (render + dependent) finishes. Hooks may still observe
   and edit the file during the pipeline, but their mutations are discarded at write time. The
   rendered output is the local file byte-for-byte. Use this when the kind's hooks would
   otherwise stomp on a hand-tuned customization. If a hook tombstones a frozen file, render
   re-introduces it under the same key so the user's content still lands in the output.

A consumer creates entries with `veil override` (see [CLI](#veil-override-resource-source)).

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

## Target side: `hooks.dependents`

A kind that wants to be dependable declares the consumer kinds it accepts in its `kind.json` under
the `hooks.dependents` block — `dependents` is just another lifecycle alongside `render`:

```json
{
  "name": "bucket",
  "sources": [...],
  "schema": "./schema.json",
  "hooks": {
    "render": [...],
    "dependents": [
      {
        "kind": "service",
        "paths": ["./hooks/src/dependents/service/inject-env.ts"],
        "params_path": "./service.params.json"
      },
      {
        "kind": "worker",
        "paths": ["./hooks/src/dependents/worker/inject-stream.ts"],
        "params_path": "./worker.params.json"
      }
    ]
  }
}
```

Each entry has:

- **`kind`** — the consumer kind that may depend on this resource.
- **`paths`** — at least one hook file path that runs when a resource of `kind` depends on this one.
  Hooks run in declaration order. An empty `paths` array is a build error.
- **`params_path`** — JSON Schema describing the `params` object the consumer must supply. Paths are
  resolved relative to the `kind.json` file. Required.

Because hooks are registered per consumer kind, each hook receives a concretely typed `consumer` and
`params` — no union narrowing inside the hook body.

## `dependent` hooks

The build pipeline emits a per-consumer pair of types into the target kind's `veil-types.ts` —
`<Consumer>DependentHook` and `<Consumer>DependentHookContext` — so each hook file is bound to a
single consumer kind and receives concretely-typed `self`, `consumer`, `params`, and `fs`. For the
bucket kind that lists `service` as a consumer, the file imports:

```ts
import type {
  ServiceDependentHook,
  ServiceDependentHookContext,
  ServiceFS,
} from '../../veil-types';
```

`<Consumer>DependentHook` is shaped like:

```ts
export interface ServiceDependentHook {
  render(ctx: ServiceDependentHookContext, fs: ServiceFS): ServiceFS | void | Promise<ServiceFS | void>;
}

export interface ServiceDependentHookContext {
  self: Resource<BucketSpec, Dependency>; // the target kind's resolved resource
  consumer: Resource<ServiceSpec>;        // the consumer kind's resolved resource
  params: ServiceParams;                  // the consumer-supplied params, typed per params_path
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

1. Resolve `(kind, name)` to a target resource in the catalog (built from
   `resource_discovery.paths`). Missing targets are a hard error.
2. Apply the target's own overlays + spec defaults so `ctx.self` matches what the target would see
   at its own render. No schema validation: targets are inspected, not re-rendered.
3. Find the target kind's `hooks.dependents` entry matching the consumer's kind. A target that
   doesn't list the consumer's kind as allowed is a hard error.
4. Run each registered dependent hook against the consumer's FS, in declaration order.

Render hooks cannot observe state injected by dependent hooks — the lifecycles are strictly ordered
(overrides → render → dependents → re-stamp `skip_hooks` overrides → write).

## Build-time integration

`veil build` walks all kinds in the registry. For each consumer kind C, it collects every kind T
that lists C in its `hooks.dependents` and writes a discriminated union into C's emitted
`kind.schema.json`:

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

All core types are defined as protobuf messages in `proto/veil/v1/` with `buf.validate` constraints.
Source-side hand-authored types live in `config.proto` and carry the `Definition` suffix
(`VeilConfigDefinition`, `KindDefinition`, `HooksDefinition`, `DependentDefinition`); the
corresponding published forms — what `veil build` emits and `veil render` consumes — live in
`registry.proto` with bare names (`Kind`, `Hooks`, `Hook`, `DependentHook`, `Registry`,
`RegistryEntry`). The convention: **if it lives in a local JSON file authored by hand, it's a
`*Definition`; if it lives in a published artifact, it's just the type**. Sub-messages used in both
contexts (`Variable`, `VariableType`) keep bare names.

The `Makefile` runs a generation pipeline:

1. `buf generate` — produces Go code (`api/go/`) and JSON Schemas (`api/jsonschema/`)
2. `scripts/deref-jsonschema/` — post-processes the JSON Schemas: dereferences all `$ref` pointers, simplifies
   enum representations, cleans filenames (e.g. `veil.v1.KindDefinition.schema.bundle.json` → `KindDefinition.schema.json`)
3. Cleaned schemas are copied to `pkg/embeds/jsonschema/` and embedded in the binary via `//go:embed`

The embedded schemas are available via `veil schema {config,kind,kind-definition,resource,metadata}`.

All on-disk veil JSON is encoded via `pkg/protoencode`, which centralizes the canonical `protojson`
configuration: `UseProtoNames: true` (snake_case field names) for marshalling, `DiscardUnknown: true`
for unmarshalling so editor metadata like `$schema` doesn't break loading.

# CLI commands

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
`hooks/src/veil-types.ts`, run `tsc --noEmit --strict` (or `tsgo`) against the hooks dir if a
TypeScript compiler is on PATH, bundle each hook (esbuild, minified — both render hooks and any
`hooks.dependents[].paths`). Cross-kind: for every consumer kind, collect every target kind that
lists it in `hooks.dependents` and emit the discriminated `dependencies` schema (and corresponding
TS types) into the consumer's `kind.schema.json` and `veil-types.ts`. After both passes succeed
for every kind, the per-kind `kind.json` files and the top-level `registry.json` are written.

The build also bakes the project's declared variables into the metadata schema's `overlays[].if`
property — `additionalProperties: false` plus an enumerated `properties` map so a typo in a
variable reference (`evn` instead of `env`) fails JSON-schema validation rather than silently
never matching.

Flags:

- `--config` — path to `veil.json`. Defaults to the nearest `.veil/veil.json` (searched upward from cwd).
- `--out` — output directory. Defaults to `<veil.json dir>/r`.
- `--no-typecheck` — skip the `tsc`/`tsgo` invocation.

## `veil render <path>`

The primary command. Renders one resource at a time. The required `<path>` positional is the
filesystem path to a single resource JSON file. The CLI converts the path to its `fs.FS`-relative
form against the project root and consults the catalog (`resource_discovery.paths`) to recover the
resource's `(kind, name)` — those identify the entry point that the renderer pulls in via the
catalog and walks outward from.

1. Build the catalog from `resource_discovery.paths` (lazy `(kind, name)` index — see
   [Resource discovery](#resource-discovery))
2. Load compiled kinds from every configured **registry** (see below)
3. Resolve the entry point: look up the path argument in the catalog → `(kind, name)` → fully
   parsed Resource
4. Evaluate `metadata.overlays` — for each overlay whose `if` map matches every variable's stringified
   value against the listed Go regex, merge the overlay's `spec` into the base `spec`. Overlay files
   themselves are read from the project FS (relative to the resource's own directory).
5. Validate the merged `spec` against the kind's schema (which also validates `dependencies[].params`
   against the discriminated schema baked in at build time)
6. Load the `sources` (already embedded in the compiled `kind.json`) into an initial `FS`, then apply any
   `metadata.overrides` — each entry replaces the corresponding source's contents in the FS so the hook
   pipeline operates on the override as its starting point. Entries with `skip_hooks: true` are recorded
   for the re-stamp pass at step 9.
7. For each render hook, pre-flight every name in its `access.env` declaration via `os.LookupEnv`. Any
   missing names abort the render with one error listing all of them plus the kind's descriptions. On
   success, log the granted vars and pass them to the hook on `ctx.env`.
8. Apply `hooks.render` in order (calling each `RenderHook.render`), threading the FS through the pipeline
9. Apply `dependencies` — for each entry, look up the target via the catalog, find the target kind's
   matching `hooks.dependents` entry for the consumer's kind, and run those hooks against the
   consumer's FS. See [Dependencies](#dependencies).
10. Re-stamp every `skip_hooks: true` override's bytes onto the bundle, discarding any in-flight hook
    mutations to those files.
11. Write the final files to disk

### Registries

`veil render` discovers compiled kinds by loading one or more `registry.json` files. Sources are consulted
in precedence order (first match wins):

1. `--registry <path>` (repeatable) on the CLI — every entry lands under the **default alias** (`""`)
2. `VEIL_REGISTRY` environment variable (colon-separated list of paths) — also default-aliased
3. The `registries` map in `veil.json` (alias → path; aliases are arbitrary opaque strings — `"acme"`, `"@scope"`, etc. all work as long as the same string is used in references. Paths resolve relative to the `veil.json`'s directory)

There is **no implicit fallback** — registries must be declared explicitly somewhere on this list,
or `veil render` fails. The `veil new kind` scaffolder pre-populates `veil.json` with the local
build output (`"": "./public/r/registry.json"`) so a fresh project works out of the box.

Kind references in `metadata.kind` and `dependencies[].kind` are matched by alias:

- Bare names (e.g. `"service"`) resolve against the default alias (`""`).
- `<alias>/<kind>` (e.g. `"acme/service"` or `"@scope/service"`) resolves against the registry
  registered under that alias. The substring before the first `/` is taken as the alias verbatim;
  the rest is the kind name.

A kind name collision **within** a single alias is a hard error. The same kind name across different
aliases is fine — the `<alias>/` prefix disambiguates.

## `veil override <resource> [<source>...]`

Replace one or more kind source files on a single resource with local copies. Each named source is
copied next to the resource (or to `--out`), registered under `metadata.overrides`, and from then
on render uses the local file in place of the kind's source.

- `<resource>`: Path to the resource JSON file the overrides are attached to.
- `<source>...`: Source paths declared in the kind's `sources` list (e.g. `sources/app.yaml`). Omit
  to enter **discovery mode** — the command prints every source the kind declares, flags the ones
  already covered by an existing override, and tells the user how to re-invoke.

Flags:

- `--skip-hooks` — every override registered in this call gets `skip_hooks: true`. See
  [Override](#override) for the runtime semantics. The flag applies uniformly to every source
  named in the call.
- `--out <dir>` — directory where the override files land (default: alongside the resource file).
  Resolved relative to the resource if not absolute.

The command validates every requested `<source>` against the kind's declared sources up front, so
a typo on the third argument fails before the first override file is written. Each successful file
write is tracked; if any later step fails (a duplicate registration, a write error, etc.) all
previously written files are removed so a partial-apply doesn't leave dead bytes on disk.

Examples:

```
# List sources the kind declares (discovery mode):
$ veil override services/users/_deploy/service.json
→ Sources declared by kind "service":
  sources/app.yaml
  sources/hpa.yaml
  sources/pdb.yaml
  sources/service.yaml
  sources/role.tf
→ Pick one and re-run: veil override services/users/_deploy/service.json <source> [--skip-hooks]

# Override one source:
$ veil override services/users/_deploy/service.json sources/app.yaml

# Override several at once, all marked skip-hooks:
$ veil override services/users/_deploy/service.json \
    sources/app.yaml sources/hpa.yaml sources/pdb.yaml --skip-hooks
```

After overriding, the developer owns those files entirely. They are used as the hook pipeline's
starting point for the named sources; with `--skip-hooks` they are also re-stamped after the
pipeline so the rendered output matches them byte-for-byte.

## `veil graph <path>`

Walks the dependency graph rooted at a resource file and prints it. Useful for sanity-checking
which targets a service actually depends on, debugging cycles, and feeding visual renderers.

- `<path>`: A resource JSON file the graph roots at.

Flags:

- `--config <path>` — path to `veil.json` (defaults to the nearest one).
- `--format <tree|mermaid|dot>` — output format. Default `tree`.
  - `tree` — Unicode box-drawing tree. Each node prints `<kind>/<name>  (<resource path>)` plus
    the dependency's `params` map. Nodes visited via more than one path collapse to one node;
    repeats are flagged with `(↺)` so the structure stays honest.
  - `mermaid` — `flowchart LR` source you can paste into a mermaid renderer.
  - `dot` — Graphviz DOT; pipe through `dot -Tsvg` (or similar).

BFS traversal, edges sorted by `kind/name` so output is stable across runs. A missing dependency
target fails with the chain of `(kind, name)` pairs that led to it.

Example:

```
$ veil graph services/api-feature-flags/_deploy/service.json
service/api-feature-flags  (services/api-feature-flags/_deploy/service.json)
├── bucket/eddie  (infra/buckets/eddie.json)  [action=read, url_env_var=MY_BUCKET_URL]
└── cache/rate-limits  (infra/caches/rate-limits.json)  [env_var=RATE_LIMITS_CACHE_MODE]
```

## `veil schema <type>`

Prints the embedded JSON Schema for a veil type to stdout. Available subcommands:

- `veil schema config` — `.veil/veil.json` schema
- `veil schema kind-definition` — hand-authored `kind.json` schema
- `veil schema kind` — published, compiled `Kind` schema (the `veil build` output)
- `veil schema resource` — resource schema (metadata + spec envelope)
- `veil schema metadata` — metadata schema (name, kind, overlays, overrides)
