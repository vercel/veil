// Package registry resolves compiled kind documents lazily on demand. The
// Registry interface is what the render pipeline talks to: it asks for a
// kind by name and gets back the compiled kind.json (plus the path to its
// schema) only when that kind is actually about to be rendered. Loading
// is cached so the heavy kind.json bodies — sources + bundled hook code —
// are read at most once per render even if many resources share a kind.
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/protoencode"
)

// Registry resolves compiled kind documents by name. Implementations are
// expected to load each kind at most once per registry instance.
type Registry interface {
	// LoadKind returns the compiled kind document with the given name,
	// plus the absolute path to its kind.schema.json. Errors when the
	// kind isn't registered with this registry, or when its kind.json
	// fails to read or parse.
	LoadKind(name string) (*LoadedKind, error)
}

// LoadedKind pairs a compiled kind's wire-shape body with the on-disk
// path to its companion kind.schema.json, which the render pipeline
// needs for spec validation and default-application.
type LoadedKind struct {
	*veilv1.Kind
	SchemaPath string
}

// FromIndex builds a Registry by reading one or more compiled
// registry.json index files. Index files are tiny (kind name → relative
// path), so they're loaded eagerly; the kind.json bodies stay on disk
// until LoadKind is called for a particular name. Duplicate kind names
// across indices are a hard error.
func FromIndex(paths []string) (Registry, error) {
	loaders := make(map[string]func() (*LoadedKind, error))
	seen := make(map[string]string)
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", p, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("loading registry %s: %w", p, err)
		}
		var r veilv1.Registry
		if err := protoencode.Unmarshal.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parsing registry %s: %w", p, err)
		}
		dir := filepath.Dir(abs)
		for name, entry := range r.Kinds {
			if entry.GetPath() == "" {
				return nil, fmt.Errorf("registry %s: kind %q is missing \"path\"", p, name)
			}
			kindPath := resolveAgainst(dir, entry.GetPath())
			schemaPath := resolveAgainst(dir, entry.GetSchema())
			if entry.GetSchema() == "" {
				// Default to the conventional sibling location if the
				// index didn't record a schema path explicitly.
				schemaPath = filepath.Join(filepath.Dir(kindPath), "kind.schema.json")
			}
			if existing, ok := seen[name]; ok {
				return nil, fmt.Errorf("kind %q provided by multiple registries: %s and %s", name, existing, kindPath)
			}
			seen[name] = kindPath
			loaders[name] = sync.OnceValues(loadKindFn(name, kindPath, schemaPath))
		}
	}
	return &cachedRegistry{loaders: loaders}, nil
}

// cachedRegistry implements Registry against a fully resolved index.
// Each kind has its own sync.OnceValues-backed loader so the kind.json
// is read at most once per registry — concurrent LoadKind calls for the
// same name see the same cached result without any external sync.
type cachedRegistry struct {
	loaders map[string]func() (*LoadedKind, error)
}

func (r *cachedRegistry) LoadKind(name string) (*LoadedKind, error) {
	fn, ok := r.loaders[name]
	if !ok {
		return nil, fmt.Errorf("kind %q not found in any loaded registry", name)
	}
	return fn()
}

// loadKindFn returns the closure handed to sync.OnceValues for one
// (kindPath, schemaPath) pair. Pulled out of FromIndex so the loop
// variables are captured by parameter, not by reference.
func loadKindFn(name, kindPath, schemaPath string) func() (*LoadedKind, error) {
	return func() (*LoadedKind, error) {
		data, err := os.ReadFile(kindPath)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", name, err)
		}
		var ck veilv1.Kind
		if err := protoencode.Unmarshal.Unmarshal(data, &ck); err != nil {
			return nil, fmt.Errorf("parsing kind %s: %w", name, err)
		}
		return &LoadedKind{Kind: &ck, SchemaPath: schemaPath}, nil
	}
}

// resolveAgainst returns p as an absolute path, using base as the parent
// when p is relative.
func resolveAgainst(base, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}
