# Agents

## Testing

- Use `github.com/stretchr/testify` for all tests
- Use testify's `suite.Suite` struct embedding for test organization
- Use suite methods directly (e.g. `s.Equal()`, `s.Error()`, `s.Require().NoError()`) — do not use standalone `assert.*` or `require.*` functions

## Building
- Use `make build` to compile the binary
- Use `make proto` to regenerate protobuf outputs after editing anything
  under `proto/` (Go code + JSON schemas + embedded schemas)
