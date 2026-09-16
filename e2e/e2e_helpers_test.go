// Test scaffolding for the end-to-end suite: the suite type, the binary
// build, and the helpers that drive it. Kept apart from e2e_test.go so
// that file reads as a list of scenarios and nothing else.
//
// This is a _test.go file rather than a plain .go one so `testing` and
// testify stay out of the package's non-test build.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type E2ESuite struct {
	suite.Suite
	bin  string // the veil binary under test
	root string // absolute path of ./playground
}

// SetupSuite compiles the binary once into a temp dir, so the tests
// exercise whatever is in the working tree rather than a stale ./veil.
func (s *E2ESuite) SetupSuite() {
	repo, err := filepath.Abs("..")
	s.Require().NoError(err)
	s.root = filepath.Join(repo, "e2e", "playground")

	s.bin = filepath.Join(s.T().TempDir(), "veil")
	build := exec.Command("go", "build", "-o", s.bin, "./cmd/veil")
	build.Dir = repo
	out, err := build.CombinedOutput()
	s.Require().NoError(err, "building veil: %s", out)

	// The compiled registry is build output, not source: it is
	// gitignored, so a clean checkout has none. Remove whatever a
	// previous run left and compile it fresh, which is also the only way
	// these tests prove `veil build` produces something the rest of the
	// CLI can actually consume.
	s.Require().NoError(os.RemoveAll(filepath.Join(s.root, "public")))
	built, err := s.run("build")
	s.Require().NoError(err, "veil build: %s", built)
}

// run invokes the binary inside the playground and returns its combined
// output. The error is returned rather than asserted so tests can cover
// the failure paths too.
func (s *E2ESuite) run(args ...string) (string, error) {
	s.T().Helper()
	cmd := exec.Command(s.bin, args...)
	cmd.Dir = s.root
	cmd.Env = append(os.Environ(), "VEIL_OUTPUT=json")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// render renders one resource into a fresh directory and returns it.
func (s *E2ESuite) render(resource string, extra ...string) string {
	s.T().Helper()
	out := s.T().TempDir()
	args := append([]string{"render", resource, "--out", out, "--quiet"}, extra...)
	stdout, err := s.run(args...)
	s.Require().NoError(err, "rendering %s: %s", resource, stdout)
	return out
}

// sandbox copies the whole playground — compiled registry included — to
// a temp dir. Fixtures that break project-wide operations (a dependency
// cycle fails `veil build` for every resource, not just its own) go here
// rather than into the shared tree.
func (s *E2ESuite) sandbox() string {
	s.T().Helper()
	dst := filepath.Join(s.T().TempDir(), "playground")
	cp := exec.Command("cp", "-R", s.root, dst)
	out, err := cp.CombinedOutput()
	s.Require().NoError(err, "copying playground: %s", out)
	return dst
}

// runIn is run against an explicit project directory.
func (s *E2ESuite) runIn(dir string, args ...string) (string, error) {
	s.T().Helper()
	cmd := exec.Command(s.bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "VEIL_OUTPUT=json")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// write drops a resource file into a sandbox.
func (s *E2ESuite) write(dir, rel, body string) {
	s.T().Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o755))
	s.Require().NoError(os.WriteFile(path, []byte(body), 0o644))
}

// renderFails asserts a render fails, and returns its error message.
func (s *E2ESuite) renderFails(resource string, extra ...string) string {
	s.T().Helper()
	args := append([]string{"render", resource, "--out", s.T().TempDir()}, extra...)
	out, err := s.run(args...)
	s.Require().Error(err, "expected %s to fail, got: %s", resource, out)
	return s.errorMessage(out)
}

// errorMessage pulls the message out of the CLI's JSON envelope. Reading
// the raw output instead would mean asserting against JSON escaping —
// a lineage like "a -> b" arrives as "a -> b".
func (s *E2ESuite) errorMessage(out string) string {
	s.T().Helper()
	var envelope struct {
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	// The envelope is the last line; anything before it is log output.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	s.Require().NoError(json.Unmarshal([]byte(last), &envelope), "decoding CLI output: %s", out)
	s.Equal("error", envelope.Outcome)
	return envelope.Error
}

func (s *E2ESuite) read(dir, resource, path string) string {
	s.T().Helper()
	data, err := os.ReadFile(filepath.Join(dir, resource, filepath.FromSlash(path)))
	s.Require().NoError(err)
	return string(data)
}

// snapshotRegistry reads every built artifact into one map, so two builds
// can be compared byte for byte.
func (s *E2ESuite) snapshotRegistry() map[string]string {
	s.T().Helper()
	root := filepath.Join(s.root, "public")
	files := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		files[rel] = string(data)
		return nil
	})
	s.Require().NoError(err)
	s.Require().NotEmpty(files)
	return files
}

func sortedAscending(lines []string) bool {
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			return false
		}
	}
	return true
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// compiledKind is the slice of a built kind.json these tests care
// about: the file list and the deprecated mirror of it.
type compiledKind struct {
	Files []struct {
		Path     string  `json:"path"`
		Contents string  `json:"contents"`
		Schema   *string `json:"schema"`
		Render   bool    `json:"render"`
	} `json:"files"`
	Sources []struct {
		Path     string  `json:"path"`
		Contents string  `json:"contents"`
		Schema   *string `json:"schema"`
	} `json:"sources"`
}

// readRegistryKind decodes a compiled kind from the built registry.
func (s *E2ESuite) readRegistryKind(name string) compiledKind {
	s.T().Helper()
	raw, err := os.ReadFile(filepath.Join(s.root, "public", "r", name, "kind.json"))
	s.Require().NoError(err)
	var ck compiledKind
	s.Require().NoError(json.Unmarshal(raw, &ck))
	return ck
}
