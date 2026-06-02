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
		// it). Not pretty in JSON or --quiet mode, so the error routes
		// through slog as a structured log line rather than styled text —
		// in JSON the CommandResult envelope (written by withResult)
		// already carried it.
		pretty := !interact.IsJSON() && !app.Bool("quiet")
		interact.NewPrinter(app.ErrWriter, pretty).Errorf("%s", err)
		os.Exit(1)
	}
}
