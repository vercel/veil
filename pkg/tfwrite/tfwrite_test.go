package tfwrite

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/suite"
)

type TFWriteSuite struct {
	suite.Suite
}

func TestTFWriteSuite(t *testing.T) {
	suite.Run(t, new(TFWriteSuite))
}

// messyFile is deliberately not canonically formatted: odd alignment,
// blank lines, comments in every position. All of it has to survive.
const messyFile = `# Top of file.
terraform {
  required_version = ">= 1.5"
}

variable "environment" { type = string }

# The bucket holds build logs.
resource "aws_s3_bucket" "logs" {
  bucket =    "acme-logs-${var.environment}"   # odd spacing on purpose

  # Nested comment.
  tags = {
    env = var.environment
  }
}

module "network" {
  source = "./modules/network"
}
`

// TestUntouchedFileIsByteIdentical is the property the package exists
// for. Anything less and a hook that reads a file and writes it back
// would reformat somebody's Terraform behind their back.
func (s *TFWriteSuite) TestUntouchedFileIsByteIdentical() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)
	s.Equal(messyFile, f.String())
}

// TestCommentsArePlacedWithTheirItem checks the reconstruction, since
// comments are not in the syntax tree and have to be put back.
func (s *TFWriteSuite) TestCommentsArePlacedWithTheirItem() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	top := f.Body().Comments()
	s.Require().Len(top, 2, "file-level comments stay at the top level")
	s.Equal("# Top of file.", top[0].Text)
	s.Equal("# The bucket holds build logs.", top[1].Text)

	bucket := f.Resource("aws_s3_bucket", "logs")
	s.Require().NotNil(bucket)
	inner := bucket.Body().Comments()
	s.Require().Len(inner, 2, "a comment inside a block belongs to that block")
	s.Contains(inner[1].Text, "Nested comment")
}

// TestEditIsLocal is the other half of the property: changing one
// attribute reprints that attribute and nothing else, so the odd spacing
// and comments elsewhere in the file are still there afterwards.
func (s *TFWriteSuite) TestEditIsLocal() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	f.Module("network").Body().SetAttribute("source", `"./modules/vpc"`)
	out := f.String()

	s.Contains(out, `source = "./modules/vpc"`)
	s.NotContains(out, "./modules/network")
	// Everything the edit did not touch is untouched.
	s.Contains(out, `bucket =    "acme-logs-${var.environment}"   # odd spacing on purpose`)
	s.Contains(out, "# Top of file.")
	s.Contains(out, "# Nested comment.")
	s.Contains(out, `variable "environment" { type = string }`)
}

// TestRenameResourceKeepsItsBody covers relabelling, which reprints the
// block header but must not disturb what is inside it.
func (s *TFWriteSuite) TestRenameResourceKeepsItsBody() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	f.Resource("aws_s3_bucket", "logs").SetLabels([]string{"aws_s3_bucket", "build_logs"})
	out := f.String()

	s.Contains(out, `resource "aws_s3_bucket" "build_logs" {`)
	s.NotContains(out, `"logs"`)
	s.Contains(out, "# Nested comment.", "the body came through the rename")
	s.Contains(out, "env = var.environment")
}

// TestExpressionsStaySourceText pins that nothing is evaluated: a
// reference read out and written back is still a reference.
func (s *TFWriteSuite) TestExpressionsStaySourceText() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	bucket := f.Resource("aws_s3_bucket", "logs")
	s.Equal(`"acme-logs-${var.environment}"`, bucket.Body().Attribute("bucket").Expr)

	tf := f.Terraform()
	s.Require().NotNil(tf)
	s.Equal(`">= 1.5"`, tf.Body().Attribute("required_version").Expr)
}

func (s *TFWriteSuite) TestAddAndRemove() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	blk, err := f.AppendBlock("output", "bucket_name")
	s.Require().NoError(err)
	blk.Body().SetAttribute("value", "aws_s3_bucket.logs.id")

	s.True(f.Module("network").Body().RemoveAttribute("source"))

	out := f.String()
	s.Contains(out, `output "bucket_name" {`)
	s.Contains(out, "value = aws_s3_bucket.logs.id")
	s.NotContains(out, "./modules/network")

	// What we wrote has to be parseable again, and stable on a second pass.
	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestAppendBlockRejectsBadShape is the Terraform coupling earning its
// keep: hclwrite would accept either of these and produce a file
// Terraform refuses to load.
func (s *TFWriteSuite) TestAppendBlockRejectsBadShape() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	_, err = f.AppendBlock("provisioner", "local-exec")
	s.Require().Error(err, "provisioner is not a top-level block")
	s.Contains(err.Error(), "not a Terraform block type")

	_, err = f.AppendBlock("resource", "aws_s3_bucket")
	s.Require().Error(err, "resource takes two labels")
	s.Contains(err.Error(), "takes 2 label(s), got 1")
}

// TestBlockLabelsMatchTerraform pins the table against the counts
// Terraform's own configFileSchema declares, since it cannot be
// imported and so can only be copied.
func (s *TFWriteSuite) TestBlockLabelsMatchTerraform() {
	s.Equal(map[string]int{
		"terraform": 0, "locals": 0, "moved": 0, "removed": 0, "import": 0,
		"provider": 1, "variable": 1, "output": 1, "module": 1, "check": 1,
		"resource": 2, "data": 2, "ephemeral": 2, "action": 2,
	}, BlockLabels)
	s.NotContains(BlockLabels, "provisioner")
}

// TestTreeSurvivesJSON is what the JS layer depends on: the tree can go
// out as data, come back, and still print from the original bytes.
func (s *TFWriteSuite) TestTreeSurvivesJSON() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	data, err := f.MarshalTree()
	s.Require().NoError(err)

	back, err := UnmarshalTree([]byte(messyFile), data)
	s.Require().NoError(err)
	s.Equal(messyFile, back.String(), "an untouched tree still prints byte-identically after a round trip")
}

// TestEditAcrossJSONReprintsOnlyThatNode covers the marker doing its
// job: a node edited on the far side comes back flagged and is
// reprinted, while its untouched neighbours are still copied.
func (s *TFWriteSuite) TestEditAcrossJSONReprintsOnlyThatNode() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)
	data, err := f.MarshalTree()
	s.Require().NoError(err)

	// Stand in for the hook: flip one attribute and mark it.
	var doc jsonFile
	s.Require().NoError(json.Unmarshal(data, &doc))
	var touched bool
	for _, it := range doc.Items {
		if it.Kind == "block" && it.Type == "module" {
			for _, inner := range it.Items {
				if inner.Kind == "attribute" && inner.Name == "source" {
					inner.Expr, inner.Changed, touched = `"./modules/vpc"`, true, true
				}
			}
		}
	}
	s.Require().True(touched)
	edited, err := json.Marshal(&doc)
	s.Require().NoError(err)

	back, err := UnmarshalTree([]byte(messyFile), edited)
	s.Require().NoError(err)
	out := back.String()

	s.Contains(out, `source = "./modules/vpc"`)
	s.Contains(out, `bucket =    "acme-logs-${var.environment}"   # odd spacing on purpose`)
	s.Contains(out, "# Nested comment.")
}

// TestRejectsSpanOutsideSource keeps a malformed tree from reading
// arbitrary bytes rather than panicking on a slice bound.
func (s *TFWriteSuite) TestRejectsSpanOutsideSource() {
	_, err := UnmarshalTree([]byte("locals {}\n"),
		[]byte(`{"filename":"main.tf","items":[{"kind":"comment","text":"x","start":0,"end":9999,"spanned":true}]}`))
	s.Require().Error(err)
	s.Contains(err.Error(), "outside the source")
}
