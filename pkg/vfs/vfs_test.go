package vfs

import (
	"io/fs"
	"sort"
	"testing"

	"github.com/stretchr/testify/suite"
)

type MemSuite struct {
	suite.Suite
	m *Mem
}

func TestMemSuite(t *testing.T) { suite.Run(t, new(MemSuite)) }

func (s *MemSuite) SetupTest() {
	s.m = NewMem()
	for _, f := range []string{
		"registry.json",
		"service/kind.json",
		"service/kind.schema.json",
		"cache/kind.json",
	} {
		s.Require().NoError(s.m.WriteFile(f, []byte("data:"+f)))
	}
}

func (s *MemSuite) TestWriteReadRoundTrip() {
	got, err := s.m.ReadFile("service/kind.json")
	s.Require().NoError(err)
	s.Equal("data:service/kind.json", string(got))
}

func (s *MemSuite) TestWriteFileCopiesData() {
	buf := []byte("original")
	s.Require().NoError(s.m.WriteFile("x.txt", buf))
	buf[0] = 'X' // mutate caller's slice
	got, _ := s.m.ReadFile("x.txt")
	s.Equal("original", string(got), "stored bytes must not alias the caller's slice")
}

func (s *MemSuite) TestNormalizesLeadingDotSlash() {
	s.Require().NoError(s.m.WriteFile("./a/b.txt", []byte("v")))
	got, err := s.m.ReadFile("a/b.txt")
	s.Require().NoError(err)
	s.Equal("v", string(got))
}

func (s *MemSuite) TestReadFileMissing() {
	_, err := s.m.ReadFile("nope.json")
	s.ErrorIs(err, fs.ErrNotExist)
}

func (s *MemSuite) TestReadFileOnDirIsError() {
	_, err := s.m.ReadFile("service")
	s.Require().Error(err)
	s.NotErrorIs(err, fs.ErrNotExist) // it exists, just not a file
}

func (s *MemSuite) TestStat() {
	fi, err := s.m.Stat("service/kind.json")
	s.Require().NoError(err)
	s.False(fi.IsDir())
	s.EqualValues(len("data:service/kind.json"), fi.Size())

	di, err := s.m.Stat("service")
	s.Require().NoError(err)
	s.True(di.IsDir())
}

func (s *MemSuite) TestReadDirImmediateChildren() {
	entries, err := s.m.ReadDir(".")
	s.Require().NoError(err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	s.Equal([]string{"cache", "registry.json", "service"}, names)

	// cache/ and service/ are synthesized directories.
	for _, e := range entries {
		if e.Name() == "service" {
			s.True(e.IsDir())
		}
		if e.Name() == "registry.json" {
			s.False(e.IsDir())
		}
	}
}

func (s *MemSuite) TestWalkDirVisitsEverything() {
	var files []string
	err := fs.WalkDir(s.m, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	s.Require().NoError(err)
	sort.Strings(files)
	s.Equal([]string{
		"cache/kind.json",
		"registry.json",
		"service/kind.json",
		"service/kind.schema.json",
	}, files)
}

func (s *MemSuite) TestGlob() {
	got, err := fs.Glob(s.m, "*/kind.json")
	s.Require().NoError(err)
	sort.Strings(got)
	s.Equal([]string{"cache/kind.json", "service/kind.json"}, got)
}

func (s *MemSuite) TestSub() {
	sub, err := fs.Sub(s.m, "service")
	s.Require().NoError(err)
	got, err := fs.ReadFile(sub, "kind.json")
	s.Require().NoError(err)
	s.Equal("data:service/kind.json", string(got))

	entries, err := fs.ReadDir(sub, ".")
	s.Require().NoError(err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	s.Equal([]string{"kind.json", "kind.schema.json"}, names)
}

// TestImplementsFSReadFile confirms fs.ReadFile picks the optimized
// ReadFile path (the type satisfies fs.ReadFileFS).
func (s *MemSuite) TestReadFileViaFSHelper() {
	got, err := fs.ReadFile(s.m, "registry.json")
	s.Require().NoError(err)
	s.Equal("data:registry.json", string(got))
}
