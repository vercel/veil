package commands

import (
	"fmt"
	"os"

	"github.com/vercel/veil/pkg/config"
)

// loadRegistry loads a Registry either from the explicit configPath (when
// non-empty) or by walking up from the current working directory to find
// the nearest .veil/veil.json. Commands that accept --config should use
// this to honor both modes consistently.
func loadRegistry(configPath string) (*config.Registry, error) {
	if configPath != "" {
		return config.Load(configPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}
	return config.Discover(cwd)
}
