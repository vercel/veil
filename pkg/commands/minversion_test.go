package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/vercel/veil/pkg/interact"
)

type MinVersionSuite struct {
	suite.Suite
}

func TestMinVersionSuite(t *testing.T) {
	suite.Run(t, new(MinVersionSuite))
}

func (s *MinVersionSuite) TestParseCoreVersion() {
	cases := []struct {
		in   string
		core [3]int
		pre  string
		ok   bool
	}{
		{"v1.2.3", [3]int{1, 2, 3}, "", true},
		{"1.2.3", [3]int{1, 2, 3}, "", true}, // leading v optional
		{"v10.20.30", [3]int{10, 20, 30}, "", true},
		{"v1.2.3-rc.1", [3]int{1, 2, 3}, "rc.1", true},
		{"v1.2.3+build.5", [3]int{1, 2, 3}, "", true}, // build metadata dropped
		{"v1.2.3-rc.1+build.5", [3]int{1, 2, 3}, "rc.1", true},
		{"dev", [3]int{}, "", false},
		{"edge-abc123", [3]int{}, "", false},
		{"v1.2", [3]int{}, "", false},   // too few components
		{"v1.2.x", [3]int{}, "", false}, // non-numeric
		{"", [3]int{}, "", false},
	}
	for _, c := range cases {
		core, pre, ok := parseCoreVersion(c.in)
		s.Equal(c.ok, ok, "ok for %q", c.in)
		if c.ok {
			s.Equal(c.core, core, "core for %q", c.in)
			s.Equal(c.pre, pre, "prerelease for %q", c.in)
		}
	}
}

func (s *MinVersionSuite) TestCompareVersions() {
	cases := []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"v1.2.3", "v1.2.3", 0, true},
		{"v1.2.3", "v1.2.4", -1, true},
		{"v1.3.0", "v1.2.9", 1, true},
		{"v2.0.0", "v1.9.9", 1, true},
		{"1.2.3", "v1.2.3", 0, true},        // leading v optional on either side
		{"v1.2.3-rc.1", "v1.2.3", -1, true}, // pre-release ranks below its release
		{"v1.2.3", "v1.2.3-rc.1", 1, true},
		{"v1.2.3-rc.1", "v1.2.3-rc.2", -1, true},
		{"dev", "v1.0.0", 0, false},   // running build not comparable
		{"v1.0.0", "edge-abc", 0, false},
	}
	for _, c := range cases {
		cmp, ok := compareVersions(c.a, c.b)
		s.Equal(c.ok, ok, "ok for %q vs %q", c.a, c.b)
		if c.ok {
			s.Equal(c.cmp, cmp, "cmp for %q vs %q", c.a, c.b)
		}
	}
}

// TestEnforceMinVersionNoops covers every path that must NOT trigger a
// self-update (and so stays offline + safe in a unit test). The
// update/re-exec path is integration territory and isn't exercised here.
func (s *MinVersionSuite) TestEnforceMinVersionNoops() {
	ctx := context.Background()
	p := interact.Default()
	orig := Version
	defer func() { Version = orig }()

	// Unset cli_version → no-op (regardless of running version).
	s.Require().NoError(enforceMinVersion(ctx, "", p))

	// Malformed cli_version → error (caught before any download).
	s.Require().Error(enforceMinVersion(ctx, "not-a-version", p))

	// A dev/edge running build can't be compared, so any minimum is a
	// no-op rather than clobbering a local build.
	Version = "edge-abc123"
	s.Require().NoError(enforceMinVersion(ctx, "v999.0.0", p))

	// When the running build already satisfies the minimum, no-op.
	Version = "v2.0.0"
	s.Require().NoError(enforceMinVersion(ctx, "v1.0.0", p)) // above
	s.Require().NoError(enforceMinVersion(ctx, "v2.0.0", p)) // equal
}
