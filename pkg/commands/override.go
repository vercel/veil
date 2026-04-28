package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/config"
	"github.com/vercel/veil/pkg/interact"
	"github.com/vercel/veil/pkg/registry"
	"github.com/vercel/veil/pkg/resource"
)

// Override returns the "override" subcommand. An override copies a
// kind's source file next to the resource that's overriding it and
// records the substitution under metadata.overrides; render replaces
// the kind's content with the local file before any hook runs.
//
// `--skip-hooks` additionally re-stamps the local file's bytes after
// the pipeline finishes, so hook mutations to that file are discarded
// — useful when a kind's hooks would otherwise stomp on a customized
// override.
func Override() *cli.Command {
	return &cli.Command{
		Name:      "override",
		Usage:     "Override a kind source file with a local replacement",
		UsageText: "veil override <resource> <source> [--skip-hooks] [--out <path>]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Usage: "Custom output path for the override file (default: alongside the resource)",
			},
			&cli.BoolFlag{
				Name:  "skip-hooks",
				Usage: "Discard hook mutations to this file at write time — the rendered output is the override file verbatim",
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "resource",
				UsageText: "Path to the resource JSON file the override is attached to",
			},
			&cli.StringArg{
				Name:      "source",
				UsageText: "Source filename declared by the resource's kind (e.g. \"sources/app.yaml\")",
			},
		},
		Action: runOverride,
	}
}

func runOverride(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	resourceArg := c.StringArg("resource")
	sourceArg := c.StringArg("source")
	if resourceArg == "" || sourceArg == "" {
		return fmt.Errorf("override: <resource> and <source> are required")
	}
	skipHooks := c.Bool("skip-hooks")

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	reg, err := config.Discover(cwd)
	if err != nil {
		return err
	}

	resourceAbs, err := filepath.Abs(resourceArg)
	if err != nil {
		return fmt.Errorf("resolving resource path: %w", err)
	}
	relFromRoot, err := filepath.Rel(reg.Root, resourceAbs)
	if err != nil || strings.HasPrefix(relFromRoot, "..") {
		return fmt.Errorf("%s is outside the project root %s", resourceArg, reg.Root)
	}

	res, err := resource.Load(os.DirFS(reg.Root), filepath.ToSlash(relFromRoot))
	if err != nil {
		return fmt.Errorf("loading resource %s: %w", resourceArg, err)
	}

	kindName := res.GetMetadata().GetKind()
	if kindName == "" {
		return fmt.Errorf("%s: metadata.kind is missing", resourceArg)
	}

	// Resolve the kind via the same registry render uses, so we can
	// look up the kind's compiled sources without reaching into
	// .veil/kinds/ on disk.
	registries, err := resolveRegistries(nil, reg)
	if err != nil {
		return err
	}
	kindReg, err := registry.Load(registries)
	if err != nil {
		return err
	}
	loadedKind, err := kindReg.LoadKind(kindName)
	if err != nil {
		return fmt.Errorf("loading kind %q: %w", kindName, err)
	}
	sourceContent, ok := loadedKind.Kind.GetSources()[sourceArg]
	if !ok {
		known := make([]string, 0, len(loadedKind.Kind.GetSources()))
		for k := range loadedKind.Kind.GetSources() {
			known = append(known, k)
		}
		return fmt.Errorf("kind %q does not declare a source named %q (known sources: %s)", kindName, sourceArg, strings.Join(known, ", "))
	}

	// Default output path: same basename as the source, dropped
	// alongside the resource file. The source's path is kept on the
	// override entry verbatim so the renderer can match against it.
	resourceDir := filepath.Dir(resourceAbs)
	outRel := c.String("out")
	if outRel == "" {
		outRel = filepath.Base(sourceArg)
	}
	outAbs := outRel
	if !filepath.IsAbs(outAbs) {
		outAbs = filepath.Join(resourceDir, outRel)
	}
	if _, err := os.Stat(outAbs); err == nil {
		return fmt.Errorf("override file %s already exists", outAbs)
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0755); err != nil {
		return fmt.Errorf("creating override directory: %w", err)
	}
	if err := os.WriteFile(outAbs, []byte(sourceContent), 0644); err != nil {
		return fmt.Errorf("writing override file: %w", err)
	}

	// Path stored on the override entry is relative to the resource
	// file's directory — matches the resolution rule in render's
	// applyOverrides. Use forward slashes so the JSON is stable
	// across platforms.
	storedPath := outRel
	if filepath.IsAbs(outRel) {
		storedPath = outAbs
	}
	storedPath = filepath.ToSlash(storedPath)

	if err := registerOverride(resourceAbs, sourceArg, storedPath, skipHooks); err != nil {
		// Roll back the file we just wrote so a partial-apply doesn't
		// leave dead bytes on disk.
		_ = os.Remove(outAbs)
		return err
	}

	p.Successf("Overrode %s with %s", sourceArg, storedPath)
	if skipHooks {
		p.Mutedf("  skip_hooks: true (hook mutations discarded at render)")
	}
	return nil
}

// registerOverride mutates the resource JSON in place to append the new
// override under metadata.overrides. The file is round-tripped through
// a generic map so unrelated fields and formatting hints stay intact.
func registerOverride(resourcePath, source, path string, skipHooks bool) error {
	data, err := os.ReadFile(resourcePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", resourcePath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", resourcePath, err)
	}
	meta, _ := doc["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		doc["metadata"] = meta
	}
	overrides, _ := meta["overrides"].([]any)
	for _, raw := range overrides {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["source"] == source {
			return fmt.Errorf("an override for %q already exists in %s", source, resourcePath)
		}
	}
	entry := map[string]any{
		"source": source,
		"path":   path,
	}
	if skipHooks {
		entry["skip_hooks"] = true
	}
	meta["overrides"] = append(overrides, entry)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", resourcePath, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(resourcePath, out, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", resourcePath, err)
	}
	return nil
}
