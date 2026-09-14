package resource

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/puzpuzpuz/xsync/v4"

	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/vfs"
)

// Catalog resolves project resources by (kind, name) — or by their
// fs.FS-relative path — on demand. Built from an fs.FS plus a slice
// of Handles, it loads each resource at most once and caches the
// result; concurrent lookups for the same resource see the same value
// without external sync regardless of which method they call.
//
// Loading a resource also loads everything it depends on, transitively,
// and links the results onto Resource.Dependencies. Callers therefore
// get a whole reachable graph from one lookup and never have to walk
// back to the catalog mid-traversal — which also means a missing
// dependency, or a dependency cycle, fails here at load rather than
// partway through a render.
type Catalog interface {
	// LoadResource returns the resource registered under (kind, name),
	// or an error when no such resource is in the catalog or the file
	// — or any resource it depends on — fails to load.
	LoadResource(kind, name string) (*Resource, error)

	// LoadByPath returns the resource whose Handle.Path matches the
	// given fs.FS-relative path. Shares the same load cache as
	// LoadResource — the file is read at most once even if a resource
	// is reached via both methods in succession.
	LoadByPath(path string) (*Resource, error)
}

// NewCatalog builds a Catalog from an fs.FS, a Handle slice, and the
// registry that resolves each resource's kind. Duplicate (kind, name)
// pairs are a hard error so dependency resolution stays unambiguous.
func NewCatalog(proj vfs.FS, handles []*Handle, kinds registry.Registry) (Catalog, error) {
	if kinds == nil {
		return nil, fmt.Errorf("catalog: a kind registry is required")
	}
	paths := make(map[catalogKey]string, len(handles))
	byPath := make(map[string]catalogKey, len(handles))
	for _, h := range handles {
		key := catalogKey{Kind: h.Kind, Name: h.Name}
		if existing, dup := paths[key]; dup {
			return nil, fmt.Errorf("duplicate resource (kind=%s, name=%s): %s and %s",
				h.Kind, h.Name, existing, h.Path)
		}
		paths[key] = h.Path
		byPath[h.Path] = key
	}
	return &lazyCatalog{
		project: proj,
		paths:   paths,
		byPath:  byPath,
		kinds:   kinds,
		entries: xsync.NewMap[catalogKey, *catalogEntry](),
	}, nil
}

// catalogKey is the (kind, name) tuple used to index resources.
type catalogKey struct {
	Kind string
	Name string
}

func (k catalogKey) String() string { return k.Kind + "/" + k.Name }

// catalogEntry is one resource's memo slot. body is sync.OnceValues, so
// the second goroutine to want a resource waits for the first and then
// gets the parsed value rather than re-reading the file. link guards the
// one write of Resource.Dependencies — resolve computes the same targets
// in every goroutine that walks through here, and this makes exactly one
// of them publish the slice.
//
// link stays a bare Once, never a recursive resolve, deliberately: a
// per-key lock around a recursive operation deadlocks on a cycle (one
// goroutine holding A and wanting B while another holds B and wants A,
// neither reaching its own cycle check). With nothing waiting inside it,
// the shape of the declared graph cannot hang the walk.
type catalogEntry struct {
	body func() (*Resource, error)
	link sync.Once

	// forwarded is what this resource hands to its consumers: the
	// declared edges marked for forwarding, plus whatever their targets
	// forward in turn. Written inside link, so a consumer that has
	// resolved this entry can read it without further synchronization.
	//
	// It is not a filter of the resource's own Dependencies. An edge
	// carries the forward flag its author set, which says whether the
	// resource that *declared* it exposes it — once that edge has been
	// flattened into a consumer's list, the flag no longer says whether
	// it should keep travelling. For A -> B unmarked and B -> C marked,
	// A's dependencies contain the marked B -> C edge, but A forwards
	// nothing.
	forwarded []*Dependency
}

// lazyCatalog is the canonical Catalog implementation. paths and byPath
// are the immutable index built at construction; entries is the memo
// table, an xsync.Map so lookups from the render pool need no external
// sync.
type lazyCatalog struct {
	project fs.FS
	paths   map[catalogKey]string
	byPath  map[string]catalogKey
	kinds   registry.Registry
	entries *xsync.Map[catalogKey, *catalogEntry]
}

// LoadResource implements Catalog.
func (c *lazyCatalog) LoadResource(kind, name string) (*Resource, error) {
	return c.resolve(catalogKey{Kind: kind, Name: name})
}

// LoadByPath implements Catalog.
func (c *lazyCatalog) LoadByPath(path string) (*Resource, error) {
	key, ok := c.byPath[path]
	if !ok {
		return nil, fmt.Errorf("resource at %s not in catalog — check resource_discovery.paths covers it", path)
	}
	return c.resolve(key)
}

// resolve is the entry point's view of resolveEntry: the resource,
// without the memo slot the resolver threads around internally.
func (c *lazyCatalog) resolve(key catalogKey) (*Resource, error) {
	e, err := c.resolveEntry(key, nil)
	if err != nil {
		return nil, err
	}
	return e.body()
}

// resolveEntry loads key, walks its declared dependencies, and links
// the result. seen is the chain of resources being resolved above key —
// a key already on it closes a cycle, which is reported with the
// lineage rather than followed.
//
// It returns the memo slot rather than the resource because the walk
// needs each target's forwarded set, which lives there. Dependencies
// are resolved before key is linked, so by the time a caller holds a
// resource every target hanging off it is finished too.
func (c *lazyCatalog) resolveEntry(key catalogKey, seen []catalogKey) (*catalogEntry, error) {
	if i := slices.Index(seen, key); i >= 0 {
		return nil, fmt.Errorf("dependency cycle: %s", lineage(append(seen[i:], key)))
	}
	e, ok := c.entry(key)
	if !ok {
		return nil, fmt.Errorf("%sresource (kind=%s, name=%s) not in catalog — check resource_discovery.paths covers it",
			via(seen, key), key.Kind, key.Name)
	}
	r, err := e.body()
	if err != nil {
		return nil, fmt.Errorf("%s%w", via(seen, key), err)
	}

	// Each declared edge contributes itself plus whatever its target
	// forwards; the target's forwarded set is already final by the time
	// resolveEntry returns it. A marked edge passes both on in turn,
	// which is what makes forwarding chain with no second traversal.
	declared := r.GetDependencies()
	forwardAll := r.Kind.GetForwardDependencies()
	kind := r.GetMetadata().GetKind()
	effective := make([]*Dependency, 0, len(declared))
	var forwarded []*Dependency
	for _, decl := range declared {
		targetEntry, err := c.resolveEntry(catalogKey{Kind: decl.GetKind(), Name: decl.GetName()}, append(seen, key))
		if err != nil {
			return nil, err
		}
		target, err := targetEntry.body()
		if err != nil {
			return nil, err
		}
		edge := &Dependency{Dependency: decl, Resource: target}
		effective = append(effective, edge)
		// An inherited edge only lands if its target would have accepted
		// this resource as a consumer in the first place. A platform that
		// forwards everything it uses shouldn't push its private pieces
		// onto a service the target never declared dependents for.
		for _, inherited := range targetEntry.forwarded {
			if inherited.Resource.Kind.AcceptsDependent(kind) {
				effective = append(effective, inherited)
			}
		}
		// What propagates further up is unfiltered: eligibility depends
		// on the consumer, and each level re-checks against its own kind.
		if decl.GetForward() || forwardAll {
			forwarded = append(forwarded, edge)
			forwarded = append(forwarded, targetEntry.forwarded...)
		}
	}
	e.link.Do(func() {
		r.Dependencies = effective
		e.forwarded = forwarded
	})
	return e, nil
}

// loadWithKind parses a resource file and attaches its compiled kind,
// so everything downstream — forwarding policy, schemas, hooks — reads
// off the resource instead of going back to the registry.
func (c *lazyCatalog) loadWithKind(path string) (*Resource, error) {
	r, err := Load(c.project, path)
	if err != nil {
		return nil, err
	}
	kind, err := c.kinds.LoadKind(r.GetMetadata().GetKind())
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	r.Kind = kind
	return r, nil
}

// entry returns key's memo slot, creating it on first use. The compute
// callback only allocates — it never touches the map — so it cannot
// deadlock on its own bucket.
func (c *lazyCatalog) entry(key catalogKey) (*catalogEntry, bool) {
	if e, ok := c.entries.Load(key); ok {
		return e, true
	}
	path, ok := c.paths[key]
	if !ok {
		return nil, false
	}
	e, _ := c.entries.LoadOrCompute(key, func() (*catalogEntry, bool) {
		return &catalogEntry{
			body: sync.OnceValues(func() (*Resource, error) { return c.loadWithKind(path) }),
		}, false
	})
	return e, true
}

// lineage renders a chain of resources as "svc/a -> svc/b -> svc/a".
func lineage(chain []catalogKey) string {
	parts := make([]string, 0, len(chain))
	for _, key := range chain {
		parts = append(parts, key.String())
	}
	return strings.Join(parts, " -> ")
}

// via prefixes an error with the chain that reached key, so a failure
// deep in the graph says how the walk got there. Empty at the entry
// point, where the caller already knows what it asked for.
func via(seen []catalogKey, key catalogKey) string {
	if len(seen) == 0 {
		return ""
	}
	return lineage(append(seen, key)) + ": "
}
