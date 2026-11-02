resource "aws_db_subnet_group" "postgres" {
  name        = "${local.name}-postgres"
  description = "Isolated PostgreSQL subnets in two AZs"
  subnet_ids  = [for az in var.availability_zones : aws_subnet.data[az].id]
}

resource "aws_db_parameter_group" "postgres" {
  name_prefix = "${local.name}-postgres16-"
  family      = "postgres16"
  description = "PostgreSQL 16 with TLS enforced"

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_cloudwatch_log_group" "postgres" {
  name              = "/aws/rds/instance/${local.name}-postgres/postgresql"
  retention_in_days = 30

  depends_on = [terraform_data.deployment_guard]
}

# This is a Multi-AZ DB INSTANCE with one synchronous, non-readable standby,
# NOT Aurora, a Multi-AZ DB cluster, or an application read-replica deployment.
resource "aws_db_instance" "postgres" {
  identifier     = "${local.name}-postgres"
  engine         = "postgres"
  engine_version = "16"
  instance_class = var.db_instance_class

  db_name  = "sub2api"
  username = "sub2api_bootstrap"
  # Never expose the master credential to application tasks. A separately
  # authorized bootstrap must create least-privilege migrator/runtime DB roles.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.foundation["data"].arn

  multi_az               = true
  db_subnet_group_name   = aws_db_subnet_group.postgres.name
  vpc_security_group_ids = [aws_security_group.database.id]
  publicly_accessible    = false
  port                   = 5432
  parameter_group_name   = aws_db_parameter_group.postgres.name
  ca_cert_identifier     = "rds-ca-rsa2048-g1"

  storage_type          = "gp3"
  allocated_storage     = var.db_storage_gib
  max_allocated_storage = var.db_storage_gib * 2
  storage_encrypted     = true
  kms_key_id            = aws_kms_key.foundation["data"].arn

  backup_retention_period   = 14
  backup_window             = "08:00-09:00"
  maintenance_window        = "sun:10:00-sun:11:00"
  copy_tags_to_snapshot     = true
  delete_automated_backups  = false
  deletion_protection       = true
  skip_final_snapshot       = false
  final_snapshot_identifier = "${local.name}-postgres-final"

  auto_minor_version_upgrade      = true
  allow_major_version_upgrade     = false
  apply_immediately               = false
  enabled_cloudwatch_logs_exports = ["postgresql"]

  # Explicitly disable Extended Support enrollment. Reassess the chosen major
  # version before its standard-support expiry; this is not a version freeze.
  engine_lifecycle_support = "open-source-rds-extended-support-disabled"

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [aws_cloudwatch_log_group.postgres]
}

resource "aws_elasticache_subnet_group" "valkey" {
  name        = "${local.name}-valkey"
  description = "Isolated Valkey subnets in two AZs"
  subnet_ids  = [for az in var.availability_zones : aws_subnet.data[az].id]
}

resource "aws_elasticache_parameter_group" "valkey" {
  name        = "${local.name}-valkey7"
  family      = "valkey7"
  description = "Unsharded Valkey with explicit noeviction"

  parameter {
    name  = "maxmemory-policy"
    value = "noeviction"
  }
}

# Reserved, DISABLED runtime user. Enabling a least-privilege ACL is a reviewed
# runtime-readiness change, not something this foundation pretends is complete.
# Native VALKEY groups automatically disable the unauthenticated default user.
resource "aws_elasticache_user" "reserved_runtime" {
  user_id              = "${local.name}-runtime"
  user_name            = "${local.name}-runtime"
  engine               = "valkey"
  access_string        = "off -@all"
  no_password_required = false
  passwords_wo         = var.valkey_password
  passwords_wo_version = var.valkey_password_version

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_elasticache_user_group" "valkey" {
  user_group_id = "${local.name}-valkey"
  engine        = "valkey"
  user_ids      = [aws_elasticache_user.reserved_runtime.user_id]
}

resource "aws_cloudwatch_log_group" "valkey" {
  for_each = toset(["engine", "slow"])

  name              = "/elasticache/${local.name}/${each.key}"
  retention_in_days = 30

  depends_on = [terraform_data.deployment_guard]
}

# Node-based, cluster-mode-disabled: EXACTLY one primary + one replica across AZs.
resource "aws_elasticache_replication_group" "valkey" {
  replication_group_id = "${local.name}-valkey"
  description          = "${local.name} Valkey primary and replica; runtime access blocked"
  engine               = "valkey"
  engine_version       = "7.2"
  node_type            = var.valkey_node_type
  port                 = 6379

  cluster_mode                = "disabled"
  num_cache_clusters          = 2
  preferred_cache_cluster_azs = var.availability_zones
  multi_az_enabled            = true
  automatic_failover_enabled  = true
  parameter_group_name        = aws_elasticache_parameter_group.valkey.name
  subnet_group_name           = aws_elasticache_subnet_group.valkey.name
  security_group_ids          = [aws_security_group.cache.id]
  user_group_ids              = [aws_elasticache_user_group.valkey.user_group_id]

  at_rest_encryption_enabled = true
  kms_key_id                 = aws_kms_key.foundation["data"].arn
  transit_encryption_enabled = true
  transit_encryption_mode    = "required"

  snapshot_retention_limit   = 7
  snapshot_window            = "06:00-07:00"
  maintenance_window         = "sun:11:00-sun:12:00"
  final_snapshot_identifier  = "${local.name}-valkey-final"
  auto_minor_version_upgrade = true
  apply_immediately          = false

  log_delivery_configuration {
    destination      = aws_cloudwatch_log_group.valkey["engine"].name
    destination_type = "cloudwatch-logs"
    log_format       = "json"
    log_type         = "engine-log"
  }

  log_delivery_configuration {
    destination      = aws_cloudwatch_log_group.valkey["slow"].name
    destination_type = "cloudwatch-logs"
    log_format       = "json"
    log_type         = "slow-log"
  }

  lifecycle {
    prevent_destroy = true
  }
}
