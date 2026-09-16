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
	// Files are the kind's compiled files in wire order, each paired
	// with the validator built from its declared schema. Includes both
	// the files that seed the rendered output and the assets that only
	// the kind's hooks read.
	Files []*LoadedFile
	// validator is the composite kind.schema.json compiled once at load.
	validator *jsonschema.Schema
	// fileByPath indexes Files for lookup by kind-dir-relative path.
	fileByPath map[string]*LoadedFile
}

// RenderFiles returns the files that seed the rendered resource, in wire
// order — what `sources` used to mean.
func (k *LoadedKind) RenderFiles() []*LoadedFile {
	out := make([]*LoadedFile, 0, len(k.Files))
	for _, f := range k.Files {
		if f.GetRender() {
			out = append(out, f)
		}
	}
	return out
}

// LoadedFile is one compiled file ready for render: the wire-shape File
// plus the validator compiled from its inlined schema. Pairing them
// means a caller that has the file never has to go looking for its
// schema, or re-compile one that was already built at load.
type LoadedFile struct {
	*veilv1.File
	// Validator checks this file's parsed content against the schema
	// inlined in File.schema. nil when the file declared none, in which
	// case the file is never schema-checked.
	Validator *jsonschema.Schema
}

// File returns the compiled file at path, or nil when the kind declares
// none.
func (k *LoadedKind) File(path string) *LoadedFile {
	return k.fileByPath[path]
}

// AcceptsDependent reports whether this kind registers dependent hooks
// for consumers of the given kind — that is, whether a resource of that
// kind is allowed to depend on one of these. Forwarding consults it
// before handing an inherited edge to a consumer, and rejects the load
// when the answer is no: a dependency the target would not accept
// directly is not one it can be given indirectly either.
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
	src := k.fileByPath[path]
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
	src := k.fileByPath[path]
	return src != nil && src.Validator != nil
}

// SchemaSources returns every schema-declared source path, in
// deterministic order — the set the render pipeline's pre-render gate
// iterates.
func (k *LoadedKind) SchemaSources() []string {
	paths := make([]string, 0, len(k.Files))
	for _, src := range k.Files {
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
func loadFiles(files []*veilv1.File, legacy []*veilv1.Source) ([]*LoadedFile, map[string]*LoadedFile, error) {
	files = mergeLegacySources(files, legacy)
	if len(files) == 0 {
		return nil, nil, nil
	}
	compiler := jsonschema.NewCompiler()
	out := make([]*LoadedFile, 0, len(files))
	byPath := make(map[string]*LoadedFile, len(files))
	for _, src := range files {
		path := src.GetPath()
		if _, dup := byPath[path]; dup {
			return nil, nil, fmt.Errorf("file %q: declared more than once", path)
		}
		loaded := &LoadedFile{File: src}
		if raw := src.GetSchema(); raw != "" {
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				return nil, nil, fmt.Errorf("file %q: parsing schema: %w", path, err)
			}
			uri := "mem://file/" + path
			if err := compiler.AddResource(uri, doc); err != nil {
				return nil, nil, fmt.Errorf("file %q: registering schema: %w", path, err)
			}
			sch, err := compiler.Compile(uri)
			if err != nil {
				return nil, nil, fmt.Errorf("file %q: compiling schema: %w", path, err)
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

// mergeLegacySources folds a registry's deprecated `sources` into its
// `files`. A registry built before `files` existed carries only
// `sources`, and every one of those seeded the rendered output, so they
// become files with render=true. A registry built after carries both —
// `sources` mirrors the renderable files for older readers — so anything
// already present as a file is skipped rather than duplicated.
func mergeLegacySources(files []*veilv1.File, legacy []*veilv1.Source) []*veilv1.File {
	if len(legacy) == 0 {
		return files
	}
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f.GetPath()] = true
	}
	out := files
	for _, src := range legacy {
		if seen[src.GetPath()] {
			continue
		}
		out = append(out, &veilv1.File{
			Path:     src.GetPath(),
			Contents: src.GetContents(),
			Schema:   src.Schema,
			Render:   true,
		})
	}
	return out
}
