package tfwrite

import (
	"strings"
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
	s.Equal("# Top of file.", top[0].Text())
	s.Equal("# The bucket holds build logs.", top[1].Text())

	bucket := f.Resource("aws_s3_bucket", "logs")
	s.Require().NotNil(bucket)
	inner := bucket.Body().Comments()
	s.Require().Len(inner, 2, "a comment inside a block belongs to that block")
	s.Contains(inner[1].Text(), "Nested comment")
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

	f.Resource("aws_s3_bucket", "logs").SetName("build_logs")
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
	s.Equal(`"acme-logs-${var.environment}"`, bucket.Body().Attribute("bucket").Expr())

	tf := f.Terraform()
	s.Require().NotNil(tf)
	s.Equal(`">= 1.5"`, tf.Body().Attribute("required_version").Expr())
}

func (s *TFWriteSuite) TestAddAndRemove() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	f.AddOutput("bucket_name").Body().SetAttribute("value", "aws_s3_bucket.logs.id")

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

// TestEachBlockTypeHasItsOwnLabels is the reason the block types are
// separate structs. Terraform does not give blocks a uniform label
// shape, so neither does this: a resource has a type and a name, a
// provider only a name, locals neither, and a backend's single label is
// a type rather than a name.
//
// The shapes a generic API lets you build wrong — a resource with one
// label, a provider with two — are not expressible here at all. There is
// no test for rejecting them because there is no call that produces one.
func (s *TFWriteSuite) TestEachBlockTypeHasItsOwnLabels() {
	f, err := Parse([]byte("locals {}\n"), "main.tf")
	s.Require().NoError(err)

	r := f.AddResource("aws_s3_bucket", "logs")
	s.Equal("aws_s3_bucket", r.ResourceType())
	s.Equal("logs", r.Name())

	p := f.AddProvider("aws")
	s.Equal("aws", p.Name())

	v := f.AddVariable("region")
	s.Equal("region", v.Name())

	s.Require().Len(f.Locals(), 1, "the parsed locals block came through as its own type")

	m := f.AddModule("network")
	m.Body().SetAttribute("source", `"./modules/network"`)
	s.Equal("./modules/network", m.Source(), "a modelled block can read its own key attribute")

	out := f.String()
	s.Contains(out, `resource "aws_s3_bucket" "logs" {`)
	s.Contains(out, `provider "aws" {`)
	s.Contains(out, `variable "region" {`)
	s.Contains(out, `module "network" {`)

	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestUnmodelledBlocksStayEditable covers the escape hatch: a nested
// block, or one Terraform adds after this was written, still parses and
// round-trips rather than failing.
func (s *TFWriteSuite) TestUnmodelledBlocksStayEditable() {
	src := `resource "aws_instance" "web" {
  provisioner "local-exec" {
    command = "echo hi"
  }
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)
	s.Equal(src, f.String())

	web := f.Resource("aws_instance", "web")
	s.Require().NotNil(web)
	nested := web.Body().Blocks()
	s.Require().Len(nested, 1)
	s.Equal("provisioner", nested[0].BlockType())
	s.Equal([]string{"local-exec"}, nested[0].Labels())
	s.IsType(&Generic{}, nested[0], "an unmodelled block is Generic, not an error")
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

// TestRemoveActuallyRemoves is not as obvious as it sounds. The printer
// copies the original text between two surviving items to keep blank
// lines, and that gap spans anything dropped from between them — so a
// removal that updates the item list and nothing else prints the removed
// text straight back out.
func (s *TFWriteSuite) TestRemoveActuallyRemoves() {
	src := `# Keep this comment.
resource "aws_s3_bucket" "a" {
  bucket = "a"
}

# Drop this comment.
resource "aws_s3_bucket" "b" {
  bucket = "b"
}

resource "aws_s3_bucket" "c" {
  bucket = "c"
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)

	s.True(f.Remove(f.Resource("aws_s3_bucket", "b")), "a resource in the middle")

	comments := f.Body().Comments()
	s.Require().Len(comments, 2)
	s.True(f.Body().RemoveComment(comments[1]), "the comment that described it")

	out := f.String()
	s.NotContains(out, `"b"`, "the resource is gone")
	s.NotContains(out, "Drop this comment", "and so is the comment")
	s.Contains(out, "# Keep this comment.")
	s.Contains(out, `resource "aws_s3_bucket" "a"`)
	s.Contains(out, `resource "aws_s3_bucket" "c"`)

	// What is left still parses, and is stable on a second pass.
	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestRemoveFromTheEnd covers the tail separately: the printer copies
// whatever followed the last item, which is the file's final newline —
// or, once the last item is dropped, the item itself.
func (s *TFWriteSuite) TestRemoveFromTheEnd() {
	src := `resource "aws_s3_bucket" "a" {
  bucket = "a"
}

resource "aws_s3_bucket" "last" {
  bucket = "last"
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)
	s.True(f.Remove(f.Resource("aws_s3_bucket", "last")))

	out := f.String()
	s.NotContains(out, "last")
	s.Contains(out, `resource "aws_s3_bucket" "a"`)
	s.True(strings.HasSuffix(out, "}\n"), "the file still ends with a newline, got %q", out)
}

// TestRemoveMissingIsANoOp keeps f.Remove(f.Resource(...)) safe to write
// without checking for nil first.
func (s *TFWriteSuite) TestRemoveMissingIsANoOp() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	s.False(f.Remove(f.Resource("aws_s3_bucket", "nope")), "no such resource")
	s.False(f.Body().RemoveComment(nil))
	s.Equal(messyFile, f.String(), "a failed removal changes nothing")
}

// TestDeleteOnTheItemItself is the call the API is meant to read as:
// find a thing and delete it, without naming the body in between.
func (s *TFWriteSuite) TestDeleteOnTheItemItself() {
	src := `# Keep.
resource "aws_s3_bucket" "a" {
  bucket   = "a"
  acl      = "private"
}

# Drop.
resource "aws_s3_bucket" "b" {
  bucket = "b"
}

variable "region" {
  default = "us-east-1"
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)

	s.True(f.Resource("aws_s3_bucket", "b").Delete())
	s.True(f.Variable("region").Delete())
	s.True(f.Body().Comments()[1].Delete(), "a comment deletes itself the same way")
	s.True(f.Resource("aws_s3_bucket", "a").Body().Attribute("acl").Delete(),
		"and so does an attribute, from the body that holds it")

	out := f.String()
	s.NotContains(out, `"b"`)
	s.NotContains(out, "Drop.")
	s.NotContains(out, "region")
	s.NotContains(out, "acl")
	s.Contains(out, "# Keep.")
	s.Contains(out, `bucket   = "a"`, "the attribute left behind keeps its original alignment")

	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestDeleteOnAMissingThingIsSafe is why Delete has a nil check on every
// type rather than being promoted from the embedded block: a lookup that
// finds nothing returns a nil pointer, and calling a promoted method on
// it would dereference before it could test.
func (s *TFWriteSuite) TestDeleteOnAMissingThingIsSafe() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)

	s.False(f.Resource("aws_s3_bucket", "nope").Delete())
	s.False(f.Provider("nope").Delete())
	s.False(f.Variable("nope").Delete())
	s.False(f.Terraform().Body().Attribute("nope").Delete())
	s.Equal(messyFile, f.String(), "nothing changed")
}

// TestDeleteSurvivesTheJSONRoundTrip keeps the parent wiring honest on
// the far side, where the tree is rebuilt rather than parsed.
func (s *TFWriteSuite) TestDeleteSurvivesTheJSONRoundTrip() {
	f, err := Parse([]byte(messyFile), "main.tf")
	s.Require().NoError(err)
	data, err := f.MarshalTree()
	s.Require().NoError(err)

	back, err := UnmarshalTree([]byte(messyFile), data)
	s.Require().NoError(err)
	s.True(back.Module("network").Delete(), "a rebuilt block knows its parent")

	out := back.String()
	s.NotContains(out, "./modules/network")
	s.Contains(out, "# Top of file.")
}

// TestCommentsInsideExpressionsAreNotItems covers a comment that lives
// inside an attribute's value rather than between items:
//
//	tags = {
//	  env = var.environment   # inside the expression
//	}
//
// Those bytes are part of the attribute's own text. Collecting the
// comment as an item too means it is held twice, which is invisible
// while the block prints from its original span and doubles the moment
// anything makes it reprint.
func (s *TFWriteSuite) TestCommentsInsideExpressionsAreNotItems() {
	src := `resource "aws_s3_bucket" "logs" {
  bucket =    "acme-logs"   # trailing comment

  # Standalone comment.
  tags = {
    env = var.environment   # inside the expression
  }
  # Last comment
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)
	s.Equal(src, f.String())

	bucket := f.Resource("aws_s3_bucket", "logs")
	var texts []string
	for _, c := range bucket.Body().Comments() {
		texts = append(texts, c.Text())
	}
	s.Equal([]string{
		"# trailing comment",
		"# Standalone comment.",
		"# Last comment",
	}, texts, "the one inside the expression belongs to the attribute, not the body")

	// Force a reprint and check nothing doubled.
	bucket.Body().SetAttribute("bucket", `"changed"`)
	out := f.String()
	s.Equal(1, strings.Count(out, "# inside the expression"))
	s.Equal(1, strings.Count(out, "# Last comment"))
	s.Contains(out, "# Last comment\n}", "and no blank line crept in before the brace")

	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestBuildFromBlank covers generating a file rather than editing one:
// nothing to copy original bytes from, so every node is printed fresh.
func (s *TFWriteSuite) TestBuildFromBlank() {
	f, err := Parse([]byte(""), "main.tf")
	s.Require().NoError(err)

	tf := f.AddTerraform()
	tf.SetAttribute("required_version", `">= 1.5"`)
	tf.AddBlock("required_providers").
		SetAttribute("aws", `{ source = "hashicorp/aws", version = "~> 5.0" }`)

	f.AddVariable("region").SetAttribute("type", "string")

	r := f.AddResource("aws_s3_bucket", "logs")
	r.SetAttribute("bucket", `"acme-logs"`)
	r.AddBlock("lifecycle").SetAttribute("prevent_destroy", "true")

	f.AddOutput("id").SetAttribute("value", "aws_s3_bucket.logs.id")

	out := f.String()
	s.True(strings.HasSuffix(out, "}\n"), "a generated file ends with a newline")
	s.Contains(out, "}\n\nvariable \"region\" {", "top-level blocks are separated by a blank line")
	s.Contains(out, "  lifecycle {\n    prevent_destroy = true\n  }", "nested blocks are indented")

	// The real check: what came out is Terraform, and stable.
	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err, "generated output has to parse")
	s.Equal(out, again.String())
}

// TestRenameAttribute covers renaming a field in place — the one edit
// that was missing, since Name was a reader with no setter behind it.
func (s *TFWriteSuite) TestRenameAttribute() {
	src := `resource "aws_s3_bucket" "logs" {
  # Keep this.
  bucket =    "acme-logs"   # trailing
  acl    = "private"
}
`
	f, err := Parse([]byte(src), "main.tf")
	s.Require().NoError(err)
	r := f.Resource("aws_s3_bucket", "logs")

	s.True(r.RenameAttribute("bucket", "bucket_name"))
	out := f.String()
	s.Contains(out, `bucket_name = "acme-logs"`, "renamed, expression intact")
	s.NotContains(out, "bucket =", "the old name is gone")
	s.Contains(out, "# Keep this.", "comments survive")
	s.Contains(out, `acl    = "private"`, "the untouched sibling keeps its alignment")

	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String())
}

// TestRenameAttributeRefusesACollision is the guard: renaming onto a
// sibling would leave two attributes of one name, which Terraform
// rejects and which no later lookup could tell apart.
func (s *TFWriteSuite) TestRenameAttributeRefusesACollision() {
	f, err := Parse([]byte("locals {\n  a = 1\n  b = 2\n}\n"), "main.tf")
	s.Require().NoError(err)
	l := f.Locals()[0]

	s.False(l.RenameAttribute("a", "b"), "b is taken")
	s.False(l.RenameAttribute("nope", "c"), "no such attribute")
	s.False(l.RenameAttribute("a", "a"), "renaming to itself is not a change")
	s.Equal("locals {\n  a = 1\n  b = 2\n}\n", f.String(), "nothing changed")

	s.True(l.RenameAttribute("a", "c"))
	s.Contains(f.String(), "c = 1")
}

// TestSetNameOnANilAttributeIsSafe keeps the chain safe, as the other
// setters are.
func (s *TFWriteSuite) TestSetNameOnANilAttributeIsSafe() {
	var a *Attribute
	a.SetName("x")
	s.Equal("", a.Name())
}
