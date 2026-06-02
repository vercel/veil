// Package vfs provides the small filesystem surfaces veil's build
// pipeline writes through. FS is what `veil build` targets: Dir
// writes to an on-disk directory, Mem to an in-memory map. Both are
// fs.FS, so the registry can read a freshly-built in-memory registry
// through the same FSStore it uses for an on-disk one.
package vfs

import (
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
)

// FS is an fs.FS that also accepts writes addressed by a
// slash-separated, unrooted path. veil build writes its artifacts
// through one so the same pipeline targets disk or memory.
type FS interface {
	fs.FS
	WriteFile(name string, data []byte) error
}

// normalize maps a caller-supplied name (which may carry a leading "./"
// or stray slashes) onto a clean, slash-separated path. The root is ".".
func normalize(name string) string {
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "."
	}
	return path.Clean(name)
}

// --- in-memory ----------------------------------------------------------

// Mem is a concurrency-safe in-memory FS backed by a flat map of
// clean slash paths to file contents. It supports exactly the reads a
// registry performs — Open on a stored path — and deliberately does not
// synthesize directories or support walking/globbing, which the registry
// never needs. The build pipeline writes every kind.json /
// kind.schema.json / registry.json into one; the registry then reads them
// back through an FSStore. Backed by an xsync.Map so the parallel render
// pool's concurrent reads need no external locking.
type Mem struct {
	files *xsync.Map[string, []byte]
}

// NewMem returns an empty in-memory filesystem.
func NewMem() *Mem { return &Mem{files: xsync.NewMap[string, []byte]()} }

var _ FS = (*Mem)(nil)

// WriteFile stores a copy of data at name.
func (m *Mem) WriteFile(name string, data []byte) error {
	name = normalize(name)
	if name == "." || !fs.ValidPath(name) {
		return &fs.PathError{Op: "writefile", Path: name, Err: fs.ErrInvalid}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.files.Store(name, cp)
	return nil
}

// Open implements fs.FS for stored files.
func (m *Mem) Open(name string) (fs.File, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	data, ok := m.files.Load(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{name: path.Base(name), data: data}, nil
}

type memFile struct {
	name string
	data []byte
	off  int
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return memFileInfo{name: f.name, size: int64(len(f.data))}, nil
}
func (f *memFile) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}
func (f *memFile) Close() error { return nil }

type memFileInfo struct {
	name string
	size int64
}

func (fi memFileInfo) Name() string       { return fi.name }
func (fi memFileInfo) Size() int64        { return fi.size }
func (fi memFileInfo) Mode() fs.FileMode  { return 0o444 }
func (fi memFileInfo) ModTime() time.Time { return time.Time{} }
func (fi memFileInfo) IsDir() bool        { return false }
func (fi memFileInfo) Sys() any           { return nil }

// --- on-disk ------------------------------------------------------------

// Dir is a FS rooted at an on-disk directory. Reads go through
// os.DirFS; WriteFile creates parent directories as needed.
type Dir struct {
	root string
	fs.FS
}

// NewDir returns a Dir rooted at the given directory.
func NewDir(root string) *Dir { return &Dir{root: root, FS: os.DirFS(root)} }

var _ FS = (*Dir)(nil)

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
