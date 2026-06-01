package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"
)

type ResultSuite struct {
	suite.Suite
}

func TestResultSuite(t *testing.T) {
	suite.Run(t, new(ResultSuite))
}

type sampleResponse struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

func (s *ResultSuite) decode(out string) CommandResult[sampleResponse] {
	var got CommandResult[sampleResponse]
	s.Require().NoError(json.Unmarshal([]byte(strings.TrimSpace(out)), &got))
	return got
}

func (s *ResultSuite) TestWriteResultSuccess() {
	var buf bytes.Buffer
	resp := sampleResponse{Name: "api-devbox", Files: []string{"app.yaml"}}
	s.Require().NoError(writeResult(&buf, resp, ""))

	got := s.decode(buf.String())
	s.Equal(OutcomeSuccess, got.Outcome)
	s.Empty(got.Error)
	s.Equal("api-devbox", got.Response.Name)
	s.Equal([]string{"app.yaml"}, got.Response.Files)
}

func (s *ResultSuite) TestWriteResultErrorCarriesPartialResponse() {
	var buf bytes.Buffer
	// A partial result: one resource rendered, but the call still failed.
	resp := sampleResponse{Name: "api-devbox"}
	s.Require().NoError(writeResult(&buf, resp, "boom"))

	got := s.decode(buf.String())
	s.Equal(OutcomeError, got.Outcome)
	s.Equal("boom", got.Error)
	s.Equal("api-devbox", got.Response.Name, "partial response survives an error")
}

func (s *ResultSuite) TestWriteErrorResultHasNoResponse() {
	var buf bytes.Buffer
	s.Require().NoError(WriteErrorResult(&buf, assertErr("kaboom")))

	out := strings.TrimSpace(buf.String())
	s.Contains(out, `"outcome":"error"`)
	s.Contains(out, `"error":"kaboom"`)
	s.NotContains(out, `"response"`, "response is omitted when nil")
}

func (s *ResultSuite) TestWriteResultIsSingleLine() {
	var buf bytes.Buffer
	s.Require().NoError(writeResult(&buf, sampleResponse{Name: "x"}, ""))
	s.Equal(1, strings.Count(buf.String(), "\n"), "envelope is exactly one line")
}

// assertErr is a tiny error helper so the test doesn't pull in errors.New
// at each call site.
type assertErr string

func (e assertErr) Error() string { return string(e) }
