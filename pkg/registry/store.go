package registry

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Store is the raw byte source behind a registry: a filesystem (on disk
// or in memory) or an HTTP remote. A Store knows nothing about kinds,
// schemas, or caching — the Registry layers those on top.
type Store interface {
	// Open resolves a slash-separated, store-relative location to a
	// ReadCloser the caller streams and closes.
	Open(name string) (io.ReadCloser, error)
	// Location maps a store-relative name to an external, tooling-facing
	// location — an absolute disk path or a URL — or "" when the store has
	// none (e.g. an in-memory FS). `veil new resource` uses it to write a
	// relative `$schema` pointer from a new resource file to its kind's
	// schema.
	Location(name string) string
}

// FSStore is a Store backed by an fs.FS: os.DirFS for an on-disk
// registry, or a writable in-memory FS for `veil render --build`. Root,
// when set, is the on-disk directory FS is rooted at, used only to
// surface absolute Locations; it is empty for an in-memory FS.
type FSStore struct {
	FS   fs.FS
	Root string
}

func (s *FSStore) Open(name string) (io.ReadCloser, error) {
	return s.FS.Open(cleanLocation(name))
}

// Location returns the absolute on-disk path of name, or "" when this
// store isn't on disk (Root unset).
func (s *FSStore) Location(name string) string {
	if s.Root == "" {
		return ""
	}
	return filepath.Join(s.Root, filepath.FromSlash(cleanLocation(name)))
}

// HTTPStore is a Store backed by an HTTP(S) endpoint; names resolve
// against Base. A nil Client uses a shared 30-second-timeout client.
type HTTPStore struct {
	Base   string
	Client *http.Client
}

func (s *HTTPStore) Open(name string) (io.ReadCloser, error) {
	loc := s.Location(name)
	client := s.Client
	if client == nil {
		client = defaultHTTPClient
	}
	resp, err := client.Get(loc)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", loc, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetching %s: HTTP %d %s", loc, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resp.Body, nil
}

// Location resolves name against the base URL.
func (s *HTTPStore) Location(name string) string {
	if u, err := url.JoinPath(s.Base, cleanLocation(name)); err == nil {
		return u
	}
	return strings.TrimSuffix(s.Base, "/") + "/" + cleanLocation(name)
}

// storeForReference turns a registry reference (a local filesystem path
// or an HTTP(S) URL to a registry.json) into the Store rooted at its
// containing directory plus the index file's base name.
func storeForReference(loc string) (store Store, indexName string, err error) {
	if isHTTPURL(loc) {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, "", err
		}
		base := *u
		base.Path = path.Dir(u.Path)
		return &HTTPStore{Base: base.String()}, path.Base(u.Path), nil
	}
	abs, err := filepath.Abs(loc)
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Dir(abs)
	return &FSStore{FS: os.DirFS(dir), Root: dir}, filepath.Base(abs), nil
}

var (
	_ Store = (*FSStore)(nil)
	_ Store = (*HTTPStore)(nil)
)

// readAll opens name on store and returns its full contents.
func readAll(store Store, name string) ([]byte, error) {
	rc, err := store.Open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// cleanLocation normalizes a possibly-"./"-prefixed, slash-separated
// location to a clean store path.
func cleanLocation(name string) string {
	name = strings.TrimPrefix(name, "./")
	if name == "" {
		return "."
	}
	return path.Clean(name)
}

// isHTTPURL reports whether s is an http(s) URL rather than a path.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// defaultHTTPClient fetches registry resources served over HTTP(S). The
// 30-second timeout is a sane default for a small JSON file.
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}
