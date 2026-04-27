package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/build"
	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/fsutil"
	"github.com/vercel/veil/pkg/interact"
)

var nameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// New returns the "new" command group for scaffolding kinds and hooks.
func New() *cli.Command {
	return &cli.Command{
		Name:  "new",
		Usage: "Scaffold new kinds or hooks",
		Commands: []*cli.Command{
			newKind(),
			newHook(),
		},
	}
}

func newKind() *cli.Command {
	return &cli.Command{
		Name:      "kind",
		Usage:     "Scaffold a new kind",
		UsageText: "veil new kind <name>",
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "name",
				UsageText: "Name of the kind (lowercase, hyphens allowed)",
			},
		},
		Action: runNewKind,
	}
}

func newHook() *cli.Command {
	return &cli.Command{
		Name:      "hook",
		Usage:     "Scaffold a new hook for a kind",
		UsageText: "veil new hook <name> --kind <kind>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "kind",
				Usage:    "Name of the kind this hook belongs to",
				Required: true,
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "name",
				UsageText: "Name of the hook (lowercase, hyphens allowed)",
			},
		},
		Action: runNewHook,
	}
}

func runNewKind(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	name := c.StringArg("name")
	if err := validateName("kind name", name); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	var rb rollback
	defer rb.run()

	initialized, err := ensureVeilJSON(cwd)
	if err != nil {
		return err
	}
	if initialized {
		rb.removeFile(filepath.Join(cwd, "veil.json"))
		p.Successf("Initialized %s", filepath.Join(cwd, "veil.json"))
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return err
	}

	for _, k := range reg.Kinds {
		if k.Name == name {
			return fmt.Errorf("kind %q already exists", name)
		}
	}

	kindDir := filepath.Join(reg.Root, config.ArtifactsDir, "kinds", name)
	if _, err := os.Stat(kindDir); err == nil {
		return fmt.Errorf("directory %s already exists", kindDir)
	}

	sourcesDir := filepath.Join(kindDir, "sources")
	hookSrcDir := filepath.Join(kindDir, "hooks", "src")
	if err := os.MkdirAll(sourcesDir, 0755); err != nil {
		return fmt.Errorf("creating kind directory: %w", err)
	}
	if err := os.MkdirAll(hookSrcDir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}
	rb.removeTree(kindDir)

	schema := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema",
		"type":        "object",
		"description": fmt.Sprintf("Schema for the %q resource spec", name),
		"properties":  map[string]any{},
		"required":    []string{},
	}
	if err := writeJSON(filepath.Join(kindDir, "schema.json"), schema); err != nil {
		return err
	}

	sourceBlurb := fmt.Sprintf("This is a source file for %s.\n", name)
	if err := os.WriteFile(filepath.Join(sourcesDir, "source.txt"), []byte(sourceBlurb), 0644); err != nil {
		return fmt.Errorf("writing source.txt: %w", err)
	}

	helloTS := build.HookTemplate("hello-world")
	if err := os.WriteFile(filepath.Join(hookSrcDir, "hello-world.ts"), []byte(helloTS), 0644); err != nil {
		return fmt.Errorf("writing hello-world.ts: %w", err)
	}

	kindJSON := map[string]any{
		"name":    name,
		"sources": []string{"./sources/source.txt"},
		"hooks": map[string]any{
			"render": []map[string]any{
				{"path": "./hooks/src/hello-world.ts"},
			},
		},
		"schema": "./schema.json",
	}
	if err := writeJSON(filepath.Join(kindDir, "kind.json"), kindJSON); err != nil {
		return err
	}

	veilJSONPath := filepath.Join(reg.Root, "veil.json")
	prevVeil, err := os.ReadFile(veilJSONPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", veilJSONPath, err)
	}
	relKind := "./" + filepath.ToSlash(filepath.Join(config.ArtifactsDir, "kinds", name, "kind.json"))
	if err := registerKindInVeilJSON(reg.Root, relKind); err != nil {
		return err
	}
	rb.restoreFile(veilJSONPath, prevVeil)

	p.Successf("Scaffolded kind %q at %s", name, kindDir)

	reg, err = config.Discover(cwd)
	if err != nil {
		return fmt.Errorf("re-discovering registry after scaffold: %w", err)
	}
	if err := runBuildPipeline(reg, filepath.Join(reg.Root, config.PublicDir, "r"), true, p); err != nil {
		return err
	}
	rb.commit()
	return nil
}

func runNewHook(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	name := c.StringArg("name")
	if err := validateName("hook name", name); err != nil {
		return err
	}
	kindName := c.String("kind")

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return err
	}

	var k *config.Kind
	for _, candidate := range reg.Kinds {
		if candidate.Name == kindName {
			k = candidate
			break
		}
	}
	if k == nil {
		return fmt.Errorf("kind %q not found in registry", kindName)
	}

	outPath := filepath.Join(k.Dir, "hooks", "src", name+".ts")
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("hook %s already exists", outPath)
	}

	var rb rollback
	defer rb.run()

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	ts := build.HookTemplate(name)
	if err := os.WriteFile(outPath, []byte(ts), 0644); err != nil {
		return fmt.Errorf("writing hook: %w", err)
	}
	rb.removeFile(outPath)

	kindJSONPath := filepath.Join(k.Dir, "kind.json")
	prevKind, err := os.ReadFile(kindJSONPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", kindJSONPath, err)
	}
	relHook := "./" + filepath.ToSlash(filepath.Join("hooks", "src", name+".ts"))
	if err := appendHookToKind(k.Dir, "render", relHook); err != nil {
		return err
	}
	rb.restoreFile(kindJSONPath, prevKind)

	p.Successf("Scaffolded hook %s", outPath)

	reg, err = config.Discover(cwd)
	if err != nil {
		return fmt.Errorf("re-discovering registry after scaffold: %w", err)
	}
	if err := runBuildPipeline(reg, filepath.Join(reg.Root, config.PublicDir, "r"), true, p); err != nil {
		return err
	}
	rb.commit()
	return nil
}

// rollback collects undo actions in order. If commit() is not called
// before run() executes (deferred), the actions run in reverse order to
// restore the pre-scaffold state. Used so that a failed follow-up build
// does not leave the user with a partially-applied scaffold on disk.
type rollback struct {
	actions   []func()
	committed bool
}

func (r *rollback) commit() { r.committed = true }

func (r *rollback) run() {
	if r.committed {
		return
	}
	for i := len(r.actions) - 1; i >= 0; i-- {
		r.actions[i]()
	}
}

func (r *rollback) removeFile(path string) {
	r.actions = append(r.actions, func() { _ = os.Remove(path) })
}

func (r *rollback) removeTree(path string) {
	r.actions = append(r.actions, func() { _ = os.RemoveAll(path) })
}

func (r *rollback) restoreFile(path string, prev []byte) {
	r.actions = append(r.actions, func() { _ = os.WriteFile(path, prev, 0644) })
}

func validateName(label, name string) error {
	if name == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("%s %q must start with a lowercase letter and contain only lowercase letters, digits, '-', and '_'", label, name)
	}
	return nil
}

// ensureVeilJSON creates a bare veil.json at cwd if no veil.json exists
// in cwd or any ancestor. Returns true when it created one.
func ensureVeilJSON(cwd string) (bool, error) {
	if fsutil.FindAncestor(cwd, "veil.json") != "" {
		return false, nil
	}
	if err := writeJSON(filepath.Join(cwd, "veil.json"), map[string]any{"kinds": []string{}}); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// registerKindInVeilJSON appends relKind to the kinds[] array in veil.json,
// preserving the existing list.
func registerKindInVeilJSON(veilDir, relKind string) error {
	path := filepath.Join(veilDir, "veil.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	var kinds []string
	if raw, ok := cfg["kinds"]; ok && raw != nil {
		arr, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: \"kinds\" must be an array", path)
		}
		for _, v := range arr {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s: \"kinds\" entries must be strings", path)
			}
			kinds = append(kinds, s)
		}
	}

	if slices.Contains(kinds, relKind) {
		return nil
	}
	kinds = append(kinds, relKind)
	cfg["kinds"] = kinds

	return writeJSON(path, cfg)
}

// appendHookToKind appends relHook to the kind.json hooks.<lifecycle> array.
func appendHookToKind(kindDir, lifecycle, relHook string) error {
	path := filepath.Join(kindDir, "kind.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	hooksObj, _ := raw["hooks"].(map[string]any)
	if hooksObj == nil {
		hooksObj = map[string]any{}
	}

	var list []map[string]any
	if existing, ok := hooksObj[lifecycle]; ok && existing != nil {
		arr, ok := existing.([]any)
		if !ok {
			return fmt.Errorf("%s: \"hooks.%s\" must be an array", path, lifecycle)
		}
		for _, v := range arr {
			entry, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: \"hooks.%s\" entries must be objects with a \"path\" field", path, lifecycle)
			}
			list = append(list, entry)
		}
	}

	for _, entry := range list {
		if entry["path"] == relHook {
			return nil
		}
	}
	list = append(list, map[string]any{"path": relHook})
	hooksObj[lifecycle] = list
	raw["hooks"] = hooksObj

	return writeJSON(path, raw)
}
