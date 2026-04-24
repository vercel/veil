package commands

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
	"github.com/vercel/veil/pkg/embeds"
)

// Schema returns the "schema" subcommand.
func Schema() *cli.Command {
	return &cli.Command{
		Name:  "schema",
		Usage: "Print JSON schemas for veil types",
		Commands: []*cli.Command{
			{
				Name:  "config",
				Usage: "Print the JSON schema for .veil/veil.json",
				Action: func(_ context.Context, _ *cli.Command) error {
					fmt.Println(string(embeds.VeilConfigSchema))
					return nil
				},
			},
			{
				Name:  "kind",
				Usage: "Print the JSON schema for a kind definition",
				Action: func(_ context.Context, _ *cli.Command) error {
					fmt.Println(string(embeds.KindSchema))
					return nil
				},
			},
			{
				Name:  "resource",
				Usage: "Print the JSON schema for a resource",
				Action: func(_ context.Context, _ *cli.Command) error {
					fmt.Println(string(embeds.ResourceSchema))
					return nil
				},
			},
			{
				Name:  "metadata",
				Usage: "Print the JSON schema for resource metadata",
				Action: func(_ context.Context, _ *cli.Command) error {
					fmt.Println(string(embeds.MetadataSchema))
					return nil
				},
			},
			{
				Name:  "compiled-kind",
				Usage: "Print the JSON schema for a compiled kind (veil build output)",
				Action: func(_ context.Context, _ *cli.Command) error {
					fmt.Println(string(embeds.CompiledKindSchema))
					return nil
				},
			},
		},
	}
}
