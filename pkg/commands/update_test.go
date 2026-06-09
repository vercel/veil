package commands

import (
	"testing"

	"github.com/stretchr/testify/suite"
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

	// edge (the default) and explicit tags use the tagged-download path.
	s.Equal(base+"/download/edge", releaseBaseURL("edge"))
	s.Equal(base+"/download/v1.2.3", releaseBaseURL("v1.2.3"))
	s.Equal(base+"/download/v1.0.0-rc.1", releaseBaseURL("v1.0.0-rc.1"))

	// latest resolves through GitHub's newest-stable redirect.
	s.Equal(base+"/latest/download", releaseBaseURL("latest"))
}
