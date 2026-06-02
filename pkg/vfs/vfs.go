// Package vfs provides small filesystem abstractions veil writes through
// so build output can land either on disk or in memory.
//
// WritableFS is the write surface `veil build` targets. Mem is a
// concurrency-safe in-memory implementation backed by a flat map of
// cleaned slash paths to file contents; it implements the major read
// interfaces (fs.FS, fs.ReadFileFS, fs.ReadDirFS, fs.StatFS, fs.GlobFS,
// fs.SubFS), synthesizing directories from the path segments of the
// files it holds, so fs.WalkDir / fs.Glob / fs.Sub all work over it.
// Dir is a thin os-backed WritableFS for the on-disk path.
package vfs

import (
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// WritableFS is an fs.FS that also accepts writes addressed by an
// fs.FS-style (slash-separated, unrooted) path. veil build writes its
// artifacts through one of these so the same pipeline can target disk
// or memory.
type WritableFS interface {
	fs.FS
	// WriteFile stores data at name, creating any parent directories the
	// implementation needs. name is an fs.ValidPath.
	WriteFile(name string, data []byte) error
}

// normalize maps a caller-supplied name (which may carry a leading
// "./" or stray slashes) onto a clean fs.FS path. The root is ".".
func normalize(name string) string {
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "."
	}
	return path.Clean(name)
}

// --- in-memory implementation -------------------------------------------

// Mem is a concurrency-safe in-memory filesystem. The zero value is not
// usable; call NewMem.
type Mem struct {
	mu    sync.RWMutex
	files map[string][]byte
}

// NewMem returns an empty in-memory filesystem.
func NewMem() *Mem { return &Mem{files: make(map[string][]byte)} }

var _ WritableFS = (*Mem)(nil)
var _ fs.ReadFileFS = (*Mem)(nil)
var _ fs.ReadDirFS = (*Mem)(nil)
var _ fs.StatFS = (*Mem)(nil)
var _ fs.GlobFS = (*Mem)(nil)
var _ fs.SubFS = (*Mem)(nil)

// WriteFile stores a copy of data at name.
func (m *Mem) WriteFile(name string, data []byte) error {
	name = normalize(name)
	if name == "." || !fs.ValidPath(name) {
		return &fs.PathError{Op: "writefile", Path: name, Err: fs.ErrInvalid}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = cp
	return nil
}

// ReadFile returns a copy of the bytes stored at name.
func (m *Mem) ReadFile(name string) ([]byte, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.files[name]
	if !ok {
		if m.isDirLocked(name) {
			return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid} // is a directory
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// Open implements fs.FS. Files return a readable handle; directories
// (the root "." or any path prefix of a stored file) return a
// fs.ReadDirFile.
func (m *Mem) Open(name string) (fs.File, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if data, ok := m.files[name]; ok {
		return &memFile{name: path.Base(name), data: data}, nil
	}
	if name == "." || m.isDirLocked(name) {
		return &memDir{fsys: m, name: name, entries: m.readDirLocked(name)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Stat implements fs.StatFS.
func (m *Mem) Stat(name string) (fs.FileInfo, error) {
	name = normalize(name)
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if data, ok := m.files[name]; ok {
		return fileInfo{name: path.Base(name), size: int64(len(data))}, nil
	}
	if name == "." || m.isDirLocked(name) {
		return fileInfo{name: path.Base(name), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// ReadDir implements fs.ReadDirFS, listing the immediate children of dir.
func (m *Mem) ReadDir(dir string) ([]fs.DirEntry, error) {
	dir = normalize(dir)
	if !fs.ValidPath(dir) {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrInvalid}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if dir != "." && !m.isDirLocked(dir) {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrNotExist}
	}
	return m.readDirLocked(dir), nil
}

// Glob implements fs.GlobFS by matching pattern against every stored
// file path and every synthesized directory.
func (m *Mem) Glob(pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{})
	var out []string
	consider := func(p string) {
		if _, dup := seen[p]; dup {
			return
		}
		if ok, _ := path.Match(pattern, p); ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for f := range m.files {
		consider(f)
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			consider(d)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Sub implements fs.SubFS, returning a view rooted at dir.
func (m *Mem) Sub(dir string) (fs.FS, error) {
	dir = normalize(dir)
	if !fs.ValidPath(dir) {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrInvalid}
	}
	if dir == "." {
		return m, nil
	}
	m.mu.RLock()
	isDir := m.isDirLocked(dir)
	m.mu.RUnlock()
	if !isDir {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrNotExist}
	}
	return &subFS{parent: m, prefix: dir}, nil
}

// isDirLocked reports whether name names a directory: some stored file
// sits beneath it. Caller holds the lock.
func (m *Mem) isDirLocked(name string) bool {
	prefix := name + "/"
	for f := range m.files {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// readDirLocked returns the immediate children of dir as DirEntries,
// sorted by name. Caller holds the lock.
func (m *Mem) readDirLocked(dir string) []fs.DirEntry {
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}
	files := make(map[string]int64) // child name -> size
	dirs := make(map[string]struct{})
	for f, data := range m.files {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		rest := f[len(prefix):]
		if rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			dirs[rest[:i]] = struct{}{}
		} else {
			files[rest] = int64(len(data))
		}
	}
	out := make([]fs.DirEntry, 0, len(files)+len(dirs))
	for name := range dirs {
		out = append(out, fs.FileInfoToDirEntry(fileInfo{name: name, dir: true}))
	}
	for name, size := range files {
		out = append(out, fs.FileInfoToDirEntry(fileInfo{name: name, size: size}))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// subFS is the fs.FS view returned by Mem.Sub, rooting reads at prefix.
type subFS struct {
	parent *Mem
	prefix string
}

func (s *subFS) full(name string) string {
	if name == "." {
		return s.prefix
	}
	return s.prefix + "/" + name
}

func (s *subFS) Open(name string) (fs.File, error) { return s.parent.Open(s.full(normalize(name))) }
func (s *subFS) ReadFile(name string) ([]byte, error) {
	return s.parent.ReadFile(s.full(normalize(name)))
}
func (s *subFS) Stat(name string) (fs.FileInfo, error) { return s.parent.Stat(s.full(normalize(name))) }
func (s *subFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return s.parent.ReadDir(s.full(normalize(name)))
}

// --- in-memory file + dir handles ---------------------------------------

type memFile struct {
	name string
	data []byte
	off  int64
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return fileInfo{name: f.name, size: int64(len(f.data))}, nil
}
func (f *memFile) Read(p []byte) (int, error) {
	if f.off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += int64(n)
	return n, nil
}
func (f *memFile) Close() error { return nil }

type memDir struct {
	fsys    *Mem
	name    string
	entries []fs.DirEntry
	off     int
}

func (d *memDir) Stat() (fs.FileInfo, error) {
	return fileInfo{name: path.Base(d.name), dir: true}, nil
}
func (d *memDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: fs.ErrInvalid}
}
func (d *memDir) Close() error { return nil }
func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		rest := d.entries[d.off:]
		d.off = len(d.entries)
		return rest, nil
	}
	if d.off >= len(d.entries) {
		return nil, io.EOF
	}
	end := d.off + n
	if end > len(d.entries) {
		end = len(d.entries)
	}
	rest := d.entries[d.off:end]
	d.off = end
	return rest, nil
}

// fileInfo is the fs.FileInfo for in-memory entries.
type fileInfo struct {
	name string
	size int64
	dir  bool
}

func (fi fileInfo) Name() string { return fi.name }
func (fi fileInfo) Size() int64  { return fi.size }
func (fi fileInfo) Mode() fs.FileMode {
	if fi.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (fi fileInfo) ModTime() time.Time { return time.Time{} }
func (fi fileInfo) IsDir() bool        { return fi.dir }
func (fi fileInfo) Sys() any           { return nil }

// --- on-disk implementation ----------------------------------------------

// Dir is a WritableFS rooted at an on-disk directory. Reads go through
// os.DirFS; WriteFile creates parent directories as needed.
type Dir struct {
	root string
	fs.FS
}

// NewDir returns a WritableFS rooted at the given directory.
func NewDir(root string) *Dir { return &Dir{root: root, FS: os.DirFS(root)} }

var _ WritableFS = (*Dir)(nil)

// WriteFile writes data to <root>/<name>, creating parent dirs.
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
