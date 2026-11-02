# All runs use this mocked provider. The security-contract run simulates a
# resource lifecycle ONLY in Terraform's mock engine. No AWS API is available.
mock_provider "aws" {
  override_during = apply

  mock_resource "aws_kms_key" {
    defaults = {
      arn    = "arn:aws:kms:us-west-2:123456789012:key/11111111-1111-4111-8111-111111111111"
      key_id = "11111111-1111-4111-8111-111111111111"
    }
  }

  mock_resource "aws_db_instance" {
    defaults = {
      master_user_secret = [{
        secret_arn    = "arn:aws:secretsmanager:us-west-2:123456789012:secret:fixture-bootstrap-abcdef"
        kms_key_id    = "arn:aws:kms:us-west-2:123456789012:key/11111111-1111-4111-8111-111111111111"
        secret_status = "active"
      }]
    }
  }

  mock_resource "aws_s3_bucket" {
    defaults = {
      id  = "sub2api-foundation-123456789012-archive"
      arn = "arn:aws:s3:::sub2api-foundation-123456789012-archive"
    }
  }

}

# Distinct queue identities make the DLQ wiring assertions meaningful.
override_resource {
  target = aws_sqs_queue.work
  values = {
    arn = "arn:aws:sqs:us-west-2:123456789012:fixture-work"
    url = "https://sqs.us-west-2.amazonaws.com/123456789012/fixture-work"
  }
}

override_resource {
  target = aws_sqs_queue.dead_letter
  values = {
    arn = "arn:aws:sqs:us-west-2:123456789012:fixture-dlq"
    url = "https://sqs.us-west-2.amazonaws.com/123456789012/fixture-dlq"
  }
}

variables {
  # Public test fixtures, not a target-account claim, role, or usable credential.
  allowed_account_ids = ["123456789012"]
  deployment_role_arn = "arn:aws:iam::123456789012:role/fixture-deployment"
  deployment_enabled  = true
  valkey_password     = "mock-only-not-a-real-secret-0123456789"
}

run "default_deployment_is_blocked" {
  command = plan

  variables {
    deployment_enabled  = false
    deployment_role_arn = null
  }

  expect_failures = [terraform_data.deployment_guard]
}

run "role_is_required_even_when_enabled" {
  command = plan

  variables {
    deployment_role_arn = null
  }

  expect_failures = [terraform_data.deployment_guard]
}

run "cross_account_role_is_blocked" {
  command = plan

  variables {
    deployment_role_arn = "arn:aws:iam::999999999999:role/fixture-deployment"
  }

  expect_failures = [terraform_data.deployment_guard]
}

run "unapproved_region_is_blocked" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

run "invalid_account_is_blocked" {
  command = plan

  variables {
    allowed_account_ids = ["invalid-account"]
    deployment_role_arn = null
    deployment_enabled  = false
  }

  expect_failures = [var.allowed_account_ids]
}

run "weak_external_password_is_blocked" {
  command = plan

  variables {
    valkey_password = "too-short"
  }

  expect_failures = [var.valkey_password]
}

run "foundation_security_contract" {
  # Mock-only lifecycle, NOT a real terraform apply. Exercises computed outputs.
  command = apply

  assert {
    condition = (
      length(aws_subnet.public) == 2 && length(aws_subnet.tasks) == 2 && length(aws_subnet.data) == 2 &&
      length(aws_nat_gateway.tasks) == 2 && length(aws_eip.nat) == 2 &&
      length(toset(concat(
        [for subnet in aws_subnet.public : subnet.cidr_block],
        [for subnet in aws_subnet.tasks : subnet.cidr_block],
        [for subnet in aws_subnet.data : subnet.cidr_block]
      ))) == 6
    )
    error_message = "Use six distinct subnets across two AZs, with a NAT and public IPv4 per AZ."
  }

  assert {
    condition = alltrue([for az in var.availability_zones :
      aws_nat_gateway.tasks[az].subnet_id == aws_subnet.public[az].id &&
      aws_route.tasks_nat[az].nat_gateway_id == aws_nat_gateway.tasks[az].id &&
      aws_route_table_association.tasks[az].route_table_id == aws_route_table.tasks[az].id &&
      aws_route_table_association.data[az].route_table_id == aws_route_table.data.id &&
      !aws_subnet.tasks[az].map_public_ip_on_launch && !aws_subnet.data[az].map_public_ip_on_launch
    ])
    error_message = "Task egress must use its same-AZ NAT, and data subnets their isolated route table."
  }

  assert {
    condition = (
      aws_vpc_security_group_ingress_rule.database_from_tasks.referenced_security_group_id == aws_security_group.tasks.id &&
      aws_vpc_security_group_ingress_rule.database_from_tasks.from_port == 5432 &&
      aws_vpc_security_group_ingress_rule.database_from_tasks.cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.database_from_tasks.cidr_ipv6 == null &&
      aws_vpc_security_group_ingress_rule.cache_from_tasks.referenced_security_group_id == aws_security_group.tasks.id &&
      aws_vpc_security_group_ingress_rule.cache_from_tasks.from_port == 6379 &&
      aws_vpc_security_group_ingress_rule.cache_from_tasks.cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.cache_from_tasks.cidr_ipv6 == null &&
      aws_vpc_security_group_ingress_rule.cache_replication.referenced_security_group_id == aws_security_group.cache.id &&
      aws_vpc_security_group_ingress_rule.cache_replication.cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.cache_replication.cidr_ipv6 == null &&
      length(aws_default_security_group.deny_all.ingress) == 0 &&
      length(aws_default_security_group.deny_all.egress) == 0
    )
    error_message = "DB/cache ingress must use security-group references, never public IPv4/IPv6 ranges; the default SG must remain unused and empty."
  }

  assert {
    condition = (
      aws_db_instance.postgres.engine == "postgres" &&
      aws_db_instance.postgres.multi_az && !aws_db_instance.postgres.publicly_accessible &&
      aws_db_instance.postgres.storage_encrypted &&
      aws_db_instance.postgres.kms_key_id == aws_kms_key.foundation["data"].arn &&
      aws_db_instance.postgres.deletion_protection && !aws_db_instance.postgres.skip_final_snapshot &&
      aws_db_instance.postgres.backup_retention_period == 14 &&
      !aws_db_instance.postgres.delete_automated_backups &&
      aws_db_instance.postgres.manage_master_user_password &&
      aws_db_instance.postgres.password == null &&
      aws_db_instance.postgres.username == "sub2api_bootstrap" &&
      output.database.sslmode == "verify-full" &&
      one(aws_db_parameter_group.postgres.parameter).name == "rds.force_ssl" &&
      one(aws_db_parameter_group.postgres.parameter).value == "1"
    )
    error_message = "Require encrypted private RDS Multi-AZ DB instance, managed admin secret, TLS, PITR and deletion safeguards."
  }

  assert {
    condition = (
      aws_elasticache_replication_group.valkey.engine == "valkey" &&
      aws_elasticache_replication_group.valkey.cluster_mode == "disabled" &&
      aws_elasticache_replication_group.valkey.num_cache_clusters == 2 &&
      aws_elasticache_replication_group.valkey.multi_az_enabled &&
      aws_elasticache_replication_group.valkey.automatic_failover_enabled &&
      length(distinct(aws_elasticache_replication_group.valkey.preferred_cache_cluster_azs)) == 2 &&
      aws_elasticache_replication_group.valkey.at_rest_encryption_enabled &&
      aws_elasticache_replication_group.valkey.transit_encryption_enabled &&
      aws_elasticache_replication_group.valkey.transit_encryption_mode == "required" &&
      aws_elasticache_replication_group.valkey.auth_token == null &&
      one(aws_elasticache_parameter_group.valkey.parameter).name == "maxmemory-policy" &&
      one(aws_elasticache_parameter_group.valkey.parameter).value == "noeviction"
    )
    error_message = "Valkey must be unsharded primary+replica across AZs, encrypted, TLS required and noeviction."
  }

  assert {
    condition = (
      aws_elasticache_user_group.valkey.engine == "valkey" &&
      one(aws_elasticache_user_group.valkey.user_ids) == aws_elasticache_user.reserved_runtime.user_id &&
      aws_elasticache_user.reserved_runtime.engine == "valkey" &&
      aws_elasticache_user.reserved_runtime.access_string == "off -@all" &&
      !aws_elasticache_user.reserved_runtime.no_password_required &&
      aws_elasticache_user.reserved_runtime.passwords_wo == null &&
      aws_elasticache_user.reserved_runtime.passwords == null &&
      one(aws_elasticache_replication_group.valkey.user_group_ids) == aws_elasticache_user_group.valkey.user_group_id &&
      !output.valkey.runtime_enabled && !output.runtime_ready
    )
    error_message = "Only a disabled native VALKEY ACL user is reserved; no ordinary password/state or anonymous default user is allowed."
  }

  assert {
    condition = (
      one(aws_ecs_cluster_capacity_providers.foundation.capacity_providers) == "FARGATE" &&
      aws_ecr_repository.application.image_tag_mutability == "IMMUTABLE" &&
      !aws_ecr_repository.application.force_delete &&
      aws_cloudwatch_log_group.application.retention_in_days == 30
    )
    error_message = "Reserve Fargate with immutable ECR images and bounded application log retention."
  }

  assert {
    condition = (
      aws_sqs_queue.work.sqs_managed_sse_enabled && aws_sqs_queue.dead_letter.sqs_managed_sse_enabled &&
      aws_sqs_queue.dead_letter.message_retention_seconds > aws_sqs_queue.work.message_retention_seconds &&
      jsondecode(aws_sqs_queue_redrive_policy.work.redrive_policy).deadLetterTargetArn == aws_sqs_queue.dead_letter.arn &&
      jsondecode(aws_sqs_queue_redrive_policy.work.redrive_policy).maxReceiveCount == 5 &&
      jsondecode(aws_sqs_queue_redrive_allow_policy.dead_letter.redrive_allow_policy).redrivePermission == "byQueue" &&
      one(jsondecode(aws_sqs_queue_redrive_allow_policy.dead_letter.redrive_allow_policy).sourceQueueArns) == aws_sqs_queue.work.arn &&
      alltrue([for policy in aws_sqs_queue_policy.tls_only :
        one(jsondecode(policy.policy).Statement).Effect == "Deny" &&
        one(jsondecode(policy.policy).Statement).Condition.Bool["aws:SecureTransport"] == "false"
      ])
    )
    error_message = "Encrypt both queues, restrict DLQ redrive, keep DLQ messages longer and deny insecure transport."
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.archive.block_public_acls &&
      aws_s3_bucket_public_access_block.archive.block_public_policy &&
      aws_s3_bucket_public_access_block.archive.ignore_public_acls &&
      aws_s3_bucket_public_access_block.archive.restrict_public_buckets &&
      one(aws_s3_bucket_ownership_controls.archive.rule).object_ownership == "BucketOwnerEnforced" &&
      one(aws_s3_bucket_versioning.archive.versioning_configuration).status == "Enabled" &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.archive.rule).apply_server_side_encryption_by_default).sse_algorithm == "aws:kms" &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.archive.rule).apply_server_side_encryption_by_default).kms_master_key_id == aws_kms_key.foundation["archive"].arn &&
      !aws_s3_bucket.archive.force_destroy &&
      length(jsondecode(aws_s3_bucket_policy.archive.policy).Statement) == 3 &&
      alltrue([for key in aws_kms_key.foundation : key.enable_key_rotation && key.deletion_window_in_days == 30])
    )
    error_message = "Archive must remain private, versioned, ownership-enforced and KMS encrypted with protected rotating keys."
  }
}
