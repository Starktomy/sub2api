# Foundation ONLY. Intentionally no task definitions, ECS services, autoscaling,
# execution/task IAM roles, ALB, application image deployment or legacy app startup.
resource "aws_ecs_cluster" "foundation" {
  name = local.name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_ecs_cluster_capacity_providers" "foundation" {
  cluster_name       = aws_ecs_cluster.foundation.name
  capacity_providers = ["FARGATE"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    base              = 0
    weight            = 1
  }
}

resource "aws_ecr_repository" "application" {
  name                 = "${local.name}/application"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false

  encryption_configuration {
    encryption_type = "AES256"
  }

  # Repository-scoped scanning avoids taking ownership of account-wide registry
  # scanning settings; reassess existing registry/Inspector policy before deploy.
  image_scanning_configuration {
    scan_on_push = true
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_ecr_lifecycle_policy" "untagged_only" {
  repository = aws_ecr_repository.application.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after 14 days; keep tagged rollback images"
      selection = {
        tagStatus   = "untagged"
        countType   = "sinceImagePushed"
        countUnit   = "days"
        countNumber = 14
      }
      action = { type = "expire" }
    }]
  })
}

resource "aws_cloudwatch_log_group" "application" {
  name              = "/ecs/${local.name}/application"
  retention_in_days = 30

  depends_on = [terraform_data.deployment_guard]
}
