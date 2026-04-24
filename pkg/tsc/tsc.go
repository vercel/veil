// Package tsc runs TypeScript type-checking via an external `tsc` or `tsgo`
// binary. It is used by `veil build` to surface type errors in user-authored
// transform files before they are bundled.
package tsc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vercel/veil/pkg/fsutil"
)

// candidates are the compiler names checked on PATH, in order. tsgo (the
// Go port of tsc) is preferred when available — it's much faster on
// cold-start.
var candidates = []string{"tsgo", "tsc"}

// Checker type-checks a directory of .ts files.
type Checker interface {
	// Bin returns the path of the underlying compiler binary (for logging).
	Bin() string

	// Check type-checks every .ts file in dir. It is a no-op if dir contains
	// no .ts files. A non-zero exit from the compiler is returned as an
	// error whose message includes the combined stdout/stderr so the caller
	// can surface the diagnostics verbatim.
	Check(dir string) error
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

func (c *execChecker) Check(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil {
		return fmt.Errorf("scanning %s: %w", dir, err)
	}
	if len(matches) == 0 {
		return nil
	}

	var args []string
	if tsconfig := fsutil.FindAncestor(dir, "tsconfig.json"); tsconfig != "" {
		// Honor the project's tsconfig — --noEmit is enforced either way so
		// type-checking never accidentally writes output files.
		args = []string{"-p", tsconfig, "--noEmit"}
	} else {
		args = append([]string{
			"--noEmit",
			"--strict",
			"--target", "ES2022",
			"--module", "ESNext",
			"--moduleResolution", "bundler",
		}, matches...)
	}

	out, err := exec.Command(c.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("typecheck failed:\n%s", strings.TrimSpace(string(out)))
	}
	return nil
}
