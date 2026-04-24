package commands

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// Eject returns the "eject" subcommand.
func Eject() *cli.Command {
	return &cli.Command{
		Name:      "eject",
		Usage:     "Eject a source file from a resource definition for local override",
		UsageText: "veil eject <target> <source-filename> [--out <path>]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Usage: "Custom output path for the ejected file",
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "target",
				UsageText: "Resource instance file to update",
			},
			&cli.StringArg{
				Name:      "source",
				UsageText: "Source filename from the resource definition to eject",
			},
		},
		Action: runEject,
	}
}

func runEject(ctx context.Context, c *cli.Command) error {
	return fmt.Errorf("eject: not yet implemented")
}
