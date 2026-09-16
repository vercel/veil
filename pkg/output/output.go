// Package output publishes root-owned bundles with conservative reconciliation.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vercel/veil/pkg/hook"
	"golang.org/x/text/unicode/norm"
)

const metadata = ".veil"
const manifestPath = metadata + "/manifest.json"
const journalPath = metadata + "/intent.json"
const lockPath = metadata + "/lock"

// Root is one successfully computed resource. Bundle destinations are relative
// to Name, just as in unmanaged rendering; ownership uses both Kind and Name.
type Root struct {
	Kind   string
	Name   string
	Bundle hook.Bundle
}

// Options controls explicit adoption of existing, byte-identical unowned files.
type Options struct{ Adopt bool }

type manifest struct {
	Version int                          `json:"version"`
	Roots   map[string]map[string]string `json:"roots"`
}

type operation struct {
	Path    string `json:"path"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	Content string `json:"content,omitempty"`
}

type intent struct {
	Before     manifest    `json:"before"`
	After      manifest    `json:"after"`
	Operations []operation `json:"operations"`
}

// Publish preflights the entire selected batch before changing output files.
// An interrupted publication leaves a durable intent; Recover must finish it
// before another publication. This is not a multi-file atomic transaction.
func Publish(outDir string, roots []Root, opts Options) error {
	return withLock(outDir, func(root *os.Root) error {
		if err := requireNoIntent(root); err != nil {
			return err
		}
		before, err := loadManifest(root)
		if err != nil {
			return err
		}
		after := cloneManifest(before)
		contents := map[string]string{}
		sources := map[string]string{}
		selected := map[string]bool{}
		for _, resource := range roots {
			id, err := identity(resource.Kind, resource.Name)
			if err != nil {
				return err
			}
			if selected[id] {
				return fmt.Errorf("root %s selected more than once", id)
			}
			selected[id] = true
			if _, err := cleanPath(resource.Name); err != nil {
				return fmt.Errorf("invalid resource output directory: %w", err)
			}
			files := map[string]string{}
			keys := make([]string, 0, len(resource.Bundle))
			for key := range resource.Bundle {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				file := resource.Bundle[key]
				if file.Deleted {
					continue
				}
				destination := file.Path
				if destination == "" {
					destination = key
				}
				if filepath.IsAbs(destination) || filepath.IsAbs(resource.Name) {
					return fmt.Errorf("absolute output destination %q for root %s", destination, id)
				}
				path, err := cleanPath(filepath.Join(resource.Name, destination))
				if err != nil {
					return err
				}
				source := fmt.Sprintf("root %s source %q", id, key)
				folded := foldPath(path)
				if previous, exists := sources[folded]; exists {
					return fmt.Errorf("path collision at %q: %s and %s", path, previous, source)
				}
				sources[folded] = source
				contents[path] = file.Content
				files[path] = hash(file.Content)
			}
			after.Roots[id] = files
		}
		for path, source := range sources {
			for parent := filepath.ToSlash(filepath.Dir(path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
				if previous, exists := sources[parent]; exists {
					return fmt.Errorf("file/directory path collision at %q: %s and %s", path, previous, source)
				}
			}
		}
		return reconcile(root, before, after, contents, selected, opts.Adopt)
	})
}

// Remove releases an exact qualified kind/name root, including when its resource
// declaration no longer exists. Modified tracked files are never removed.
func Remove(outDir, kind, name string) error {
	id, err := identity(kind, name)
	if err != nil {
		return err
	}
	return withLock(outDir, func(root *os.Root) error {
		if err := requireNoIntent(root); err != nil {
			return err
		}
		before, err := loadManifest(root)
		if err != nil {
			return err
		}
		if _, exists := before.Roots[id]; !exists {
			return fmt.Errorf("root %s is not owned by this output directory", id)
		}
		after := cloneManifest(before)
		delete(after.Roots, id)
		return reconcile(root, before, after, nil, map[string]bool{id: true}, false)
	})
}

// Recover resumes a persisted intent only if every affected file is still in
// its recorded before or after state. Unknown bytes stop recovery before writes.
func Recover(outDir string) error {
	return withLock(outDir, func(root *os.Root) error {
		var pending intent
		if err := readJSON(root, journalPath, &pending); err != nil {
			return fmt.Errorf("reading publication intent: %w", err)
		}
		if err := validateIntent(pending); err != nil {
			return err
		}
		current, err := loadManifest(root)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, pending.Before) && !reflect.DeepEqual(current, pending.After) {
			return fmt.Errorf("manifest does not match publication intent; restore the recorded manifest before recovery")
		}
		return finish(root, pending)
	})
}

func identity(kind, name string) (string, error) {
	if kind == "" || name == "" {
		return "", fmt.Errorf("qualified kind and resource name are required")
	}
	data, err := json.Marshal([2]string{kind, name})
	return string(data), err
}

func hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func cleanPath(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) || path == "." || strings.Contains(path, "\\") {
		return "", fmt.Errorf("output path %q escapes the output directory or is invalid", path)
	}
	first := strings.Split(filepath.ToSlash(path), "/")[0]
	if strings.EqualFold(first, metadata) {
		return "", fmt.Errorf("output path %q uses reserved metadata directory %s", path, metadata)
	}
	return filepath.ToSlash(path), nil
}

func cloneManifest(in manifest) manifest {
	out := manifest{Version: 1, Roots: make(map[string]map[string]string, len(in.Roots))}
	for id, files := range in.Roots {
		out.Roots[id] = files
	}
	return out
}

func owners(m manifest) (map[string]string, error) {
	if m.Version != 1 || m.Roots == nil {
		return nil, fmt.Errorf("invalid or unsupported managed output manifest")
	}
	out := map[string]string{}
	folded := map[string]string{}
	for id, files := range m.Roots {
		var parts [2]string
		if err := json.Unmarshal([]byte(id), &parts); err != nil {
			return nil, fmt.Errorf("invalid root identity %q", id)
		}
		canonical, err := identity(parts[0], parts[1])
		if err != nil || canonical != id {
			return nil, fmt.Errorf("invalid root identity %q", id)
		}
		for path, digest := range files {
			clean, err := cleanPath(path)
			if err != nil {
				return nil, err
			}
			if clean != path {
				return nil, fmt.Errorf("non-normalized managed path %q", path)
			}
			if bytes, err := hex.DecodeString(digest); err != nil || len(bytes) != sha256.Size {
				return nil, fmt.Errorf("invalid hash for %q", path)
			}
			key := foldPath(path)
			if prev, exists := folded[key]; exists {
				return nil, fmt.Errorf("conflicting owners or normalized path collision: %q (%s) and %q (%s)", prev, out[prev], path, id)
			}
			folded[key] = path
			out[path] = id
		}
	}
	for path := range out {
		for parent := filepath.ToSlash(filepath.Dir(path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if prev, exists := folded[foldPath(parent)]; exists {
				return nil, fmt.Errorf("file/directory path collision: %q and %q", prev, path)
			}
		}
	}
	return out, nil
}

func loadManifest(root *os.Root) (manifest, error) {
	m := manifest{Version: 1, Roots: map[string]map[string]string{}}
	if err := readJSON(root, manifestPath, &m); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return manifest{}, fmt.Errorf("reading managed manifest: %w", err)
	}
	_, err := owners(m)
	return m, err
}

func reconcile(root *os.Root, before, after manifest, contents map[string]string, selected map[string]bool, adopt bool) error {
	oldOwners, err := owners(before)
	if err != nil {
		return err
	}
	newOwners, err := owners(after)
	if err != nil {
		return err
	}
	// Ownership transfers require an explicit remove first, even if both roots
	// happen to be selected in the same batch.
	for path, owner := range newOwners {
		if old, exists := oldOwners[path]; exists && old != owner {
			return fmt.Errorf("output %q is owned by %s, not %s; remove the old root explicitly first", path, old, owner)
		}
	}
	paths := map[string]bool{}
	for path, owner := range oldOwners {
		if selected[owner] {
			paths[path] = true
		}
	}
	for path := range contents {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	pending := intent{Before: before, After: after}
	for _, path := range ordered {
		actual, err := diskHash(root, path)
		if err != nil {
			return err
		}
		if owner, tracked := oldOwners[path]; tracked {
			if actual != before.Roots[owner][path] {
				return fmt.Errorf("tracked output %q was modified or removed; restore its recorded bytes before rendering/removing (save local edits elsewhere)", path)
			}
		} else if actual != "" {
			content, writes := contents[path]
			if !writes || actual != hash(content) {
				return fmt.Errorf("unowned output %q differs; move it aside before rendering", path)
			}
			if !adopt {
				return fmt.Errorf("unowned output %q already exists; use --adopt to claim byte-identical files", path)
			}
		}
		op := operation{Path: path, Before: actual}
		if content, exists := contents[path]; exists {
			op.Content = content
			op.After = hash(content)
		}
		pending.Operations = append(pending.Operations, op)
	}
	if err := writeJSON(root, journalPath, pending); err != nil {
		return err
	}
	return finish(root, pending)
}

func validateIntent(pending intent) error {
	oldOwners, err := owners(pending.Before)
	if err != nil {
		return err
	}
	newOwners, err := owners(pending.After)
	if err != nil {
		return err
	}
	for path, owner := range newOwners {
		if previous, exists := oldOwners[path]; exists && previous != owner {
			return fmt.Errorf("intent transfers ownership of %q without explicit removal", path)
		}
	}
	seen := map[string]bool{}
	for _, op := range pending.Operations {
		path, err := cleanPath(op.Path)
		if err != nil {
			return err
		}
		if path != op.Path || seen[path] {
			return fmt.Errorf("invalid duplicate or non-normalized intent path %q", op.Path)
		}
		seen[path] = true
		if op.After != "" {
			if hash(op.Content) != op.After || pending.After.Roots[newOwners[path]][path] != op.After {
				return fmt.Errorf("invalid intent content for %q", path)
			}
		} else if _, exists := newOwners[path]; exists {
			return fmt.Errorf("intent deletes owned path %q", path)
		}
		if owner, exists := oldOwners[path]; exists {
			if pending.Before.Roots[owner][path] != op.Before {
				return fmt.Errorf("invalid intent original hash for %q", path)
			}
		} else if op.Before != "" && op.Before != op.After {
			return fmt.Errorf("intent would overwrite unowned path %q", path)
		}
	}
	for path, owner := range oldOwners {
		if pending.Before.Roots[owner][path] != pending.After.Roots[owner][path] && !seen[path] {
			return fmt.Errorf("intent omits changed path %q", path)
		}
	}
	for path, owner := range newOwners {
		if pending.Before.Roots[owner][path] != pending.After.Roots[owner][path] && !seen[path] {
			return fmt.Errorf("intent omits changed path %q", path)
		}
	}
	return nil
}

func finish(root *os.Root, pending intent) error {
	// Check the entire intent before resuming any operation, not just each file
	// as it is reached. Recheck immediately before each mutation as well.
	for _, op := range pending.Operations {
		if err := checkOperation(root, op); err != nil {
			return err
		}
	}
	for _, op := range pending.Operations {
		if err := checkOperation(root, op); err != nil {
			return err
		}
		actual, err := diskHash(root, op.Path)
		if err != nil {
			return err
		}
		if actual == op.After {
			continue
		}
		if op.After == "" {
			if err := root.Remove(op.Path); err != nil {
				return fmt.Errorf("removing %q (intent retained; run veil outputs recover): %w", op.Path, err)
			}
			if err := syncDir(root, filepath.Dir(op.Path)); err != nil {
				return err
			}
		} else if err := atomicWrite(root, op.Path, []byte(op.Content)); err != nil {
			return fmt.Errorf("writing %q (intent retained; run veil outputs recover): %w", op.Path, err)
		}
	}
	if err := writeJSON(root, manifestPath, pending.After); err != nil {
		return err
	}
	if err := root.Remove(journalPath); err != nil {
		return err
	}
	return syncDir(root, metadata)
}

func checkOperation(root *os.Root, op operation) error {
	actual, err := diskHash(root, op.Path)
	if err != nil {
		return err
	}
	if actual != op.Before && actual != op.After {
		return fmt.Errorf("output %q differs from both states in the publication intent; save local edits elsewhere and restore recorded before/after bytes, then run veil outputs recover", op.Path)
	}
	return nil
}

// Reject symlinks even when they point inside the root. os.Root additionally
// confines operations if directories change between inspection and writing.
func checkPath(root *os.Root, path string) error {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		dir, err := root.Open(filepath.Dir(prefix))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if foldPath(entry.Name()) == foldPath(parts[i]) && entry.Name() != parts[i] {
				return fmt.Errorf("output path %q aliases existing differently-cased entry %q", prefix, entry.Name())
			}
		}
		info, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink at output path %q is not allowed", prefix)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("output parent %q is not a directory", prefix)
		}
	}
	return nil
}

func diskHash(root *os.Root, path string) (string, error) {
	if err := checkPath(root, path); err != nil {
		return "", err
	}
	info, err := root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("output %q is not a regular file", path)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hash(string(data)), nil
}

func readJSON(root *os.Root, path string, value any) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSON(root *os.Root, path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(root, path, append(data, '\n'))
}

func atomicWrite(root *os.Root, path string, content []byte) error {
	if err := checkPath(root, path); err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Temp files live only in the reserved directory; a crash cannot leave
	// an unowned temporary output mixed in with user files.
	temp := metadata + "/write-" + fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temp, path); err != nil {
		return err
	}
	if err := syncDir(root, metadata); err != nil {
		return err
	}
	// Persist newly created destination directory entries all the way to the
	// output root before committing the manifest.
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := syncDir(root, dir); err != nil {
			return err
		}
		if dir == "." {
			break
		}
	}
	return nil
}

func syncDir(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func requireNoIntent(root *os.Root) error {
	if err := checkPath(root, journalPath); err != nil {
		return err
	}
	_, err := root.Lstat(journalPath)
	if err == nil {
		return fmt.Errorf("unfinished managed publication; run veil outputs recover --out %q before rendering or removing roots", root.Name())
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func withLock(outDir string, action func(*os.Root) error) error {
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	// Inspect ancestors before MkdirAll/OpenRoot, which otherwise follow an
	// output-root symlink. This also makes the lock name unambiguous.
	for current := abs; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in output directory %q is not allowed; use its canonical path", current)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkPath(root, metadata); err != nil {
		return err
	}
	if err := root.MkdirAll(metadata, 0755); err != nil {
		return err
	}
	if err := syncDir(root, "."); err != nil {
		return err
	}
	if err := checkPath(root, lockPath); err != nil {
		return err
	}
	lock, err := root.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, fs.ErrExist) {
		owner, _ := root.ReadFile(lockPath)
		return fmt.Errorf("managed output is locked (%s); confirm that publisher is no longer running before manually removing %s, then run veil outputs recover if intent.json exists", strings.TrimSpace(string(owner)), filepath.Join(outDir, lockPath))
	}
	if err != nil {
		return err
	}
	defer root.Remove(lockPath)
	host, _ := os.Hostname()
	_, err = fmt.Fprintf(lock, "pid=%d host=%s started=%s\n", os.Getpid(), host, time.Now().UTC().Format(time.RFC3339))
	if err == nil {
		err = lock.Sync()
	}
	closeErr := lock.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := syncDir(root, metadata); err != nil {
		return err
	}
	return action(root)
}

func foldPath(path string) string {
	return strings.ToLower(norm.NFC.String(path))
}
