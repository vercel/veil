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
	return FindAncestorAny(start, []string{name})
}

// FindAncestorAny walks upward from start, returning the absolute path of
// the first file matching any name in `names` (checked in the order
// listed, at each directory level). Returns "" if none of the names is
// found before the filesystem root. Order matters: pass the preferred
// name (e.g. "veil.json") first so it wins over alternatives
// ("veil.yaml", "veil.yml") when more than one is present in the same
// directory.
func FindAncestorAny(start string, names []string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
