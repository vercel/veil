package resource

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/vfs"
)

type CatalogSuite struct {
	suite.Suite
}

// stubKinds is a registry that invents a compiled kind for any name.
// forward lists the kinds that forward everything they depend on;
// accepts lists the consumer kinds every invented kind registers
// dependents for, since an inherited edge only lands on a consumer its
// target would have accepted. Defaults to the "svc" the graph fixtures
// use.
type stubKinds struct {
	forward map[string]bool
	accepts []string
}

func (k stubKinds) LoadKind(ref string) (*registry.LoadedKind, error) {
	accepts := k.accepts
	if accepts == nil {
		accepts = []string{"svc"}
	}
	dependents := make([]*veilv1.DependentHook, 0, len(accepts))
	for _, consumer := range accepts {
		dependents = append(dependents, &veilv1.DependentHook{Kind: consumer})
	}
	return &registry.LoadedKind{
		Kind: &veilv1.Kind{
			Name:                ref,
			ForwardDependencies: k.forward[ref],
			Hooks:               &veilv1.Hooks{Dependents: dependents},
		},
	}, nil
}

func TestCatalogSuite(t *testing.T) {
	suite.Run(t, new(CatalogSuite))
}

// catalogFor builds a catalog over one file per (name -> dependency
// names) entry. Every resource is kind "svc" so the fixtures stay
// about the shape of the graph.
func (s *CatalogSuite) catalogFor(graph map[string][]string) Catalog {
	return s.catalogWithKinds(graph, stubKinds{})
}

// catalogWithKinds is catalogFor against a specific set of compiled
// kinds. Each graph entry is "name" for a plain edge or "name!" to mark
// it forwarded on the edge itself.
func (s *CatalogSuite) catalogWithKinds(graph map[string][]string, kinds registry.Registry) Catalog {
	files := fstest.MapFS{}
	handles := make([]*Handle, 0, len(graph))
	for name, deps := range graph {
		name, _ = strings.CutSuffix(name, "!")
		doc := fmt.Sprintf("metadata:\n  kind: svc\n  name: %s\nspec: {}\n", name)
		if len(deps) > 0 {
			doc += "dependencies:\n"
			for _, d := range deps {
				target, fwd := strings.CutSuffix(d, "!")
				doc += fmt.Sprintf("  - kind: svc\n    name: %s\n    params:\n      via: %s\n", target, name)
				if fwd {
					doc += "    forward: true\n"
				}
			}
		}
		path := name + ".yaml"
		files[path] = &fstest.MapFile{Data: []byte(doc)}
		handles = append(handles, &Handle{Kind: "svc", Name: name, Path: path})
	}
	cat, err := NewCatalog(vfs.New(files, ""), handles, kinds)
	s.Require().NoError(err)
	return cat
}

// depNames lists the effective dependency targets, in order.
func depNames(r *Resource) []string {
	out := make([]string, 0, len(r.Dependencies))
	for _, d := range r.Dependencies {
		out = append(out, d.Resource.GetMetadata().GetName())
	}
	return out
}

// TestUnforwardedDependenciesStopAtTheirConsumer is the default: A -> B
// -> C leaves A holding only B. A service that depends on a package
// does not inherit the package's private table.
func (s *CatalogSuite) TestUnforwardedDependenciesStopAtTheirConsumer() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": {"c"}, "c": nil})

	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b"}, depNames(a))

	b := a.Dependencies[0].Resource
	s.Equal([]string{"c"}, depNames(b))
	s.Empty(depNames(b.Dependencies[0].Resource))
}

// TestForwardedDependenciesReachTheConsumer is the opt-in: B marks its
// edge to C forwarded, so A sees both B and C.
func (s *CatalogSuite) TestForwardedDependenciesReachTheConsumer() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": {"c!"}, "c": nil})

	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b", "c"}, depNames(a))
	// The forwarded edge keeps the params B declared on it.
	s.Equal("b", a.Dependencies[1].GetParams().AsMap()["via"])
}

// TestForwardingChains covers the transitive case: every hop forwards,
// so the far end arrives at the top.
func (s *CatalogSuite) TestForwardingChains() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": {"c!"}, "c": {"d!"}, "d": nil})

	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b", "c", "d"}, depNames(a))
}

// TestForwardingStopsAtAnUnforwardedHop pins that one unmarked hop
// breaks the chain — d is forwarded by c, but c is private to b.
func (s *CatalogSuite) TestForwardingStopsAtAnUnforwardedHop() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"d!"}, "d": nil})

	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b"}, depNames(a))

	// b still sees d, since b consumes c which forwards it.
	s.Equal([]string{"c", "d"}, depNames(a.Dependencies[0].Resource))
}

// TestKindPolicyForwardsWithoutPerEdgeFlags covers the kind-level flag:
// no resource sets `forward`, the kind forwards everything for them,
// and it chains the same way the per-edge flag does.
func (s *CatalogSuite) TestKindPolicyForwardsWithoutPerEdgeFlags() {
	graph := map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": nil}

	on := stubKinds{forward: map[string]bool{"svc": true}}
	a, err := s.catalogWithKinds(graph, on).LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b", "c", "d"}, depNames(a))

	// Off for everything leaves only the per-edge flags.
	a, err = s.catalogWithKinds(graph, stubKinds{}).LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b"}, depNames(a))
}

// TestKindPolicyAppliesToTheDeclaringKind pins which kind is consulted:
// the one declaring the edge, not the one being depended on. Only "mid"
// forwards, so its edge to the leaf reaches the top while the top's own
// kind forwarding nothing is irrelevant to what it receives.
func (s *CatalogSuite) TestKindPolicyAppliesToTheDeclaringKind() {
	files := fstest.MapFS{
		"top.yaml":  &fstest.MapFile{Data: []byte("metadata:\n  kind: top\n  name: t\nspec: {}\ndependencies:\n  - kind: mid\n    name: m\n")},
		"mid.yaml":  &fstest.MapFile{Data: []byte("metadata:\n  kind: mid\n  name: m\nspec: {}\ndependencies:\n  - kind: leaf\n    name: l\n")},
		"leaf.yaml": &fstest.MapFile{Data: []byte("metadata:\n  kind: leaf\n  name: l\nspec: {}\n")},
	}
	handles := []*Handle{
		{Kind: "top", Name: "t", Path: "top.yaml"},
		{Kind: "mid", Name: "m", Path: "mid.yaml"},
		{Kind: "leaf", Name: "l", Path: "leaf.yaml"},
	}
	cat, err := NewCatalog(vfs.New(files, ""), handles, stubKinds{
		forward: map[string]bool{"mid": true},
		accepts: []string{"top", "mid"},
	})
	s.Require().NoError(err)

	top, err := cat.LoadResource("top", "t")
	s.Require().NoError(err)
	s.Equal([]string{"m", "l"}, depNames(top))
}

// TestSharedDependencyIsOneInstance proves the memo table is shared
// across branches: a diamond resolves D once, so a walker's visited set
// can key on the pointer.
func (s *CatalogSuite) TestSharedDependencyIsOneInstance() {
	cat := s.catalogFor(map[string][]string{"a": {"b", "c"}, "b": {"d"}, "c": {"d"}, "d": nil})

	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Require().Len(a.Dependencies, 2)
	viaB := a.Dependencies[0].Resource.Dependencies[0].Resource
	viaC := a.Dependencies[1].Resource.Dependencies[0].Resource
	s.Same(viaB, viaC)

	// Reached directly, it is still the same instance.
	d, err := cat.LoadResource("svc", "d")
	s.Require().NoError(err)
	s.Same(viaB, d)
}

// TestCycleIsRejectedWithLineage covers the loop cases and the shape of
// the report: the chain that closes the cycle, in order, so the author
// can see which edge to cut.
func (s *CatalogSuite) TestCycleIsRejectedWithLineage() {
	for name, tc := range map[string]struct {
		graph map[string][]string
		from  string
		want  string
	}{
		"self": {
			graph: map[string][]string{"a": {"a"}},
			from:  "a",
			want:  "svc/a -> svc/a",
		},
		"pair": {
			graph: map[string][]string{"a": {"b"}, "b": {"a"}},
			from:  "a",
			want:  "svc/a -> svc/b -> svc/a",
		},
		"long": {
			graph: map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": {"b"}},
			from:  "a",
			want:  "svc/b -> svc/c -> svc/d -> svc/b",
		},
	} {
		s.Run(name, func() {
			_, err := s.catalogFor(tc.graph).LoadResource("svc", tc.from)
			s.Require().Error(err)
			s.Contains(err.Error(), "dependency cycle")
			s.Contains(err.Error(), tc.want)
		})
	}
}

// TestAcyclicRepeatsAreNotCycles guards the detector against the easy
// false positive: a diamond visits the shared node twice on different
// branches, which is not a loop.
func (s *CatalogSuite) TestAcyclicRepeatsAreNotCycles() {
	cat := s.catalogFor(map[string][]string{"a": {"b", "c"}, "b": {"d"}, "c": {"d"}, "d": nil})
	_, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
}

// TestDependenciesPairDeclarationWithTarget pins that each edge carries
// its own declaration, so params never have to be matched up by index.
func (s *CatalogSuite) TestDependenciesPairDeclarationWithTarget() {
	cat := s.catalogFor(map[string][]string{"a": {"b", "c"}, "b": nil, "c": nil})
	a, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)

	decls := a.GetDependencies()
	s.Require().Len(a.Dependencies, len(decls))
	for i, dep := range a.Dependencies {
		s.Equal(decls[i].GetName(), dep.Resource.GetMetadata().GetName())
		s.Equal(decls[i].GetName(), dep.GetName())
	}
}

// TestMissingDependencyFailsAtLoad moves the failure from partway
// through a render to the load itself, naming the chain that led there.
func (s *CatalogSuite) TestMissingDependencyFailsAtLoad() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": {"ghost"}})

	_, err := cat.LoadResource("svc", "a")
	s.Require().Error(err)
	s.Contains(err.Error(), "svc/a -> svc/b -> svc/ghost")
	s.Contains(err.Error(), "not in catalog")

	// The half-linked resources are not handed out on a retry either.
	_, err = cat.LoadResource("svc", "a")
	s.Require().Error(err)
	_, err = cat.LoadResource("svc", "b")
	s.Require().Error(err)

	// An unrelated resource in the same catalog still loads.
	cat2 := s.catalogFor(map[string][]string{"a": {"ghost"}, "fine": nil})
	ok, err := cat2.LoadResource("svc", "fine")
	s.Require().NoError(err)
	s.Empty(depNames(ok))
}

func (s *CatalogSuite) TestLoadByPathLinksTheSameGraph() {
	cat := s.catalogFor(map[string][]string{"a": {"b"}, "b": nil})

	byPath, err := cat.LoadByPath("a.yaml")
	s.Require().NoError(err)
	s.Equal([]string{"b"}, depNames(byPath))

	byName, err := cat.LoadResource("svc", "a")
	s.Require().NoError(err)
	s.Same(byPath, byName)
}

// TestConcurrentLoadsShareOneInstance is the property the serialized
// cold path exists to protect: many goroutines resolving overlapping
// subgraphs must converge on one *Resource per (kind, name), or a
// walker that dedupes by pointer sees the same resource twice. Run
// under -race, this also covers the memo table itself.
func (s *CatalogSuite) TestConcurrentLoadsShareOneInstance() {
	cat := s.catalogFor(map[string][]string{
		"a":      {"shared", "b"},
		"b":      {"shared", "c"},
		"c":      {"shared"},
		"shared": {"leaf"},
		"leaf":   nil,
	})

	const goroutines = 16
	got := make([]*Resource, goroutines)
	errs := make([]error, goroutines)
	starts := []string{"a", "b", "c", "shared"}
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			r, err := cat.LoadResource("svc", starts[i%len(starts)])
			if err != nil {
				errs[i] = err
				return
			}
			// Walk to the shared node from wherever this goroutine started.
			got[i], errs[i] = reachShared(r)
		})
	}
	wg.Wait()

	for i := range goroutines {
		s.Require().NoError(errs[i])
		s.Require().NotNil(got[i])
		s.Same(got[0], got[i], "goroutine %d saw a different instance of shared", i)
	}
}

// reachShared finds the "shared" resource by walking the linked graph.
func reachShared(from *Resource) (*Resource, error) {
	seen := map[*Resource]bool{}
	queue := []*Resource{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if cur.GetMetadata().GetName() == "shared" {
			return cur, nil
		}
		for _, dep := range cur.Dependencies {
			queue = append(queue, dep.Resource)
		}
	}
	return nil, fmt.Errorf("shared not reachable from %s", from.GetMetadata().GetName())
}

// TestForwardedEdgeRejectedWhenTargetRejectsTheConsumer is the limit on
// inheritance: b forwards c, but c registers no dependents for a's kind.
// That is a mistake in b, not something to paper over — a silently
// dropped edge would leave a missing wiring it never asked about.
func (s *CatalogSuite) TestForwardedEdgeRejectedWhenTargetRejectsTheConsumer() {
	files := fstest.MapFS{
		"a.yaml": &fstest.MapFile{Data: []byte("metadata:\n  kind: app\n  name: a\nspec: {}\ndependencies:\n  - kind: mid\n    name: b\n")},
		"b.yaml": &fstest.MapFile{Data: []byte("metadata:\n  kind: mid\n  name: b\nspec: {}\ndependencies:\n  - kind: priv\n    name: c\n    forward: true\n")},
		"c.yaml": &fstest.MapFile{Data: []byte("metadata:\n  kind: priv\n  name: c\nspec: {}\n")},
	}
	handles := []*Handle{
		{Kind: "app", Name: "a", Path: "a.yaml"},
		{Kind: "mid", Name: "b", Path: "b.yaml"},
		{Kind: "priv", Name: "c", Path: "c.yaml"},
	}

	// priv accepts mid but not app: b may depend on c, but forwarding it
	// to a is an error naming both ends and how to fix it.
	cat, err := NewCatalog(vfs.New(files, ""), handles, stubKinds{accepts: []string{"mid"}})
	s.Require().NoError(err)
	_, err = cat.LoadResource("app", "a")
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot inherit priv/c")
	s.Contains(err.Error(), "forwarded by mid/b")
	s.Contains(err.Error(), `kind "priv" declares no dependents for kind "app"`)
	s.Contains(err.Error(), "stop forwarding")

	// b itself is fine — it declared that edge directly.
	cat2, err := NewCatalog(vfs.New(files, ""), handles, stubKinds{accepts: []string{"mid"}})
	s.Require().NoError(err)
	b, err := cat2.LoadResource("mid", "b")
	s.Require().NoError(err)
	s.Equal([]string{"c"}, depNames(b))

	// Once priv accepts app too, the same graph forwards it through.
	cat, err = NewCatalog(vfs.New(files, ""), handles, stubKinds{accepts: []string{"mid", "app"}})
	s.Require().NoError(err)
	a, err := cat.LoadResource("app", "a")
	s.Require().NoError(err)
	s.Equal([]string{"b", "c"}, depNames(a))
}
