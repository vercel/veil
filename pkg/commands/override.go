package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-json"
	"github.com/urfave/cli/v3"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
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
		Usage:     "Override one or more kind source files with local replacements",
		UsageText: "veil override <resource> [<source>...] [--skip-hooks] [--out <path>]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Usage: "Custom output directory for override files (default: alongside the resource)",
			},
			&cli.BoolFlag{
				Name:  "skip-hooks",
				Usage: "Discard hook mutations to overridden files at write time — rendered output matches the override files verbatim",
			},
		},
		Arguments: []cli.Argument{
			&cli.StringArg{
				Name:      "resource",
				UsageText: "Path to the resource JSON file the overrides are attached to",
			},
			&cli.StringArgs{
				Name:      "sources",
				Min:       0,
				Max:       -1,
				UsageText: "Source filenames declared by the resource's kind (e.g. \"sources/app.yaml\"). Omit to list available sources.",
			},
		},
		Action: runOverride,
	}
}

func runOverride(ctx context.Context, c *cli.Command) error {
	p := interact.Default()

	resourceArg := c.StringArg("resource")
	sourceArgs := c.StringArgs("sources")
	if resourceArg == "" {
		return fmt.Errorf("override: <resource> is required")
	}
	skipHooks := c.Bool("skip-hooks")
	outDir := c.String("out")

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

	sources := loadedKind.Kind.GetSources()

	// Discovery mode: only the resource was given. List the kind's
	// sources so the user can pick one for the next invocation.
	if len(sourceArgs) == 0 {
		return listOverridableSources(p, kindName, resourceArg, res.GetMetadata().GetOverrides(), sources)
	}

	// Validate every requested source up front so we don't half-apply
	// when one is misspelled.
	for _, s := range sourceArgs {
		if _, ok := sources[s]; !ok {
			return fmt.Errorf(
				"kind %q does not declare a source named %q (known sources: %s)",
				kindName, s, strings.Join(sortedKeys(sources), ", "),
			)
		}
	}

	resourceDir := filepath.Dir(resourceAbs)

	// Track every file we successfully wrote so we can roll them all
	// back if any later step fails.
	var writtenFiles []string
	rollback := func() {
		for _, f := range writtenFiles {
			_ = os.Remove(f)
		}
	}

	for _, sourceName := range sourceArgs {
		sourceContent := sources[sourceName]

		// Default output path: same basename as the source, dropped
		// alongside the resource file. With --out the file lands under
		// that directory (relative to the resource).
		basename := filepath.Base(sourceName)
		outRel := basename
		if outDir != "" {
			outRel = filepath.Join(outDir, basename)
		}
		outAbs := outRel
		if !filepath.IsAbs(outAbs) {
			outAbs = filepath.Join(resourceDir, outRel)
		}
		if _, err := os.Stat(outAbs); err == nil {
			rollback()
			return fmt.Errorf("override file %s already exists", outAbs)
		}
		if err := os.MkdirAll(filepath.Dir(outAbs), 0755); err != nil {
			rollback()
			return fmt.Errorf("creating override directory: %w", err)
		}
		if err := os.WriteFile(outAbs, []byte(sourceContent), 0644); err != nil {
			rollback()
			return fmt.Errorf("writing override file %s: %w", outAbs, err)
		}
		writtenFiles = append(writtenFiles, outAbs)

		// Path stored on the override entry is relative to the
		// resource file's directory — matches the resolution rule in
		// render's applyOverrides. Forward slashes for cross-platform
		// stability.
		storedPath := outRel
		if filepath.IsAbs(outRel) {
			storedPath = outAbs
		}
		storedPath = filepath.ToSlash(storedPath)

		if err := registerOverride(resourceAbs, sourceName, storedPath, skipHooks); err != nil {
			rollback()
			return err
		}

		p.Successf("Overrode %s with %s", sourceName, storedPath)
	}

	if skipHooks {
		p.Mutedf("  skip_hooks: true (hook mutations discarded at render)")
	}
	return nil
}

// listOverridableSources prints the kind's source list for the user
// when the override command is invoked without a source. Already-
// overridden entries are flagged so the user knows what's already
// taken without re-reading the resource JSON.
func listOverridableSources(p interact.Printer, kindName, resourceArg string, existing []*veilv1.Override, sources map[string]string) error {
	taken := make(map[string]bool, len(existing))
	for _, ov := range existing {
		taken[ov.GetSource()] = true
	}

	keys := sortedKeys(sources)
	if len(keys) == 0 {
		p.Infof("kind %q declares no sources", kindName)
		return nil
	}

	p.Infof("Sources declared by kind %q:", kindName)
	for _, k := range keys {
		if taken[k] {
			p.Mutedf("  %s  (already overridden)", k)
		} else {
			p.Mutedf("  %s", k)
		}
	}
	p.Infof("Pick one and re-run: veil override %s <source> [--skip-hooks]", resourceArg)
	return nil
}

// sortedKeys returns the map's keys in lexical order. Used so the
// override listing is stable across runs.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
