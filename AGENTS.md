# Sub2API Cloud fork conventions

## Scope and provenance

- This is a local development fork of Sub2API, based on upstream commit `4a4a83ee5`.
- Keep upstream copyright, license, legal notices, and source attribution intact.
- Do not modify the sibling source repositories (`../sub2api`, `../CLIProxyAPI`, `../new-api`).
- Work on feature branches. Do not commit, push, or publish unless the user asks.
- Read `docs/CLOUD_ARCHITECTURE.zh-CN.md` and `docs/IMPLEMENTATION_STATUS.md` for decisions and current implementation boundaries.

## Architecture

- One inference gateway, one resource scheduler, one customer ledger. Do not embed or chain a second gateway or scheduler.
- Prefer existing Sub2API capabilities, then evaluate CLIProxyAPI/new-api for missing capabilities; implement new behavior only when reuse is unsuitable.
- CLIProxyAPI is MIT. Direct reuse of new-api/RelayKit/TokenKit code requires an explicit AGPL compatibility decision; do not silently copy it into this fork.
- Target AWS region: `us-west-2`. Target runtime: ECS Fargate, not Kubernetes.
- Database: RDS PostgreSQL Multi-AZ **DB instance**. Coordination/cache: node-based ElastiCache for Valkey, cluster mode disabled, Multi-AZ.
- PostgreSQL is authoritative for money, orders, credential versions, and durable work. Redis/Valkey is not a financial ledger.
- An implementation status must distinguish proposed, implemented, and verified capabilities. Unsupported cloud roles must fail closed rather than start the legacy all-in-one process under a different label.

## Safety and AWS operations

- Never put usable credentials, tokens, database passwords, or private keys in code, Markdown, Terraform variables/examples, logs, or Git.
- Read-only AWS discovery is permitted. Before creating/modifying billable or outward-facing AWS resources, review the target account, region, deploy role, plan, and cost scope with the user.
- Do not provision with AWS account root credentials. Use a dedicated deployment role with least privilege.
- Do not run `terraform apply`, `terraform destroy`, publish images, or change DNS without explicit authorization for that deployment.
- No network services with empty/example passwords or public database/cache access.
- Tools, scripts, and downloaded archives must be kept separate; validate official checksums before executing downloaded tooling.

## Backend and tests

- Preserve existing Go module paths until a module-path migration is explicitly planned.
- Follow existing Go style, `gofmt`, context cancellation, and structured errors.
- Financial transitions need durable idempotency and explicit pending/ambiguous outcomes. Never retry an external generation solely because a local database commit is ambiguous.
- Do not change previously applied SQL migrations. Add new forward migrations only.
- Runtime schema checks must perform no DDL, advisory-lock acquisition, seeding, or secret bootstrap.
- Use small connection pools; do not hold DB transactions while waiting for upstream generation.
- Default legacy behavior stays compatible unless a deliberate, documented opt-in selects stricter cloud behavior.
- Run focused tests for each change, plus backend build where possible. On this 2-vCPU/4-GiB machine prefer `GOMAXPROCS=2 go test -p=1 ...`.
- Infrastructure validation must be offline (`terraform init -backend=false`, `validate`, mock-provider tests); do not turn tests into AWS provisioning.
- Record exact commands and outcomes, including skipped or blocked verification.
