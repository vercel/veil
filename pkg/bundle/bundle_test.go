package bundle

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

type BundleSuite struct {
	suite.Suite
}

func TestBundleSuite(t *testing.T) {
	suite.Run(t, new(BundleSuite))
}

func (s *BundleSuite) TestBundlesRelativeImports() {
	root := fstest.MapFS{
		"utils.ts": &fstest.MapFile{
			Data: []byte(`export function add(a: number, b: number): number { return a + b; }`),
		},
		"main.ts": &fstest.MapFile{
			Data: []byte(`import { add } from './utils'; export const sum = add(2, 3);`),
		},
	}

	out, err := Bundle("main.ts", root, &Options{})
	s.Require().NoError(err)
	s.Contains(out, "add", "bundle should reference the imported function")
	s.Contains(out, "sum", "bundle should expose the entrypoint export")
}

func (s *BundleSuite) TestBundlesNodeModulesViaPackageJSON() {
	root := fstest.MapFS{
		"node_modules/math-lib/package.json": &fstest.MapFile{
			Data: []byte(`{"name": "math-lib", "module": "index.js"}`),
		},
		"node_modules/math-lib/index.js": &fstest.MapFile{
			Data: []byte(`export function double(n) { return n * 2; }`),
		},
		"main.ts": &fstest.MapFile{
			Data: []byte(`import { double } from 'math-lib'; export const x = double(21);`),
		},
	}

	out, err := Bundle("main.ts", root, &Options{})
	s.Require().NoError(err)
	s.Contains(out, "double")
}

func (s *BundleSuite) TestIIFEFormatAssignsGlobal() {
	root := fstest.MapFS{
		"main.ts": &fstest.MapFile{
			Data: []byte(`const h = { render() { return null; } }; export default h;`),
		},
	}

	out, err := Bundle("main.ts", root, &Options{GlobalName: "__veilMod"})
	s.Require().NoError(err)
	s.True(strings.Contains(out, "__veilMod"), "IIFE format should bind the global name")
}
