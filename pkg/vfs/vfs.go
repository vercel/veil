// Package vfs provides a small on-disk filesystem helper veil's build
// pipeline writes through. Dir is an os-backed directory rooted at a
// path: reads go through os.DirFS, and WriteFile creates parent
// directories as needed, so the build pipeline can address its output by
// clean, slash-separated, registry-relative paths.
package vfs

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// normalize maps a caller-supplied name (which may carry a leading "./"
// or stray slashes) onto a clean fs.FS-style path. The root is ".".
func normalize(name string) string {
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "."
	}
	return path.Clean(name)
}

// Dir is an fs.FS rooted at an on-disk directory that also accepts writes
// addressed by a slash-separated, unrooted path. Reads go through
// os.DirFS; WriteFile creates parent directories as needed. `veil build`
// and `veil new kind|hook` write their compiled kinds through one.
type Dir struct {
	root string
	fs.FS
}

// NewDir returns a Dir rooted at the given directory.
func NewDir(root string) *Dir { return &Dir{root: root, FS: os.DirFS(root)} }

// WriteFile writes data to <root>/<name>, creating parent dirs. name is a
// slash-separated path interpreted relative to the root.
func (d *Dir) WriteFile(name string, data []byte) error {
	name = normalize(name)
	full := filepath.Join(d.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// Root returns the on-disk root directory.
func (d *Dir) Root() string { return d.root }
