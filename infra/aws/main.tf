locals {
  account_id = one(var.allowed_account_ids)
  name       = var.name_prefix
  azs        = { for index, az in var.availability_zones : az => index }
  tags = merge(var.tags, {
    Project     = var.name_prefix
    Environment = var.environment
    ManagedBy   = "Terraform"
    Layer       = "foundation"
  })
}

# This is deliberately a blocking resource precondition, NOT a warning-only check.
# It does not grant permission to deploy, create a role, or bootstrap remote state.
resource "terraform_data" "deployment_guard" {
  input = {
    account_id = local.account_id
    region     = var.region
  }

  lifecycle {
    precondition {
      condition     = var.deployment_enabled && var.deployment_role_arn != null
      error_message = "Deployment is blocked. Current root credentials are not an approved deployment identity. Obtain a deployment role, remote-state bootstrap, cost review and separate deployment authorization first. Offline mock tests may override this gate only with fixture identities."
    }
    precondition {
      condition     = var.deployment_role_arn == null ? true : try(split(":", var.deployment_role_arn)[4] == local.account_id, false)
      error_message = "The deployment role must belong to the sole allowed account."
    }
  }
}

# Separate archive and data keys avoid granting future archive writers access to
# database storage or the bootstrap-only RDS master credential.
resource "aws_kms_key" "foundation" {
  for_each = toset(["archive", "data"])

  description             = "${local.name} ${each.key} encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "EnableAccountIAMDelegation"
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::${local.account_id}:root" }
      Action    = "kms:*"
      Resource  = "*"
    }]
  })

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_kms_alias" "foundation" {
  for_each = aws_kms_key.foundation

  name          = "alias/${local.name}-${each.key}"
  target_key_id = each.value.key_id
}
