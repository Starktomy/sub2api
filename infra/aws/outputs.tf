output "network" {
  description = "Reserved runtime networking. No application or public ingress exists."
  value = {
    vpc_id                   = aws_vpc.foundation.id
    availability_zones       = var.availability_zones
    private_task_subnet_ids  = [for az in var.availability_zones : aws_subnet.tasks[az].id]
    isolated_data_subnet_ids = [for az in var.availability_zones : aws_subnet.data[az].id]
    task_security_group_id   = aws_security_group.tasks.id
  }
}

output "ecs_cluster_arn" {
  description = "Empty Fargate-capable cluster; no task definitions or services."
  value       = aws_ecs_cluster.foundation.arn
}

output "ecr_repository_url" {
  value = aws_ecr_repository.application.repository_url
}

output "application_log_group_name" {
  value = aws_cloudwatch_log_group.application.name
}

output "database" {
  description = "PostgreSQL connection metadata ONLY. Runtime username/secret is deliberately absent until least-privilege bootstrap."
  value = {
    address  = aws_db_instance.postgres.address
    port     = aws_db_instance.postgres.port
    name     = aws_db_instance.postgres.db_name
    sslmode  = "verify-full"
    multi_az = aws_db_instance.postgres.multi_az
  }
}

output "database_bootstrap_master_secret_arn" {
  description = "BOOTSTRAP/ADMIN ONLY. Never inject this secret into an application task or grant a runtime role access to it."
  value       = aws_db_instance.postgres.master_user_secret[0].secret_arn
}

output "valkey" {
  description = "CMD primary endpoint, not a sharded client endpoint. Reserved ACL user is DISABLED until runtime ACL/keyspace review. Password is never an output."
  value = {
    primary_endpoint = aws_elasticache_replication_group.valkey.primary_endpoint_address
    port             = aws_elasticache_replication_group.valkey.port
    tls_required     = true
    user_name        = aws_elasticache_user.reserved_runtime.user_name
    runtime_enabled  = false
  }
}

output "queues" {
  value = {
    work_arn = aws_sqs_queue.work.arn
    work_url = aws_sqs_queue.work.url
    dlq_arn  = aws_sqs_queue.dead_letter.arn
    dlq_url  = aws_sqs_queue.dead_letter.url
  }
}

output "archive" {
  description = "Writers need scoped IAM and explicit aws:kms + key ARN encryption headers. No retention/WORM policy has been approved."
  value = {
    bucket_name = aws_s3_bucket.archive.id
    bucket_arn  = aws_s3_bucket.archive.arn
    kms_key_arn = aws_kms_key.foundation["archive"].arn
  }
}

output "runtime_ready" {
  description = "Deliberately false: this is infrastructure foundation, not a functioning or split application deployment."
  value       = false
}
