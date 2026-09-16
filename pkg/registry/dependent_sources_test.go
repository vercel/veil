package registry

import (
	"testing"

	"github.com/stretchr/testify/suite"
	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"google.golang.org/protobuf/proto"
)

type DependentSourcesSuite struct{ suite.Suite }

func TestDependentSourcesSuite(t *testing.T) { suite.Run(t, new(DependentSourcesSuite)) }

func (s *DependentSourcesSuite) TestIdentityPreservesEveryComponent() {
	ids := map[string]bool{}
	for _, kind := range []string{"table", "vendor/table", "vendor%2Ftable"} {
		for _, name := range []string{"same", "a/b", "a%2Fb", ".", ".."} {
			for _, source := range []string{"a.json", "./a.json", "a/b.json", "a%2Fb.json", ".", ".."} {
				id := DependencySourceID(kind, name, source)
				s.False(ids[id], id)
				ids[id] = true
			}
		}
	}
	s.Equal("dependencies/vendor%2Ftable/same/.%2Fa.json", DependencySourceID("vendor/table", "same", "./a.json"))
	s.Equal("dependencies/table/%2E%2E/%2E", DependencySourceID("table", "..", "."))
}

func (s *DependentSourcesSuite) TestDistinctSpellingKeepsDistinctSchemas() {
	sources, _, err := loadSources([]*veilv1.Source{
		{Path: "./a.json", Schema: proto.String(`{"type":"integer"}`)},
		{Path: "a.json", Schema: proto.String(`{"type":"string"}`)},
	})
	s.Require().NoError(err)
	s.NoError(sources[0].Validate(1))
	s.Error(sources[0].Validate("one"))
	s.NoError(sources[1].Validate("one"))
	s.Error(sources[1].Validate(1))
}
