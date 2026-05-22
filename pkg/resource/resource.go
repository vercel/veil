// Package resource owns "where do resources live, and how do we load
// them" for veil. Discover walks an fs.FS for resource files and
// returns lightweight Handles. NewCatalog turns those Handles into
// on-demand, cached loaders that hand back fully-parsed Resources.
// Everything in this package operates against an fs.FS so the same
// pipeline works against on-disk projects and against fstest.MapFS in
// unit tests.
package resource

import (
	"bytes"
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
// form.
//
// Resource yaml accepts `metadata.hooks.render: ["./hooks/foo.ts"]`
// as bare-string shorthand for the full RenderHookDefinition shape;
// expandHookShorthand normalizes that into the form protojson
// understands before unmarshalling.
//
// After unmarshal we validate on the proto: resources only support
// the `render` lifecycle under metadata.hooks. The other lifecycles
// (dependents, validate, post_render) are kind-scoped — letting a
// resource quietly populate them would shadow the kind's own hooks.
func Load(fsys fs.FS, path string) (*Resource, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	defer f.Close()

	var doc map[string]any
	if err := protoencode.Decode(f, &doc); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	if md, ok := doc["metadata"].(map[string]any); ok {
		if hooks, ok := md["hooks"].(map[string]any); ok {
			expandHookShorthand(hooks)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("loading %s: re-encoding for protojson: %w", path, err)
	}
	r := &veilv1.Resource{}
	if err := protoencode.UnmarshalProto(bytes.NewReader(raw), r); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	if err := validateResourceHooks(r.GetMetadata().GetHooks()); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &Resource{Resource: r, Path: path}, nil
}

// validateResourceHooks rejects the kind-only lifecycles when a
// resource declares them on metadata.hooks. Operates on the
// unmarshalled proto so the checks read like ordinary proto-field
// validation, no generic-map gymnastics.
func validateResourceHooks(hooks *veilv1.HooksDefinition) error {
	if hooks == nil {
		return nil
	}
	if len(hooks.GetDependents()) > 0 {
		return fmt.Errorf("metadata.hooks.dependents is not supported on resources (dependents are declared on the target kind)")
	}
	if len(hooks.GetValidate()) > 0 {
		return fmt.Errorf("metadata.hooks.validate is not supported on resources (validation hooks are declared on the kind)")
	}
	if len(hooks.GetPostRender()) > 0 {
		return fmt.Errorf("metadata.hooks.post_render is not supported on resources (post_render is the kind's final normalization pass)")
	}
	return nil
}

// expandHookShorthand rewrites bare-string entries inside the
// HooksDefinition-shaped sub-map into the proto's full
// `{path: <string>}` form. Local helper rather than a shared
// utility because the shorthand is a YAML/JSON convenience layer
// specific to resource and kind loading — protoencode shouldn't
// know what a HooksDefinition is.
func expandHookShorthand(hooks map[string]any) {
	if hooks == nil {
		return
	}
	for _, key := range []string{"render", "validate", "post_render"} {
		arr, ok := hooks[key].([]any)
		if !ok {
			continue
		}
		for i, entry := range arr {
			if s, ok := entry.(string); ok {
				arr[i] = map[string]any{"path": s}
			}
		}
		hooks[key] = arr
	}
}
