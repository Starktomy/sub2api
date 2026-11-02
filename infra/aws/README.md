# AWS foundation — 仅验证，尚未部署

本目录是单层 sub2api 云原生改造的**基础设施 foundation**，不是可运行应用或完整部署流水线。Region 固定为 **`us-west-2`**。本次只运行格式检查、provider 安装、静态验证及 mock-provider 测试；**没有调用 AWS API，没有创建、更改或删除任何 AWS 资源，没有执行 `terraform apply`，没有提交或推送**。

当前只有 AWS root 访问且没有部署角色，因此**禁止真实 plan/apply**。`deployment_enabled` 默认 `false`，阻断型 precondition 同时要求提供目标账号内的已有部署角色。即使手动打开变量，也不代表取得部署授权，更不代表 remote state、预算或 runtime 已准备好。这里不创建部署角色、不保存凭据、不使用 AWS 数据源。

## 范围与默认拓扑

| 部分 | 本目录定义 |
| --- | --- |
| 网络 | 一 VPC，两 AZ；每 AZ 一个 public `/24`、一个 private task `/24`、一个 isolated data `/24`，共六个不重叠子网。默认 `10.42.0.0/16`，须另查现有网络重叠。 |
| 出网 | **每 AZ 一个 zonal NAT gateway + 一个 Elastic IP，共两个 NAT、两个公网 IPv4**。task 子网走本 AZ NAT；public 子网走 IGW；data 子网只保留 VPC-local route，不连 NAT/IGW。public 子网也不自动分配公网 IP。 |
| S3 网络 | task 路由表附 S3 gateway endpoint；其余 AWS API/外部 HTTPS 暂走 NAT，不提前铺设有小时费用的 interface endpoints。 |
| 安全组 | 默认 SG 清空；预留 task SG 无入站，只能 TCP 443 出网及到 DB 5432/cache 6379；DB 仅接受 task SG，cache 接受 task SG 及 cache 自身 6379 复制；DB/cache 不允许公网 CIDR。安全组不进行域名过滤，task 443 出网仍需后续审查。 |
| PostgreSQL | **RDS PostgreSQL 16 Multi-AZ DB instance**，一主一同步备用（备用不可读），不是 Aurora、Multi-AZ DB cluster 或 read replica。独立 SG、isolated 子网、禁止公网、gp3 加密、自管 KMS key，`rds.force_ssl=1`。 |
| DB 保护 | 14 天自动备份/PITR、保留自动备份、删除保护、final snapshot、`prevent_destroy`。初始 100 GiB，自动扩到初始值两倍；后者不等于费用封顶。 |
| DB 密钥 | `manage_master_user_password=true`，RDS/Secrets Manager 管理 `sub2api_bootstrap` 的 master 密码，自管 KMS key 加密；Terraform 只引用 secret ARN，从不读取值。**master 只用于受控 bootstrap/admin，绝不能用作 runtime。** |
| Valkey | **节点型 CMD（cluster mode disabled）**，Valkey 7.2 / `valkey7`，固定两节点（一 primary、一 replica），不同 AZ，Multi-AZ + automatic failover；TLS required、静态 KMS 加密、`maxmemory-policy=noeviction`、7 天快照、final snapshot、`prevent_destroy`。 |
| Valkey ACL | 原生 `engine=valkey` user group 自动禁用匿名 default 用户；仅预留 `off -@all` **禁用**用户，不授予应用访问。ACL/keyspace/命令清单及运行时外部 secret 集成完成前不能启用。 |
| ECS/ECR | 空 ECS cluster，只有 `FARGATE` capacity provider，Container Insights 开启；ECR tag immutable、禁止 force delete、push scan、只清理 14 天前的 untagged image，保留 tagged rollback image；应用/DB/cache 日志保留 30 天。 |
| 队列 | Standard SQS work queue + DLQ，SSE-SQS 加密、HTTPS-only、仅该 work queue 可 redrive 到 DLQ；work 留 4 天、DLQ 留 14 天、5 次失败进 DLQ、暂定 visibility 300 秒。没有生产者或消费者。 |
| 归档 | 私有 S3，四项 public-access-block、BucketOwnerEnforced、versioning、独立自管 KMS key + rotation、bucket key，拒绝 HTTP、非 KMS 或错误 key 写入。只清理未完成 multipart upload（7 天）；**不擅自删除 object/version，不假定 WORM/保留期**。 |

数据库/cache 共用 isolated data 子网层，但不同 SG 和端口边界。各 AZ 名称是账号相关映射；默认 `us-west-2a`、`us-west-2b` 不代表已用目标账号确认可用性。实例规格 `db.t4g.medium`、`cache.t4g.small` 是占位 sizing，不是已验证的容量/区域可售承诺。DB 只固定 major，Valkey 固定 7.2，允许 AWS 维护 minor 更新；真实部署前复核兼容性与支持周期。

## 故意不实现的应用部分

- 不定义 ECS task definition、service、autoscaling、ALB、域名、证书、task/execution IAM roles、CI/CD 发布、数据库迁移 job 或旧应用自启动。
- `runtime_ready` 输出固定 `false`；这是明确的未就绪状态，不会把旧单体启动冒充已拆分/已解耦。
- 不把 DB master secret 放入 task 环境/secret，不生成普通明文 `random_password`，不读取 Secrets Manager `secret_string`，不在 Terraform 中创建应用 DB 登录。
- 后续须在受控的一次性 bootstrap 中创建独立 migrator 与最小权限 runtime DB role，分别提供外部 Secrets Manager ARN 和 rotation 方案。runtime 对 master secret 的读取权限必须保持禁止。
- Valkey 现阶段 ACL 用户禁用。开启前须验证真实命令（包括 Lua、脚本/事务、PubSub）、key 前缀、TLS 校验及 failover 语义，细化 ACL；按实际客户端评估外部密码或 IAM 认证，不默认迁移已完成。
- SQS visibility timeout、消息协议、幂等/重试/DLQ 重放，以及 S3 写入和保留策略，依赖后端 runtime 合同，不代表业务已经使用这些资源。

## 无明文 state 的 Valkey 密码边界

Terraform **>= 1.11**，AWS provider 锁定 **6.71.0**。已通过本机 `terraform providers schema -json` 确认 `aws_elasticache_user.passwords_wo` 是 `string` 且 `write_only=true`，`passwords_wo_version` 是数值。`valkey_password` 同时标记 `ephemeral=true` 和 `sensitive=true`，仅送入 write-only 字段；普通 `passwords` / `auth_token` 均未使用。

真实输入必须来自部署环境中的**外部 secret 注入**，无默认值，不放 `.tfvars`、命令行字面量、仓库、普通 outputs、plan/state 或 shell history。测试里的 `mock-only-...` 明确是无效 fixture，不是应用凭据。`sensitive` 只会掩码，并不能阻止 state 保存；这里依赖的是 ephemeral + provider 的 write-only，而不是仅加 sensitive。

foundation 不创建持有该密码的 Secrets Manager secret version，也不读取既有 secret value。未来授权的部署执行器需要安全取得同一个外部 secret：一份注入本 Terraform write-only 输入，一份由 runtime 使用 secret ARN；这部分执行器/IAM/轮换流程不在本次范围。修改外部密码时递增 `valkey_password_version`。当前接口只有一个 write-only 密码，**未实现双密码无中断轮换**，启用 runtime 前需另定协调流程。ephemeral/write-only 不防止恶意调试日志或凭据泄漏；不要启用含敏感请求的日志、不要 echo 真实密码。

## 安全验证（无 AWS API）

在仓库根目录运行，或修改绝对路径。Terraform 已安装于 `/root/.local/bin/terraform`；CI 可用 PATH 上的 `terraform`。

```sh
export PATH="/root/.local/bin:$PATH"
terraform -chdir=infra/aws fmt -check -recursive
terraform -chdir=infra/aws init -backend=false -input=false
terraform -chdir=infra/aws validate -no-color

env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
  -u AWS_PROFILE -u AWS_DEFAULT_PROFILE \
  AWS_EC2_METADATA_DISABLED=true \
  AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_CONFIG_FILE=/dev/null \
  terraform -chdir=infra/aws test -no-color
```

- `init -backend=false` 仅联网下载校验 HashiCorp provider，不连接 AWS。已生成 `.terraform.lock.hcl`，应与源码一起保留；本地 `.terraform/`、state、plan、真实 tfvars 已忽略。
- 测试在 `tests/foundation.tftest.hcl`，所有 runs 使用同一个 `mock_provider "aws"`，没有 real-provider alias。六个反例验证部署门、缺失角色、错误账号角色、错误 Region、错误 account ID、弱密码；安全合同验证网络关联、SG、RDS、Valkey、ECR、SQS、S3/KMS。
- 安全合同 run 的 `command = apply` **只在 Terraform mock engine 中模拟创建/销毁**，用于获得 computed 属性并验证引用；它不是执行 `terraform apply`、不会访问 AWS、不会创建资源。其余 runs 是 mock plan。没有实际 IAM/网络/服务端行为测试。
- 已实测 Terraform 1.17.1 / AWS provider 6.71.0：fmt、init、validate 成功；**7 tests passed / 0 failed**。这是静态/schema/mock 证据，**不是云部署验收、不是 RDS 16 上的业务/迁移验证**。

## State 与账号/身份门禁

本目录刻意没有 remote backend 配置；Terraform 隐式 local backend **只用于当前 init/validate/mock 阶段，不能作为真实部署 state**。本次没有创建 state bucket、锁、部署角色或访问凭据。真实 backend 的账号/Region、KMS、版本/恢复、权限、审计、并发锁及 bootstrap 所有权仍待确认；应单独评审授权并完成 bootstrap/migration 后再谈真实 plan。

`allowed_account_ids` 是必填且只接受一个经人工核实的 12 位 ID，没有账号默认值。provider 配置同时设置该 allowlist 并仅通过提供的 `deployment_role_arn` assume role；阻断门校验 role 属于同一账号。mock 使用的 `123456789012` 不是用户账号。现在不能用 root 做探测或部署；先建立经过批准的最小权限非 root 身份/角色，并禁止 root 作日常部署主体。本地门禁可被代码修改，不替代 IAM/SCP/人工审批。

KMS key policy 中账号 `root` ARN 是标准的**账号级 IAM 授权委派主体**，并非允许/要求用 root 部署，也未给跨账号主体授权。后续 archive/task 权限只应授权 archive key，不能顺带获得 data key/master secret 的访问。这里不建立账户级策略或 registry 扫描所有权；ECR 使用 repository-scoped push scan，避免改动账号现有全局配置。

## 费用预期与仍待确认项

**尚未估价；没有预算金额、成本承诺或已部署资源。** 未来即使不运行 ECS task，一旦另行获批部署仍会产生固定费用，至少包括：

- **两个 NAT gateway 的小时费、NAT data processing、两个 Elastic IP/公网 IPv4 的小时费**；每 AZ NAT 选择优先 AZ 故障隔离，明显高于单 NAT。服务间/跨 AZ 流量还可能收费，same-AZ NAT 不消除所有跨 AZ 成本。
- RDS Multi-AZ 主/备用计算和存储、备份超额、Valkey 两节点和快照；规格/存储增长须用 `us-west-2` 当前定价单独核算。
- 两个自管 KMS key、KMS 请求、RDS 托管 master secret/Secrets Manager、CloudWatch Logs/Container Insights、ECR 存储/扫描、SQS 请求、S3 存储/版本与请求及传输。
- ECS control plane 空 cluster 不代表整套 foundation 免费；没有 task 所以没有本目录启动的 Fargate task 计算费。带版本归档不自动过期，存储会持续增长。

后续独立授权清单：

1. 正确 account ID、部署角色/IAM 边界、remote backend bootstrap、预算和标签 Owner/CostCenter。
2. 两 AZ 的服务可用性、CIDR 无冲突、RDS/Valkey 引擎与规格可售/容量、Region 定价、配额与支持周期。
3. RDS 16 预生产业务/迁移、PITR/restore/failover 实测；恢复的是新实例，PITR 不是就地回滚。Valkey 异步复制在 failover 时可能丢数据，noeviction 在内存耗尽时会拒绝写入；不能把它当财务/配额唯一 durable source of truth。
4. 最小权限 DB migrator/runtime 角色和外部 secrets；Valkey ACL、外部 secret 分发/rotation；区分 execution role 与 task role，禁止对 bootstrap master secret 授权 runtime。
5. runtime ready 证据、镜像 digest、任务资源/健康检查/优雅退出、ALB 入口和证书/域名、日志脱敏、告警/值班、出网边界。
6. 队列幂等、visibility/超时、DLQ 告警与重放；archive schema/写入 KMS headers、保留/恢复/WORM 决策。

`prevent_destroy` 是 Terraform 配置保护，不是 AWS 防删除权限；RDS 另有服务端删除保护。final snapshot 名是固定兜底，未来受控拆除/重建前必须检查 snapshot 重名并另行制定唯一命名、关闭保护和数据保留流程；本目录不提供 destroy 命令。

## 核对来源

- [RDS Multi-AZ DB instance（同步 standby、不可读）](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.MultiAZSingleStandby.html)
- [RDS PostgreSQL TLS / rds.force_ssl](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/PostgreSQL.Concepts.General.SSL.html)
- [AWS provider 6.71.0 DB instance](https://registry.terraform.io/providers/hashicorp/aws/6.71.0/docs/resources/db_instance)
- [Valkey 原生 RBAC / 自动禁用 default user](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Clusters.RBAC.html)
- [Valkey 参数族 / noeviction](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/ParameterGroups.Engine.html)
- [Valkey Multi-AZ / 异步复制限制](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/AutoFailover.html)
- [AWS provider 6.71.0 ElastiCache user/write-only](https://registry.terraform.io/providers/hashicorp/aws/6.71.0/docs/resources/elasticache_user)
- [AWS provider 6.71.0 replication group](https://registry.terraform.io/providers/hashicorp/aws/6.71.0/docs/resources/elasticache_replication_group)
- [Terraform mock provider 测试](https://developer.hashicorp.com/terraform/language/tests/mocking)
- [NAT 定价项](https://aws.amazon.com/vpc/pricing/)
