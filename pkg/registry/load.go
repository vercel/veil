package registry

import (
	"fmt"
	"os"

	"github.com/vercel/veil/pkg/config"
)

// LoadProject loads the source-side project — either from the explicit
// configPath or by walking upward from the current working directory to
// find the nearest .veil/veil.json. Commands that accept a --config
// flag should use this so both invocation modes are handled uniformly.
//
// The returned *config.Registry carries the source-side veil.json
// payload (variables, source kind definitions). For looking up compiled
// kinds at render time, use FromIndex + the Registry interface in this
// same package.
func LoadProject(configPath string) (*config.Registry, error) {
	if configPath != "" {
		return config.Load(configPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}
	return config.Discover(cwd)
}
