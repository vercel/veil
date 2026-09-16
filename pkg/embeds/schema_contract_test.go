package embeds_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/suite"
	"github.com/vercel/veil/pkg/embeds"
)

// SchemaContractSuite guards the published JSON Schemas against two
// regressions that are invisible in Go and only bite in an editor.
type SchemaContractSuite struct {
	suite.Suite
}

func TestSchemaContractSuite(t *testing.T) {
	suite.Run(t, new(SchemaContractSuite))
}

// readEmbedded reads a schema from the embedded copy on disk, for the
// ones with no exported var.
func (s *SchemaContractSuite) readEmbedded(name string) []byte {
	raw, err := os.ReadFile(filepath.Join("jsonschema", name))
	s.Require().NoError(err)
	return raw
}

func (s *SchemaContractSuite) decode(raw []byte) map[string]any {
	var doc map[string]any
	s.Require().NoError(json.Unmarshal(raw, &doc))
	return doc
}

// compile builds a validator for a schema document, the same way an
// editor would.
func (s *SchemaContractSuite) compile(doc map[string]any) *jsonschema.Schema {
	c := jsonschema.NewCompiler()
	var resource any = doc
	s.Require().NoError(c.AddResource("schema.json", resource))
	sch, err := c.Compile("schema.json")
	s.Require().NoError(err)
	return sch
}

// TestPointedAtSchemasAllowTheSchemaKey covers the way these schemas are
// actually used: a file names one in a "$schema" key so an editor can
// find it. Every generated object schema sets additionalProperties:false,
// so unless the key is declared the reference is itself the file's first
// validation error — including in the files veil writes, since `veil
// init`, `veil new kind` and `veil build` all stamp one.
func (s *SchemaContractSuite) TestPointedAtSchemasAllowTheSchemaKey() {
	for name, raw := range map[string][]byte{
		"VeilConfigDefinition": embeds.VeilConfigDefinitionSchema,
		"KindDefinition":       embeds.KindDefinitionSchema,
		"Resource":             embeds.ResourceSchema,
		"Kind":                 embeds.KindSchema,
		// Registry has no embedded var — only a URL constant — but
		// `veil build` stamps a $schema into registry.json all the same.
		"Registry": s.readEmbedded("Registry.schema.json"),
	} {
		s.Run(name, func() {
			doc := s.decode(raw)
			props, ok := doc["properties"].(map[string]any)
			s.Require().True(ok, "%s has no properties", name)
			s.Contains(props, "$schema",
				"%s is referenced by a $schema key, so it has to permit one", name)
		})
	}
}

// TestKindDefinitionAcceptsBothSourceShapes pins the polymorphism of
// `sources`. The proto field is a repeated google.protobuf.Value because
// protojson cannot say "string or message", which left every entry typed
// as anything at all — no completion, and no typo caught, on a field most
// kind.json edits touch.
func (s *SchemaContractSuite) TestKindDefinitionAcceptsBothSourceShapes() {
	sch := s.compile(s.decode(embeds.KindDefinitionSchema))

	valid := []any{
		"./sources/deployment.yaml",
		map[string]any{"path": "./sources/deployment.yaml"},
		map[string]any{"path": "./sources/d.yaml", "schema": "./schemas/d.schema.json"},
		// Unknown keys are allowed on purpose: a kind.json written
		// against a newer veil has to keep validating against an older
		// veil's schemas, so adding a field is not a breaking change.
		map[string]any{"path": "./sources/d.yaml", "fieldFromALaterVersion": true},
	}
	for _, entry := range valid {
		s.NoError(sch.Validate(map[string]any{
			"name":    "service",
			"sources": []any{entry},
		}), "should accept %v", entry)
	}

	// additionalProperties is true, so an unknown key is never itself an
	// error. What still has to fail is a missing or malformed `path` —
	// including the misspelling, which fails for that reason rather than
	// for being unrecognised.
	invalid := map[string]any{
		"misspelled path key": map[string]any{"pth": "./sources/deployment.yaml"},
		"no path at all":      map[string]any{"schema": "./schemas/d.schema.json"},
		"empty path":          map[string]any{"path": ""},
		"not an entry":        float64(123),
	}
	for label, entry := range invalid {
		s.Error(sch.Validate(map[string]any{
			"name":    "service",
			"sources": []any{entry},
		}), "should reject %s", label)
	}
}
