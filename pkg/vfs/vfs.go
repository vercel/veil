// Package vfs is veil's filesystem surface. FS is a read-only tree that
// knows where it is rooted; Filesystem adds writes. Dir is backed by an
// on-disk directory, Mem by an in-memory map, and both satisfy
// Filesystem — so the registry can read a freshly-built in-memory
// registry through the same FSStore it uses for an on-disk one, and a
// consumer that only reads can say so in its signature.
//
// Carrying the root on the FS is what keeps callers from threading a
// (root string, fsys fs.FS) pair through every signature — the build
// pipeline needs both to resolve a hook entrypoint relative to the
// project, and the registry needs both to report an absolute location.
package vfs

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
)

// FS is a read-only filesystem that knows where it is rooted. It
// embeds every extension interface io/fs defines, so a value satisfying
// FS can be handed to any stdlib helper — fs.ReadDir, fs.Glob, fs.Sub,
// fs.WalkDir — without a capability check first. (fs.WalkDir needs no
// interface of its own: it walks through ReadDirFS.)
//
// Take an FS in any signature that only reads. Writers take Filesystem.
type FS interface {
	fs.FS
	fs.GlobFS
	fs.ReadDirFS
	fs.ReadFileFS
	fs.ReadLinkFS
	fs.StatFS
	fs.SubFS

	// Root reports the directory this filesystem is rooted at, as it was
	// configured. Empty for a filesystem with no on-disk location — Mem,
	// or a freshly-built in-memory registry.
	Root() string

	// Abs reports Root resolved to an absolute path. Empty when Root is.
	Abs() string
}

// Filesystem is an FS that also accepts writes addressed by a
// slash-separated, unrooted path. veil build writes its artifacts
// through one so the same pipeline targets disk or memory.
type Filesystem interface {
	FS

	// WriteFile writes data to name, a slash-separated path relative to
	// the root.
	WriteFile(name string, data []byte) error
}

// New adapts an arbitrary fs.FS — an fstest.MapFS in a test, an
// embed.FS, a subtree handed over from elsewhere — into a read-only FS
// rooted at root. root may be empty when the tree has no on-disk
// location.
//
// The extension methods route through the io/fs helpers, which use the
// wrapped value's own implementation where it has one and the generic
// Open-based fallback where it doesn't. The wrapper therefore satisfies
// every extension interface regardless of what went in, so wrapping
// never costs a capability.
func New(fsys fs.FS, root string) FS {
	w := &wrapFS{FS: fsys, root: root, abs: root}
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			w.abs = abs
		}
	}
	return w
}

type wrapFS struct {
	fs.FS
	root string
	abs  string
}

var _ FS = (*wrapFS)(nil)

func (w *wrapFS) Root() string { return w.root }
func (w *wrapFS) Abs() string  { return w.abs }

// As in Dir, each of these delegates to the wrapped value rather than to
// w, so the io/fs helper can't dispatch back into this method.
func (w *wrapFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(w.FS, name) }
func (w *wrapFS) ReadFile(name string) ([]byte, error)       { return fs.ReadFile(w.FS, name) }
func (w *wrapFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(w.FS, name) }
func (w *wrapFS) Glob(pattern string) ([]string, error)      { return fs.Glob(w.FS, pattern) }
func (w *wrapFS) ReadLink(name string) (string, error)       { return fs.ReadLink(w.FS, name) }
func (w *wrapFS) Lstat(name string) (fs.FileInfo, error)     { return fs.Lstat(w.FS, name) }

// Sub returns the subtree rooted at dir, still an FS: its Root is dir
// joined onto this one's, so the pairing survives the descent.
func (w *wrapFS) Sub(dir string) (fs.FS, error) {
	sub, err := fs.Sub(w.FS, dir)
	if err != nil {
		return nil, err
	}
	root := ""
	if w.root != "" {
		root = filepath.Join(w.root, filepath.FromSlash(dir))
	}
	return New(sub, root), nil
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

// Mem is a concurrency-safe in-memory Filesystem backed by a flat map of
// clean slash paths to file contents. Directories are not stored; they
// are synthesized from the key set on demand, which is what lets a flat
// map satisfy ReadDirFS and friends. The build pipeline writes every
// kind.json / kind.schema.json / registry.json into one; the registry
// then reads them back through an FSStore. Backed by an xsync.Map so the
// parallel render pool's concurrent reads need no external locking.
type Mem struct {
	files *xsync.Map[string, []byte]
}

// NewMem returns an empty in-memory filesystem.
func NewMem() *Mem { return &Mem{files: xsync.NewMap[string, []byte]()} }

// Root reports "" — an in-memory Filesystem has no on-disk location.
func (m *Mem) Root() string { return "" }

// Abs reports "" — an in-memory Filesystem has no on-disk location.
func (m *Mem) Abs() string { return "" }

var (
	_ Filesystem    = (*Mem)(nil)
	_ fs.GlobFS     = (*Mem)(nil)
	_ fs.ReadDirFS  = (*Mem)(nil)
	_ fs.ReadFileFS = (*Mem)(nil)
	_ fs.ReadLinkFS = (*Mem)(nil)
	_ fs.StatFS     = (*Mem)(nil)
	_ fs.SubFS      = (*Mem)(nil)
)

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

// ReadFile returns a copy of the bytes stored at name.
func (m *Mem) ReadFile(name string) ([]byte, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}
	data, ok := m.files.Load(name)
	if !ok {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(data), nil
}

// Stat describes name, which is a file when it was written and a
// directory when any stored path sits beneath it.
func (m *Mem) Stat(name string) (fs.FileInfo, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := m.files.Load(name); ok {
		return &memFileInfo{name: path.Base(name), size: int64(len(data))}, nil
	}
	if m.isDir(name) {
		return &memFileInfo{name: path.Base(name), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// ReadDir synthesizes the listing for name out of the flat key set:
// each stored path beneath it contributes either itself, when it is an
// immediate child, or the one directory segment that holds it. Entries
// come back sorted by name, as the fs.ReadDirFS contract requires.
func (m *Mem) ReadDir(name string) ([]fs.DirEntry, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	prefix := ""
	if name != "." {
		prefix = name + "/"
	}
	sizes := map[string]int64{}
	dirs := map[string]bool{}
	m.files.Range(func(p string, data []byte) bool {
		if !strings.HasPrefix(p, prefix) {
			return true
		}
		rest := p[len(prefix):]
		if rest == "" {
			return true
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			dirs[rest[:i]] = true
		} else {
			sizes[rest] = int64(len(data))
		}
		return true
	})
	if len(sizes) == 0 && len(dirs) == 0 && name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	entries := make([]fs.DirEntry, 0, len(sizes)+len(dirs))
	for child := range dirs {
		entries = append(entries, &memFileInfo{name: child, dir: true})
	}
	for child, size := range sizes {
		// A path can only be a file or a directory prefix, never both:
		// WriteFile stores leaves, so "a/b" and "a/b/c" can't coexist.
		if dirs[child] {
			continue
		}
		entries = append(entries, &memFileInfo{name: child, size: size})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// Glob matches pattern against the synthesized tree. It routes through
// a reader view rather than m so fs.Glob doesn't find this method and
// call straight back into it.
func (m *Mem) Glob(pattern string) ([]string, error) {
	return fs.Glob(memReader{m}, pattern)
}

// Sub returns the subtree rooted at dir. The result reads through to
// this Mem; it is a view, not a copy.
func (m *Mem) Sub(dir string) (fs.FS, error) {
	dir = normalize(dir)
	if !fs.ValidPath(dir) {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrInvalid}
	}
	if dir == "." {
		return m, nil
	}
	return fs.Sub(memReader{m}, dir)
}

// ReadLink always fails: an in-memory tree stores bytes at paths and has
// no notion of a symbolic link. Mem implements fs.ReadLinkFS so callers
// need no capability check, not because links exist.
func (m *Mem) ReadLink(name string) (string, error) {
	return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
}

// Lstat is Stat: with no symbolic links, there is nothing to not follow.
func (m *Mem) Lstat(name string) (fs.FileInfo, error) { return m.Stat(name) }

// isDir reports whether any stored path sits beneath name.
func (m *Mem) isDir(name string) bool {
	if name == "." {
		return true
	}
	prefix := name + "/"
	found := false
	m.files.Range(func(p string, _ []byte) bool {
		if strings.HasPrefix(p, prefix) {
			found = true
			return false
		}
		return true
	})
	return found
}

// memReader exposes just enough of a Mem for the io/fs helpers to work
// against — Open and ReadDir — while hiding Glob and Sub. Handing m
// itself to fs.Glob or fs.Sub would make them dispatch to m.Glob /
// m.Sub, which is where they were called from.
type memReader struct{ m *Mem }

func (r memReader) Open(name string) (fs.File, error)          { return r.m.Open(name) }
func (r memReader) ReadDir(name string) ([]fs.DirEntry, error) { return r.m.ReadDir(name) }

type memFile struct {
	name string
	data []byte
	off  int
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return &memFileInfo{name: f.name, size: int64(len(f.data))}, nil
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

// memFileInfo describes a stored file or a synthesized directory. It
// serves as both fs.FileInfo and fs.DirEntry so ReadDir can hand back
// entries without a second wrapper type.
type memFileInfo struct {
	name string
	size int64
	dir  bool
}

var (
	_ fs.FileInfo = (*memFileInfo)(nil)
	_ fs.DirEntry = (*memFileInfo)(nil)
)

func (fi *memFileInfo) Name() string       { return fi.name }
func (fi *memFileInfo) Size() int64        { return fi.size }
func (fi *memFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *memFileInfo) IsDir() bool        { return fi.dir }
func (fi *memFileInfo) Sys() any           { return nil }

func (fi *memFileInfo) Mode() fs.FileMode {
	if fi.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

// Type and Info complete fs.DirEntry.
func (fi *memFileInfo) Type() fs.FileMode          { return fi.Mode().Type() }
func (fi *memFileInfo) Info() (fs.FileInfo, error) { return fi, nil }

// --- on-disk ------------------------------------------------------------

// Dir is a Filesystem rooted at an on-disk directory. Reads go through
// os.DirFS; WriteFile creates parent directories as needed.
//
// Dir implements every io/fs extension interface by routing through the
// matching io/fs helper, which uses os.DirFS's own implementation where
// it has one and the generic Open-based fallback where it doesn't. A
// type switch for fs.ReadDirFS therefore succeeds on a Dir, so passing
// one where a plain fs.FS was expected never costs a capability.
type Dir struct {
	root string
	abs  string
	fs.FS
}

// NewDir returns a Dir rooted at the given directory, which may be
// relative to the working directory.
func NewDir(root string) *Dir {
	d := &Dir{root: root, abs: root, FS: os.DirFS(root)}
	if abs, err := filepath.Abs(root); err == nil {
		d.abs = abs
	}
	return d
}

var (
	_ Filesystem    = (*Dir)(nil)
	_ fs.GlobFS     = (*Dir)(nil)
	_ fs.ReadDirFS  = (*Dir)(nil)
	_ fs.ReadFileFS = (*Dir)(nil)
	_ fs.ReadLinkFS = (*Dir)(nil)
	_ fs.StatFS     = (*Dir)(nil)
	_ fs.SubFS      = (*Dir)(nil)
)

// Each of these delegates to the embedded os.DirFS, never to the Dir
// itself — routing through d would send the io/fs helper back into this
// method and spin.
func (d *Dir) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(d.FS, name) }
func (d *Dir) ReadFile(name string) ([]byte, error)       { return fs.ReadFile(d.FS, name) }
func (d *Dir) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(d.FS, name) }
func (d *Dir) Glob(pattern string) ([]string, error)      { return fs.Glob(d.FS, pattern) }
func (d *Dir) ReadLink(name string) (string, error)       { return fs.ReadLink(d.FS, name) }
func (d *Dir) Lstat(name string) (fs.FileInfo, error)     { return fs.Lstat(d.FS, name) }

// Sub returns the subtree rooted at dir as a Dir, so the descent keeps
// both the root pairing and write access rather than degrading to a
// bare fs.FS.
func (d *Dir) Sub(dir string) (fs.FS, error) {
	if !fs.ValidPath(dir) {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrInvalid}
	}
	return NewDir(filepath.Join(d.root, filepath.FromSlash(dir))), nil
}

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

// Root returns the on-disk root directory, as it was configured.
func (d *Dir) Root() string { return d.root }

// Abs returns the root resolved to an absolute path.
func (d *Dir) Abs() string { return d.abs }
