package commands

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/interact"
)

// Outcome is the success/error discriminant carried on every
// CommandResult envelope.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeError   Outcome = "error"
)

// CommandResult is the machine-readable envelope veil emits on stdout
// in `--output json` mode. It mirrors the shape the devbox and bridge
// CLIs use: a single object per command invocation with an outcome
// discriminant, an optional error string, and a typed response payload.
// Agents read exactly one of these — the last line on stdout — to learn
// what a command did. `response` and `error` can both be populated when
// a command partially succeeds (e.g. a multi-file render where some
// files rendered and others failed).
type CommandResult[T any] struct {
	Outcome  Outcome `json:"outcome"`
	Error    string  `json:"error,omitempty"`
	Response T       `json:"response,omitempty"`
}

// resultEmitted records whether any command body has already written a
// CommandResult this process. main() consults it so a top-level error
// (one that happened before/around a command, or in a command that
// hasn't adopted withResult) still produces exactly one error envelope
// in JSON mode — never zero, never a duplicate.
var resultEmitted atomic.Bool

// ResultEmitted reports whether a CommandResult has already been written
// to stdout during this process.
func ResultEmitted() bool { return resultEmitted.Load() }

// writeResult marshals a CommandResult around resp/errMsg and writes it
// as a single line to w. errMsg promotes the outcome to "error"; resp is
// still serialized when non-zero so partial results survive a failure.
func writeResult[T any](w io.Writer, resp T, errMsg string) error {
	result := CommandResult[T]{Outcome: OutcomeSuccess, Response: resp}
	if errMsg != "" {
		result.Outcome = OutcomeError
		result.Error = errMsg
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshalling command result: %w", err)
	}
	resultEmitted.Store(true)
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// withResult adapts a typed command body into a cli.ActionFunc. The body
// returns its typed response plus an error; in JSON mode the wrapper
// emits a single CommandResult envelope (carrying the response, the
// error message, or both) to the command's stdout. The error is always
// returned to the CLI unchanged, so exit codes and pretty-mode error
// reporting behave exactly as before.
func withResult[T any](fn func(context.Context, *cli.Command) (T, error)) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		resp, err := fn(ctx, cmd)
		if interact.IsJSON() {
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			_ = writeResult(cmd.Root().Writer, resp, errMsg)
		}
		return err
	}
}

// WriteErrorResult emits a response-less error envelope. main() calls it
// for failures that never reached a withResult body so that, in JSON
// mode, every non-zero exit still prints one structured result.
func WriteErrorResult(w io.Writer, err error) error {
	return writeResult[any](w, nil, err.Error())
}
