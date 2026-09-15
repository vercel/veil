package codec

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
)

type ProtoSuite struct {
	suite.Suite
}

func TestProtoSuite(t *testing.T) {
	suite.Run(t, new(ProtoSuite))
}

// kind is a message that exercises the parts of protojson a plain JSON
// decoder can't reach: a repeated google.protobuf.Value (whose
// polymorphic oneof encoding/json cannot populate) and a snake_case
// field name that differs from its camelCase JSON alias.
func (s *ProtoSuite) kind() *veilv1.KindDefinition {
	return &veilv1.KindDefinition{
		Name: "service",
		Hooks: &veilv1.HooksDefinition{
			PostRender: []*structpb.Value{structpb.NewStringValue("./after.ts")},
		},
	}
}

const kindJSON = `{"$schema":"https://veil.dev/kind.json","name":"service","hooks":{"post_render":["./after.ts"]}}`

// TestDecodeRoutesProtoTargetsThroughProtojson pins the dispatch: the
// same bytes decode into a proto via protojson (structpb oneof
// populated, unknown `$schema` discarded) and into a generic map via
// goccy/go-json (`$schema` preserved, because nothing claims to know
// the document's shape).
func (s *ProtoSuite) TestDecodeRoutesProtoTargetsThroughProtojson() {
	var k veilv1.KindDefinition
	s.Require().NoError(Decode(strings.NewReader(kindJSON), &k))
	s.Equal("service", k.GetName())
	s.Require().Len(k.GetHooks().GetPostRender(), 1)
	s.Equal("./after.ts", k.GetHooks().GetPostRender()[0].GetStringValue())

	var raw map[string]any
	s.Require().NoError(Decode(strings.NewReader(kindJSON), &raw))
	s.Equal("https://veil.dev/kind.json", raw["$schema"])
}

// TestDecodeYAMLIntoProto covers the YAML -> JSON -> protojson bridge:
// yaml.v3 can't populate a structpb.Value on its own.
func (s *ProtoSuite) TestDecodeYAMLIntoProto() {
	const doc = "name: service\nhooks:\n  post_render:\n    - ./after.ts\n"
	var k veilv1.KindDefinition
	s.Require().NoError(Decode(strings.NewReader(doc), &k))
	s.Equal("service", k.GetName())
	s.Require().Len(k.GetHooks().GetPostRender(), 1)
	s.Equal("./after.ts", k.GetHooks().GetPostRender()[0].GetStringValue())
}

// TestEmptyInputLeavesProtoAtZeroValue pins the asymmetry documented on
// Decode: a proto is populated in place, so an absent document is a
// no-op, while a generic target reports the empty read.
func (s *ProtoSuite) TestEmptyInputLeavesProtoAtZeroValue() {
	var k veilv1.KindDefinition
	s.Require().NoError(Decode(strings.NewReader(""), &k))
	s.Empty(k.GetName())

	var raw map[string]any
	s.Error(Decode(strings.NewReader(""), &raw))
}

// TestMarshalUsesProtoNames confirms UseProtoNames survives the generic
// entry point — field names stay snake_case, matching the .proto source
// and the on-disk documents users hand-edit.
func (s *ProtoSuite) TestMarshalUsesProtoNames() {
	raw, err := Marshal(s.kind())
	s.Require().NoError(err)
	s.Contains(string(raw), `"post_render"`)
	s.NotContains(string(raw), `"postRender"`)
}

// TestConvertNarrowsStructValueIntoMessage covers the config/resource
// call sites: an on-wire google.protobuf.Value object narrowed into the
// typed message it stands for, with unknown fields discarded.
func (s *ProtoSuite) TestConvertNarrowsStructValueIntoMessage() {
	v, err := structpb.NewStruct(map[string]any{"path": "./sources/app.yaml", "schema": "./app.schema.json"})
	s.Require().NoError(err)
	def := &veilv1.SourceDefinition{}
	s.Require().NoError(Convert(v, def))
	s.Equal("./sources/app.yaml", def.GetPath())
	s.Equal("./app.schema.json", def.GetSchema())

	unknown, err := structpb.NewStruct(map[string]any{"path": "./a.yaml", "typo": "x"})
	s.Require().NoError(err)
	s.Require().NoError(Convert(unknown, &veilv1.SourceDefinition{}))
}

// TestConvertFlattensProtoIntoMap is the other direction — the shape a
// render hook's ctx round-trips through.
func (s *ProtoSuite) TestConvertFlattensProtoIntoMap() {
	var out map[string]any
	s.Require().NoError(Convert(s.kind(), &out))
	s.Equal("service", out["name"])
	hooks, ok := out["hooks"].(map[string]any)
	s.Require().True(ok)
	s.Equal([]any{"./after.ts"}, hooks["post_render"])
}

// TestEncoderRoundTripsProto covers both encoder branches for a proto
// source — including the YAML one, which has to flatten through
// protojson before yaml.v3 ever sees the value.
func (s *ProtoSuite) TestEncoderRoundTripsProto() {
	for _, name := range []string{"kind.yaml", "kind.json"} {
		s.Run(name, func() {
			var buf bytes.Buffer
			s.Require().NoError(EncodeWriter(name, &buf, s.kind()))
			s.Contains(buf.String(), "post_render")

			var back veilv1.KindDefinition
			s.Require().NoError(Decode(bytes.NewReader(buf.Bytes()), &back))
			s.Equal("service", back.GetName())
			s.Require().Len(back.GetHooks().GetPostRender(), 1)
			s.Equal("./after.ts", back.GetHooks().GetPostRender()[0].GetStringValue())
		})
	}
}

// TestValidateEnforcesProtoConstraints exercises the buf.validate
// annotations on KindDefinition.name (lowercase, required).
func (s *ProtoSuite) TestValidateEnforcesProtoConstraints() {
	s.Require().NoError(Validate(s.kind()))
	s.Error(Validate(&veilv1.KindDefinition{Name: "Service"}))
	s.Error(Validate(&veilv1.KindDefinition{}))
}
