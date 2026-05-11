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
// demand. Source format is detected by extension — .json files go
// straight to protojson, .yaml/.yml files are decoded via yaml.v3
// then handed to protojson as JSON.
func Load(fsys fs.FS, path string) (*Resource, error) {
	r := &veilv1.Resource{}
	if err := protoencode.ReadProtoFS(fsys, path, r); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return &Resource{Resource: r, Path: path}, nil
}
