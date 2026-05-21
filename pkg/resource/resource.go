// Package resource owns "where do resources live, and how do we load
// them" for veil. Discover walks an fs.FS for resource files and
// returns lightweight Handles. NewCatalog turns those Handles into
// on-demand, cached loaders that hand back fully-parsed Resources.
// Everything in this package operates against an fs.FS so the same
// pipeline works against on-disk projects and against fstest.MapFS in
// unit tests.
package resource

import (
	"fmt"
	"io/fs"

	"github.com/goccy/go-json"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/protoencode"
)

// Resource pairs a proto-defined Resource with the fs.FS-relative
// path it was loaded from. The path is needed to resolve overlay
// references but isn't part of the Resource's wire shape.
type Resource struct {
	*veilv1.Resource
	Path string
}

// Load reads a single resource file from fsys and returns its parsed
// form. Used by Catalog implementations to materialize a Handle on
// demand. Source format is detected by extension — .json files go
// straight to protojson, .yaml/.yml files are decoded via yaml.v3
// then handed to protojson as JSON.
//
// Resource yaml accepts `metadata.hooks.render: ["./hooks/foo.ts"]`
// as bare-string shorthand for the full RenderHookDefinition shape.
// protojson rejects the bare string, so we read into a map first,
// expand the shorthand on metadata.hooks, then re-encode for the
// proto unmarshaller.
//
// The resource hook lifecycle on a resource only supports `render` —
// `dependents` and `validate` are kind-scoped concepts. Both are
// rejected with a clear error rather than silently dropped.
func Load(fsys fs.FS, path string) (*Resource, error) {
	var raw map[string]any
	if err := protoencode.ReadFS(fsys, path, &raw); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	metadata, _ := raw["metadata"].(map[string]any)
	if metadata != nil {
		hooks, _ := metadata["hooks"].(map[string]any)
		if hooks != nil {
			if _, hasDeps := hooks["dependents"]; hasDeps {
				return nil, fmt.Errorf("loading %s: metadata.hooks.dependents is not supported on resources (dependents are declared on the target kind)", path)
			}
			if _, hasValidate := hooks["validate"]; hasValidate {
				return nil, fmt.Errorf("loading %s: metadata.hooks.validate is not supported on resources (validation hooks are declared on the kind)", path)
			}
			protoencode.ExpandHookShorthand(hooks)
		}
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encoding %s for protojson: %w", path, err)
	}
	r := &veilv1.Resource{}
	if err := protoencode.Unmarshal.Unmarshal(jsonBytes, r); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &Resource{Resource: r, Path: path}, nil
}
