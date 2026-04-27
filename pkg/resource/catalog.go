package resource

import (
	"fmt"
	"io/fs"
	"sync"
)

// Catalog resolves project resources by (kind, name) — or by their
// fs.FS-relative path — on demand. Built from an fs.FS plus a slice
// of Handles, it loads each resource at most once and caches the
// result; concurrent lookups for the same resource see the same value
// without external sync regardless of which method they call.
type Catalog interface {
	// LoadResource returns the resource registered under (kind, name),
	// or an error when no such resource is in the catalog or the file
	// fails to load.
	LoadResource(kind, name string) (*Resource, error)

	// LoadByPath returns the resource whose Handle.Path matches the
	// given fs.FS-relative path. Shares the same load cache as
	// LoadResource — the file is read at most once even if a resource
	// is reached via both methods in succession.
	LoadByPath(path string) (*Resource, error)
}

// NewCatalog builds a Catalog from an fs.FS and a Handle slice.
// Duplicate (kind, name) pairs are a hard error so dependency
// resolution stays unambiguous.
func NewCatalog(fsys fs.FS, handles []*Handle) (Catalog, error) {
	loaders := make(map[catalogKey]func() (*Resource, error), len(handles))
	seen := make(map[catalogKey]string, len(handles))
	byPath := make(map[string]catalogKey, len(handles))
	for _, h := range handles {
		key := catalogKey{Kind: h.Kind, Name: h.Name}
		if existing, dup := seen[key]; dup {
			return nil, fmt.Errorf("duplicate resource (kind=%s, name=%s): %s and %s",
				h.Kind, h.Name, existing, h.Path)
		}
		seen[key] = h.Path
		byPath[h.Path] = key
		loaders[key] = sync.OnceValues(loadResourceFn(fsys, h.Path))
	}
	return &lazyCatalog{loaders: loaders, byPath: byPath}, nil
}

// catalogKey is the (kind, name) tuple used to index loaders.
type catalogKey struct {
	Kind string
	Name string
}

// lazyCatalog is the canonical Catalog implementation: each (kind,
// name) maps to a sync.OnceValues-backed loader so the proto body is
// read at most once per registered Handle. byPath is a parallel index
// keyed on each Handle's fs.FS-relative path so LoadByPath can route
// a path argument to the same cached loader.
type lazyCatalog struct {
	loaders map[catalogKey]func() (*Resource, error)
	byPath  map[string]catalogKey
}

// LoadResource implements Catalog.
func (c *lazyCatalog) LoadResource(kind, name string) (*Resource, error) {
	fn, ok := c.loaders[catalogKey{Kind: kind, Name: name}]
	if !ok {
		return nil, fmt.Errorf("resource (kind=%s, name=%s) not in catalog — check resource_discovery.paths covers it", kind, name)
	}
	return fn()
}

// LoadByPath implements Catalog.
func (c *lazyCatalog) LoadByPath(path string) (*Resource, error) {
	key, ok := c.byPath[path]
	if !ok {
		return nil, fmt.Errorf("resource at %s not in catalog — check resource_discovery.paths covers it", path)
	}
	return c.loaders[key]()
}

// loadResourceFn returns the closure handed to sync.OnceValues. Pulled
// out of NewCatalog so the loop variable is captured by parameter.
func loadResourceFn(fsys fs.FS, path string) func() (*Resource, error) {
	return func() (*Resource, error) {
		return Load(fsys, path)
	}
}
