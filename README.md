# veil

Go CLI for tracking, transforming, and rendering arbitrary deployment configuration
(Kubernetes, Envoy, Terraform, …) through TypeScript hooks. See [SPEC.md](SPEC.md) for the design.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/vercel/veil/main/install.sh | sh
```

## Develop

Required:

- [Go 1.26+](https://go.dev/doc/install)

Optional:

- [buf](https://buf.build/docs/installation) — regenerate protobuf + JSON Schema after editing anything under `proto/`
- [TypeScript](https://www.typescriptlang.org/download) — `tsc` on `PATH` lets `veil build` typecheck hook sources

```sh
make build        # build ./veil
make proto        # regenerate Go types + embedded JSON schemas
make all          # clean + proto + build
go test ./...     # run tests
```

Tests use [testify suite](https://github.com/stretchr/testify) — embed `suite.Suite` and call `s.Equal`, `s.Require().NoError`, etc. rather than the standalone `assert.*` / `require.*` helpers.

## Layout

- `cmd/veil/` — CLI entrypoint
- `pkg/` — build, render, hook runtime, typegen, config, …
- `proto/veil/v1/` — protobuf sources; outputs land in `api/go/`, `api/jsonschema/`, and `pkg/embeds/jsonschema/`
- `scripts/deref-jsonschema/` — post-processor that simplifies the buf-generated bundle schemas
- `example/` — working veil project used as the integration target
- [SPEC.md](SPEC.md) — design / source of truth
- [AGENTS.md](AGENTS.md) — conventions for AI assistants
