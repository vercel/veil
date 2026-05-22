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
// demand. Source format is detected by extension —
// protoencode.ReadProtoFSWithRewrite handles the JSON / YAML
// dispatch and the protojson round-trip; this function just
// supplies the rewrite step that normalizes the document into
// something protojson accepts.
//
// Resource yaml accepts `metadata.hooks.render: ["./hooks/foo.ts"]`
// as bare-string shorthand for the full RenderHookDefinition shape;
// the rewrite expands that. It also rejects `metadata.hooks.{dependents,
// validate,post_render}` outright because those lifecycles are
// kind-scoped — accepting them silently would let a resource shadow
// the kind's own hooks.
func Load(fsys fs.FS, path string) (*Resource, error) {
	r := &veilv1.Resource{}
	err := protoencode.ReadProtoFSWithRewrite(fsys, path, r, func(doc map[string]any) error {
		metadata, _ := doc["metadata"].(map[string]any)
		if metadata == nil {
			return nil
		}
		hooks, _ := metadata["hooks"].(map[string]any)
		if hooks == nil {
			return nil
		}
		if _, hasDeps := hooks["dependents"]; hasDeps {
			return fmt.Errorf("metadata.hooks.dependents is not supported on resources (dependents are declared on the target kind)")
		}
		if _, hasValidate := hooks["validate"]; hasValidate {
			return fmt.Errorf("metadata.hooks.validate is not supported on resources (validation hooks are declared on the kind)")
		}
		if _, hasPost := hooks["post_render"]; hasPost {
			return fmt.Errorf("metadata.hooks.post_render is not supported on resources (post_render is the kind's final normalization pass)")
		}
		protoencode.ExpandHookShorthand(hooks)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &Resource{Resource: r, Path: path}, nil
}
