package commands

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// Validate returns the "validate" subcommand.
func Validate() *cli.Command {
	return &cli.Command{
		Name:  "validate",
		Usage: "Run schema validation without writing output",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "dir",
				Usage:   "Directory to validate (defaults to cwd)",
				Value:   ".",
				Sources: cli.EnvVars("VEIL_DIR"),
			},
		},
		Action: runValidate,
	}
}

func runValidate(ctx context.Context, c *cli.Command) error {
	return fmt.Errorf("validate: not yet implemented")
}
