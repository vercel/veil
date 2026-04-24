package commands

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// Gen returns the "gen" subcommand.
func Gen() *cli.Command {
	return &cli.Command{
		Name:  "gen",
		Usage: "Run transforms on a resource and generate output files",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "dir",
				Usage:   "Directory containing the resource (defaults to cwd)",
				Value:   ".",
				Sources: cli.EnvVars("VEIL_DIR"),
			},
		},
		Action: runGen,
	}
}

func runGen(ctx context.Context, c *cli.Command) error {
	return fmt.Errorf("gen: not yet implemented")
}
