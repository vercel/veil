# File header comment.

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
