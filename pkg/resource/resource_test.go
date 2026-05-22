package resource

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

type ResourceLoadSuite struct {
	suite.Suite
}

func TestResourceLoadSuite(t *testing.T) {
	suite.Run(t, new(ResourceLoadSuite))
}

func (s *ResourceLoadSuite) TestLoadParsesMetadataHooksRenderShorthand() {
	const yaml = `metadata:
  kind: worker
  name: foo
  hooks:
    render:
      - ./hook-a.ts
      - path: ./hook-b.ts
spec: {}
`
	fsys := fstest.MapFS{"svc/foo.yaml": &fstest.MapFile{Data: []byte(yaml)}}

	r, err := Load(fsys, "svc/foo.yaml")
	s.Require().NoError(err)

	hooks := r.RenderHooks()
	s.Require().Len(hooks, 2)
	s.Equal("./hook-a.ts", hooks[0].GetPath())
	s.Equal("./hook-b.ts", hooks[1].GetPath())
}

func (s *ResourceLoadSuite) TestLoadRejectsResourceLevelDependents() {
	const yaml = `metadata:
  kind: worker
  name: foo
  hooks:
    dependents:
      - kind: other
        params_path: ./params.json
        paths: [./dep.ts]
spec: {}
`
	fsys := fstest.MapFS{"svc/foo.yaml": &fstest.MapFile{Data: []byte(yaml)}}

	_, err := Load(fsys, "svc/foo.yaml")
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata.hooks.dependents is not supported on resources")
}

func (s *ResourceLoadSuite) TestLoadRejectsResourceLevelValidate() {
	const yaml = `metadata:
  kind: worker
  name: foo
  hooks:
    validate:
      - ./check.ts
spec: {}
`
	fsys := fstest.MapFS{"svc/foo.yaml": &fstest.MapFile{Data: []byte(yaml)}}

	_, err := Load(fsys, "svc/foo.yaml")
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata.hooks.validate is not supported on resources")
}

func (s *ResourceLoadSuite) TestLoadRejectsResourceLevelPostRender() {
	const yaml = `metadata:
  kind: worker
  name: foo
  hooks:
    post_render:
      - ./post.ts
spec: {}
`
	fsys := fstest.MapFS{"svc/foo.yaml": &fstest.MapFile{Data: []byte(yaml)}}

	_, err := Load(fsys, "svc/foo.yaml")
	s.Require().Error(err)
	s.Contains(err.Error(), "metadata.hooks.post_render is not supported on resources")
}

func (s *ResourceLoadSuite) TestLoadIgnoresMissingHooks() {
	const yaml = `metadata:
  kind: worker
  name: foo
spec: {}
`
	fsys := fstest.MapFS{"svc/foo.yaml": &fstest.MapFile{Data: []byte(yaml)}}

	r, err := Load(fsys, "svc/foo.yaml")
	s.Require().NoError(err)
	s.Nil(r.GetMetadata().GetHooks())
}
