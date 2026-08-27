package registry

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	// sourceValidators holds one compiled validator per schema-declared
	// source (Kind.source_schemas), keyed the same way source_schemas
	// is — the kind-dir-relative path. A source with no declared
	// schema has no entry here.
	sourceValidators map[string]*jsonschema.Schema
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

// ValidateSource checks doc — the parsed content of a schema-declared
// source — against that source's compiled validator. A source with no
// declared schema (not present in sourceValidators) always passes: it
// never opted into this contract.
func (k *LoadedKind) ValidateSource(path string, doc any) error {
	sch, ok := k.sourceValidators[path]
	if !ok {
		return nil
	}
	if err := sch.Validate(doc); err != nil {
		return errors.New(stripSourceSchemaURL(path, err.Error()))
	}
	return nil
}

// HasSourceSchema reports whether the source at path declared a
// `schema` — i.e. whether ValidateSource actually checks anything for
// it.
func (k *LoadedKind) HasSourceSchema(path string) bool {
	_, ok := k.sourceValidators[path]
	return ok
}

// SchemaSources returns every source path that declared a `schema`, in
// deterministic order — the set the render pipeline's pre-render gate
// and final post-post_render check both iterate.
func (k *LoadedKind) SchemaSources() []string {
	paths := make([]string, 0, len(k.sourceValidators))
	for p := range k.sourceValidators {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
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

// compileSourceSchemas compiles one validator per entry in a compiled
// Kind's source_schemas map (path -> raw JSON Schema text). Each gets
// its own URI under the same compiler instance so a build/render with
// many typed sources doesn't spin up a separate jsonschema.Compiler
// per source. Returns nil for a kind with no schema-declared sources.
func compileSourceSchemas(schemas map[string]string) (map[string]*jsonschema.Schema, error) {
	if len(schemas) == 0 {
		return nil, nil
	}
	compiler := jsonschema.NewCompiler()
	out := make(map[string]*jsonschema.Schema, len(schemas))
	for path, raw := range schemas {
		var doc any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("source %q: parsing schema: %w", path, err)
		}
		uri := "mem://source/" + path
		if err := compiler.AddResource(uri, doc); err != nil {
			return nil, fmt.Errorf("source %q: registering schema: %w", path, err)
		}
		sch, err := compiler.Compile(uri)
		if err != nil {
			return nil, fmt.Errorf("source %q: compiling schema: %w", path, err)
		}
		out[path] = sch
	}
	return out, nil
}

func stripSourceSchemaURL(path, msg string) string {
	re := regexp.MustCompile(`'mem://source/` + regexp.QuoteMeta(path) + `#?[^']*'`)
	label := fmt.Sprintf("source %q schema", path)
	msg = re.ReplaceAllString(msg, label)
	return strings.TrimPrefix(msg, fmt.Sprintf("jsonschema validation failed with %s\n", label))
}
