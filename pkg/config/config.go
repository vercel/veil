package config

import (
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/fsutil"
	"github.com/vercel/veil/pkg/protoencode"
)

const (
	// ArtifactsDir is the directory under the project root where veil
	// stores source-side artifacts (kind definitions, hooks, schemas).
	// veil.json itself sits at the project root, *not* under this dir.
	ArtifactsDir = ".veil"
	// PublicDir is the directory under the project root where `veil
	// build` writes its publishable output (compiled kinds + registry).
	// Mirrors shadcn's `public/r/` convention.
	PublicDir = "public"
)

// VeilFiles is the ordered list of project-root config filenames
// `Discover` walks ancestors looking for. JSON is preferred when more
// than one is present, since that's the original default and what the
// scaffolders write.
var VeilFiles = []string{"veil.json", "veil.yaml", "veil.yml"}

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
	Import *veilv1.KindImport

	sources         []*veilv1.SourceDefinition
	renderHooks     []*veilv1.RenderHookDefinition
	validateHooks   []*veilv1.RenderHookDefinition
	postRenderHooks []*veilv1.RenderHookDefinition
}

// SourceDefs returns the parsed `sources` entries — path plus optional
// `schema`.
func (k *Kind) SourceDefs() []*veilv1.SourceDefinition { return k.sources }

// SourcePaths returns just the declared source paths, in declaration
// order. Used by every call site that only needs the path list (FS
// accessor generation, dependency-graph nodes, override discovery) —
// the schema-aware ones use SourceDefs directly.
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

// Registry is the set of kind definitions and project-level configuration
// discovered from veil.json, plus the project root directory (which is not
// part of any wire format). ConfigPath is the absolute path of the
// config file the registry was loaded from (veil.json, veil.yaml, or
// veil.yml) — mutation commands use it so a project authored in YAML
// stays in YAML across edits.
type Registry struct {
	Root              string
	ConfigPath        string
	Kinds             []*Kind
	Variables         map[string]*veilv1.Variable
	Registries        map[string]string
	ResourceDiscovery *veilv1.ResourceDiscovery
	Generators        *veilv1.Generators
	// CliVersion is the project's minimum required veil CLI version
	// (semver, leading "v" optional), or "" when unset. Enforced by
	// `veil render`. See VeilConfigDefinition.cli_version.
	CliVersion string
}

// DefaultKindsDir is the path (relative to the project root) where
// `veil new kind` scaffolds a new kind when generators.kinds_dir is
// unset.
var DefaultKindsDir = filepath.Join(ArtifactsDir, "kinds")

// KindsDir returns the absolute path of the directory where `veil new
// kind` should scaffold new kind trees. Honors generators.kinds_dir from
// veil.json when set; otherwise falls back to <root>/.veil/kinds.
func (r *Registry) KindsDir() string {
	dir := r.Generators.GetKindsDir()
	if dir == "" {
		dir = DefaultKindsDir
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(r.Root, dir))
}

// TypesOutputDir returns the absolute directory where `veil build` writes
// the shared types package (generators.types.output_dir), or "" when unset.
// When set, kinds whose `kinds` entry carries an `import` emit their
// generated types here and their hooks import them by package specifier.
func (r *Registry) TypesOutputDir() string {
	dir := r.Generators.GetTypes().GetOutputDir()
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(r.Root, dir))
}

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

// Discover walks upward from startDir to find a directory containing
// a project config file (veil.json, veil.yaml, or veil.yml), loads it,
// resolves all kind paths, and returns the loaded registry.
func Discover(startDir string) (*Registry, error) {
	configPath, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}
	return Load(configPath)
}

// Load reads a veil.json at the given path and resolves all kind references
// relative to its parent directory. Unlike Discover, it does not walk the
// filesystem — the path is used as-is.
func Load(configPath string) (*Registry, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", configPath, err)
	}

	if err := validateVariables(cfg.Variables); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	root := filepath.Dir(configPath)
	kinds := make([]*Kind, 0, len(cfg.Kinds))
	for i, entry := range cfg.Kinds {
		ref, err := parseKindEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: kinds[%d]: %w", configPath, i, err)
		}
		path := ref.GetPath()
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path = filepath.Clean(path)

		k, err := loadKind(path)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", ref.GetPath(), err)
		}
		k.Import = ref.GetImport()
		kinds = append(kinds, k)
	}

	merged, err := mergeVariables(cfg.Variables, kinds)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	return &Registry{
		Root:              root,
		ConfigPath:        configPath,
		Kinds:             kinds,
		Variables:         merged,
		Registries:        cfg.Registries,
		ResourceDiscovery: cfg.ResourceDiscovery,
		Generators:        cfg.Generators,
		CliVersion:        cfg.GetCliVersion(),
	}, nil
}

// mergeVariables flattens project-level variables and per-kind
// variables into a single namespace. Conflicts (same name across any
// pair of sources) are rejected — the error names both sources so the
// user can resolve the collision. Each kind's variables are validated
// individually before they enter the merge.
func mergeVariables(project map[string]*veilv1.Variable, kinds []*Kind) (map[string]*veilv1.Variable, error) {
	merged := make(map[string]*veilv1.Variable, len(project))
	source := make(map[string]string, len(project))
	for name, v := range project {
		merged[name] = v
		source[name] = "veil.json"
	}
	for _, k := range kinds {
		kv := k.GetVariables()
		if len(kv) == 0 {
			continue
		}
		if err := validateVariables(kv); err != nil {
			return nil, fmt.Errorf("kind %q: %w", k.Name, err)
		}
		kindLabel := fmt.Sprintf("kind %q", k.Name)
		for name, v := range kv {
			if prev, ok := source[name]; ok {
				return nil, fmt.Errorf("variable %q declared in both %s and %s", name, prev, kindLabel)
			}
			merged[name] = v
			source[name] = kindLabel
		}
	}
	return merged, nil
}

// validateVariables checks each variable's type is one of the supported
// set, that any default value matches that type, and that any declared
// enum is well-formed (bool vars can't have an enum; each entry must
// match the declared type; the default, if present, must be in the
// enum set).
func validateVariables(vars map[string]*veilv1.Variable) error {
	for name, v := range vars {
		if v == nil {
			return fmt.Errorf(`variable %q: declaration is empty`, name)
		}
		switch v.Type {
		case veilv1.VariableType_string, veilv1.VariableType_number, veilv1.VariableType_bool:
		default:
			return fmt.Errorf(`variable %q: type must be "string", "number", or "bool" (got %q)`, name, v.Type)
		}
		if len(v.Enum) > 0 && v.Type == veilv1.VariableType_bool {
			return fmt.Errorf(`variable %q: enum is not supported for bool`, name)
		}
		enumVals, err := ParsedEnum(v)
		if err != nil {
			return fmt.Errorf("variable %q enum: %w", name, err)
		}
		if HasDefault(v) {
			def, err := ParsedDefault(v)
			if err != nil {
				return fmt.Errorf("variable %q default: %w", name, err)
			}
			if enumVals != nil && !containsValue(enumVals, def) {
				return fmt.Errorf("variable %q default %v is not in enum %v", name, def, enumVals)
			}
		}
	}
	return nil
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

// containsValue reports whether needle is present in haystack using
// equality that mirrors CoerceValue's output types (string/float64/bool).
func containsValue(haystack []any, needle any) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// findProjectRoot walks upward from dir looking for a project config
// file (veil.json, veil.yaml, or veil.yml), returning the absolute
// path of the file. Order matters: veil.json wins over the YAML
// variants when more than one is present in the same directory.
func findProjectRoot(dir string) (string, error) {
	found := fsutil.FindAncestorAny(dir, VeilFiles)
	if found == "" {
		abs, _ := filepath.Abs(dir)
		return "", fmt.Errorf("no veil.{json,yaml,yml} found (searched up from %s)", abs)
	}
	return found, nil
}

func loadConfig(path string) (*veilv1.VeilConfigDefinition, error) {
	var cfg veilv1.VeilConfigDefinition
	if err := protoencode.ReadProtoFile(path, &cfg); err != nil {
		return nil, err
	}
	if err := protoencode.Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadKind(path string) (*Kind, error) {
	pk := &veilv1.KindDefinition{}
	if err := protoencode.ReadProtoFile(path, pk); err != nil {
		return nil, err
	}
	if err := protoencode.Validate(pk); err != nil {
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
		for _, def := range parsed {
			if src := def.GetSource(); src != "" && !containsValue(sourcePathValues(sources), src) {
				return nil, fmt.Errorf("kind at %s: %s: hook %q: source %q is not declared in this kind's `sources`", path, lc.label, def.GetPath(), src)
			}
		}
		*lc.dest = parsed
	}
	return k, nil
}

// sourcePathValues extracts the declared paths from a slice of parsed
// source definitions, for use with containsValue's []any haystack.
func sourcePathValues(sources []*veilv1.SourceDefinition) []any {
	out := make([]any, len(sources))
	for i, s := range sources {
		out[i] = s.GetPath()
	}
	return out
}

// validateSourceSchemas checks that every declared source's `schema`
// (when set) resolves to a file that actually exists, relative to the
// kind's directory — the same failure mode as an unresolvable `schema`
// on the kind itself, just caught at load time instead of surfacing as
// a confusing read error deep in the build pipeline.
func validateSourceSchemas(k *Kind) error {
	for _, s := range k.sources {
		schema := s.GetSchema()
		if schema == "" {
			continue
		}
		abs := schema
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(k.Dir, schema)
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("source %q: schema %q: %w", s.GetPath(), schema, err)
		}
	}
	return nil
}

// parseSourceEntries narrows each on-wire google.protobuf.Value into a
// SourceDefinition. The wire field is polymorphic — each entry is
// either a bare string path or a {path, schema?} object — mirroring
// parseHookEntries for the same reason: protojson can't express
// "string OR struct" on a typed field.
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
		raw, err := protojson.Marshal(kind.StructValue)
		if err != nil {
			return nil, fmt.Errorf("marshalling source entry: %w", err)
		}
		def := &veilv1.SourceDefinition{}
		if err := protoencode.Unmarshal.Unmarshal(raw, def); err != nil {
			return nil, fmt.Errorf("unmarshalling source entry: %w", err)
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

// parseKindEntry narrows one on-wire `kinds` entry — a bare path string or a
// {path, import?} object — into a KindRef, mirroring parseHookEntry. The wire
// field is google.protobuf.Value because protojson can't express "string OR
// struct" on a typed field; narrowing it once at load time lets every
// consumer read ref.Path / ref.Import directly.
func parseKindEntry(v *structpb.Value) (*veilv1.KindRef, error) {
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
		// Reject unknown keys before unmarshalling: protoencode.Unmarshal uses
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
		raw, err := protojson.Marshal(kind.StructValue)
		if err != nil {
			return nil, fmt.Errorf("marshalling kind entry: %w", err)
		}
		ref := &veilv1.KindRef{}
		if err := protoencode.Unmarshal.Unmarshal(raw, ref); err != nil {
			return nil, fmt.Errorf("unmarshalling kind entry: %w", err)
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
