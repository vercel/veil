package resource

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/goccy/go-json"
)

// Handle is a lightweight reference to a resource on disk: its
// (kind, name) identity plus the fs.FS-relative path that produced it.
// Discover returns Handles; NewCatalog turns each into an on-demand
// loader keyed on (kind, name) and on path.
type Handle struct {
	Kind string
	Name string
	Path string
}

// Discover walks fsys for every doublestar glob pattern in patterns
// and returns one Handle per match that parses as a resource (has
// `metadata.kind`, `metadata.name`, and `spec`). Files that don't
// match the resource shape — overlays, fragments, schemas, any
// non-resource JSON the glob happens to capture — are silently
// skipped. Match identity is keyed on the fs.FS-relative path; a file
// matched by multiple patterns is indexed once.
//
// The context is checked between pattern expansions and per-match so
// a long glob walk aborts promptly on cancellation.
func Discover(ctx context.Context, fsys fs.FS, patterns []string) ([]*Handle, error) {
	var handles []*Handle
	indexed := make(map[string]struct{})
	for _, pattern := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matches, err := doublestar.Glob(fsys, pattern)
		if err != nil {
			return nil, fmt.Errorf("resource_discovery pattern %q: %w", pattern, err)
		}
		for _, path := range matches {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, dup := indexed[path]; dup {
				continue
			}
			indexed[path] = struct{}{}
			kind, name, ok := peekIdentity(fsys, path)
			if !ok {
				continue
			}
			handles = append(handles, &Handle{Kind: kind, Name: name, Path: path})
		}
	}
	return handles, nil
}

// peekIdentity reads just enough of an fs.FS file to recover the
// resource's (kind, name). Returns ok=false when the file isn't valid
// JSON, doesn't have the expected metadata fields, or omits spec —
// the same set of conditions that the full-parse path would also
// reject as "not a resource". Overlays naturally fall into this
// "skip" path because they lack name/kind/spec; we don't consult
// metadata.file_type here, since that field exists only to shape
// JSON schemas at build time, not to drive runtime behavior.
func peekIdentity(fsys fs.FS, path string) (kind, name string, ok bool) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", "", false
	}
	var idx resourceIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return "", "", false
	}
	if idx.Metadata.Kind == "" || idx.Metadata.Name == "" || idx.Spec == nil {
		return "", "", false
	}
	return idx.Metadata.Kind, idx.Metadata.Name, true
}

// resourceIndex is a minimal proto-shape Resource used by peekIdentity.
// Spec is *json.RawMessage so we can distinguish "spec absent" (nil)
// from "spec is JSON null" (non-nil pointer pointing at "null").
type resourceIndex struct {
	Metadata struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"metadata"`
	Spec *json.RawMessage `json:"spec,omitempty"`
}
