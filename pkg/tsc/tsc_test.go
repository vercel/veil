package tsc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type TscSuite struct {
	suite.Suite
	dir     string
	checker Checker
}

func TestTscSuite(t *testing.T) {
	suite.Run(t, new(TscSuite))
}

func (s *TscSuite) SetupTest() {
	s.checker = Find()
	if s.checker == nil {
		s.T().Skip("no tsc/tsgo on PATH")
	}
	s.dir = s.T().TempDir()
}

func (s *TscSuite) TestCheckNoOpOnEmptyList() {
	s.Require().NoError(s.checker.Check(nil))
	s.Require().NoError(s.checker.Check([]string{}))
}

func (s *TscSuite) TestCheckPassesCleanCode() {
	path := s.write("ok.ts", `export const x: number = 1;`)
	s.Require().NoError(s.checker.Check([]string{path}))
}

func (s *TscSuite) TestCheckFailsOnTypeError() {
	path := s.write("bad.ts", `export const x: number = "not a number";`)
	err := s.checker.Check([]string{path})
	s.Require().Error(err)
	s.Contains(err.Error(), "typecheck failed")
	s.Contains(err.Error(), "bad.ts")
}

// TestCheckIgnoresAncestorTsconfig is the regression test for the
// monorepo OOM. Earlier versions walked the directory tree looking for
// a tsconfig.json and ran tsc -p against it, which in a giant monorepo
// dragged the entire repo into the type-check. The new behavior is to
// pass only the listed hook files with self-contained flags, so an
// ancestor tsconfig that loosens strictness must NOT take effect.
func (s *TscSuite) TestCheckIgnoresAncestorTsconfig() {
	src := filepath.Join(s.dir, "src")
	s.Require().NoError(os.MkdirAll(src, 0755))
	loose := filepath.Join(src, "loose.ts")
	s.Require().NoError(os.WriteFile(
		loose,
		[]byte("export function f(x) { return x; }\n"),
		0644,
	))

	s.Require().NoError(os.WriteFile(filepath.Join(s.dir, "tsconfig.json"), []byte(`{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": false,
    "noEmit": true
  },
  "include": ["src/**/*.ts"]
}`), 0644))

	err := s.checker.Check([]string{loose})
	s.Require().Error(err, "ancestor tsconfig with strict:false must not relax the strict default")
	s.Contains(err.Error(), "loose.ts")
}

// TestCheckIgnoresCwdTsconfig is the regression test for TS5112. tsgo
// walks up from its own cwd looking for a tsconfig.json; if it finds one
// while explicit files were passed, it refuses to run. Reproduce by
// chdir'ing into a directory that contains a tsconfig.json before
// invoking Check.
func (s *TscSuite) TestCheckIgnoresCwdTsconfig() {
	cfgDir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(cfgDir, "tsconfig.json"), []byte(`{
  "compilerOptions": {"strict": true, "noEmit": true},
  "include": ["**/*.ts"]
}`), 0644))
	s.T().Chdir(cfgDir)

	path := s.write("ok.ts", `export const x: number = 1;`)
	s.Require().NoError(s.checker.Check([]string{path}))
}

func (s *TscSuite) TestBinReturnsResolvedPath() {
	s.NotEmpty(s.checker.Bin())
}

func (s *TscSuite) write(name, contents string) string {
	path := filepath.Join(s.dir, name)
	s.Require().NoError(os.WriteFile(path, []byte(contents), 0644))
	return path
}
