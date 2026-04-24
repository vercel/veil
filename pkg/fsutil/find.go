// Package fsutil has small, dependency-free filesystem helpers shared
// across veil's subsystems.
package fsutil

import (
	"os"
	"path/filepath"
)

// FindAncestor walks upward from start looking for a regular file named
// `name`, returning its absolute path. If the file does not exist in any
// ancestor directory, returns "". Stops once it reaches the filesystem
// root, so callers don't need to pass a ceiling.
func FindAncestor(start, name string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
