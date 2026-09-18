# Top of file.
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
