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

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/protoencode"
)

// Resource pairs a proto-defined Resource with the fs.FS-relative
// path it was loaded from. The path is needed to resolve overlay
// references but isn't part of the Resource's wire shape.
//
// metadata.hooks.render entries arrive as google.protobuf.Value
// (each entry may be a bare string path or a {path, access?} object).
// Load parses them into typed RenderHookDefinitions once and stashes
// them on RenderHooks; the render pipeline reads them directly.
type Resource struct {
	*veilv1.Resource
	Path        string
	RenderHooks []*veilv1.RenderHookDefinition
}

// Load reads a single resource file from fsys, unmarshals it via
// protojson, parses the polymorphic hook entries, and validates the
// result. Resources only support the `render` lifecycle under
// metadata.hooks; the other lifecycles (dependents, validate,
// post_render) are kind-scoped and rejected here.
func Load(fsys fs.FS, path string) (*Resource, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	defer f.Close()

	r := &veilv1.Resource{}
	if err := protoencode.UnmarshalProto(f, r); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	if err := validateResourceHooks(r.GetMetadata().GetHooks()); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	parsed, err := parseHookEntries(r.GetMetadata().GetHooks().GetRender())
	if err != nil {
		return nil, fmt.Errorf("loading %s: metadata.hooks.render: %w", path, err)
	}
	return &Resource{Resource: r, Path: path, RenderHooks: parsed}, nil
}

// validateResourceHooks rejects the kind-only lifecycles when a
// resource declares them on metadata.hooks.
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

// parseHookEntries narrows each on-wire google.protobuf.Value into a
// RenderHookDefinition. Identical in spirit to the version in
// pkg/config — both wrap the same wire polymorphism but at different
// load points (kind vs resource).
func parseHookEntries(entries []*structpb.Value) ([]*veilv1.RenderHookDefinition, error) {
	out := make([]*veilv1.RenderHookDefinition, 0, len(entries))
	for i, v := range entries {
		def, err := parseHookEntry(v)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out = append(out, def)
	}
	return out, nil
}

func parseHookEntry(v *structpb.Value) (*veilv1.RenderHookDefinition, error) {
	if v == nil {
		return nil, fmt.Errorf("hook entry is nil")
	}
	switch kind := v.Kind.(type) {
	case *structpb.Value_StringValue:
		if kind.StringValue == "" {
			return nil, fmt.Errorf("hook entry path is empty")
		}
		return &veilv1.RenderHookDefinition{Path: kind.StringValue}, nil
	case *structpb.Value_StructValue:
		raw, err := protojson.Marshal(kind.StructValue)
		if err != nil {
			return nil, fmt.Errorf("marshalling hook entry: %w", err)
		}
		def := &veilv1.RenderHookDefinition{}
		if err := protoencode.Unmarshal.Unmarshal(raw, def); err != nil {
			return nil, fmt.Errorf("unmarshalling hook entry: %w", err)
		}
		if def.GetPath() == "" {
			return nil, fmt.Errorf("hook entry object missing required `path` field")
		}
		return def, nil
	default:
		return nil, fmt.Errorf("hook entry must be a string path or {path, access?} object, got %T", v.Kind)
	}
}
