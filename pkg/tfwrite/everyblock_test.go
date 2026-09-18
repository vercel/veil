package tfwrite

import "strings"

// everyBlockType exercises every block the Terraform *configuration*
// language defines — each top-level type, and each block that nests
// inside one — laid out the way the schemas in terraform/internal/configs
// declare them. Comments sit in every position: before a block, inside
// one, trailing a line, and inside an expression.
//
// Test files (.tftest.hcl) and query files (.tfquery.hcl) are separate
// languages with their own schemas and are deliberately not here; see
// TestSameKeywordDiffersByContext for why they cannot share one table.
const everyBlockType = `# File header comment.

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  backend "s3" {
    bucket = "tfstate"
  }

  cloud {
    organization = "acme"
  }

  provider_meta "aws" {
    module_name = "acme"
  }
}

provider "aws" {
  region = var.region

  lifecycle {
    postcondition {
      condition     = true
      error_message = "never"
    }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"

  validation {
    condition     = length(var.region) > 0
    error_message = "region is required"
  }
}

locals {
  prefix = "acme" # trailing comment on a local
}

# A resource with every nested block it accepts.
resource "aws_instance" "web" {
  ami = "ami-123"

  lifecycle {
    create_before_destroy = true
    precondition {
      condition     = true
      error_message = "ok"
    }
  }

  connection {
    type = "ssh"
  }

  provisioner "local-exec" {
    command = "echo hi"
  }

  action_trigger {
    events = ["after_create"]
  }
}

data "aws_ami" "ubuntu" {
  most_recent = true
}

ephemeral "aws_secretsmanager_secret_version" "db" {
  secret_id = "db"
}

action "aws_lambda_invoke" "notify" {
  config {
    function_name = "notify"
  }
}

module "network" {
  source = "./modules/network"

  providers = {
    aws = aws.west
  }
}

output "vpc_id" {
  value = module.network.vpc_id

  precondition {
    condition     = true
    error_message = "ok"
  }
}

check "health" {
  data "http" "status" {
    url = "https://acme.test/health"
  }

  assert {
    condition     = true
    error_message = "unhealthy"
  }
}

moved {
  from = aws_instance.old
  to   = aws_instance.web
}

removed {
  from = aws_instance.gone

  lifecycle {
    destroy = false
  }
}

import {
  to = aws_instance.web
  id = "i-abc123"
}
`

// TestEveryBlockTypeRoundTrips is the load-bearing one: whatever the
// package does or does not model, it must never damage a file. Every
// block type, every nesting, every comment position — parsed and written
// back byte for byte.
func (s *TFWriteSuite) TestEveryBlockTypeRoundTrips() {
	f, err := Parse([]byte(everyBlockType), "main.tf")
	s.Require().NoError(err)
	s.Equal(everyBlockType, f.String())
}

// TestEveryTopLevelBlockIsTyped checks each top-level keyword reaches
// its own struct with its own labels, rather than falling through to
// Generic.
func (s *TFWriteSuite) TestEveryTopLevelBlockIsTyped() {
	f, err := Parse([]byte(everyBlockType), "main.tf")
	s.Require().NoError(err)

	s.NotNil(f.Terraform())
	s.Require().NotNil(f.Provider("aws"))
	s.Equal("aws", f.Provider("aws").Name())
	s.Require().NotNil(f.Variable("region"))
	s.Equal("region", f.Variable("region").Name())
	s.Len(f.Locals(), 1)
	s.Require().NotNil(f.Resource("aws_instance", "web"))
	s.Equal("aws_instance", f.Resource("aws_instance", "web").ResourceType())
	s.Equal("web", f.Resource("aws_instance", "web").Name())
	s.Require().NotNil(f.DataSource("aws_ami", "ubuntu"))
	s.Equal("aws_ami", f.DataSource("aws_ami", "ubuntu").DataType())
	s.Require().NotNil(f.Ephemeral("aws_secretsmanager_secret_version", "db"))
	s.Require().NotNil(f.Action("aws_lambda_invoke", "notify"))
	s.Require().NotNil(f.Module("network"))
	s.Equal("./modules/network", f.Module("network").Source())
	s.Require().NotNil(f.Output("vpc_id"))
	s.Require().NotNil(f.Check("health"))
	s.Len(f.Moved(), 1)
	s.Len(f.Removed(), 1)
	s.Len(f.Imports(), 1)

	// Every top-level block is accounted for by one of the above: none
	// fell through to Generic.
	// Fourteen: every type configFileSchema accepts at the top level.
	// required_providers is in that schema too but only so Terraform can
	// say "nest it inside terraform" — it is not valid here, so it is
	// not in the fixture and not a type this package models.
	s.Require().Len(f.Blocks(), 14, "the fixture has one of every top-level block type")
	for _, blk := range f.Blocks() {
		_, generic := blk.(*Generic)
		s.False(generic, "top-level %q should have its own type", blk.BlockType())
	}
}

// TestEveryNestedBlockIsReachable walks the blocks that nest inside
// others. They are Generic by design — the same keyword means different
// things under different parents — but they have to be readable,
// editable and correctly scoped to their parent.
func (s *TFWriteSuite) TestEveryNestedBlockIsReachable() {
	f, err := Parse([]byte(everyBlockType), "main.tf")
	s.Require().NoError(err)

	nested := func(parent *Body) map[string][]string {
		out := map[string][]string{}
		for _, blk := range parent.Blocks() {
			out[blk.BlockType()] = blk.Labels()
		}
		return out
	}

	tf := nested(f.Terraform().Body())
	s.Equal([]string{}, orNil(tf["required_providers"]))
	s.Equal([]string{"s3"}, tf["backend"])
	s.Equal([]string{}, orNil(tf["cloud"]))
	s.Equal([]string{"aws"}, tf["provider_meta"])

	res := nested(f.Resource("aws_instance", "web").Body())
	s.Contains(res, "lifecycle")
	s.Contains(res, "connection")
	s.Equal([]string{"local-exec"}, res["provisioner"])
	s.Contains(res, "action_trigger")

	// Two levels down.
	lifecycle := f.Resource("aws_instance", "web").Body().Blocks()[0]
	s.Equal("lifecycle", lifecycle.BlockType())
	s.Equal("precondition", lifecycle.Body().Blocks()[0].BlockType())

	s.Contains(nested(f.Variable("region").Body()), "validation")
	s.Contains(nested(f.Output("vpc_id").Body()), "precondition")

	chk := nested(f.Check("health").Body())
	s.Equal([]string{"http", "status"}, chk["data"])
	s.Contains(chk, "assert")

	s.Contains(nested(f.Action("aws_lambda_invoke", "notify").Body()), "config")
	s.Contains(nested(f.Removed()[0].Body()), "lifecycle")
}

// TestSameKeywordDiffersByContext is why nested blocks are Generic
// rather than typed, and why there is no one table of keyword to labels.
// Terraform gives the same keyword different shapes under different
// parents:
//
//	backend "s3"   in terraform   labels a type
//	backend "b"    in a test file labels a name
//	provider "aws" at top level   labels a name
//	provider "aws" in state_store labels a type
//	module "x"     at top level   labels a name
//	module         in a test file has none
//
// A block's shape is a property of where it sits, not of its keyword, so
// only the top level — where there is exactly one context — can be typed
// from the keyword alone.
func (s *TFWriteSuite) TestSameKeywordDiffersByContext() {
	f, err := Parse([]byte(everyBlockType), "main.tf")
	s.Require().NoError(err)

	// `provider` at top level is a Provider, labelled with a name.
	top := f.Provider("aws")
	s.Require().NotNil(top)
	s.Equal("aws", top.Name())

	// The same keyword nested inside terraform.provider_meta is a
	// different thing entirely, and is not reached by File.Provider.
	s.Len(f.Providers(), 1, "only the top-level one")

	// `data` nested inside a check is not a top-level data source.
	s.Nil(f.DataSource("http", "status"), "the one inside check/health is not top level")
	s.NotNil(f.DataSource("aws_ami", "ubuntu"))
}

// TestEditEveryBlockTypeStaysStable edits one attribute in every
// top-level block and checks the file still parses and is stable — the
// reprint path exercised across every block shape rather than just the
// couple the other tests touch.
func (s *TFWriteSuite) TestEditEveryBlockTypeStaysStable() {
	f, err := Parse([]byte(everyBlockType), "main.tf")
	s.Require().NoError(err)

	for _, blk := range f.Blocks() {
		blk.Body().SetAttribute("veil_touched", "yes")
	}
	out := f.String()

	again, err := Parse([]byte(out), "main.tf")
	s.Require().NoError(err)
	s.Equal(out, again.String(), "an edited file is stable on reparse")

	s.Equal(len(f.Blocks()), strings.Count(out, "veil_touched"),
		"every block took the edit exactly once")
	// Comments in every position survived a reprint of every block.
	s.Contains(out, "# File header comment.")
	s.Contains(out, "# A resource with every nested block it accepts.")
	s.Contains(out, "# trailing comment on a local")
}

// orNil normalizes an empty label slice for comparison, since a block
// with no labels yields nil rather than an empty slice.
func orNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
