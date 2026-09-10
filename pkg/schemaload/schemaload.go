// Package schemaload reads local and HTTP(S) schemas within one build.
package schemaload

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// Resolve validates a URL or resolves a path without reading its contents.
func Resolve(dir, ref string) (location string, remote bool, err error) {
	if schemePrefix.MatchString(ref) {
		u, err := url.Parse(ref)
		if err != nil {
			return "", false, fmt.Errorf("invalid schema URL")
		}
		if u.User != nil {
			return "", false, fmt.Errorf("schema URL authentication is not supported")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", false, fmt.Errorf("schema %q: unsupported URL scheme %q (use HTTP or HTTPS)", ref, u.Scheme)
		}
		if u.Hostname() == "" || u.Opaque != "" {
			return "", false, fmt.Errorf("schema %q: HTTP(S) URL requires a host", ref)
		}
		if strings.Contains(ref, "#") {
			return "", false, fmt.Errorf("schema %q: URL fragments are not supported", ref)
		}
		return u.String(), true, nil
	}
	if !filepath.IsAbs(ref) {
		ref = filepath.Join(dir, ref)
	}
	location, err = filepath.Abs(ref)
	return location, false, err
}

type result struct {
	done chan struct{}
	data []byte
	err  error
}

// Loader shares schema bytes and in-flight reads for the lifetime of a build.
// Returned bytes are shared and must not be modified.
type Loader struct {
	ctx    context.Context
	client *http.Client
	mu     sync.Mutex
	reads  map[string]*result
}

// New creates a build-scoped loader with a 30-second HTTP timeout.
func New(ctx context.Context) *Loader {
	return &Loader{
		ctx:    ctx,
		client: &http.Client{Timeout: 30 * time.Second},
		reads:  make(map[string]*result),
	}
}

// Read returns the schema bytes, reading each resolved location at most once.
func (l *Loader) Read(dir, ref string) ([]byte, error) {
	location, remote, err := Resolve(dir, ref)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if cached, ok := l.reads[location]; ok {
		l.mu.Unlock()
		select {
		case <-cached.done:
			return cached.data, cached.err
		case <-l.ctx.Done():
			return nil, l.ctx.Err()
		}
	}
	cached := &result{done: make(chan struct{})}
	l.reads[location] = cached
	l.mu.Unlock()
	cached.data, cached.err = l.read(location, remote)
	close(cached.done)
	return cached.data, cached.err
}

func (l *Loader) read(location string, remote bool) ([]byte, error) {
	if err := l.ctx.Err(); err != nil {
		return nil, err
	}
	if !remote {
		return os.ReadFile(location)
	}
	req, err := http.NewRequestWithContext(l.ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, fmt.Errorf("schema %q: %w", location, err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("schema %q: %w", location, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schema %q: HTTP %s", location, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("schema %q: %w", location, err)
	}
	return data, nil
}
