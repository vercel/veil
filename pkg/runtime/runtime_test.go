package runtime

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

type RuntimeSuite struct {
	suite.Suite
}

func TestRuntimeSuite(t *testing.T) {
	suite.Run(t, new(RuntimeSuite))
}

func (s *RuntimeSuite) TestEvalWithImport() {
	root := fstest.MapFS{
		"utils.ts": &fstest.MapFile{
			Data: []byte(`export function add(a: number, b: number): number { return a + b; }`),
		},
		"main.ts": &fstest.MapFile{
			Data: []byte(`import { add } from './utils'; add(2, 3);`),
		},
	}

	result, err := Eval("main.ts", root)
	s.Require().NoError(err)
	s.Equal("5", result)
}

func (s *RuntimeSuite) TestEvalWithNodeModules() {
	root := fstest.MapFS{
		"node_modules/math-lib/package.json": &fstest.MapFile{
			Data: []byte(`{"name": "math-lib", "module": "index.js"}`),
		},
		"node_modules/math-lib/index.js": &fstest.MapFile{
			Data: []byte(`export function double(n) { return n * 2; }`),
		},
		"main.ts": &fstest.MapFile{
			Data: []byte(`import { double } from 'math-lib'; double(21);`),
		},
	}

	result, err := Eval("main.ts", root)
	s.Require().NoError(err)
	s.Equal("42", result)
}
