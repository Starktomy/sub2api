terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.71.0" }
  }
  backend "s3" {}
}

provider "aws" {
  region              = "us-west-2"
  allowed_account_ids = [var.account_id]
  assume_role {
    role_arn     = var.deployment_role_arn
    session_name = "sub2api-integration"
  }
  default_tags {
    tags = { Project = "sub2api-cloud", Environment = "integration", ManagedBy = "Terraform" }
  }
}

variable "account_id" {
  type = string
  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "An explicitly verified 12-digit target account ID is required."
  }
}
variable "deployment_role_arn" {
  type = string
  validation {
    condition     = can(regex("^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9_+=,.@/-]+$", var.deployment_role_arn))
    error_message = "Use an approved IAM role, never root or an IAM user."
  }
}
variable "existing_vpc_id" {
  type = string
  validation {
    condition     = can(regex("^vpc-[0-9a-f]+$", var.existing_vpc_id))
    error_message = "Supply the existing integration EC2's verified VPC."
  }
}
variable "ec2_security_group_id" {
  type = string
  validation {
    condition     = can(regex("^sg-[0-9a-f]+$", var.ec2_security_group_id))
    error_message = "Supply the existing integration EC2 security-group ID."
  }
}
variable "additional_runtime_security_group_ids" {
  description = "Explicitly approved private application SGs; empty preserves the original integration topology."
  type        = set(string)
  default     = []
  validation {
    condition     = alltrue([for id in var.additional_runtime_security_group_ids : can(regex("^sg-[0-9a-f]+$", id))])
    error_message = "Additional runtime sources must be explicit security-group IDs, never CIDRs."
  }
}

variable "deployment_enabled" {
  type    = bool
  default = false
}
variable "valkey_password" {
  type      = string
  sensitive = true
  ephemeral = true
  validation {
    condition     = can(regex("^[A-Za-z0-9_-]{32,128}$", var.valkey_password))
    error_message = "Inject an external 32-128 character random password; never store it in tfvars/state."
  }
}
variable "valkey_password_version" {
  type    = number
  default = 1
}

locals {
  name = "sub2api-it"
  subnets = {
    "us-west-2b" = "172.31.128.0/24"
    "us-west-2a" = "172.31.129.0/24"
  }
}

resource "terraform_data" "guard" {
  lifecycle {
    precondition {
      condition     = var.deployment_enabled && can(regex("^arn:aws:iam::${var.account_id}:role/", var.deployment_role_arn))
      error_message = "Explicit deployment authorization and an approved same-account role are required. Never apply as root."
    }
  }
}

resource "aws_subnet" "data" {
  for_each                = local.subnets
  vpc_id                  = var.existing_vpc_id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-data-${each.key}" }
  depends_on              = [terraform_data.guard]
}
resource "aws_route_table" "data" {
  vpc_id     = var.existing_vpc_id
  tags       = { Name = "${local.name}-isolated" }
  depends_on = [terraform_data.guard]
}
resource "aws_route_table_association" "data" {
  for_each       = local.subnets
  subnet_id      = aws_subnet.data[each.key].id
  route_table_id = aws_route_table.data.id
}
resource "aws_security_group" "postgres" {
  name        = "${local.name}-postgres"
  description = "Integration PostgreSQL from the explicitly selected EC2 SG only"
  vpc_id      = var.existing_vpc_id
  depends_on  = [terraform_data.guard]
}
resource "aws_security_group" "valkey" {
  name        = "${local.name}-valkey"
  description = "Integration TLS Valkey from selected EC2 and replication peers only"
  vpc_id      = var.existing_vpc_id
  depends_on  = [terraform_data.guard]
}
resource "aws_vpc_security_group_ingress_rule" "postgres" {
  security_group_id            = aws_security_group.postgres.id
  referenced_security_group_id = var.ec2_security_group_id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}
resource "aws_vpc_security_group_ingress_rule" "valkey" {
  security_group_id            = aws_security_group.valkey.id
  referenced_security_group_id = var.ec2_security_group_id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}
resource "aws_vpc_security_group_ingress_rule" "additional_postgres" {
  for_each                     = var.additional_runtime_security_group_ids
  security_group_id            = aws_security_group.postgres.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  description                  = "Multi-host-private-runtime"
}
resource "aws_vpc_security_group_ingress_rule" "additional_valkey" {
  for_each                     = var.additional_runtime_security_group_ids
  security_group_id            = aws_security_group.valkey.id
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "Multi-host-private-runtime"
}

resource "aws_vpc_security_group_ingress_rule" "replication" {
  security_group_id            = aws_security_group.valkey.id
  referenced_security_group_id = aws_security_group.valkey.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}
resource "aws_vpc_security_group_egress_rule" "replication" {
  security_group_id            = aws_security_group.valkey.id
  referenced_security_group_id = aws_security_group.valkey.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

resource "aws_db_subnet_group" "postgres" {
  name       = "${local.name}-postgres"
  subnet_ids = [for subnet in aws_subnet.data : subnet.id]
}
resource "aws_db_parameter_group" "postgres" {
  name   = "${local.name}-postgres16"
  family = "postgres16"
  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }
  depends_on = [terraform_data.guard]
}
resource "aws_db_instance" "postgres" {
  identifier                  = "${local.name}-postgres"
  engine                      = "postgres"
  engine_version              = "16.13"
  instance_class              = "db.t4g.medium"
  allocated_storage           = 20
  storage_type                = "gp3"
  storage_encrypted           = true
  multi_az                    = true
  db_name                     = "sub2api_integration_test"
  username                    = "sub2api_bootstrap"
  manage_master_user_password = true
  db_subnet_group_name        = aws_db_subnet_group.postgres.name
  vpc_security_group_ids      = [aws_security_group.postgres.id]
  parameter_group_name        = aws_db_parameter_group.postgres.name
  publicly_accessible         = false
  backup_retention_period     = 7
  copy_tags_to_snapshot       = true
  deletion_protection         = true
  skip_final_snapshot         = false
  final_snapshot_identifier   = "${local.name}-postgres-final"
  auto_minor_version_upgrade  = true
  allow_major_version_upgrade = false
  engine_lifecycle_support    = "open-source-rds-extended-support-disabled"
  apply_immediately           = false
  lifecycle { prevent_destroy = true }
}
resource "aws_elasticache_subnet_group" "valkey" {
  name       = "${local.name}-valkey"
  subnet_ids = [for subnet in aws_subnet.data : subnet.id]
}
resource "aws_elasticache_parameter_group" "valkey" {
  name   = "${local.name}-valkey7"
  family = "valkey7"
  parameter {
    name  = "maxmemory-policy"
    value = "noeviction"
  }
  depends_on = [terraform_data.guard]
}
resource "aws_elasticache_user" "integration" {
  user_id   = "${local.name}-runtime"
  user_name = "${local.name}-runtime"
  engine    = "valkey"
  # Dedicated EMPTY integration cache only. No production data; deny administrative
  # commands while permitting the existing Lua, Pub/Sub and scanning contracts.
  access_string        = "on ~* &* +@all -@admin"
  no_password_required = false
  passwords_wo         = var.valkey_password
  passwords_wo_version = var.valkey_password_version
  depends_on           = [terraform_data.guard]
}
resource "aws_elasticache_user_group" "valkey" {
  user_group_id = "${local.name}-valkey"
  engine        = "valkey"
  user_ids      = [aws_elasticache_user.integration.user_id]
}
resource "aws_elasticache_replication_group" "valkey" {
  replication_group_id        = "${local.name}-valkey"
  description                 = "Sub2API EC2 integration TLS primary plus cross-AZ replica"
  engine                      = "valkey"
  engine_version              = "7.2"
  node_type                   = "cache.t4g.small"
  cluster_mode                = "disabled"
  num_cache_clusters          = 2
  preferred_cache_cluster_azs = ["us-west-2b", "us-west-2a"]
  multi_az_enabled            = true
  automatic_failover_enabled  = true
  subnet_group_name           = aws_elasticache_subnet_group.valkey.name
  security_group_ids          = [aws_security_group.valkey.id]
  parameter_group_name        = aws_elasticache_parameter_group.valkey.name
  user_group_ids              = [aws_elasticache_user_group.valkey.user_group_id]
  at_rest_encryption_enabled  = true
  transit_encryption_enabled  = true
  transit_encryption_mode     = "required"
  snapshot_retention_limit    = 3
  auto_minor_version_upgrade  = true
  apply_immediately           = false
  lifecycle { prevent_destroy = true }
}
output "postgres" {
  value = {
    host              = aws_db_instance.postgres.address
    port              = aws_db_instance.postgres.port
    dbname            = aws_db_instance.postgres.db_name
    master_secret_arn = aws_db_instance.postgres.master_user_secret[0].secret_arn
  }
}
output "valkey" {
  value = {
    host     = aws_elasticache_replication_group.valkey.primary_endpoint_address
    port     = aws_elasticache_replication_group.valkey.port
    username = aws_elasticache_user.integration.user_name
    tls      = true
  }
}
