resource "aws_vpc" "foundation" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_default_security_group" "deny_all" {
  vpc_id  = aws_vpc.foundation.id
  ingress = []
  egress  = []
  tags    = { Name = "${local.name}-unused-default" }
}

resource "aws_internet_gateway" "foundation" {
  vpc_id = aws_vpc.foundation.id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  for_each = local.azs

  vpc_id                  = aws_vpc.foundation.id
  availability_zone       = each.key
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, each.value)
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-public-${each.key}", Tier = "public" }
}

resource "aws_subnet" "tasks" {
  for_each = local.azs

  vpc_id                  = aws_vpc.foundation.id
  availability_zone       = each.key
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, 16 + each.value)
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-tasks-${each.key}", Tier = "private" }
}

# DB and cache share an isolated subnet tier, but have different security groups.
# These subnets have only VPC-local routes: no NAT or internet-gateway default route.
resource "aws_subnet" "data" {
  for_each = local.azs

  vpc_id                  = aws_vpc.foundation.id
  availability_zone       = each.key
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, 32 + each.value)
  map_public_ip_on_launch = false
  tags                    = { Name = "${local.name}-data-${each.key}", Tier = "isolated" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.foundation.id
  tags   = { Name = "${local.name}-public" }
}

resource "aws_route" "public_internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.foundation.id
}

resource "aws_route_table_association" "public" {
  for_each = local.azs

  subnet_id      = aws_subnet.public[each.key].id
  route_table_id = aws_route_table.public.id
}

# Two zonal NAT gateways + two public IPv4 addresses incur continuous charges even
# with zero ECS tasks. Each task subnet uses its same-AZ NAT, not a shared NAT.
resource "aws_eip" "nat" {
  for_each = local.azs

  domain = "vpc"
  tags   = { Name = "${local.name}-nat-${each.key}" }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_nat_gateway" "tasks" {
  for_each = local.azs

  allocation_id     = aws_eip.nat[each.key].id
  subnet_id         = aws_subnet.public[each.key].id
  connectivity_type = "public"
  tags              = { Name = "${local.name}-nat-${each.key}" }

  depends_on = [aws_route.public_internet, aws_route_table_association.public]
}

resource "aws_route_table" "tasks" {
  for_each = local.azs

  vpc_id = aws_vpc.foundation.id
  tags   = { Name = "${local.name}-tasks-${each.key}" }
}

resource "aws_route" "tasks_nat" {
  for_each = local.azs

  route_table_id         = aws_route_table.tasks[each.key].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.tasks[each.key].id
}

resource "aws_route_table_association" "tasks" {
  for_each = local.azs

  subnet_id      = aws_subnet.tasks[each.key].id
  route_table_id = aws_route_table.tasks[each.key].id
}

resource "aws_route_table" "data" {
  vpc_id = aws_vpc.foundation.id
  tags   = { Name = "${local.name}-isolated-data" }
}

resource "aws_route_table_association" "data" {
  for_each = local.azs

  subnet_id      = aws_subnet.data[each.key].id
  route_table_id = aws_route_table.data.id
}

# S3 gateway endpoint has no endpoint-hour fee and avoids NAT processing for
# same-Region S3 traffic. IAM/bucket policies, not this endpoint, grant access.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.foundation.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [for az in var.availability_zones : aws_route_table.tasks[az].id]
  tags              = { Name = "${local.name}-s3" }
}

# No ingress until the runtime's ALB and port contract have been reviewed.
resource "aws_security_group" "tasks" {
  name_prefix = "${local.name}-tasks-"
  description = "Reserved future ECS tasks; no ingress in foundation"
  vpc_id      = aws_vpc.foundation.id
  tags        = { Name = "${local.name}-tasks" }
}

resource "aws_security_group" "database" {
  name_prefix = "${local.name}-database-"
  description = "PostgreSQL only from the reserved task security group"
  vpc_id      = aws_vpc.foundation.id
  tags        = { Name = "${local.name}-database" }
}

resource "aws_security_group" "cache" {
  name_prefix = "${local.name}-cache-"
  description = "TLS Valkey only from reserved tasks and cache replication peers"
  vpc_id      = aws_vpc.foundation.id
  tags        = { Name = "${local.name}-cache" }
}

resource "aws_vpc_security_group_ingress_rule" "database_from_tasks" {
  security_group_id            = aws_security_group.database.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  description                  = "PostgreSQL from future ECS tasks only; database requires TLS"
}

resource "aws_vpc_security_group_egress_rule" "tasks_to_database" {
  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.database.id
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  description                  = "PostgreSQL to isolated DB only"
}

resource "aws_vpc_security_group_ingress_rule" "cache_from_tasks" {
  security_group_id            = aws_security_group.cache.id
  referenced_security_group_id = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "TLS Valkey from future ECS tasks only"
}

resource "aws_vpc_security_group_egress_rule" "tasks_to_cache" {
  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "TLS Valkey to isolated cache only"
}

resource "aws_vpc_security_group_ingress_rule" "cache_replication" {
  security_group_id            = aws_security_group.cache.id
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "Cache replication within the cache security group"
}

resource "aws_vpc_security_group_egress_rule" "cache_replication" {
  security_group_id            = aws_security_group.cache.id
  referenced_security_group_id = aws_security_group.cache.id
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
  description                  = "Cache replication within the cache security group"
}

resource "aws_vpc_security_group_egress_rule" "tasks_https" {
  security_group_id = aws_security_group.tasks.id
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
  description       = "HTTPS to AWS APIs and upstreams via same-AZ NAT; not domain-filtered"
}
