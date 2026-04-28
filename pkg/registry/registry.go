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
	"sort"
	"strings"
	"sync"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/protoencode"
)

// Registry resolves compiled kind documents by name. Implementations are
// expected to load each kind at most once per registry instance.
type Registry interface {
	// LoadKind returns the compiled kind document with the given
	// reference, plus the absolute path to its kind.schema.json. The
	// reference may be a bare kind name (resolved against the default
	// registry) or `<alias>/<kind>` (resolved against the named alias).
	// Errors when the alias is unknown, the kind isn't registered there,
	// or its kind.json fails to read or parse.
	LoadKind(ref string) (*LoadedKind, error)
}

// Reference pairs an alias with one registry path. The empty alias
// names the default registry; resources reference its kinds without a
// prefix. Named aliases are referenced via `<alias>/<kind>` lookups.
type Reference struct {
	Alias string
	Path  string
}

// LoadedKind pairs a compiled kind's wire-shape body with the on-disk
// path to its companion kind.schema.json, which the render pipeline
// needs for spec validation and default-application.
type LoadedKind struct {
	*veilv1.Kind
	SchemaPath string
}

// Load builds a Registry by reading every (alias, path) pair as a
// compiled registry.json. Index files are tiny, so they're loaded
// eagerly; the kind.json bodies stay on disk until LoadKind is called
// for a particular name. Within one alias, duplicate kind names across
// indices are a hard error; across aliases the same kind name is fine
// and is disambiguated by the `<alias>/` prefix at lookup time.
func Load(refs []Reference) (Registry, error) {
	loaders := make(map[string]map[string]func() (*LoadedKind, error))
	seen := make(map[string]map[string]string)
	for _, src := range refs {
		abs, err := filepath.Abs(src.Path)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", src.Path, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("loading registry %s: %w", src.Path, err)
		}
		var r veilv1.Registry
		if err := protoencode.Unmarshal.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parsing registry %s: %w", src.Path, err)
		}
		dir := filepath.Dir(abs)
		if loaders[src.Alias] == nil {
			loaders[src.Alias] = make(map[string]func() (*LoadedKind, error))
			seen[src.Alias] = make(map[string]string)
		}
		for name, entry := range r.Kinds {
			if entry.GetPath() == "" {
				return nil, fmt.Errorf("registry %s: kind %q is missing \"path\"", src.Path, name)
			}
			kindPath := resolveAgainst(dir, entry.GetPath())
			schemaPath := resolveAgainst(dir, entry.GetSchema())
			if entry.GetSchema() == "" {
				schemaPath = filepath.Join(filepath.Dir(kindPath), "kind.schema.json")
			}
			if existing, ok := seen[src.Alias][name]; ok {
				return nil, fmt.Errorf("kind %q provided by multiple registries: %s and %s", aliasedName(src.Alias, name), existing, kindPath)
			}
			seen[src.Alias][name] = kindPath
			loaders[src.Alias][name] = sync.OnceValues(loadKindFn(name, kindPath, schemaPath))
		}
	}
	return &cachedRegistry{loaders: loaders}, nil
}

// cachedRegistry implements Registry against a fully resolved index
// keyed by (alias, kind name). Each kind has its own sync.OnceValues-
// backed loader so the kind.json is read at most once per registry —
// concurrent LoadKind calls for the same reference see the same cached
// result without any external sync.
type cachedRegistry struct {
	loaders map[string]map[string]func() (*LoadedKind, error)
}

func (r *cachedRegistry) LoadKind(ref string) (*LoadedKind, error) {
	alias, name, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	byKind, ok := r.loaders[alias]
	if !ok {
		return nil, fmt.Errorf("registry alias %q is not configured (known aliases: %s)", alias, strings.Join(r.knownAliases(), ", "))
	}
	fn, ok := byKind[name]
	if !ok {
		return nil, fmt.Errorf("kind %q not found in any loaded registry", aliasedName(alias, name))
	}
	return fn()
}

// knownAliases returns the configured alias set in deterministic order
// for use in error messages. The default alias surfaces as `""` so the
// user can spot whether a default registry is wired up at all.
func (r *cachedRegistry) knownAliases() []string {
	out := make([]string, 0, len(r.loaders))
	for a := range r.loaders {
		out = append(out, fmt.Sprintf("%q", a))
	}
	sort.Strings(out)
	return out
}

// ParseRef splits a kind reference into its alias and bare kind name.
// `acme/service` → ("acme", "service"); `service` → ("", "service").
// Aliases can be any non-empty string (the `@`-prefixed convention is
// optional, not required) — a reference is aliased iff it contains a
// `/`, with the substring before the first `/` taken as the alias and
// the rest as the kind name. The empty-string alias names the default
// registry. Errors when either side of the slash is empty.
func ParseRef(ref string) (alias, name string, err error) {
	idx := strings.Index(ref, "/")
	if idx < 0 {
		return "", ref, nil
	}
	alias = ref[:idx]
	name = ref[idx+1:]
	if alias == "" {
		return "", "", fmt.Errorf("invalid kind reference %q: alias is empty", ref)
	}
	if name == "" {
		return "", "", fmt.Errorf("invalid kind reference %q: kind name is empty", ref)
	}
	return alias, name, nil
}

// aliasedName renders an (alias, name) pair back into the canonical
// reference syntax, used in error messages.
func aliasedName(alias, name string) string {
	if alias == "" {
		return name
	}
	return alias + "/" + name
}

// loadKindFn returns the closure handed to sync.OnceValues for one
// (kindPath, schemaPath) pair. Pulled out of Load so the loop
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
