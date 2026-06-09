package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/urfave/cli/v3"

	"github.com/vercel/veil/pkg/interact"
)

// releaseBaseURL returns the GitHub release download base for a version
// selector. "edge" (the rolling prerelease the curl-piped install.sh
// defaults to) and explicit tags like "v1.2.3" use the tagged-download
// path; "latest" uses GitHub's redirect to the newest non-prerelease
// release. Kept in sync with install.sh's URL logic.
func releaseBaseURL(version string) string {
	const releases = "https://github.com/vercel/veil/releases"
	if version == "latest" {
		return releases + "/latest/download"
	}
	return releases + "/download/" + version
}

// Update returns the "update" command — replaces the running veil binary
// with a release build. Defaults to the rolling `edge` build; --version
// selects `latest` (newest stable release) or a specific tag (e.g.
// v1.2.3). Mirrors install.sh: same platform asset naming and macOS
// quarantine cleanup.
func Update() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Usage:     "Download a veil release and replace this binary",
		UsageText: "veil update [--version <latest|edge|vX.Y.Z>]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "version",
				Usage: "Release to install: latest (newest stable release, default), edge (latest main build), or a tag like v1.2.3",
				Value: "latest",
			},
		},
		Action: withResult(runUpdate),
	}
}

// updateResponse is the JSON payload for `veil update`.
type updateResponse struct {
	Path    string `json:"path"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

func runUpdate(ctx context.Context, c *cli.Command) (*updateResponse, error) {
	p := interact.Default()

	version := c.String("version")
	osName, arch, err := updateTarget()
	if err != nil {
		return nil, err
	}

	target, url, err := selfReplace(ctx, version, p)
	if err != nil {
		return nil, err
	}

	p.Successf("veil (%s) updated at %s", version, target)
	return &updateResponse{Path: target, OS: osName, Arch: arch, Version: version, URL: url}, nil
}

// selfReplace downloads the veil release asset for `version` (edge /
// latest / a tag) for the current platform and atomically replaces the
// running binary, returning the resolved binary path and the asset URL.
// Shared by `veil update` and render's cli_version auto-update.
func selfReplace(ctx context.Context, version string, p interact.Printer) (target, url string, err error) {
	osName, arch, err := updateTarget()
	if err != nil {
		return "", "", err
	}

	// os.Executable returns the path used to invoke the process; resolve
	// symlinks so we overwrite the actual binary, not a shim.
	target, err = os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("locating current binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}

	url = fmt.Sprintf("%s/veil-%s-%s", releaseBaseURL(version), osName, arch)
	p.Infof("Downloading %s", url)

	tmpPath, err := downloadToTemp(ctx, url, filepath.Dir(target))
	if err != nil {
		return "", "", err
	}
	// downloadToTemp returns a closed file; we own the cleanup until the
	// rename succeeds.
	defer os.Remove(tmpPath)

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return "", "", fmt.Errorf("chmod %s: %w", tmpPath, err)
	}

	if osName == "darwin" {
		// Best-effort: strip Gatekeeper's quarantine xattr. Failure here
		// just means a launch-time prompt, not a broken install.
		_ = exec.Command("xattr", "-cr", tmpPath).Run()
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return "", "", fmt.Errorf("replacing %s: %w", target, err)
	}
	return target, url, nil
}

// updateTarget maps GOOS/GOARCH to the release asset's `${os}-${arch}`
// suffix. Mirrors the case tables in install.sh — supported pairs are
// linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.
func updateTarget() (string, string, error) {
	var osName string
	switch runtime.GOOS {
	case "linux", "darwin":
		osName = runtime.GOOS
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	var arch string
	switch runtime.GOARCH {
	case "amd64", "arm64":
		arch = runtime.GOARCH
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
	return osName, arch, nil
}

// downloadToTemp streams url into a temp file alongside dir (so the
// final rename stays on one filesystem) and returns the temp path.
// The caller is responsible for removing the file on error.
func downloadToTemp(ctx context.Context, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(dir, ".veil-update-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	return tmp.Name(), nil
}
