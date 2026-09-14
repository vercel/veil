// Package ioutil opens documents by URI: an HTTP(S) URL or a file path.
package ioutil

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// IsRemote reports whether uri has an http or https scheme. Anything
// else is treated as a file path.
func IsRemote(uri string) bool {
	lower := strings.ToLower(uri)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// Open returns a reader over uri. An http(s) URL is fetched over HTTP;
// anything else is a file path, absolute or relative to the working
// directory. The caller closes the returned reader.
func Open(uri string) (io.ReadCloser, error) {
	if !IsRemote(uri) {
		return os.Open(uri)
	}
	resp, err := http.Get(uri)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %s", uri, resp.Status)
	}
	return resp.Body, nil
}

// Read returns the full contents of uri.
func Read(uri string) ([]byte, error) {
	r, err := Open(uri)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	return data, nil
}
