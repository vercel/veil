package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	veilFile  = "veil.json"
)

// Kind is a kind definition loaded from disk. It embeds the proto-generated
// KindDefinition (so all wire fields — Name, Sources, Hooks, Schema,
// Dependents — are accessible directly) and adds Dir for resolving the
// kind's relative paths against the local filesystem.
type Kind struct {
	*veilv1.KindDefinition
	Dir string
}

// Registry is the set of kind definitions and project-level configuration
// discovered from veil.json, plus the project root directory (which is not
// part of any wire format).
type Registry struct {
	Root       string
	Kinds      []Kind
	Variables  map[string]*veilv1.Variable
	Registries []string
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
// veil.json, loads the config, resolves all kind paths, and returns the
// loaded registry.
func Discover(startDir string) (*Registry, error) {
	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}
	return Load(filepath.Join(root, veilFile))
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
	kinds := make([]Kind, 0, len(cfg.Kinds))
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

	return &Registry{
		Root:       root,
		Kinds:      kinds,
		Variables:  cfg.Variables,
		Registries: cfg.Registries,
	}, nil
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

// validateDependents enforces structural rules on a kind's dependents
// list: every entry needs a consumer kind, at least one hook, and a
// params_path; consumer kinds may only appear once.
func validateDependents(deps []*veilv1.DependentDefinition) error {
	seen := make(map[string]bool, len(deps))
	for i, d := range deps {
		if d == nil || d.Kind == "" {
			return fmt.Errorf("dependents[%d]: kind is required", i)
		}
		if seen[d.Kind] {
			return fmt.Errorf("dependents[%d]: duplicate consumer kind %q", i, d.Kind)
		}
		seen[d.Kind] = true
		if len(d.Hooks) == 0 {
			return fmt.Errorf("dependents[%d] (%q): hooks must be non-empty", i, d.Kind)
		}
		if d.ParamsPath == "" {
			return fmt.Errorf("dependents[%d] (%q): params_path is required", i, d.Kind)
		}
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

// findProjectRoot walks upward from dir looking for a bare veil.json,
// returning the directory that contains it.
func findProjectRoot(dir string) (string, error) {
	found := fsutil.FindAncestor(dir, veilFile)
	if found == "" {
		abs, _ := filepath.Abs(dir)
		return "", fmt.Errorf("no %s found (searched up from %s)", veilFile, abs)
	}
	return filepath.Dir(found), nil
}

func loadConfig(path string) (*veilv1.VeilConfigDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg veilv1.VeilConfigDefinition
	if err := protoencode.Unmarshal.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadKind(path string) (Kind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Kind{}, err
	}
	var pk veilv1.KindDefinition
	if err := protoencode.Unmarshal.Unmarshal(data, &pk); err != nil {
		return Kind{}, err
	}
	if pk.Name == "" {
		return Kind{}, fmt.Errorf("kind at %s is missing required field \"name\"", path)
	}
	if err := validateDependents(pk.Dependents); err != nil {
		return Kind{}, fmt.Errorf("kind at %s: %w", path, err)
	}
	return Kind{KindDefinition: &pk, Dir: filepath.Dir(path)}, nil
}
