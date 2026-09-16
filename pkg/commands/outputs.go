package commands

import (
	"context"

	"github.com/urfave/cli/v3"
	"github.com/vercel/veil/pkg/output"
)

// outputsCommand operates only on the output-root manifest, so deleted resource
// declarations and unavailable registries cannot prevent safe removal/recovery.
func outputsCommand() *cli.Command {
	return &cli.Command{
		Name:  "outputs",
		Usage: "Remove owned resource outputs or recover an interrupted publication",
		Commands: []*cli.Command{
			{
				Name:  "remove",
				Usage: "Remove an exact owned root without loading its resource declaration",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "out", Value: "out", Usage: "Managed output directory"},
					&cli.StringFlag{Name: "kind", Required: true, Usage: "Exact registry-qualified kind (for example @platform/service)"},
					&cli.StringFlag{Name: "name", Required: true, Usage: "Exact resource metadata.name"},
				},
				Action: withResult(func(ctx context.Context, c *cli.Command) (map[string]string, error) {
					if err := output.Remove(c.String("out"), c.String("kind"), c.String("name")); err != nil {
						return nil, err
					}
					return map[string]string{"kind": c.String("kind"), "name": c.String("name"), "outDir": c.String("out")}, nil
				}),
			},
			{
				Name:  "recover",
				Usage: "Finish a persisted publication intent, refusing unknown file edits",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "out", Value: "out", Usage: "Managed output directory"},
				},
				Action: withResult(func(ctx context.Context, c *cli.Command) (map[string]string, error) {
					if err := output.Recover(c.String("out")); err != nil {
						return nil, err
					}
					return map[string]string{"outDir": c.String("out")}, nil
				}),
			},
		},
	}
}
