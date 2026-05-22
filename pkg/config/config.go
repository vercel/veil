package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
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
type Kind struct {
	*veilv1.KindDefinition
	Path string
	Dir  string
}

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
	for _, ref := range cfg.Kinds {
		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path = filepath.Clean(path)

		k, err := loadKind(path)
		if err != nil {
			return nil, fmt.Errorf("loading kind %s: %w", ref, err)
		}
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

func loadKind(filepathArg string) (*Kind, error) {
	f, err := os.Open(filepathArg)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// The kind file's `hooks.{render,validate,post_render}` arrays
	// accept a bare-string shorthand (`["./hooks/foo.ts"]`) that
	// protojson rejects. Decode into a generic map first so we can
	// expand the shorthand in place, then re-encode and hand the
	// resulting JSON to protojson.
	var doc map[string]any
	if err := protoencode.Decode(f, &doc); err != nil {
		return nil, fmt.Errorf("loading %s: %w", filepathArg, err)
	}
	if hooks, ok := doc["hooks"].(map[string]any); ok {
		expandHookShorthand(hooks)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("loading %s: re-encoding for protojson: %w", filepathArg, err)
	}
	pk := &veilv1.KindDefinition{}
	if err := protoencode.UnmarshalProto(bytes.NewReader(raw), pk); err != nil {
		return nil, fmt.Errorf("loading %s: %w", filepathArg, err)
	}
	if err := protoencode.Validate(pk); err != nil {
		return nil, fmt.Errorf("kind at %s: %w", filepathArg, err)
	}
	if err := validateDependents(pk.GetHooks().GetDependents()); err != nil {
		return nil, fmt.Errorf("kind at %s: %w", filepathArg, err)
	}
	return &Kind{KindDefinition: pk, Path: filepathArg, Dir: filepath.Dir(filepathArg)}, nil
}

// expandHookShorthand normalizes the bare-string entries that
// kind.yaml accepts inside hooks.{render,validate,post_render} into
// the proto-defined {path: <string>} form. Local helper rather than a
// shared utility because the shorthand is a kind-yaml convenience
// layer; protoencode is generic and shouldn't know about it.
func expandHookShorthand(hooks map[string]any) {
	if hooks == nil {
		return
	}
	for _, key := range []string{"render", "validate", "post_render"} {
		arr, ok := hooks[key].([]any)
		if !ok {
			continue
		}
		for i, entry := range arr {
			if s, ok := entry.(string); ok {
				arr[i] = map[string]any{"path": s}
			}
		}
		hooks[key] = arr
	}
}
