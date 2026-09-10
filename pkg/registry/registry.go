// Package registry resolves compiled kind documents lazily on demand. The
// Registry interface is what the render pipeline talks to: it asks for a
// kind by name and gets back the compiled kind.json plus its already
// parsed spec subschema and compiled validator, only when that kind is
// about to be rendered. Loading and schema compilation are cached so the
// heavy kind.json bodies and the JSON-Schema compilation happen at most
// once per kind per registry, even when many resources share a kind.
//
// A Registry reads its bytes from a Store — a filesystem (on disk or in
// memory) or an HTTP remote (see store.go) — and layers caching and
// schema compilation on top. The in-memory case used by `veil render
// --build` is just an FSStore over an in-memory fs.FS, so disk, memory,
// and HTTP all flow through one code path.
package registry

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/protoencode"
)

// Registry resolves compiled kind documents by name. Implementations load
// and compile each kind at most once per registry instance.
type Registry interface {
	// LoadKind returns the compiled kind document for ref, with its spec
	// subschema and compiled validator ready for render. ref may be a bare
	// kind name (the default registry) or `<alias>/<kind>` (a named alias).
	// Errors when the alias is unknown, the kind isn't registered, or the
	// kind.json / schema fail to read, parse, or compile.
	LoadKind(ref string) (*LoadedKind, error)
}

// Reference pairs an alias with one registry path — a local filesystem
// path or an HTTP(S) URL to a registry.json. The empty alias names the
// default registry; resources reference its kinds without a prefix.
type Reference struct {
	Alias string
	Path  string
}

// Load builds a Registry over one or more on-disk / HTTP registries. Each
// reference's registry.json index is read eagerly (it's tiny); the
// kind.json bodies and their schemas stay unread until LoadKind is called
// for a given kind. Within one alias, duplicate kind names across indices
// are a hard error; the same name across aliases is fine, disambiguated
// by the `<alias>/` prefix at lookup.
func Load(refs []Reference) (Registry, error) {
	r := newCachedRegistry()
	for _, ref := range refs {
		store, indexName, err := storeForReference(ref.Path)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", ref.Path, err)
		}
		if err := r.addSource(ref.Alias, store, indexName); err != nil {
			return nil, fmt.Errorf("registry %s: %w", ref.Path, err)
		}
	}
	return r, nil
}

// FromStore builds a Registry from a single Store under the default
// alias, reading its index from registry.json. `veil render --build` uses
// this with an FSStore over the in-memory fs.FS the build pipeline just
// wrote, so the in-memory path reuses the same load + compile + cache
// machinery as a disk or HTTP registry.
func FromStore(store Store) (Registry, error) {
	r := newCachedRegistry()
	if err := r.addSource("", store, "registry.json"); err != nil {
		return nil, err
	}
	return r, nil
}

// cachedRegistry implements Registry against a per-(alias, kind) index of
// sync.OnceValues loaders. Each loader reads + parses its kind.json and
// reads + compiles its schema at most once; concurrent LoadKind calls for
// the same kind share the cached result without external sync.
type cachedRegistry struct {
	loaders map[string]map[string]func() (*LoadedKind, error)
	seen    map[string]map[string]string // alias → name → index location (for dup errors)
}

func newCachedRegistry() *cachedRegistry {
	return &cachedRegistry{
		loaders: map[string]map[string]func() (*LoadedKind, error){},
		seen:    map[string]map[string]string{},
	}
}

// addSource reads one registry.json index from store and registers a
// cached loader for each kind it lists, under alias.
func (r *cachedRegistry) addSource(alias string, store Store, indexName string) error {
	data, err := readAll(store, indexName)
	if err != nil {
		return fmt.Errorf("loading registry index: %w", err)
	}
	var index veilv1.Registry
	if err := protoencode.UnmarshalProto(bytes.NewReader(data), &index); err != nil {
		return fmt.Errorf("loading registry index: %w", err)
	}
	if r.loaders[alias] == nil {
		r.loaders[alias] = map[string]func() (*LoadedKind, error){}
		r.seen[alias] = map[string]string{}
	}
	for name, entry := range index.Kinds {
		if entry.GetPath() == "" {
			return fmt.Errorf("kind %q is missing \"path\"", name)
		}
		kindPath := cleanLocation(entry.GetPath())
		schemaPath := cleanLocation(entry.GetSchema())
		if entry.GetSchema() == "" {
			schemaPath = path.Join(path.Dir(kindPath), "kind.schema.json")
		}
		if existing, ok := r.seen[alias][name]; ok {
			return fmt.Errorf("kind %q provided by multiple registries: %s and %s", aliasedName(alias, name), existing, kindPath)
		}
		r.seen[alias][name] = kindPath
		r.loaders[alias][name] = sync.OnceValues(loadKindFn(store, name, kindPath, schemaPath))
	}
	return nil
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

// loadKindFn returns the closure handed to sync.OnceValues for one kind:
// it reads + parses the compiled kind.json, reads its schema, extracts
// the spec subschema, and compiles the validator — all once. Pulled out
// so the loop variables are captured by parameter, not by reference.
func loadKindFn(store Store, name, kindPath, schemaPath string) func() (*LoadedKind, error) {
	return func() (*LoadedKind, error) {
		kindData, err := readAll(store, kindPath)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", name, err)
		}
		var ck veilv1.Kind
		if err := protoencode.UnmarshalProto(bytes.NewReader(kindData), &ck); err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", name, err)
		}
		schemaData, err := readAll(store, schemaPath)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s schema: %w", name, err)
		}
		spec, err := extractSpecSubschema(schemaData)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s schema: %w", name, err)
		}
		validator, err := compileSchema(schemaData)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s schema: %w", name, err)
		}
		sourceValidators, err := compileSourceSchemas(ck.GetSourceSchemas())
		if err != nil {
			return nil, fmt.Errorf("loading kind %s source schemas: %w", name, err)
		}
		return &LoadedKind{Kind: &ck, SpecSchema: spec, SchemaPath: store.Location(schemaPath), validator: validator, sourceValidators: sourceValidators}, nil
	}
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
