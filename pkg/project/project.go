// Package project models a veil project: the veil.json at its root, the
// kinds it declares, and the directory layout that follows from where
// that file sits. It is the "what am I working on" layer — pkg/config
// owns one kind definition, pkg/registry owns compiled kinds, and this
// ties them together.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/codec"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/fsutil"
	"github.com/vercel/veil/pkg/vfs"
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

// Project is the set of kind definitions and project-level configuration
// discovered from veil.json, plus the project root directory (which is not
// part of any wire format). ConfigPath is the absolute path of the
// config file the project was loaded from (veil.json, veil.yaml, or
// veil.yml) — mutation commands use it so a project authored in YAML
// stays in YAML across edits.
type Project struct {
	Root              string
	ConfigPath        string
	Kinds             []*config.Kind
	Variables         map[string]*veilv1.Variable
	Registries        map[string]string
	ResourceDiscovery *veilv1.ResourceDiscovery
	Generators        *veilv1.Generators
	// CliVersion is the project's minimum required veil CLI version
	// (semver, leading "v" optional), or "" when unset. Enforced by
	// `veil render`. See VeilConfigDefinition.cli_version.
	CliVersion string
}

// FS returns the read-only project filesystem, rooted at Root. Callers
// that need both the tree and its location take this rather than
// threading Root and an fs.FS side by side.
func (p *Project) FS() vfs.FS { return vfs.NewDir(p.Root) }

// DefaultKindsDir is the path (relative to the project root) where
// `veil new kind` scaffolds a new kind when generators.kinds_dir is
// unset.
var DefaultKindsDir = filepath.Join(ArtifactsDir, "kinds")

// KindsDir returns the absolute path of the directory where `veil new
// kind` should scaffold new kind trees. Honors generators.kinds_dir from
// veil.json when set; otherwise falls back to <root>/.veil/kinds.
func (p *Project) KindsDir() string {
	dir := p.Generators.GetKindsDir()
	if dir == "" {
		dir = DefaultKindsDir
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(p.Root, dir))
}

// TypesOutputDir returns the absolute directory where `veil build` writes
// the shared types package (generators.types.output_dir), or "" when unset.
// When set, kinds whose `kinds` entry carries an `import` emit their
// generated types here and their hooks import them by package specifier.
func (p *Project) TypesOutputDir() string {
	dir := p.Generators.GetTypes().GetOutputDir()
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(p.Root, dir))
}

// Discover walks upward from startDir to find a directory containing
// a project config file (veil.json, veil.yaml, or veil.yml), loads it,
// resolves all kind paths, and returns the loaded project.
func Discover(startDir string) (*Project, error) {
	configPath, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}
	return Load(configPath)
}

// Load reads a veil.json at the given path and resolves all kind references
// relative to its parent directory. Unlike Discover, it does not walk the
// filesystem — the path is used as-is.
func Load(configPath string) (*Project, error) {
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
	kinds := make([]*config.Kind, 0, len(cfg.Kinds))
	for i, entry := range cfg.Kinds {
		ref, err := config.ParseKindEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: kinds[%d]: %w", configPath, i, err)
		}
		path := ref.GetPath()
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path = filepath.Clean(path)

		k, err := config.LoadKind(path)
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

	return &Project{
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
func mergeVariables(project map[string]*veilv1.Variable, kinds []*config.Kind) (map[string]*veilv1.Variable, error) {
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
		enumVals, err := config.ParsedEnum(v)
		if err != nil {
			return fmt.Errorf("variable %q enum: %w", name, err)
		}
		if config.HasDefault(v) {
			def, err := config.ParsedDefault(v)
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
	if err := codec.ReadFile(path, &cfg); err != nil {
		return nil, err
	}
	if err := codec.Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadProject loads the project at configPath, or discovers one by
// walking up from the working directory when configPath is empty. The
// entry point every command uses to answer "which project am I in".
func LoadProject(configPath string) (*Project, error) {
	if configPath != "" {
		return Load(configPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}
	return Discover(cwd)
}
