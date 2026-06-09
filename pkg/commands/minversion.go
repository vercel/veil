package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/vercel/veil/pkg/interact"
)

// enforceMinVersion makes the running veil meet a project's required
// minimum CLI version. When veil.yaml sets `cli_version` and the running
// binary is an OLDER released build, it downloads and installs exactly
// that version, then re-execs so the in-flight command proceeds on the
// required CLI.
//
// It is a no-op when:
//   - cli_version is unset,
//   - the running version already satisfies the minimum, or
//   - the running version isn't a comparable release (a `dev` or
//     `edge-<sha>` build) — those are trusted as-is, so a local or edge
//     build isn't clobbered mid-render.
//
// On a satisfied minimum (or a skip) it returns nil and the caller
// continues. On a successful update it does NOT return — the process
// image is replaced via re-exec.
func enforceMinVersion(ctx context.Context, minVersion string) error {
	p := interact.Default()
	minVersion = strings.TrimSpace(minVersion)
	if minVersion == "" {
		return nil
	}
	if _, _, ok := parseCoreVersion(minVersion); !ok {
		return fmt.Errorf("cli_version %q is not a valid version (want MAJOR.MINOR.PATCH, e.g. v1.4.0)", minVersion)
	}

	cur := Version
	cmp, ok := compareVersions(cur, minVersion)
	if !ok {
		// dev / edge / otherwise non-release build — can't compare, so
		// trust the operator rather than clobber a local build.
		slog.Debug("skipping cli_version check: running version is not a comparable release",
			"running", cur, "required", minVersion)
		return nil
	}
	if cmp >= 0 {
		return nil // already at or above the required minimum
	}

	// Download exactly the required tag (preserving any pre-release) so the
	// resulting binary reports >= the minimum and the re-exec below doesn't
	// loop.
	required := minVersion
	if !strings.HasPrefix(required, "v") {
		required = "v" + required
	}
	p.Infof("veil %s is older than this project's required %s — updating…", cur, required)

	target, _, err := selfReplace(ctx, required)
	if err != nil {
		return fmt.Errorf("auto-updating to required veil %s: %w", required, err)
	}

	// Re-exec the freshly-installed binary with the same arguments so the
	// command the user actually ran proceeds on the required version. On
	// success this does not return (the process image is replaced).
	p.Infof("re-running on veil %s", required)
	if err := reexecSelf(target, os.Args); err != nil {
		return fmt.Errorf("re-running %s after update: %w", target, err)
	}
	return nil // unreachable on a successful re-exec
}

// compareVersions reports whether a is <, ==, or > b (-1/0/1) by their
// MAJOR.MINOR.PATCH core, with a pre-release ranking below its release
// (per semver, 1.2.3-rc < 1.2.3). ok is false when either side isn't a
// parseable release version (e.g. "dev", "edge-abc123"), signalling the
// caller to skip the comparison.
func compareVersions(a, b string) (cmp int, ok bool) {
	ac, ap, aok := parseCoreVersion(a)
	bc, bp, bok := parseCoreVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if ac[i] < bc[i] {
			return -1, true
		}
		if ac[i] > bc[i] {
			return 1, true
		}
	}
	// Equal cores: a release outranks a pre-release of the same core, and
	// two pre-releases fall back to a lexical compare (an approximation of
	// semver's identifier rules, sufficient for a minimum-version gate).
	switch {
	case ap == bp:
		return 0, true
	case ap == "":
		return 1, true
	case bp == "":
		return -1, true
	case ap < bp:
		return -1, true
	default:
		return 1, true
	}
}

// parseCoreVersion splits a version string into its numeric
// MAJOR.MINOR.PATCH core and any pre-release remainder. A leading "v" is
// optional and build metadata (after "+") is ignored. ok is false unless
// all three core components are present and numeric — so "dev" and
// "edge-<sha>" parse as not-a-version.
func parseCoreVersion(v string) (core [3]int, prerelease string, ok bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return core, "", false
	}
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus] // drop build metadata
	}
	if dash := strings.IndexByte(v, '-'); dash >= 0 {
		prerelease = v[dash+1:]
		v = v[:dash]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, "", false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, "", false
		}
		core[i] = n
	}
	return core, prerelease, true
}
