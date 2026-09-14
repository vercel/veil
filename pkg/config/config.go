package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/codec"
	"github.com/vercel/veil/pkg/ioutil"
)

// Kind is a kind definition loaded from disk. It embeds the proto-generated
// KindDefinition (so all wire fields — Name, Sources, Hooks, Schema,
// Dependents — are accessible directly) and adds Dir for resolving the
// kind's relative paths against the local filesystem. Path is the
// absolute path of the file the definition was loaded from — used by
// mutation commands so a kind authored in YAML is rewritten as YAML.
//
// HooksDefinition stores each lifecycle's entries as
// google.protobuf.Value because the on-disk shape allows either a bare
// path string or a {path, access?} object. loadKind parses those
// Values into typed RenderHookDefinitions once at load time and caches
// the result here, so consumers (the build pipeline, file enumeration,
// scaffolding) iterate the typed slice instead of doing the
// Value-narrowing per call.
type Kind struct {
	*veilv1.KindDefinition
	Path string
	Dir  string
	// Import is the kind's types-package import wiring, set when its
	// `kinds` entry used the {path, import} object form. nil for a bare
	// path string — in which case the kind's types are inlined per hook.
	Import          *veilv1.KindImport
	sources         []*veilv1.SourceDefinition
	renderHooks     []*veilv1.RenderHookDefinition
	validateHooks   []*veilv1.RenderHookDefinition
	postRenderHooks []*veilv1.RenderHookDefinition
}

// SchemaURI resolves a schema reference to something ioutil can open:
// an HTTP(S) URL is returned as-is, a relative path is resolved against
// the kind's directory.
func (k *Kind) SchemaURI(ref string) string {
	if ioutil.IsRemote(ref) || filepath.IsAbs(ref) {
		return ref
	}
	return filepath.Join(k.Dir, ref)
}

// ReadSchema reads a schema relative to the kind, or from an HTTP(S)
// URL, as raw bytes.
func (k *Kind) ReadSchema(ref string) ([]byte, error) {
	return ioutil.Read(k.SchemaURI(ref))
}

// DecodeSchema reads a schema relative to the kind, or from an HTTP(S)
// URL, and decodes the JSON or YAML document into v.
func (k *Kind) DecodeSchema(ref string, v any) error {
	r, err := ioutil.Open(k.SchemaURI(ref))
	if err != nil {
		return err
	}
	defer r.Close()
	return codec.Decode(r, v)
}

// SourceDefs returns the parsed `sources` entries — path plus optional
// `schema`.
func (k *Kind) SourceDefs() []*veilv1.SourceDefinition { return k.sources }

// SourcePaths returns just the declared paths, in order — for call
// sites that don't need per-source schema info (FS accessor gen,
// dependency graph, override discovery).
func (k *Kind) SourcePaths() []string {
	paths := make([]string, len(k.sources))
	for i, s := range k.sources {
		paths[i] = s.GetPath()
	}
	return paths
}

// RenderHooks returns the parsed render-lifecycle entries.
func (k *Kind) RenderHooks() []*veilv1.RenderHookDefinition { return k.renderHooks }

// ValidateHooks returns the parsed validate-lifecycle entries.
func (k *Kind) ValidateHooks() []*veilv1.RenderHookDefinition { return k.validateHooks }

// PostRenderHooks returns the parsed post_render-lifecycle entries.
func (k *Kind) PostRenderHooks() []*veilv1.RenderHookDefinition { return k.postRenderHooks }

// HasDefault reports whether v has a default value declared.
func HasDefault(v *veilv1.Variable) bool {
	return v != nil && v.Default != nil
}

// ParsedDefault returns the default decoded to its declared type, or
// (nil, nil) if no default was set.
func ParsedDefault(v *veilv1.Variable) (any, error) {
	if !HasDefault(v) {
		return nil, nil
	}
	return CoerceValue(v.Type, v.Default)
}

// ParsedEnum returns the enum values decoded to their declared type. Returns
// (nil, nil) if no enum was specified.
func ParsedEnum(v *veilv1.Variable) ([]any, error) {
	if v == nil || len(v.Enum) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(v.Enum))
	for i, e := range v.Enum {
		parsed, err := CoerceValue(v.Type, e)
		if err != nil {
			return nil, fmt.Errorf("enum[%d]: %w", i, err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

// CoerceValue decodes a structpb.Value into a Go value matching the
// declared variable type.
func CoerceValue(t veilv1.VariableType_Enum, val *structpb.Value) (any, error) {
	if val == nil {
		return nil, fmt.Errorf("expected %s, got null", t)
	}
	switch t {
	case veilv1.VariableType_string:
		s, ok := val.Kind.(*structpb.Value_StringValue)
		if !ok {
			return nil, fmt.Errorf("expected string, got %s", structKindName(val))
		}
		return s.StringValue, nil
	case veilv1.VariableType_number:
		n, ok := val.Kind.(*structpb.Value_NumberValue)
		if !ok {
			return nil, fmt.Errorf("expected number, got %s", structKindName(val))
		}
		return n.NumberValue, nil
	case veilv1.VariableType_bool:
		b, ok := val.Kind.(*structpb.Value_BoolValue)
		if !ok {
			return nil, fmt.Errorf("expected bool, got %s", structKindName(val))
		}
		return b.BoolValue, nil
	default:
		return nil, fmt.Errorf("unknown variable type %q", t)
	}
}

// structKindName returns a human-readable label for a structpb.Value's
// underlying type, used purely for error messages.
func structKindName(val *structpb.Value) string {
	switch val.Kind.(type) {
	case *structpb.Value_StringValue:
		return "string"
	case *structpb.Value_NumberValue:
		return "number"
	case *structpb.Value_BoolValue:
		return "bool"
	case *structpb.Value_NullValue:
		return "null"
	case *structpb.Value_StructValue:
		return "object"
	case *structpb.Value_ListValue:
		return "array"
	default:
		return "unknown"
	}
}

// MakeValue is a helper for constructing a structpb.Value from a Go value
// — used by call sites (mostly tests) that want to build a Variable
// programmatically rather than loading from JSON.
func MakeValue(v any) (*structpb.Value, error) {
	return structpb.NewValue(v)
}

// validateDependents enforces the one rule on a kind's dependents list
// that the proto can't express: a given consumer kind must appear at
// most once. The proto's buf.validate annotations already enforce that
// every entry has a non-empty kind, at least one hook path, and a
// params_path, so no manual checks for those.
func validateDependents(deps []*veilv1.DependentDefinition) error {
	seen := make(map[string]bool, len(deps))
	for i, d := range deps {
		if seen[d.GetKind()] {
			return fmt.Errorf("dependents[%d]: duplicate consumer kind %q", i, d.GetKind())
		}
		seen[d.GetKind()] = true
	}
	return nil
}

func LoadKind(path string) (*Kind, error) {
	pk := &veilv1.KindDefinition{}
	if err := codec.ReadFile(path, pk); err != nil {
		return nil, err
	}
	if err := codec.Validate(pk); err != nil {
		return nil, fmt.Errorf("kind at %s: %w", path, err)
	}
	if err := validateDependents(pk.GetHooks().GetDependents()); err != nil {
		return nil, fmt.Errorf("kind at %s: %w", path, err)
	}
	k := &Kind{KindDefinition: pk, Path: path, Dir: filepath.Dir(path)}

	sources, err := parseSourceEntries(pk.GetSources())
	if err != nil {
		return nil, fmt.Errorf("kind at %s: sources: %w", path, err)
	}
	k.sources = sources
	if err := validateSourceSchemas(k); err != nil {
		return nil, fmt.Errorf("kind at %s: %w", path, err)
	}

	for _, lc := range []struct {
		label   string
		entries []*structpb.Value
		dest    *[]*veilv1.RenderHookDefinition
	}{
		{"hooks.render", pk.GetHooks().GetRender(), &k.renderHooks},
		{"hooks.validate", pk.GetHooks().GetValidate(), &k.validateHooks},
		{"hooks.post_render", pk.GetHooks().GetPostRender(), &k.postRenderHooks},
	} {
		parsed, err := parseHookEntries(lc.entries)
		if err != nil {
			return nil, fmt.Errorf("kind at %s: %s: %w", path, lc.label, err)
		}
		*lc.dest = parsed
	}
	return k, nil
}

// validateSourceSchemas checks schema references without fetching URLs and
// preserves the supported extensions for typed source accessors.
func validateSourceSchemas(k *Kind) error {
	for _, s := range k.sources {
		schema := s.GetSchema()
		if schema == "" {
			continue
		}
		path := s.GetPath()
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".yaml", ".yml":
		default:
			return fmt.Errorf("source %q: schema-declared sources must have a .json, .yaml, or .yml extension", path)
		}
		if ioutil.IsRemote(schema) {
			continue
		}
		if _, err := os.Stat(k.SchemaURI(schema)); err != nil {
			return fmt.Errorf("source %q: schema %q: %w", path, schema, err)
		}
	}
	return nil
}

// parseSourceEntries narrows each on-wire google.protobuf.Value into a
// SourceDefinition — a bare string path or a {path, schema?} object,
// same polymorphism as parseHookEntries.
func parseSourceEntries(entries []*structpb.Value) ([]*veilv1.SourceDefinition, error) {
	out := make([]*veilv1.SourceDefinition, 0, len(entries))
	for i, v := range entries {
		def, err := parseSourceEntry(v)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out = append(out, def)
	}
	return out, nil
}

func parseSourceEntry(v *structpb.Value) (*veilv1.SourceDefinition, error) {
	if v == nil {
		return nil, fmt.Errorf("source entry is nil")
	}
	switch kind := v.Kind.(type) {
	case *structpb.Value_StringValue:
		if kind.StringValue == "" {
			return nil, fmt.Errorf("source entry path is empty")
		}
		return &veilv1.SourceDefinition{Path: kind.StringValue}, nil
	case *structpb.Value_StructValue:
		def := &veilv1.SourceDefinition{}
		if err := codec.Convert(kind.StructValue, def); err != nil {
			return nil, fmt.Errorf("source entry: %w", err)
		}
		if def.GetPath() == "" {
			return nil, fmt.Errorf("source entry object missing required `path` field")
		}
		return def, nil
	default:
		return nil, fmt.Errorf("source entry must be a string path or {path, schema?} object, got %T", v.Kind)
	}
}

// parseHookEntries narrows each on-wire google.protobuf.Value into a
// RenderHookDefinition. The wire field is polymorphic — each entry is
// either a bare string path or a {path, access?} object — because
// protojson can't express "string OR struct" on a typed field.
// Pulling the narrowing into loadKind means every consumer of the
// returned *Kind sees the typed slice and nothing else.
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
		def := &veilv1.RenderHookDefinition{}
		if err := codec.Convert(kind.StructValue, def); err != nil {
			return nil, fmt.Errorf("hook entry: %w", err)
		}
		if def.GetPath() == "" {
			return nil, fmt.Errorf("hook entry object missing required `path` field")
		}
		return def, nil
	default:
		return nil, fmt.Errorf("hook entry must be a string path or {path, access?} object, got %T", v.Kind)
	}
}

// ParseKindEntry narrows one on-wire `kinds` entry — a bare path string or a
// {path, import?} object — into a KindRef, mirroring parseHookEntry. The wire
// field is google.protobuf.Value because protojson can't express "string OR
// struct" on a typed field; narrowing it once at load time lets every
// consumer read ref.Path / ref.Import directly.
func ParseKindEntry(v *structpb.Value) (*veilv1.KindRef, error) {
	if v == nil {
		return nil, fmt.Errorf("kind entry is nil")
	}
	switch kind := v.Kind.(type) {
	case *structpb.Value_StringValue:
		if kind.StringValue == "" {
			return nil, fmt.Errorf("kind entry path is empty")
		}
		return &veilv1.KindRef{Path: kind.StringValue}, nil
	case *structpb.Value_StructValue:
		// Reject unknown keys before unmarshalling: codec.Unmarshal uses
		// DiscardUnknown, so a typo like `imprt:` would otherwise be silently
		// dropped and the kind would quietly degrade to inline mode with no error.
		for key := range kind.StructValue.GetFields() {
			if key != "path" && key != "import" {
				return nil, fmt.Errorf("kind entry has unknown field %q (allowed: path, import)", key)
			}
		}
		if impVal, ok := kind.StructValue.GetFields()["import"]; ok {
			if impStruct := impVal.GetStructValue(); impStruct != nil {
				for key := range impStruct.GetFields() {
					if key != "name" && key != "value" {
						return nil, fmt.Errorf("kind entry import has unknown field %q (allowed: name, value)", key)
					}
				}
			}
		}
		ref := &veilv1.KindRef{}
		if err := codec.Convert(kind.StructValue, ref); err != nil {
			return nil, fmt.Errorf("kind entry: %w", err)
		}
		if ref.GetPath() == "" {
			return nil, fmt.Errorf("kind entry object missing required `path` field")
		}
		if imp := ref.GetImport(); imp != nil && (imp.GetName() == "" || imp.GetValue() == "") {
			return nil, fmt.Errorf("kind entry %q: import requires both `name` and `value`", ref.GetPath())
		}
		return ref, nil
	default:
		return nil, fmt.Errorf("kind entry must be a string path or {path, import?} object, got %T", v.Kind)
	}
}
