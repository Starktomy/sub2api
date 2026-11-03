# EC2 + AWS managed-data integration stage

This root module is deliberately separate from `../aws` (the future ECS foundation).
It reuses the existing integration EC2 VPC/security group and creates only two isolated
subnets, data security groups, PostgreSQL parameter/subnet groups, an RDS PostgreSQL
Multi-AZ **DB instance**, and a two-node TLS Valkey replication group. No ALB, ECS,
NAT, EC2 replacement, DNS change, or public database/cache endpoint is created.

## Authorized deployment

On 2026-11-03 the user explicitly authorized creation of required integration resources
and appropriate IAM configuration. Region is `us-west-2`. Initial root credentials were
used only for IAM bootstrap/role-policy administration; actual data resources and remote
state were created through `sub2api-integration-deployer`, assumed from the dedicated
EC2 instance profile. No unrelated existing IAM role was repurposed.

- Current EC2: `i-04225834295350402`, VPC `vpc-01b33479`.
- New data subnets: `172.31.128.0/24` in `us-west-2b`, `172.31.129.0/24` in `us-west-2a`.
- A dedicated route table contains only the VPC-local route; it has no IGW/NAT default.
- Database/cache ingress references only the supplied EC2 SG; cache replication uses
  the cache's own SG. Existing EC2 security-group rules are not broadened.
- RDS: `sub2api-it-postgres`, PostgreSQL `16.13`, `db.t4g.medium`, 20 GiB gp3,
  Multi-AZ, encrypted, private, managed master password, 7-day PITR, deletion protection.
- Valkey: `sub2api-it-valkey`, engine `7.2` (actual nodes `7.2.6`), two
  `cache.t4g.small` nodes across AZs, CMD, TLS required, ACL, `noeviction`, backups.
- The `sub2api-it-runtime` cache user is dedicated to this isolated test cache;
  `on ~* &* +@all -@admin` is an integration compatibility ACL, **not** a claim of
  production command/keyspace least privilege. Native VALKEY user group disables default.

## State and secrets

Private AES256-encrypted/versioned S3 state with TLS-only policy and Terraform S3
lockfile is used. Backend configuration is in the operator's private state directory,
not the repository. Plan/state data must not be printed, committed, or used as a
credential source for application runtime.

Valkey password input is `ephemeral` and uses provider `passwords_wo`; no usable cache
password is saved in Terraform state. Generated credentials are stored in AWS Secrets
Manager and materialized only into root-owned `0600` environment files on the test EC2.
RDS master is only used for one-time bootstrap: runtime uses `sub2api_it_runtime`,
separate from migrator/schema owner. Runtime cannot create tables or access
`security_secrets`; it can SELECT the migration manifest but cannot change it.

All operational plans, credentials, bootstrap recovery records, policies, and raw
verification logs are outside Git under `/root/.local/state/sub2api-cloud/aws-integration/`.
Do not copy those files into this directory. No OAuth accounts are imported in this stage.

## Validation

```sh
terraform fmt -check -recursive
terraform validate
# Every test uses mock_provider and plan only. Clear all real credential sources.
env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
  -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_EC2_METADATA_DISABLED=true \
  AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_CONFIG_FILE=/dev/null \
  terraform test
```

If initializing an isolated checkout only for validation, use `init -backend=false`.
Do not reinitialize an active deployed workspace away from its remote backend.
Five offline tests cover disabled deployment, root/wrong-account role rejection,
password constraints, and private encrypted RDS/Valkey topology. This is separate
from actual managed-service tests documented in the deployment report.

Terraform `validate`/mock tests are not a production deployment approval. Explicit
`deployment_enabled=true`, verified account/role/VPC/SG, and secure backend are required.
Use only the operator's approved external tfvars and ephemeral secret injection.
Never run apply from stored root credentials or create a second stack to bypass a failure.

## Costs and retention

AWS public on-demand prices queried on 2026-11-03 (local date), `us-west-2`:

| Component | Unit price | Approx 730-hour month |
| --- | --- | --- |
| RDS db.t4g.medium Multi-AZ (includes standby compute) | USD 0.129/hour | USD 94.17 |
| Two Valkey cache.t4g.small nodes | USD 0.0256/node-hour | USD 37.38 |
| 20 GiB Multi-AZ gp3 | USD 0.23/GiB-month | USD 4.60 |
| Base total | | **USD 136.15/month, about USD 4.48/day** |

Excludes existing EC2, Secrets Manager, backup excess, CPU credits, cross-AZ transfer,
logs, request charges, taxes, and discounts/credits. This is not a bill cap. No extra
NAT/ALB/ECS costs are introduced in this stage. Created resources remain billable
until deliberately decommissioned. Do not destroy without user approval and a data/
final-snapshot retention decision. `prevent_destroy` and RDS deletion protection are
intentional; final snapshot names need collision review before any later removal.

## Actual application test topology

All services run as non-root `sub2api-it` on the current EC2, not in ECS:

| systemd instance | loopback port |
| --- | --- |
| `sub2api-integration@control` | 18080 |
| `sub2api-integration@gateway-a` | 18081 |
| `sub2api-integration@gateway-b` | 18082 |
| `sub2api-integration@worker` | 18083 |

They use real RDS verify-full CA validation and Valkey TLS/ACL, no load balancer.
Runtime UID is blocked from EC2 IMDS (IPv4/IPv6) so it cannot assume the deployment
identity. systemd restricts filesystem writes to each role's data directory.
Only one maintenance worker/control is supported in this experimental stage.

See `../../docs/EC2_MANAGED_DATA_VALIDATION.zh-CN.md` for evidence, limitations,
post-failover verification and the explicit pause before authorized real model tests.
