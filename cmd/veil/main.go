package main

import (
	"context"
	"os"

	"github.com/vercel/veil/pkg/commands"
	"github.com/vercel/veil/pkg/interact"
)

var version = "dev"

func main() {
	commands.Version = version

	app := commands.NewApp()
	if err := app.Run(context.Background(), os.Args); err != nil {
		// Use the cli command's own streams (urfave defaults them to
		// os.Stdout / os.Stderr during Run, and tests override them) so
		// output routing stays consistent with every other command and
		// never bypasses the command's configured writers.
		//
		// No slog.Error here: in JSON mode that line would land on stdout
		// after the result envelope (breaking "stdout is exactly one
		// CommandResult"); the error is already carried by the envelope,
		// and in pretty mode the printer logs it to the rolling file.
		if interact.IsJSON() {
			// Every non-zero JSON exit must still print exactly one
			// CommandResult. A command that adopted withResult already
			// wrote one; only emit a top-level envelope when nothing did
			// (arg parsing, the Before hook, or an unconverted command).
			if !commands.ResultEmitted() {
				_ = commands.WriteErrorResult(app.Writer, err)
			}
		} else {
			interact.NewPrinter(app.ErrWriter).Errorf("%s", err)
		}
		os.Exit(1)
	}
}
