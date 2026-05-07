// Package tsc runs TypeScript type-checking via an external `tsc` or `tsgo`
// binary. It is used by `veil build` to surface type errors in user-authored
// hook files before they are bundled.
package tsc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// candidates are the compiler names checked on PATH, in order. tsgo (the
// Go port of tsc) is preferred when available — it's much faster on
// cold-start.
var candidates = []string{"tsgo", "tsc"}

// Checker type-checks a set of TypeScript hook files.
type Checker interface {
	// Bin returns the path of the underlying compiler binary (for logging).
	Bin() string

	// Check type-checks every path in files. It is a no-op when files is
	// empty. Imports are followed automatically by the compiler, so a
	// hook that imports `./veil-types` does not require the sibling type
	// file to be passed explicitly. A non-zero exit from the compiler is
	// returned as an error whose message includes the combined
	// stdout/stderr so the caller can surface diagnostics verbatim.
	Check(files []string) error
}

// Find returns a Checker backed by the first of "tsgo" or "tsc" found on
// PATH. Returns nil if neither is available.
func Find() Checker {
	for _, bin := range candidates {
		if p, err := exec.LookPath(bin); err == nil {
			return &execChecker{bin: p}
		}
	}
	return nil
}

// execChecker runs a real tsc/tsgo binary via os/exec.
type execChecker struct {
	bin string
}

func (c *execChecker) Bin() string { return c.bin }

func (c *execChecker) Check(files []string) error {
	if len(files) == 0 {
		return nil
	}

	// Pass files explicitly with self-contained flags. We deliberately do
	// not honor any tsconfig.json on disk: a project-level tsconfig in a
	// monorepo typically `include`s the entire repo, and running tsc -p
	// against it OOMs while type-checking files unrelated to veil.
	// skipLibCheck skips checking declaration files inside node_modules,
	// which keeps memory bounded when hooks pull in large type packages
	// like kubernetes-types.
	args := append([]string{
		"--noEmit",
		"--strict",
		"--skipLibCheck",
		"--target", "ES2022",
		"--module", "ESNext",
		"--moduleResolution", "bundler",
	}, files...)

	cmd := exec.Command(c.bin, args...)
	// tsgo walks up from its own cwd looking for a tsconfig.json. If it
	// finds one alongside explicit-file arguments it rejects the run
	// with TS5112 ("tsconfig.json is present but will not be loaded if
	// files are specified on commandline"). Run from a tsconfig-free
	// directory so the ambient project config in a monorepo never
	// interferes — node_modules and relative-import resolution under
	// --moduleResolution bundler walks up from each input file, not
	// cwd, so this is safe.
	cmd.Dir = os.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("typecheck failed:\n%s", strings.TrimSpace(absolutizePaths(string(out), cmd.Dir)))
	}
	return nil
}

// diagnosticPath matches the leading `<path>.ts(L,C):` portion of a tsc
// or tsgo diagnostic line. We only rewrite this prefix; the trailing
// message (which may contain `.ts` substrings inside type literals) is
// left alone.
var diagnosticPath = regexp.MustCompile(`(?m)^([^()\n]+\.tsx?)(\([0-9]+,[0-9]+\):)`)

// absolutizePaths rewrites each diagnostic line's leading file path to
// an absolute path resolved against cwd. Without this, tsc reports
// `../../../../Users/...` because we run it from os.TempDir() to dodge
// TS5112, which is technically correct but reads as if the wrong file
// is being checked.
func absolutizePaths(out, cwd string) string {
	return diagnosticPath.ReplaceAllStringFunc(out, func(match string) string {
		m := diagnosticPath.FindStringSubmatch(match)
		if len(m) != 3 {
			return match
		}
		path := m[1]
		if !filepath.IsAbs(path) {
			path = filepath.Clean(filepath.Join(cwd, path))
		}
		return path + m[2]
	})
}
