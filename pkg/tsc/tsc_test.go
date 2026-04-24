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

func (s *TscSuite) TestCheckNoOpOnEmptyDir() {
	s.Require().NoError(s.checker.Check(s.dir))
}

func (s *TscSuite) TestCheckPassesCleanCode() {
	s.write("ok.ts", `export const x: number = 1;`)
	s.Require().NoError(s.checker.Check(s.dir))
}

func (s *TscSuite) TestCheckFailsOnTypeError() {
	s.write("bad.ts", `export const x: number = "not a number";`)
	err := s.checker.Check(s.dir)
	s.Require().Error(err)
	s.Contains(err.Error(), "typecheck failed")
	s.Contains(err.Error(), "bad.ts")
}

func (s *TscSuite) TestBinReturnsResolvedPath() {
	s.NotEmpty(s.checker.Bin())
}

func (s *TscSuite) TestProjectTsconfigIsRespected() {
	// Default flags use --strict; a tsconfig that turns strict off should
	// flip whether an implicit-any file is accepted. Running without the
	// tsconfig first proves tsc rejects the file; then we drop the
	// tsconfig and expect the same file to pass.
	src := filepath.Join(s.dir, "src")
	s.Require().NoError(os.MkdirAll(src, 0755))
	s.Require().NoError(os.WriteFile(
		filepath.Join(src, "loose.ts"),
		[]byte("export function f(x) { return x; }\n"),
		0644,
	))

	err := s.checker.Check(src)
	s.Require().Error(err, "strict default should reject implicit-any")

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

	s.Require().NoError(s.checker.Check(src), "project tsconfig with strict:false should accept implicit-any")
}

func (s *TscSuite) write(name, contents string) {
	s.Require().NoError(os.WriteFile(filepath.Join(s.dir, name), []byte(contents), 0644))
}
