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
	// Sources are the kind's compiled sources in wire order, each paired
	// with the validator built from its declared schema.
	Sources []*LoadedSource
	// validator is the composite kind.schema.json compiled once at load.
	validator *jsonschema.Schema
	// sourceByPath indexes Sources for lookup by kind-dir-relative path.
	sourceByPath map[string]*LoadedSource
}

// LoadedSource is one compiled source ready for render: the wire-shape
// Source plus the validator compiled from its inlined schema. Pairing
// them means a caller that has the source never has to go looking for
// its schema, or re-compile one that was already built at load.
type LoadedSource struct {
	*veilv1.Source
	// Validator checks this source's parsed content against the schema
	// inlined in Source.schema. nil when the source declared none, in
	// which case the source is never schema-checked.
	Validator *jsonschema.Schema
}

// Source returns the compiled source at path, or nil when the kind
// declares none.
func (k *LoadedKind) Source(path string) *LoadedSource {
	return k.sourceByPath[path]
}

// AcceptsDependent reports whether this kind registers dependent hooks
// for consumers of the given kind — that is, whether a resource of that
// kind is allowed to depend on one of these. Forwarding consults it
// before handing an inherited edge to a consumer: a dependency the
// target would not accept directly is not one it inherits.
func (k *LoadedKind) AcceptsDependent(kind string) bool {
	for _, d := range k.GetHooks().GetDependents() {
		if d.GetKind() == kind {
			return true
		}
	}
	return false
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

// ValidateSource checks doc — a schema-declared source's parsed
// content — against that source's compiled validator. A source with
// no declared schema, or no such source at all, always passes.
func (k *LoadedKind) ValidateSource(path string, doc any) error {
	src := k.sourceByPath[path]
	if src == nil || src.Validator == nil {
		return nil
	}
	if err := src.Validator.Validate(doc); err != nil {
		return errors.New(stripSourceSchemaURL(path, err.Error()))
	}
	return nil
}

// HasSourceSchema reports whether the source at path declared a
// `schema` — i.e. whether ValidateSource actually checks anything for
// it.
func (k *LoadedKind) HasSourceSchema(path string) bool {
	src := k.sourceByPath[path]
	return src != nil && src.Validator != nil
}

// SchemaSources returns every schema-declared source path, in
// deterministic order — the set the render pipeline's pre-render gate
// iterates.
func (k *LoadedKind) SchemaSources() []string {
	paths := make([]string, 0, len(k.Sources))
	for _, src := range k.Sources {
		if src.Validator != nil {
			paths = append(paths, src.GetPath())
		}
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

// loadSources pairs each wire-shape source with the validator compiled
// from its inlined schema, and indexes the result by path. Every schema
// is registered with one shared compiler (one URI per source) rather
// than a jsonschema.Compiler each.
func loadSources(sources []*veilv1.Source) ([]*LoadedSource, map[string]*LoadedSource, error) {
	if len(sources) == 0 {
		return nil, nil, nil
	}
	compiler := jsonschema.NewCompiler()
	out := make([]*LoadedSource, 0, len(sources))
	byPath := make(map[string]*LoadedSource, len(sources))
	for _, src := range sources {
		path := src.GetPath()
		if _, dup := byPath[path]; dup {
			return nil, nil, fmt.Errorf("source %q: declared more than once", path)
		}
		loaded := &LoadedSource{Source: src}
		if raw := src.GetSchema(); raw != "" {
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				return nil, nil, fmt.Errorf("source %q: parsing schema: %w", path, err)
			}
			uri := "mem://source/" + path
			if err := compiler.AddResource(uri, doc); err != nil {
				return nil, nil, fmt.Errorf("source %q: registering schema: %w", path, err)
			}
			sch, err := compiler.Compile(uri)
			if err != nil {
				return nil, nil, fmt.Errorf("source %q: compiling schema: %w", path, err)
			}
			loaded.Validator = sch
		}
		out = append(out, loaded)
		byPath[path] = loaded
	}
	return out, byPath, nil
}

func stripSourceSchemaURL(path, msg string) string {
	re := regexp.MustCompile(`'mem://source/` + regexp.QuoteMeta(path) + `#?[^']*'`)
	label := fmt.Sprintf("source %q schema", path)
	msg = re.ReplaceAllString(msg, label)
	return strings.TrimPrefix(msg, fmt.Sprintf("jsonschema validation failed with %s\n", label))
}
