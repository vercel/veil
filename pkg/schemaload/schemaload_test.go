package schemaload

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type LoaderSuite struct {
	suite.Suite
}

func TestLoaderSuite(t *testing.T) {
	suite.Run(t, new(LoaderSuite))
}

func (s *LoaderSuite) TestFetchDeduplicatesConcurrentReads() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"type":"object"}`)
	}))
	defer server.Close()
	loader := New(context.Background())
	var wg sync.WaitGroup
	data := make([][]byte, 10)
	errs := make([]error, len(data))
	for i := range data {
		wg.Go(func() {
			data[i], errs[i] = loader.Read(s.T().TempDir(), server.URL+"/schema.json?version=1")
		})
	}
	wg.Wait()
	for i := range data {
		s.Require().NoError(errs[i])
		s.Equal(`{"type":"object"}`, string(data[i]))
	}
	s.Equal(int32(1), requests.Load())
	_, err := loader.Read("", server.URL+"/schema.json?version=2")
	s.Require().NoError(err)
	s.Equal(int32(2), requests.Load())
	_, err = New(context.Background()).Read("", server.URL+"/schema.json?version=1")
	s.Require().NoError(err)
	s.Equal(int32(3), requests.Load())
}

func (s *LoaderSuite) TestLocalReadResolvesAndCachesPaths() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "schema.json")
	s.Require().NoError(os.WriteFile(path, []byte(`{"type":"string"}`), 0644))
	loader := New(context.Background())
	data, err := loader.Read(dir, "./schema.json")
	s.Require().NoError(err)
	s.Equal(`{"type":"string"}`, string(data))
	s.Require().NoError(os.Remove(path))
	cached, err := loader.Read("", path)
	s.Require().NoError(err)
	s.Equal(data, cached)
	_, err = loader.Read(dir, "missing.json")
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *LoaderSuite) TestRejectsInvalidURLs() {
	for _, ref := range []string{
		"ftp://example.com/schema.json", "file:///tmp/schema.json", "data:application/json,{}",
		"http:///schema.json", "https:", "https:schema.json", "http://:80/schema.json",
		"http://example.com:bad/schema.json", "http://bad host/schema.json", "http://example.com/%zz",
		"https://example.com/schema.json#/$defs/spec", "https://example.com/schema.json#",
	} {
		s.Run(ref, func() {
			_, _, err := Resolve("", ref)
			s.Error(err)
			_, err = New(context.Background()).Read("", ref)
			s.Error(err)
		})
	}
}

func (s *LoaderSuite) TestRejectsURLCredentialsWithoutExposingThem() {
	for _, ref := range []string{
		"https://secret-user:secret-password@example.com/schema.json",
		"https://secret-user@example.com/schema.json",
		"ftp://secret-user:secret-password@example.com/schema.json",
		"https://secret-user:secret-password@example.com/%zz",
	} {
		_, _, err := Resolve("", ref)
		s.Require().Error(err)
		s.NotContains(err.Error(), "secret-user")
		s.NotContains(err.Error(), "secret-password")
	}
}

func (s *LoaderSuite) TestResolvePreservesURLQueryAndLocalPunctuation() {
	ref := "https://example.com/schema.json?version=1%23two"
	location, remote, err := Resolve("ignored", ref)
	s.Require().NoError(err)
	s.True(remote)
	s.Equal(ref, location)
	dir := s.T().TempDir()
	location, remote, err = Resolve(dir, "schema#one%two.json")
	s.Require().NoError(err)
	s.False(remote)
	s.Equal(filepath.Join(dir, "schema#one%two.json"), location)
}

func (s *LoaderSuite) TestHTTPStatusErrorsAreCached() {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError} {
		s.Run(http.StatusText(status), func() {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			loader := New(context.Background())
			for range 2 {
				_, err := loader.Read("", server.URL)
				s.Require().Error(err)
				s.Contains(err.Error(), http.StatusText(status))
				s.Contains(err.Error(), server.URL)
			}
			s.Equal(int32(1), requests.Load())
		})
	}
}

func (s *LoaderSuite) TestTransportError() {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	_, err := New(context.Background()).Read("", server.URL)
	s.Require().Error(err)
	s.Contains(err.Error(), server.URL)
}

func (s *LoaderSuite) TestContextCancelsRequest() {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loader := New(ctx)
	finished := make(chan error, 1)
	go func() {
		_, err := loader.Read("", server.URL)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		s.FailNow("request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		s.ErrorIs(err, context.Canceled)
	case <-time.After(5 * time.Second):
		s.FailNow("request did not cancel")
	}
}

func (s *LoaderSuite) TestHTTPTimeout() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	loader := New(context.Background())
	s.Equal(30*time.Second, loader.client.Timeout)
	loader.client.Timeout = 20 * time.Millisecond
	_, err := loader.Read("", server.URL)
	s.Require().Error(err)
	s.True(errors.Is(err, context.DeadlineExceeded))
}

func (s *LoaderSuite) TestBodyReadError() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	}))
	defer server.Close()
	_, err := New(context.Background()).Read("", server.URL)
	s.ErrorIs(err, io.ErrUnexpectedEOF)
}
