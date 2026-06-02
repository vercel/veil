// Package registry resolves compiled kind documents lazily on demand. The
// Registry interface is what the render pipeline talks to: it asks for a
// kind by name and gets back the compiled kind.json (plus the path to its
// schema) only when that kind is actually about to be rendered. Loading
// is cached so the heavy kind.json bodies — sources + bundled hook code —
// are read at most once per render even if many resources share a kind.
package registry

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

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

// LoadedKind pairs a compiled kind's wire-shape body with the raw bytes
// of its companion kind.schema.json, which the render pipeline needs for
// spec validation and default-application. Carrying the schema as bytes
// (read by the registry when the kind loads) keeps the render pipeline
// free of any filesystem knowledge — the registry is the only thing that
// touches disk, an HTTP endpoint, or an in-memory FS.
type LoadedKind struct {
	*veilv1.Kind
	// SchemaPath is the location the schema was read from (an absolute
	// disk path / URL for an on-disk registry, an fs.FS path for an
	// in-memory one). Scaffolding (`veil new resource`) uses it to write a
	// relative `$schema` pointer; render uses Schema instead.
	SchemaPath string
	// Schema is the raw kind.schema.json bytes, read when the kind loads.
	Schema []byte
}

// Load builds a Registry by reading every (alias, path) pair as a
// compiled registry.json. Index files are tiny, so they're loaded
// eagerly; the kind.json bodies stay on disk until LoadKind is called
// for a particular name. Within one alias, duplicate kind names across
// indices are a hard error; across aliases the same kind name is fine
// and is disambiguated by the `<alias>/` prefix at lookup time.
func Load(refs []Reference) (Registry, error) {
	read := func(loc string) ([]byte, error) {
		rc, err := openResource(loc)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return loadWith(refs, read, absLocation, resolveAgainst)
}

// LoadFS builds a Registry that reads every registry.json plus the
// kind.json and kind.schema.json bodies it points at from fsys, with all
// paths interpreted as fs.FS paths (slash-separated, rooted at fsys).
// Used by `veil render --build`, which compiles the project into an
// in-memory FS and renders straight out of it — no on-disk registry.
func LoadFS(fsys fs.FS, refs []Reference) (Registry, error) {
	read := func(loc string) ([]byte, error) { return fs.ReadFile(fsys, loc) }
	locate := func(p string) (string, error) { return fsClean(p), nil }
	resolve := func(base, rel string) string {
		if rel == "" {
			return ""
		}
		return fsClean(path.Join(path.Dir(base), rel))
	}
	return loadWith(refs, read, locate, resolve)
}

// fsClean turns a possibly-"./"-prefixed path into a clean fs.FS path.
func fsClean(p string) string {
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return "."
	}
	return path.Clean(p)
}

// loadWith is the shared core of Load and LoadFS. read fetches the bytes
// at a location; locate normalizes a registry reference's path into a
// location; resolve resolves an entry's relative path against the
// registry.json location it came from.
func loadWith(refs []Reference, read func(string) ([]byte, error), locate func(string) (string, error), resolve func(base, rel string) string) (Registry, error) {
	loaders := make(map[string]map[string]func() (*LoadedKind, error))
	seen := make(map[string]map[string]string)
	for _, src := range refs {
		loc, err := locate(src.Path)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", src.Path, err)
		}
		data, err := read(loc)
		if err != nil {
			return nil, fmt.Errorf("loading registry %s: %w", src.Path, err)
		}
		var r veilv1.Registry
		if err := protoencode.UnmarshalProto(bytes.NewReader(data), &r); err != nil {
			return nil, fmt.Errorf("loading registry %s: %w", src.Path, err)
		}
		if loaders[src.Alias] == nil {
			loaders[src.Alias] = make(map[string]func() (*LoadedKind, error))
			seen[src.Alias] = make(map[string]string)
		}
		for name, entry := range r.Kinds {
			if entry.GetPath() == "" {
				return nil, fmt.Errorf("registry %s: kind %q is missing \"path\"", src.Path, name)
			}
			kindPath := resolve(loc, entry.GetPath())
			schemaPath := resolve(loc, entry.GetSchema())
			if entry.GetSchema() == "" {
				schemaPath = resolve(kindPath, "kind.schema.json")
			}
			if existing, ok := seen[src.Alias][name]; ok {
				return nil, fmt.Errorf("kind %q provided by multiple registries: %s and %s", aliasedName(src.Alias, name), existing, kindPath)
			}
			seen[src.Alias][name] = kindPath
			loaders[src.Alias][name] = sync.OnceValues(loadKindFn(name, kindPath, schemaPath, read))
		}
	}
	return &cachedRegistry{loaders: loaders}, nil
}

// absLocation normalizes a registry location: HTTP(S) URLs are returned
// verbatim; everything else is treated as a filesystem path and made
// absolute against cwd.
func absLocation(loc string) (string, error) {
	if isHTTPURL(loc) {
		return loc, nil
	}
	abs, err := filepath.Abs(loc)
	if err != nil {
		return "", err
	}
	return abs, nil
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
// (kindPath, schemaPath) pair. It reads and parses the compiled kind and
// reads the raw schema bytes, using read for both so the same closure
// works against disk, HTTP, or an in-memory FS. Pulled out of loadWith so
// the loop variables are captured by parameter, not by reference.
func loadKindFn(name, kindPath, schemaPath string, read func(string) ([]byte, error)) func() (*LoadedKind, error) {
	return func() (*LoadedKind, error) {
		kindData, err := read(kindPath)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", name, err)
		}
		var ck veilv1.Kind
		if err := protoencode.UnmarshalProto(bytes.NewReader(kindData), &ck); err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", name, err)
		}
		schema, err := read(schemaPath)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s schema: %w", name, err)
		}
		return &LoadedKind{Kind: &ck, SchemaPath: schemaPath, Schema: schema}, nil
	}
}

// httpClient is the package-level fetcher for registry resources served
// over HTTP(S). The 30-second timeout is a sane default for a small
// JSON file; callers needing different policies can fork this.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// ReadResource opens a registry resource (HTTP URL or local file)
// and decodes one document into v using the decoder implied by loc's
// extension. Exposed so other packages (notably pkg/render) can read
// schema files using the same dispatch — a kind.schema.json
// published alongside a remote registry needs to be fetched, not
// statted on disk.
//
// For proto messages use ReadProtoResource — that path routes
// through protojson which understands snake_case field names.
func ReadResource(loc string, v any) error {
	rc, err := openResource(loc)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := protoencode.Decode(rc, v); err != nil {
		return fmt.Errorf("decoding %s: %w", loc, err)
	}
	return nil
}

// ReadProtoResource is the proto-typed companion to ReadResource:
// opens loc and unmarshals one document into m via protojson, going
// through the yaml.v3 + JSON re-encode hop for YAML sources.
func ReadProtoResource(loc string, m proto.Message) error {
	rc, err := openResource(loc)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := protoencode.UnmarshalProto(rc, m); err != nil {
		return fmt.Errorf("decoding %s: %w", loc, err)
	}
	return nil
}

// openResource returns a ReadCloser for loc, dispatching on the URL
// scheme. The caller is responsible for Close.
func openResource(loc string) (io.ReadCloser, error) {
	if isHTTPURL(loc) {
		return fetchURLBody(loc)
	}
	return os.Open(loc)
}

// fetchURLBody returns the HTTP response body as a ReadCloser the
// caller streams from and Closes. Non-200 responses are mapped to
// an error and the body is drained on the way out so the connection
// can be reused.
func fetchURLBody(u string) (io.ReadCloser, error) {
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetching %s: HTTP %d %s", u, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resp.Body, nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// resolveAgainst returns p as an absolute filesystem path or URL,
// resolved relative to base. When base is an HTTP(S) URL, RFC 3986
// reference resolution is used (so `./foo` against
// `https://h/x/registry.json` becomes `https://h/x/foo`). Otherwise
// base is treated as a filesystem path and p is joined against base's
// containing directory. An absolute p (filesystem or URL) is returned
// as-is. Empty p returns empty.
func resolveAgainst(base, p string) string {
	if p == "" {
		return ""
	}
	if isHTTPURL(p) {
		return p
	}
	if isHTTPURL(base) {
		baseURL, err := url.Parse(base)
		if err != nil {
			return p
		}
		ref, err := url.Parse(p)
		if err != nil {
			return p
		}
		return baseURL.ResolveReference(ref).String()
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(base), p))
}
