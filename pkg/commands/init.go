package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/interact"
)

// Init returns the "init" command — scaffolds a bare veil.json in the
// current working directory.
func Init() *cli.Command {
	return &cli.Command{
		Name:      "init",
		Usage:     "Scaffold a veil.json in the current directory",
		UsageText: "veil init",
		Action:    runInit,
	}
}

func runInit(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	path := filepath.Join(cwd, "veil.json")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("veil.json already exists at %s", path)
	}

	if err := writeJSON(path, bareVeilJSON()); err != nil {
		return err
	}
	p.Successf("Initialized %s", path)
	return nil
}
