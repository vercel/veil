package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-json"
	"github.com/santhosh-tekuri/jsonschema/v6"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
)

// LoadedKind is a compiled kind ready for render: the wire-shape kind
// document plus the schema artifacts the registry parsed and compiled
// once when the kind loaded. Carrying these (rather than raw bytes or a
// path) keeps the render pipeline free of filesystem and JSON-Schema
// concerns — the registry is the only thing that reads and compiles.
type LoadedKind struct {
	*veilv1.Kind
	// SpecSchema is the `properties.spec` subschema, parsed once. Render
	// uses it to apply spec defaults.
	SpecSchema map[string]any
	// SchemaPath is the schema's external location — an absolute disk path
	// or URL — or "" for an in-memory registry. `veil new resource` uses
	// it to write a relative `$schema` pointer.
	SchemaPath string
	// validator is the composite kind.schema.json compiled once at load.
	validator *jsonschema.Schema
}

// Validate checks a resource document — its spec already overlay-merged,
// so what's validated is exactly what hooks and the renderer see —
// against this kind's compiled schema, reusing the validator built once
// when the kind loaded.
func (k *LoadedKind) Validate(doc map[string]any) error {
	if err := k.validator.Validate(doc); err != nil {
		// santhosh-tekuri/jsonschema embeds the in-memory schema URL
		// (`mem://schema#…`) in every message — strip it so users see just
		// the JSON-pointer location and the failure text.
		return errors.New(stripSchemaURL(err.Error()))
	}
	return nil
}

// extractSpecSubschema parses the composite kind.schema.json bytes and
// returns its `properties.spec` subschema — the author-facing schema that
// declares each spec field, including its `default` values. Returns an
// empty map when there's no spec subschema.
func extractSpecSubschema(schemaJSON []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(schemaJSON, &root); err != nil {
		return nil, fmt.Errorf("parsing kind schema: %w", err)
	}
	props, _ := root["properties"].(map[string]any)
	spec, _ := props["spec"].(map[string]any)
	if spec == nil {
		return map[string]any{}, nil
	}
	return spec, nil
}

// compileSchema parses and compiles the composite kind.schema.json into a
// reusable validator — done once per kind at load.
func compileSchema(schemaJSON []byte) (*jsonschema.Schema, error) {
	var schemaDoc any
	if err := json.Unmarshal(schemaJSON, &schemaDoc); err != nil {
		return nil, fmt.Errorf("parsing kind schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("mem://schema", schemaDoc); err != nil {
		return nil, fmt.Errorf("registering schema: %w", err)
	}
	return compiler.Compile("mem://schema")
}

var schemaURLRE = regexp.MustCompile(`'mem://schema#?[^']*'`)

func stripSchemaURL(msg string) string {
	msg = schemaURLRE.ReplaceAllString(msg, "kind schema")
	return strings.TrimPrefix(msg, "jsonschema validation failed with kind schema\n")
}
