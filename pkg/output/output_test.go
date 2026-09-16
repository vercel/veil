package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/vercel/veil/pkg/hook"
)

type OutputSuite struct {
	suite.Suite
	out string
}

func TestOutputSuite(t *testing.T) { suite.Run(t, new(OutputSuite)) }

func (s *OutputSuite) SetupTest() {
	root, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	s.out = filepath.Join(root, "out")
}

func (s *OutputSuite) root(kind, name string, files map[string]string) Root {
	bundle := hook.Bundle{}
	for path, content := range files {
		bundle[path] = hook.File{Path: path, Content: content}
	}
	return Root{Kind: kind, Name: name, Bundle: bundle}
}

func (s *OutputSuite) write(path, content string) {
	s.Require().NoError(os.MkdirAll(filepath.Dir(filepath.Join(s.out, path)), 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(s.out, path), []byte(content), 0644))
}

func (s *OutputSuite) content(path string) string {
	data, err := os.ReadFile(filepath.Join(s.out, path))
	s.Require().NoError(err)
	return string(data)
}

func (s *OutputSuite) state() manifest {
	var state manifest
	s.Require().NoError(json.Unmarshal([]byte(s.content(manifestPath)), &state))
	return state
}

func (s *OutputSuite) TestRenameAndPartialSelectionRetainOtherRoots() {
	a := s.root("@one/service", "a", map[string]string{"old.yaml": "old"})
	b := s.root("@two/service", "b", map[string]string{"keep.yaml": "keep"})
	s.Require().NoError(Publish(s.out, []Root{a, b}, Options{}))
	s.write("notes.txt", "handwritten")
	a.Bundle = hook.Bundle{"template": {Path: "new.yaml", Content: "new"}}
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	s.NoFileExists(filepath.Join(s.out, "a/old.yaml"))
	s.Equal("new", s.content("a/new.yaml"))
	s.Equal("keep", s.content("b/keep.yaml"))
	s.Equal("handwritten", s.content("notes.txt"))
	s.Require().NoError(Remove(s.out, "@one/service", "a"))
	s.NoFileExists(filepath.Join(s.out, "a/new.yaml"))
	s.Equal("keep", s.content("b/keep.yaml"))
	s.Len(s.state().Roots, 1)
}

func (s *OutputSuite) TestAdoptionRequiresIdenticalBytesAndExplicitFlag() {
	a := s.root("worker", "a", map[string]string{"file": "generated"})
	s.write("a/file", "handwritten")
	s.Error(Publish(s.out, []Root{a}, Options{Adopt: true}))
	s.Equal("handwritten", s.content("a/file"))
	s.NoFileExists(filepath.Join(s.out, manifestPath))
	s.write("a/file", "generated")
	s.Error(Publish(s.out, []Root{a}, Options{}))
	s.NoFileExists(filepath.Join(s.out, manifestPath))
	s.Require().NoError(Publish(s.out, []Root{a}, Options{Adopt: true}))
	s.Require().NoError(Remove(s.out, "worker", "a"))
	s.NoFileExists(filepath.Join(s.out, "a/file"))
}

func (s *OutputSuite) TestModifiedTrackedFileBlocksWholeBatchAndRemoval() {
	a := s.root("worker", "a", map[string]string{"file": "old", "stale": "stale"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.content(manifestPath)
	s.write("a/stale", "local edit")
	a.Bundle = hook.Bundle{"file": {Path: "file", Content: "new"}}
	s.Error(Publish(s.out, []Root{a}, Options{Adopt: true}))
	s.Equal("old", s.content("a/file"))
	s.Equal("local edit", s.content("a/stale"))
	s.Equal(before, s.content(manifestPath))
	s.Error(Remove(s.out, "worker", "a"))
	s.Equal("old", s.content("a/file"))
	s.Equal(before, s.content(manifestPath))
}

func (s *OutputSuite) TestMissingTrackedFileIsNotSilentlyAccepted() {
	a := s.root("worker", "a", map[string]string{"file": "old"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.content(manifestPath)
	s.Require().NoError(os.Remove(filepath.Join(s.out, "a/file")))
	s.Error(Publish(s.out, []Root{a}, Options{}))
	s.Error(Remove(s.out, "worker", "a"))
	s.Equal(before, s.content(manifestPath))
}

func (s *OutputSuite) TestExactQualifiedIdentityAndEmptyRootRemoval() {
	a := s.root("@one/worker", "a", nil)
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	s.Error(Remove(s.out, "@two/worker", "a"))
	s.Require().NoError(Remove(s.out, "@one/worker", "a"))
	s.Empty(s.state().Roots)
}

func (s *OutputSuite) TestConflictingOwnerCannotBeAdoptedOrTransferred() {
	a := s.root("@one/worker", "same", map[string]string{"file": "old"})
	b := s.root("@two/worker", "same", map[string]string{"file": "old"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.content(manifestPath)
	s.Error(Publish(s.out, []Root{b}, Options{Adopt: true}))
	a.Bundle = nil
	s.Error(Publish(s.out, []Root{a, b}, Options{}))
	s.Equal("old", s.content("same/file"))
	s.Equal(before, s.content(manifestPath))
}

func (s *OutputSuite) TestPreflightRejectsNormalizedAndParentCollisions() {
	for _, paths := range [][2]string{{"file", "./file"}, {"file", "sub/../file"}, {"parent", "parent/child"}, {"FILE", "file"}} {
		s.Run(paths[0]+":"+paths[1], func() {
			a := Root{Kind: "worker", Name: "a", Bundle: hook.Bundle{
				"first": {Path: paths[0], Content: "one"}, "second": {Path: paths[1], Content: "two"},
			}}
			err := Publish(s.out, []Root{a}, Options{})
			s.Require().Error(err)
			s.Contains(err.Error(), "first")
			s.Contains(err.Error(), "second")
			s.NoFileExists(filepath.Join(s.out, manifestPath))
			s.NoFileExists(filepath.Join(s.out, journalPath))
			s.NoDirExists(filepath.Join(s.out, "a"))
		})
	}
}

func (s *OutputSuite) TestPreflightRejectsEscapesAndReservedMetadata() {
	for _, path := range []string{"../../escape", "/absolute", "../.veil/manifest.json", "../.VEIL/lock", "..\\escape"} {
		s.Run(path, func() {
			a := s.root("worker", "a", map[string]string{path: "bad"})
			s.Error(Publish(s.out, []Root{a}, Options{}))
			s.NoFileExists(filepath.Join(s.out, manifestPath))
			s.NoFileExists(filepath.Join(s.out, journalPath))
		})
	}
	// Routing outside a resource subdirectory but inside --out is supported.
	a := s.root("worker", "a", map[string]string{"../shared.yaml": "okay"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	s.Equal("okay", s.content("shared.yaml"))
}

func (s *OutputSuite) TestSymlinkedDestinationAndOutputRootAreRejected() {
	outside, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	s.Require().NoError(os.MkdirAll(s.out, 0755))
	s.Require().NoError(os.Symlink(outside, filepath.Join(s.out, "a")))
	a := s.root("worker", "a", map[string]string{"file": "bad"})
	s.Error(Publish(s.out, []Root{a}, Options{}))
	s.NoFileExists(filepath.Join(outside, "file"))
	s.NoFileExists(filepath.Join(s.out, manifestPath))
	linked := filepath.Join(filepath.Dir(s.out), "linked")
	s.Require().NoError(os.Symlink(outside, linked))
	s.Error(Publish(linked, []Root{a}, Options{}))
	s.Error(Publish(filepath.Join(linked, "child"), []Root{a}, Options{}))
	s.NoDirExists(filepath.Join(outside, metadata))
}

func (s *OutputSuite) TestLockBlocksPublisherWithoutChangingManifest() {
	a := s.root("worker", "a", map[string]string{"file": "old"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.content(manifestPath)
	s.write(lockPath, "pid=123 host=example")
	err := Publish(s.out, []Root{a}, Options{})
	s.Require().Error(err)
	s.Contains(err.Error(), "pid=123")
	s.Equal(before, s.content(manifestPath))
	s.Equal("pid=123 host=example", s.content(lockPath))
}

// stageIntent models a process killed after persisting its plan but before (or
// between) output writes. Recovery crosses the same public seam as the CLI.
func (s *OutputSuite) stageIntent() manifest {
	a := s.root("worker", "a", map[string]string{"change": "old", "delete": "gone"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.state()
	id, err := identity("worker", "a")
	s.Require().NoError(err)
	after := manifest{Version: 1, Roots: map[string]map[string]string{id: {"a/change": hash("new"), "a/add": hash("added")}}}
	pending := intent{Before: before, After: after, Operations: []operation{
		{Path: "a/add", After: hash("added"), Content: "added"},
		{Path: "a/change", Before: hash("old"), After: hash("new"), Content: "new"},
		{Path: "a/delete", Before: hash("gone")},
	}}
	data, err := json.Marshal(pending)
	s.Require().NoError(err)
	s.write(journalPath, string(data))
	return after
}

func (s *OutputSuite) TestInterruptedPublicationResumesAndCommitsManifest() {
	after := s.stageIntent()
	before := s.content(manifestPath)
	s.write("a/add", "added")
	s.Error(Publish(s.out, []Root{s.root("worker", "a", nil)}, Options{}))
	s.Equal(before, s.content(manifestPath))
	s.Require().NoError(Recover(s.out))
	s.Equal("added", s.content("a/add"))
	s.Equal("new", s.content("a/change"))
	s.NoFileExists(filepath.Join(s.out, "a/delete"))
	s.Equal(after, s.state())
	s.NoFileExists(filepath.Join(s.out, journalPath))
}

func (s *OutputSuite) TestRecoveryRejectsUnknownEditsBeforeAnyFurtherWrites() {
	s.stageIntent()
	before := s.content(manifestPath)
	s.write("a/delete", "manual edit")
	s.Error(Recover(s.out))
	s.NoFileExists(filepath.Join(s.out, "a/add"))
	s.Equal("old", s.content("a/change"))
	s.Equal("manual edit", s.content("a/delete"))
	s.Equal(before, s.content(manifestPath))
	s.FileExists(filepath.Join(s.out, journalPath))
	s.write("a/delete", "gone")
	s.Require().NoError(Recover(s.out))
	s.NoFileExists(filepath.Join(s.out, "a/delete"))
}

func (s *OutputSuite) TestRecoveryAfterManifestCommitFinishesJournalCleanup() {
	after := s.stageIntent()
	s.write("a/add", "added")
	s.write("a/change", "new")
	s.Require().NoError(os.Remove(filepath.Join(s.out, "a/delete")))
	data, err := json.Marshal(after)
	s.Require().NoError(err)
	s.write(manifestPath, string(data))
	s.Require().NoError(Recover(s.out))
	s.Equal(after, s.state())
	s.NoFileExists(filepath.Join(s.out, journalPath))
}

func (s *OutputSuite) TestFailedPublicationKeepsIntentAndCanRecover() {
	a := s.root("worker", "a", map[string]string{"file": "old"})
	s.Require().NoError(Publish(s.out, []Root{a}, Options{}))
	before := s.state()
	id, err := identity("worker", "a")
	s.Require().NoError(err)
	after := manifest{Version: 1, Roots: map[string]map[string]string{id: {"a/file": hash("new")}}}
	pending := intent{Before: before, After: after, Operations: []operation{{Path: "a/file", Before: hash("old"), After: hash("new"), Content: "new"}}}
	data, err := json.Marshal(pending)
	s.Require().NoError(err)
	s.write(journalPath, string(data))
	// Fail manifest replacement after the output write succeeds.
	s.Require().NoError(os.Remove(filepath.Join(s.out, manifestPath)))
	s.Require().NoError(os.Mkdir(filepath.Join(s.out, manifestPath), 0755))
	root, err := os.OpenRoot(s.out)
	s.Require().NoError(err)
	s.Error(finish(root, pending))
	s.Require().NoError(root.Close())
	s.Equal("new", s.content("a/file"))
	s.FileExists(filepath.Join(s.out, journalPath))
	s.Require().NoError(os.Remove(filepath.Join(s.out, manifestPath)))
	data, err = json.Marshal(before)
	s.Require().NoError(err)
	s.write(manifestPath, string(data))
	s.Require().NoError(Recover(s.out))
	s.Equal(after, s.state())
	s.NoFileExists(filepath.Join(s.out, journalPath))
}
