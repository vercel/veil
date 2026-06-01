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
		// Surface the error through the printer using the cli command's
		// own ErrWriter (urfave defaults it to os.Stderr; tests override
		// it). In JSON mode the printer routes this through slog, so it
		// lands as a structured error log line — the command's
		// CommandResult envelope was already written by withResult. In
		// pretty mode it's styled text.
		interact.NewPrinter(app.ErrWriter).Errorf("%s", err)
		os.Exit(1)
	}
}
