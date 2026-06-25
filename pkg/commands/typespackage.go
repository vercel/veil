package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/vercel/veil/pkg/build"
	"github.com/vercel/veil/pkg/config"
)

// typesPackage is the resolved shared types package for a build: the absolute
// output_dir (generators.types.output_dir), the package name shared by every
// opted-in kind, and each opted-in kind's module subpath (derived from its
// import.name). nil when output_dir is unset or no kind opted in.
type typesPackage struct {
	dir     string
	name    string
	subpath map[string]string // kind name -> module subpath, e.g. "service"
}

// resolveTypesPackage inspects the registry's generators.types output_dir and
// each kind's import wiring, returning the shared types package to emit — or
// nil when output_dir is unset or no kind opted in. Errors when opted-in kinds
// disagree on the package name or omit a module subpath.
func resolveTypesPackage(reg *config.Registry) (*typesPackage, error) {
	dir := reg.TypesOutputDir()
	if dir == "" {
		// An import without output_dir would silently degrade to a broken
		// tree (hooks import a bare package specifier with no package to
		// resolve them), so reject it up front rather than half-applying.
		for _, k := range reg.Kinds {
			if k.Import != nil {
				return nil, fmt.Errorf("kind %q declares an import but generators.types.output_dir is unset", k.Name)
			}
		}
		return nil, nil
	}
	tp := &typesPackage{dir: dir, subpath: map[string]string{}}
	bySubpath := map[string]string{} // subpath -> kind name, to catch collisions
	for _, k := range reg.Kinds {
		if k.Import == nil {
			continue
		}
		name := k.Import.GetName()
		pkg, sub := build.SplitImportSpecifier(name)
		if sub == "" {
			return nil, fmt.Errorf("kind %q: import.name %q has no subpath (expected e.g. %q)", k.Name, name, pkg+"/"+k.Name)
		}
		if tp.name == "" {
			tp.name = pkg
		} else if tp.name != pkg {
			return nil, fmt.Errorf("kind %q: import package %q conflicts with %q — all kinds must share one types package", k.Name, pkg, tp.name)
		}
		if other, dup := bySubpath[sub]; dup {
			return nil, fmt.Errorf("kinds %q and %q both map to types subpath %q — each kind needs a distinct import.name", other, k.Name, sub)
		}
		bySubpath[sub] = k.Name
		tp.subpath[k.Name] = sub
	}
	if tp.name == "" {
		return nil, nil // output_dir set but nothing opted in
	}
	return tp, nil
}

// hookTypesImport returns the module specifier a scaffolded hook should import
// its types from: the shared package subpath for a kind wired into
// generators.types (e.g. "@platform/veil-types/service"), else the sibling
// "./veil-types".
func hookTypesImport(k *config.Kind) string {
	if k.Import != nil {
		return k.Import.GetName()
	}
	return "./veil-types"
}

// has reports whether the kind opted into the package.
func (tp *typesPackage) has(kindName string) bool {
	if tp == nil {
		return false
	}
	_, ok := tp.subpath[kindName]
	return ok
}

// moduleFile returns the absolute path of a kind's generated module within the
// package, e.g. <dir>/service.ts.
func (tp *typesPackage) moduleFile(kindName string) string {
	return filepath.Join(tp.dir, filepath.FromSlash(tp.subpath[kindName])+".ts")
}

// hostImportFor returns the module specifier a kind module uses to import the
// shared host types — a relative path from the kind module to host.ts (e.g.
// "./host", or "../host" for a nested subpath).
func (tp *typesPackage) hostImportFor(kindName string) string {
	from := filepath.Dir(tp.moduleFile(kindName))
	rel, err := filepath.Rel(from, filepath.Join(tp.dir, "host"))
	if err != nil {
		return "./host"
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
}

// writeShared writes the package's kind-independent host.ts (the shared half
// every kind module imports). The package.json manifest is written separately
// by writeManifest after the per-kind loop, so its exports never reference a
// module a failed build didn't write.
func (tp *typesPackage) writeShared(reg *config.Registry) error {
	if err := os.MkdirAll(tp.dir, 0755); err != nil {
		return err
	}
	host, err := build.HostTypes(reg.Variables)
	if err != nil {
		return fmt.Errorf("host types: %w", err)
	}
	return os.WriteFile(filepath.Join(tp.dir, "host.ts"), []byte(host), 0644)
}

// writeKindModule writes one kind's generated module (output_dir/<subpath>.ts),
// importing the shared host types, and returns the file's absolute path.
func (tp *typesPackage) writeKindModule(k *config.Kind, reg *config.Registry, graph *build.KindGraph) (string, error) {
	ts, err := build.VeilTypes(k, reg.Variables, graph, tp.hostImportFor(k.Name))
	if err != nil {
		return "", err
	}
	file := tp.moduleFile(k.Name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(ts), 0644); err != nil {
		return "", err
	}
	return file, nil
}

// writeManifest writes (or updates) the types package's package.json: its
// name plus an exports map for ./host and each successfully-built kind module
// (named in kinds). Called after the per-kind loop so exports never reference
// a module that a failed build didn't write. Other fields in an existing
// package.json are preserved.
func (tp *typesPackage) writeManifest(kinds []string) error {
	pkgPath := filepath.Join(tp.dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	m, err := decodePackageJSON(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", pkgPath, err)
	}

	m["name"] = tp.name
	if _, ok := m["version"]; !ok {
		m["version"] = "0.0.0"
	}
	m["private"] = true

	exports := map[string]any{"./host": "./host.ts"}
	for _, name := range kinds {
		sub := tp.subpath[name]
		exports["./"+sub] = "./" + sub + ".ts"
	}
	m["exports"] = exports

	out, err := encodePackageJSON(data, m)
	if err != nil {
		return err
	}
	return os.WriteFile(pkgPath, out, 0644)
}

// ensureTypesDep adds the types-package dependency to the nearest package.json
// at or above hookDir (bounded by stop). Errors when no package.json is found
// in that range.
func ensureTypesDep(hookDir, stop, name, value string) error {
	pkgPath := nearestPackageJSON(hookDir, stop)
	if pkgPath == "" {
		return fmt.Errorf("no package.json at or above %s", hookDir)
	}
	return addDepToPackageJSON(pkgPath, name, value)
}

// addDepToPackageJSON adds name → value (e.g. "@platform/veil-types" →
// "workspace:*") to the devDependencies of the package.json at pkgPath, so a
// hook's import of the package resolves. No-op when already present with the
// same value.
func addDepToPackageJSON(pkgPath, name, value string) error {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return err
	}
	m, err := decodePackageJSON(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", pkgPath, err)
	}
	dev, _ := m["devDependencies"].(map[string]any)
	if dev == nil {
		dev = map[string]any{}
	}
	if cur, ok := dev[name].(string); ok && cur == value {
		return nil
	}
	dev[name] = value
	m["devDependencies"] = dev
	out, err := encodePackageJSON(data, m)
	if err != nil {
		return err
	}
	return os.WriteFile(pkgPath, out, 0644)
}

// nearestPackageJSON walks up from dir looking for a package.json, checking
// stop last. Returns "" when none is found.
func nearestPackageJSON(dir, stop string) string {
	for {
		candidate := filepath.Join(dir, "package.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if dir == stop {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// decodePackageJSON unmarshals package.json bytes into a map, decoding numbers
// as json.Number so re-encoding preserves their literal form (no float64
// reformatting / integer-precision loss). Empty data yields an empty map.
func decodePackageJSON(data []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// encodeJSONValue marshals v with 2-space indent under the given line prefix
// and — matching the rest of the repo's JSON output (see pkg/protoencode) —
// with HTML escaping OFF, so &, <, > in existing fields (npm scripts,
// repository URLs) round-trip verbatim instead of being mangled to & etc.
func encodeJSONValue(v any, prefix string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if prefix != "" {
		enc.SetIndent(prefix, "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// encodePackageJSON re-serializes a package.json map with 2-space indent,
// preserving the original top-level key order (keys absent from orig are
// appended alphabetically) and emitting nested objects (e.g. dependency maps)
// with alphabetically-sorted keys — the map-marshal default. orig is the
// file's previous bytes (empty for a new file).
func encodePackageJSON(orig []byte, m map[string]any) ([]byte, error) {
	keys := topLevelKeyOrder(orig, m)
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range keys {
		kb, err := encodeJSONValue(k, "")
		if err != nil {
			return nil, err
		}
		vb, err := encodeJSONValue(m[k], "  ")
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  %s: %s", kb, vb)
		if i < len(keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// topLevelKeyOrder returns the keys of m ordered by first appearance in orig
// (the previous file bytes), with any keys absent from orig appended
// alphabetically.
func topLevelKeyOrder(orig []byte, m map[string]any) []string {
	var order []string
	seen := map[string]bool{}
	if len(orig) > 0 {
		dec := json.NewDecoder(bytes.NewReader(orig))
		if _, err := dec.Token(); err == nil { // opening '{'
			for dec.More() {
				tok, err := dec.Token()
				if err != nil {
					break
				}
				key, ok := tok.(string)
				if !ok {
					break
				}
				if _, present := m[key]; present && !seen[key] {
					order = append(order, key)
					seen[key] = true
				}
				skipJSONValue(dec)
			}
		}
	}
	var extra []string
	for k := range m {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

// skipJSONValue consumes the next value from dec (which has just returned an
// object key), balancing nested object/array delimiters.
func skipJSONValue(dec *json.Decoder) {
	tok, err := dec.Token()
	if err != nil {
		return
	}
	switch tok {
	case json.Delim('{'), json.Delim('['):
		depth := 1
		for depth > 0 {
			t, err := dec.Token()
			if err != nil {
				return
			}
			switch t {
			case json.Delim('{'), json.Delim('['):
				depth++
			case json.Delim('}'), json.Delim(']'):
				depth--
			}
		}
	}
}
