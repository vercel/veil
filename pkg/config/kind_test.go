package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/suite"
)

type KindSuite struct {
	suite.Suite
}

func TestKindSuite(t *testing.T) {
	suite.Run(t, new(KindSuite))
}

func (s *KindSuite) TestReadSchemaOverHTTPAndDisk() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer server.Close()
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "local.json"), []byte(`{"type":"string"}`), 0644))
	k := &Kind{Dir: dir}

	data, err := k.ReadSchema(server.URL + "/schema.json")
	s.Require().NoError(err)
	s.Equal(`{"type":"object"}`, string(data))

	var remote map[string]any
	s.Require().NoError(k.DecodeSchema(server.URL+"/schema.json", &remote))
	s.Equal("object", remote["type"])
	s.Equal(int32(2), requests.Load())

	// A relative ref resolves against the kind's directory.
	var local map[string]any
	s.Require().NoError(k.DecodeSchema("./local.json", &local))
	s.Equal("string", local["type"])
	s.Equal(filepath.Join(dir, "local.json"), k.SchemaURI("./local.json"))
	s.Error(k.DecodeSchema("./missing.json", &local))
}
