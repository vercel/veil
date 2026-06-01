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
		Action:    withResult(runInit),
	}
}

// initResponse is the JSON payload for `veil init`.
type initResponse struct {
	Path string `json:"path"`
}

func runInit(ctx context.Context, c *cli.Command) (*initResponse, error) {
	p := interact.Default()

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	path := filepath.Join(cwd, "veil.json")
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("veil.json already exists at %s", path)
	}

	if err := writeJSON(path, bareVeilJSON()); err != nil {
		return nil, err
	}
	p.Successf("Initialized %s", path)
	return &initResponse{Path: path}, nil
}
