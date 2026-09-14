package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

type VFSSuite struct {
	suite.Suite
}

func TestVFSSuite(t *testing.T) {
	suite.Run(t, new(VFSSuite))
}

func (s *VFSSuite) TestDirReadsWritesAndReportsItsRoot() {
	root := s.T().TempDir()
	d := NewDir(root)

	s.Require().NoError(d.WriteFile("r/alpha/kind.json", []byte(`{"name":"alpha"}`)))
	data, err := fs.ReadFile(d, "r/alpha/kind.json")
	s.Require().NoError(err)
	s.Equal(`{"name":"alpha"}`, string(data))
	// WriteFile creates parent directories.
	onDisk, err := os.ReadFile(filepath.Join(root, "r", "alpha", "kind.json"))
	s.Require().NoError(err)
	s.Equal(`{"name":"alpha"}`, string(onDisk))

	s.Equal(root, d.Root())
	s.Equal(root, d.Abs())
	s.True(filepath.IsAbs(d.Abs()))
}

// TestDirAbsResolvesRelativeRoots is the case Root alone can't cover:
// the Filesystem was configured with a path relative to the working directory.
func (s *VFSSuite) TestDirAbsResolvesRelativeRoots() {
	d := NewDir("testdata")
	s.Equal("testdata", d.Root())
	cwd, err := os.Getwd()
	s.Require().NoError(err)
	s.Equal(filepath.Join(cwd, "testdata"), d.Abs())
}

// TestMemHasNoLocation pins the in-memory contract: it reads and writes
// but has nowhere on disk to point at, which is what FSStore.Location
// keys off to decide whether it can report a path.
func (s *VFSSuite) TestMemHasNoLocation() {
	m := NewMem()
	s.Require().NoError(m.WriteFile("registry.json", []byte(`{"kinds":{}}`)))
	data, err := fs.ReadFile(m, "registry.json")
	s.Require().NoError(err)
	s.Equal(`{"kinds":{}}`, string(data))
	s.Empty(m.Root())
	s.Empty(m.Abs())
}

// TestDirReadsThroughEveryExtension exercises the extension methods on
// a real directory. That Dir satisfies each interface is enforced at
// compile time by vfs.go's var block and by FS embedding them all.
func (s *VFSSuite) TestDirReadsThroughEveryExtension() {
	root := s.T().TempDir()
	s.Require().NoError(os.MkdirAll(filepath.Join(root, "hooks"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(root, "hooks", "render.ts"), []byte("export default {}"), 0644))
	d := NewDir(root)

	entries, err := fs.ReadDir(d, "hooks")
	s.Require().NoError(err)
	s.Require().Len(entries, 1)
	s.Equal("render.ts", entries[0].Name())

	info, err := fs.Stat(d, "hooks/render.ts")
	s.Require().NoError(err)
	s.False(info.IsDir())

	matches, err := fs.Glob(d, "hooks/*.ts")
	s.Require().NoError(err)
	s.Equal([]string{"hooks/render.ts"}, matches)
}

// TestDirSubStaysRooted confirms a descent keeps both the root pairing
// and write access rather than degrading to a bare fs.FS.
func (s *VFSSuite) TestDirSubStaysRooted() {
	root := s.T().TempDir()
	s.Require().NoError(os.MkdirAll(filepath.Join(root, "hooks"), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(root, "hooks", "render.ts"), []byte("x"), 0644))

	sub, err := fs.Sub(NewDir(root), "hooks")
	s.Require().NoError(err)
	rooted, ok := sub.(Filesystem)
	s.Require().True(ok, "Sub must return a vfs.FS")
	s.Equal(filepath.Join(root, "hooks"), rooted.Root())

	data, err := fs.ReadFile(rooted, "render.ts")
	s.Require().NoError(err)
	s.Equal("x", string(data))

	s.Require().NoError(rooted.WriteFile("validate.ts", []byte("y")))
	onDisk, err := os.ReadFile(filepath.Join(root, "hooks", "validate.ts"))
	s.Require().NoError(err)
	s.Equal("y", string(onDisk))
}

// TestBothImplementationsSatisfyTheInterfaces is the compile-time
// contract restated as a runtime check: every io/fs extension, on both
// backings, so neither can silently regress to Open-only.
func (s *VFSSuite) TestBothImplementationsSatisfyTheInterfaces() {
	for name, f := range map[string]Filesystem{"Dir": NewDir(s.T().TempDir()), "Mem": NewMem()} {
		s.Run(name, func() {
			var ro FS = f
			_, ok := ro.(fs.GlobFS)
			s.True(ok, "GlobFS")
			_, ok = ro.(fs.ReadDirFS)
			s.True(ok, "ReadDirFS")
			_, ok = ro.(fs.ReadFileFS)
			s.True(ok, "ReadFileFS")
			_, ok = ro.(fs.ReadLinkFS)
			s.True(ok, "ReadLinkFS")
			_, ok = ro.(fs.StatFS)
			s.True(ok, "StatFS")
			_, ok = ro.(fs.SubFS)
			s.True(ok, "SubFS")
		})
	}
}

// TestMemSynthesizesDirectories covers the flat map behaving like a
// tree: listing, statting, globbing and walking paths that were never
// written as directories in their own right.
func (s *VFSSuite) TestMemSynthesizesDirectories() {
	m := NewMem()
	for _, name := range []string{"registry.json", "r/alpha/kind.json", "r/alpha/kind.schema.json", "r/beta/kind.json"} {
		s.Require().NoError(m.WriteFile(name, []byte(`{"n":"`+name+`"}`)))
	}

	entries, err := fs.ReadDir(m, ".")
	s.Require().NoError(err)
	s.Equal([]string{"r", "registry.json"}, names(entries))
	s.True(entries[0].IsDir(), "r is a directory")
	s.False(entries[1].IsDir(), "registry.json is a file")

	entries, err = fs.ReadDir(m, "r/alpha")
	s.Require().NoError(err)
	s.Equal([]string{"kind.json", "kind.schema.json"}, names(entries))

	info, err := fs.Stat(m, "r/alpha")
	s.Require().NoError(err)
	s.True(info.IsDir())
	info, err = fs.Stat(m, "r/alpha/kind.json")
	s.Require().NoError(err)
	s.False(info.IsDir())
	s.EqualValues(len(`{"n":"r/alpha/kind.json"}`), info.Size())

	matches, err := fs.Glob(m, "r/*/kind.json")
	s.Require().NoError(err)
	s.Equal([]string{"r/alpha/kind.json", "r/beta/kind.json"}, matches)

	// fs.WalkDir needs no interface of its own — it rides on ReadDir.
	var walked []string
	s.Require().NoError(fs.WalkDir(m, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			walked = append(walked, p)
		}
		return nil
	}))
	s.Equal([]string{"r/alpha/kind.json", "r/alpha/kind.schema.json", "r/beta/kind.json", "registry.json"}, walked)

	data, err := fs.ReadFile(m, "r/beta/kind.json")
	s.Require().NoError(err)
	s.Equal(`{"n":"r/beta/kind.json"}`, string(data))

	_, err = fs.ReadDir(m, "nope")
	s.ErrorIs(err, fs.ErrNotExist)
	_, err = fs.Stat(m, "nope")
	s.ErrorIs(err, fs.ErrNotExist)
}

// TestMemSubReadsThrough confirms Sub is a view over the same storage,
// not a copy.
func (s *VFSSuite) TestMemSubReadsThrough() {
	m := NewMem()
	s.Require().NoError(m.WriteFile("r/alpha/kind.json", []byte("{}")))

	sub, err := fs.Sub(m, "r")
	s.Require().NoError(err)
	data, err := fs.ReadFile(sub, "alpha/kind.json")
	s.Require().NoError(err)
	s.Equal("{}", string(data))

	s.Require().NoError(m.WriteFile("r/gamma/kind.json", []byte("[]")))
	data, err = fs.ReadFile(sub, "gamma/kind.json")
	s.Require().NoError(err)
	s.Equal("[]", string(data))
}

// TestMemHasNoSymlinks pins why Mem implements ReadLinkFS at all: so
// callers need no capability check, not because links exist.
func (s *VFSSuite) TestMemHasNoSymlinks() {
	m := NewMem()
	s.Require().NoError(m.WriteFile("kind.json", []byte("{}")))
	_, err := fs.ReadLink(m, "kind.json")
	s.Require().Error(err)
	info, err := fs.Lstat(m, "kind.json")
	s.Require().NoError(err)
	s.False(info.IsDir())
}

func names(entries []fs.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

// TestWrapAdaptsAnyFS covers the adapter that lets a bare fs.FS — a
// test fixture, an embed.FS — be passed where an FS is wanted, without
// losing an extension along the way.
func (s *VFSSuite) TestWrapAdaptsAnyFS() {
	w := New(fstest.MapFS{
		"r/alpha/kind.json": &fstest.MapFile{Data: []byte("{}")},
		"registry.json":     &fstest.MapFile{Data: []byte("[]")},
	}, "")
	s.Empty(w.Root())
	s.Empty(w.Abs())

	_, ok := w.(fs.GlobFS)
	s.True(ok, "GlobFS")
	_, ok = w.(fs.ReadDirFS)
	s.True(ok, "ReadDirFS")
	_, ok = w.(fs.ReadLinkFS)
	s.True(ok, "ReadLinkFS")
	_, ok = w.(fs.SubFS)
	s.True(ok, "SubFS")

	data, err := fs.ReadFile(w, "r/alpha/kind.json")
	s.Require().NoError(err)
	s.Equal("{}", string(data))

	entries, err := fs.ReadDir(w, "r")
	s.Require().NoError(err)
	s.Equal([]string{"alpha"}, names(entries))

	matches, err := fs.Glob(w, "*.json")
	s.Require().NoError(err)
	s.Equal([]string{"registry.json"}, matches)
}

// TestWrapKeepsRootAcrossSub confirms a wrapped tree reports a root, and
// that descending into it joins rather than drops the pairing.
func (s *VFSSuite) TestWrapKeepsRootAcrossSub() {
	w := New(fstest.MapFS{"r/alpha/kind.json": &fstest.MapFile{Data: []byte("{}")}}, "project")
	s.Equal("project", w.Root())
	cwd, err := os.Getwd()
	s.Require().NoError(err)
	s.Equal(filepath.Join(cwd, "project"), w.Abs())

	sub, err := fs.Sub(w, "r")
	s.Require().NoError(err)
	rooted, ok := sub.(FS)
	s.Require().True(ok, "Sub must return a vfs.FS")
	s.Equal(filepath.Join("project", "r"), rooted.Root())

	data, err := fs.ReadFile(rooted, "alpha/kind.json")
	s.Require().NoError(err)
	s.Equal("{}", string(data))
}
