# Offline-only mock tests. Every run is a plan with no cloud writes.
mock_provider "aws" {
  override_during = plan
  mock_resource "aws_db_instance" {
    defaults = {
      master_user_secret = [{ secret_arn = "arn:aws:secretsmanager:us-west-2:123456789012:secret:rds!db-fixture-abcdef", secret_status = "active", kms_key_id = "alias/aws/secretsmanager" }]
    }
  }
}
variables {
  account_id            = "123456789012"
  deployment_role_arn   = "arn:aws:iam::123456789012:role/integration-fixture"
  existing_vpc_id       = "vpc-12345678"
  ec2_security_group_id = "sg-12345678"
  deployment_enabled    = true
  valkey_password       = "mock-only-password-not-valid-anywhere-0123456789"
}
run "default_deploy_gate" {
  command = plan
  variables { deployment_enabled = false }
  expect_failures = [terraform_data.guard]
}
run "root_is_not_a_deployment_role" {
  command = plan
  variables { deployment_role_arn = "arn:aws:iam::123456789012:root" }
  expect_failures = [var.deployment_role_arn]
}
run "wrong_account_role_is_blocked" {
  command = plan
  variables { deployment_role_arn = "arn:aws:iam::999999999999:role/wrong" }
  expect_failures = [terraform_data.guard]
}
run "weak_password_is_rejected" {
  command = plan
  variables { valkey_password = "weak" }
  expect_failures = [var.valkey_password]
}
run "managed_data_security_contract" {
  command = plan
  assert {
    condition = (
      length(aws_subnet.data) == 2 &&
      aws_subnet.data["us-west-2a"].cidr_block == "172.31.129.0/24" &&
      aws_subnet.data["us-west-2b"].cidr_block == "172.31.128.0/24" &&
      alltrue([for subnet in aws_subnet.data : !subnet.map_public_ip_on_launch]) &&
      aws_vpc_security_group_ingress_rule.postgres.referenced_security_group_id == var.ec2_security_group_id &&
      aws_vpc_security_group_ingress_rule.valkey.referenced_security_group_id == var.ec2_security_group_id &&
      aws_vpc_security_group_ingress_rule.postgres.cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.valkey.cidr_ipv4 == null
    )
    error_message = "Data subnets and ingress must stay private and scoped to the selected EC2 security group."
  }
  assert {
    condition = (
      aws_db_instance.postgres.engine == "postgres" &&
      aws_db_instance.postgres.engine_version == "16.13" &&
      aws_db_instance.postgres.multi_az &&
      !aws_db_instance.postgres.publicly_accessible &&
      aws_db_instance.postgres.storage_encrypted &&
      aws_db_instance.postgres.manage_master_user_password &&
      aws_db_instance.postgres.deletion_protection &&
      !aws_db_instance.postgres.skip_final_snapshot &&
      aws_db_instance.postgres.backup_retention_period == 7 &&
      one(aws_db_parameter_group.postgres.parameter).name == "rds.force_ssl" &&
      one(aws_db_parameter_group.postgres.parameter).value == "1" &&
      one(aws_db_parameter_group.postgres.parameter).apply_method == "pending-reboot"
    )
    error_message = "Require private encrypted RDS Multi-AZ DB instance, managed bootstrap secret, TLS and backups."
  }
  assert {
    condition = (
      aws_elasticache_replication_group.valkey.cluster_mode == "disabled" &&
      aws_elasticache_replication_group.valkey.num_cache_clusters == 2 &&
      aws_elasticache_replication_group.valkey.multi_az_enabled &&
      aws_elasticache_replication_group.valkey.automatic_failover_enabled &&
      aws_elasticache_replication_group.valkey.transit_encryption_enabled &&
      aws_elasticache_replication_group.valkey.at_rest_encryption_enabled &&
      aws_elasticache_replication_group.valkey.transit_encryption_mode == "required" &&
      aws_elasticache_user.integration.engine == "valkey" &&
      !aws_elasticache_user.integration.no_password_required &&
      one(aws_elasticache_parameter_group.valkey.parameter).value == "noeviction"
    )
    error_message = "Require Valkey TLS/ACL, cross-AZ primary+replica, noeviction and no sharded-client mismatch."
  }
}
