package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"

	"github.com/vercel/veil/pkg/fsutil"
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

// VariableType is the declared type of an input variable.
type VariableType string

const (
	VariableTypeString VariableType = "string"
	VariableTypeNumber VariableType = "number"
	VariableTypeBool   VariableType = "bool"
)

// Variable declares a named input for use in overlay CEL expressions. Values
// are supplied at render time via --var flags or VEIL_VAR_<NAME> env vars.
type Variable struct {
	Type        VariableType      `json:"type"`
	Default     json.RawMessage   `json:"default,omitempty"`
	Description string            `json:"description,omitempty"`
	Enum        []json.RawMessage `json:"enum,omitempty"`
}

// HasDefault reports whether a default value was specified.
func (v Variable) HasDefault() bool {
	return len(v.Default) > 0
}

// ParsedDefault returns the default decoded to its declared type, or (nil,
// nil) if no default was set.
func (v Variable) ParsedDefault() (any, error) {
	if !v.HasDefault() {
		return nil, nil
	}
	return ParseTypedValue(v.Type, v.Default)
}

// ParsedEnum returns the enum values decoded to their declared type. Returns
// (nil, nil) if no enum was specified.
func (v Variable) ParsedEnum() ([]any, error) {
	if len(v.Enum) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(v.Enum))
	for i, raw := range v.Enum {
		parsed, err := ParseTypedValue(v.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("enum[%d]: %w", i, err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

// VeilConfig is the contents of .veil/veil.json.
type VeilConfig struct {
	Kinds      []string            `json:"kinds"`
	Variables  map[string]Variable `json:"variables,omitempty"`
	Registries []string            `json:"registries,omitempty"`
}

// Kind is a kind definition loaded from the registry.
type Kind struct {
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
	Hooks   Hooks    `json:"hooks,omitempty"`
	Schema  string   `json:"schema,omitempty"`

	// Dir is the directory containing this kind definition file.
	// Used to resolve relative paths for sources, hooks, etc.
	Dir string `json:"-"`
}

// Hooks groups a kind's hook files by lifecycle point. New lifecycle
// points are added as fields so kind.json can extend without breaking
// existing consumers.
type Hooks struct {
	// Render is the ordered list of hooks invoked during `veil render`.
	Render []string `json:"render,omitempty"`
}

// Registry is the set of kind definitions discovered from veil.json.
type Registry struct {
	// Root is the absolute path of the project root — the directory
	// housing veil.json. Build artifacts live under <Root>/.veil/.
	Root       string
	Kinds      []Kind
	Variables  map[string]Variable
	Registries []string
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

// ParseTypedValue decodes raw JSON into a Go value matching the declared
// variable type. number → float64, string → string, bool → bool.
func ParseTypedValue(t VariableType, raw json.RawMessage) (any, error) {
	switch t {
	case VariableTypeString:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("expected string, got %s", raw)
		}
		return v, nil
	case VariableTypeNumber:
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("expected number, got %s", raw)
		}
		return v, nil
	case VariableTypeBool:
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("expected bool, got %s", raw)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unknown variable type %q", t)
	}
}

// validateVariables checks each variable's type is one of the supported set,
// that any default value parses cleanly into that type, and that any
// declared enum is well-formed (bool vars can't have an enum; each entry
// must parse as the declared type; the default, if present, must be in the
// enum set).
func validateVariables(vars map[string]Variable) error {
	for name, v := range vars {
		switch v.Type {
		case VariableTypeString, VariableTypeNumber, VariableTypeBool:
		default:
			return fmt.Errorf(`variable %q: type must be "string", "number", or "bool" (got %q)`, name, v.Type)
		}
		if len(v.Enum) > 0 && v.Type == VariableTypeBool {
			return fmt.Errorf(`variable %q: enum is not supported for bool`, name)
		}
		enumVals, err := v.ParsedEnum()
		if err != nil {
			return fmt.Errorf("variable %q enum: %w", name, err)
		}
		if v.HasDefault() {
			def, err := v.ParsedDefault()
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

// containsValue reports whether needle is present in haystack using
// equality that mirrors ParseTypedValue's output types (string/float64/bool).
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

func loadConfig(path string) (*VeilConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg VeilConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadKind(path string) (Kind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Kind{}, err
	}
	var k Kind
	if err := json.Unmarshal(data, &k); err != nil {
		return Kind{}, err
	}
	if k.Name == "" {
		return Kind{}, fmt.Errorf("kind at %s is missing required field \"name\"", path)
	}
	k.Dir = filepath.Dir(path)
	return k, nil
}
