package commands

import (
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"
)

type UpdateSuite struct {
	suite.Suite
}

func TestUpdateSuite(t *testing.T) {
	suite.Run(t, new(UpdateSuite))
}

// TestReleaseBaseURL pins the version selector → download base mapping
// that `veil update --version` and install.sh's VEIL_VERSION share.
func (s *UpdateSuite) TestReleaseBaseURL() {
	const base = "https://github.com/vercel/veil/releases"

	// edge and explicit tags use the tagged-download path.
	s.Equal(base+"/download/edge", releaseBaseURL("edge"))
	s.Equal(base+"/download/v1.2.3", releaseBaseURL("v1.2.3"))
	s.Equal(base+"/download/v1.0.0-rc.1", releaseBaseURL("v1.0.0-rc.1"))

	// latest resolves through GitHub's newest-stable redirect.
	s.Equal(base+"/latest/download", releaseBaseURL("latest"))
}

// TestUpdateDefaultsToLatestStable guards the default: `veil update` with
// no --version installs the latest STABLE release, not the rolling edge
// build (which is also a non-comparable version that cli_version skips).
func (s *UpdateSuite) TestUpdateDefaultsToLatestStable() {
	cmd := Update()
	var version *cli.StringFlag
	for _, f := range cmd.Flags {
		if sf, ok := f.(*cli.StringFlag); ok && sf.Name == "version" {
			version = sf
			break
		}
	}
	s.Require().NotNil(version, "update should have a --version flag")
	s.Equal("latest", version.Value)
}
