variable "region" {
  description = "Approved AWS Region. This foundation is intentionally restricted to us-west-2."
  type        = string
  default     = "us-west-2"

  validation {
    condition     = var.region == "us-west-2"
    error_message = "Only the explicitly approved Region us-west-2 is supported."
  }
}

variable "allowed_account_ids" {
  description = "Exactly one explicitly verified target account ID; no default account is assumed."
  type        = list(string)
  nullable    = false

  validation {
    condition     = length(var.allowed_account_ids) == 1 && alltrue([for id in var.allowed_account_ids : can(regex("^[0-9]{12}$", id))])
    error_message = "Supply exactly one independently verified 12-digit AWS account ID."
  }
}

variable "deployment_enabled" {
  description = "Safety gate, not deployment authorization. Keep false until a role, remote state, costs and a separate deployment approval exist."
  type        = bool
  default     = false
  nullable    = false
}

variable "deployment_role_arn" {
  description = "Pre-existing, approved deployment role in the allowed account; never root or a long-lived IAM user. Not created here."
  type        = string
  default     = null

  validation {
    condition     = var.deployment_role_arn == null ? true : can(regex("^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9_+=,.@/-]+$", var.deployment_role_arn))
    error_message = "deployment_role_arn must identify an approved IAM role, not root or an IAM user."
  }
}

variable "name_prefix" {
  description = "Short resource-name prefix; stable after creation."
  type        = string
  default     = "sub2api-foundation"
  nullable    = false

  validation {
    condition     = length(var.name_prefix) <= 24 && can(regex("^[a-z][a-z0-9]*(-[a-z0-9]+)*$", var.name_prefix))
    error_message = "Use at most 24 lowercase letters, digits and single hyphens, beginning with a letter."
  }
}

variable "environment" {
  description = "Environment tag; names are isolated using name_prefix, not this tag."
  type        = string
  default     = "foundation"
  nullable    = false

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,31}$", var.environment))
    error_message = "Use a lowercase environment label of at most 32 characters."
  }
}

variable "tags" {
  description = "Additional ownership/cost tags. Project, Environment, ManagedBy and Layer are reserved."
  type        = map(string)
  default     = {}
  nullable    = false
}

variable "availability_zones" {
  description = "Two account-specific AZ names, to be checked under the future deployment role before deployment. No discovery API is used by validation."
  type        = list(string)
  default     = ["us-west-2a", "us-west-2b"]
  nullable    = false

  validation {
    condition = (
      length(var.availability_zones) == 2 && length(distinct(var.availability_zones)) == 2 &&
      alltrue([for az in var.availability_zones : can(regex("^us-west-2[a-z]$", az))])
    )
    error_message = "Choose exactly two different standard AZ names in us-west-2."
  }
}

variable "vpc_cidr" {
  description = "Canonical private IPv4 /16; six non-overlapping /24s are derived for public, task and isolated data tiers."
  type        = string
  default     = "10.42.0.0/16"
  nullable    = false

  validation {
    condition = try(
      cidrnetmask(var.vpc_cidr) == "255.255.0.0" &&
      cidrhost(var.vpc_cidr, 0) == split("/", var.vpc_cidr)[0] &&
      can(regex("^(10\\.|172\\.(1[6-9]|2[0-9]|3[01])\\.|192\\.168\\.)", var.vpc_cidr)),
      false
    )
    error_message = "Use a canonical RFC1918 IPv4 /16, e.g. 10.42.0.0/16; check organizational CIDR overlap separately."
  }
}

variable "db_instance_class" {
  description = "Provisional Multi-AZ DB instance size, not an approved sizing/cost estimate. Verify orderability in the approved Region before deployment."
  type        = string
  default     = "db.t4g.medium"
  nullable    = false

  validation {
    condition     = can(regex("^db\\.[a-z0-9]+\\.[a-z0-9]+$", var.db_instance_class))
    error_message = "Use an RDS DB instance class."
  }
}

variable "db_storage_gib" {
  description = "Initial gp3 storage for the Multi-AZ DB instance, billed together with its standby."
  type        = number
  default     = 100
  nullable    = false

  validation {
    condition     = var.db_storage_gib >= 20 && var.db_storage_gib <= 1000 && floor(var.db_storage_gib) == var.db_storage_gib
    error_message = "Initial database storage must be an integer between 20 and 1000 GiB."
  }
}

variable "valkey_node_type" {
  description = "Provisional node class; two nodes are always provisioned. Verify regional orderability and capacity before deployment."
  type        = string
  default     = "cache.t4g.small"
  nullable    = false

  validation {
    condition     = can(regex("^cache\\.[a-z0-9]+\\.[a-z0-9]+$", var.valkey_node_type))
    error_message = "Use an ElastiCache node class."
  }
}

variable "valkey_password" {
  description = "Required external secret input for the reserved ACL user. Ephemeral + write-only: never put this in tfvars, normal provider password fields, outputs or state."
  type        = string
  sensitive   = true
  ephemeral   = true
  nullable    = false

  validation {
    condition     = can(regex("^[!-~]{32,128}$", var.valkey_password)) && !can(regex("[,\"/@]", var.valkey_password))
    error_message = "Supply an external 32-128 character printable ASCII secret without comma, double quote, slash or at-sign."
  }
}

variable "valkey_password_version" {
  description = "Increment when the externally managed password changes. A single write-only password is supported; runtime rotation needs a separately reviewed coordination procedure."
  type        = number
  default     = 1
  nullable    = false

  validation {
    condition     = var.valkey_password_version >= 1 && floor(var.valkey_password_version) == var.valkey_password_version
    error_message = "Use a positive integer secret version."
  }
}
