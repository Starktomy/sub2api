terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
    }
  }

  # Deliberately no remote backend yet. Local state is ONLY for offline validation.
  # A separately authorized state-backend bootstrap and migration are prerequisites
  # for any real deployment. Do not apply this configuration with local state.
}

provider "aws" {
  region              = var.region
  allowed_account_ids = var.allowed_account_ids

  dynamic "assume_role" {
    for_each = var.deployment_role_arn == null ? [] : [var.deployment_role_arn]
    content {
      role_arn     = assume_role.value
      session_name = "sub2api-foundation"
    }
  }

  default_tags {
    tags = local.tags
  }
}
