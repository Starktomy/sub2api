# 实施状态与验证记录

更新时间：2026-11-02。此文件记录当前仓库真实能力，不把目标方案当作已交付。

完整目标方案：[CLOUD_ARCHITECTURE.zh-CN.md](CLOUD_ARCHITECTURE.zh-CN.md)。

## 已完成

- 创建独立本地 fork `/root/ai/sub2api-cloud`，分支 `feat/ecs-cloud-foundation`；upstream 保留为 `https://github.com/Wei-Shaw/sub2api.git`。
- 原始 `sub2api`、`CLIProxyAPI`、`new-api` 仓库未改动；用户已要求将首批实现提交到本地开发分支，未推送或发布。
- 用户指定后续推送目标 `https://github.com/Starktomy/sub2api`，已设置为 origin；原仓库为 upstream。
- 完整方案落盘，明确 ECS Fargate、us-west-2、RDS PostgreSQL Multi-AZ DB instance、ElastiCache Valkey 节点型 CMD。
- 只读检查 AWS 身份和实例 Region；当前为 root 身份，禁止用其创建云资源。
- 官方 Go 1.27.3 和 Terraform 1.17.1 已校验并安装到用户目录；本地 PostgreSQL 15.19 用于真实迁移/权限验证，只监听 localhost。

## 本批次已实现：P1 启动/迁移基础与 Terraform foundation

- 独立 `backend/cmd/migrate` CLI：apply/check、整体超时、信号取消、默认 verify-full TLS、凭据仅从环境读取；不启动 HTTP、Redis 或后台任务。
- `database.migration_mode=auto/check`；check 路径只 SELECT manifest，验证必需迁移/校验和并允许额外未来迁移；不执行 DDL、锁、secret bootstrap 或 simple-mode seed。
- 运行角色配置、环境变量/文件覆盖及 fail-closed；未实现的 gateway/control/worker 在 setup/DI 之前被拒绝，兼容 all。
- check 模式禁止安装向导和共享密钥自动生成，JWT/TOTP 必须显式配置。
- HTTP shutdown 预算配置 default90/max110 秒；不是完整 ECS 排空或 WebSocket 治理。
- `infra/aws` foundation：两 AZ 网络/每 AZ NAT、隔离数据子网、SG、RDS PostgreSQL 16 Multi-AZ、Valkey 7.2 CMD/TLS/noeviction/禁用预留 ACL、ECS 空集群、ECR、SQS/DLQ、KMS 和私有 S3。
- infrastructure 使用显式 Region/账号/部署角色/enable 门禁；默认阻断部署，不启动应用；Valkey 写入密码使用 ephemeral/write-only，不进入 state。
- 新增 `.github/workflows/cloud-foundation.yml`：真实 PG 迁移契约与离线 Terraform 验证。此工作流仅本地创建，尚未在 GitHub 执行。
- `.gitignore` 明确纳入架构文档和 fork 约定，避免原仓库规则将其静默忽略。

以下验证已完成：本地 PostgreSQL 15.19 fresh 300 个非空迁移、重复执行、只读 session、SELECT-only 角色的 InitEnt 初始化、CLI 特殊密码 quoting、真实进程门禁；聚焦测试与两个二进制 build；Terraform init/validate/7 个 mock 测试。PG16/RDS 实际云环境仍待验。

## 第二批：角色路由与显式生命周期

首批基础改造已提交为 `9a450028a`；未 push。第二批继续以下实际代码，不解除独立进程角色门禁：

- 按 all/gateway/control/worker 分离实际 HTTP 路由；all 保持原功能，worker 路由层只暴露 health，gateway 不挂登录/后台/支付/MCP/HTML。
- `RuntimeLifecycle` 明确任务作用域，构造时登记、图构建完后统一启动、仅逆序停止成功任务；处理重复Start/Stop、无效登记、部分失败和取消。
- 36 个 provider 启动迁移到 lifecycle（31 个wire.go登记＋5个其他provider）；本地请求闭包不直接搬到远端worker。
- server 显式启动 lifecycle；listener/startup 错误回到主协程，先执行 defer cleanup 再非0退出，不在后台 goroutine 直接 os.Exit。
- cleanup 先停插件/PromptAudit生产者、排空usage，再停managed任务和eager资源，最后 TimingWheel/Redis/DB；新登记任务不重复进入旧parallel cleanup。
- Wire 按源生成，补齐生成工具缺失的两个 go.sum 校验项，不修改 go.mod。
- 构造器隐式任务、ChannelMonitor本地回调传播、Pricing、Concurrency无Stop等仍阻塞生产角色启动，详见 [CLOUD_RUNTIME_MIGRATION.zh-CN.md](CLOUD_RUNTIME_MIGRATION.zh-CN.md)。

验证说明：首次Wire生成因缺少工具依赖校验项失败，补齐后已重新生成；中间一轮生成代码与cleanup参数编辑不同步导致server编译失败，重新生成后修复。一次路由编译因两组编译并行触发本机OOM，被内核终止，未进入测试断言。后续采用串行、`GOMEMLIMIT=1800MiB GOGC=60` 重跑，以下已通过：

- `go test -p=1 ./internal/server ./internal/server/routes -count=1`。
- `go test -p=1 -tags=unit ./internal/server ./internal/server/routes -count=1`（包含既有API契约）。
- `go test -p=1 ./internal/service -run 'TestRuntimeLifecycle|TestProvideTimingWheelService' -count=1`。
- `go test -p=1 ./cmd/server ./internal/config -count=1`。
- `go build -p=1 -o bin/server ./cmd/server`。

后续已完成：`go test -p=1 ./internal/service -count=1 -timeout=5m` 通过（147.444s）；`go vet -p=1 ./cmd/server ./internal/service ./internal/server ./internal/server/routes` 通过；再次运行 Wire 后 SHA256 保持 `6e13885e960111786db151bc32f7c47ef4261c8e9bcc76a5ab8b1e39cea153fc`，生成可再现。实际 worker 启动仍明确非0退出，未开放业务。前端/embed、完整 all 进程外部依赖启动、真实拆分角色/跨实例任务和AWS部署未验。

本批按用户要求进行第二个本地阶段提交，不推送。下一批优先处理 eager 构造器和可取消循环，再补持久配置变化传播，不提前解除角色门禁。

## 尚未实现，禁止按已完成部署

- gateway/control/worker 的真实依赖与后台任务隔离；当前仍只有 legacy all 角色可运行。
- 跨供应商统一候选调度、quota domain、持久请求/attempt/价格快照模型。
- 全平台刷新 CAS/租约强化、KMS 动态凭据加密。
- 完整持久预留、结算终态、账务 outbox/SQS 和补偿。
- ECS Task definitions/services、ALB/域名/证书、发布和长连接排空控制。
- 真实上游协议、支付沙箱及云故障验收。

## 云部署门禁

- 用户已确认 Region 为 `us-west-2`、数据库为 RDS PostgreSQL Multi-AZ DB instance。
- 尚未指定专用 deploy role、资源预算、域名/ACM 证书、生产账号/Secret bootstrap 策略。
- 不执行 `terraform apply`/`destroy`，不创建付费资源，不推镜像、不更改 DNS。
- Terraform state 需要加密远端 backend 和锁；本地 backend 仅用于离线验证。

## 验证记录

| 检查 | 当前结果 |
| --- | --- |
| 原仓库状态 | 初始 clean |
| AWS STS 身份 | 只读成功，发现 root 身份 |
| EC2 metadata Region | us-west-2，与用户选择一致 |
| Go 官方 checksum | 通过，1.27.3 linux/amd64 |
| Terraform 官方 checksum | 通过，1.17.1 linux/amd64 |
| 后端依赖 | `go mod download` 在 backend 模块成功；go.mod/go.sum 未改动 |
| 配置完整包 | `GOMAXPROCS=2 go test -p=1 ./internal/config -count=1` 通过 |
| schema/权限聚焦 | `go test -p=1 ./internal/repository -run 'Test(CheckMigrations\|InitializeSchema\|InitEntCheck\|InitEntPostgres)' -count=1` 携真实 PG DSN 通过 |
| 仓储完整单元回归 | `GOMAXPROCS=2 go test -p=1 ./internal/repository -count=1` 通过（无 integration tag；真实 PG 专项见上一行） |
| server 包 | `go test -p=1 ./cmd/server -count=1` 通过 |
| migrate CLI 包 | `go test -p=1 ./cmd/migrate -count=1` 通过，含真实特殊字符密码与 TLS handshake 契约 |
| 静态检查 | `go vet -p=1 ./cmd/migrate ./cmd/server ./internal/config ./internal/repository` 通过 |
| 二进制 | `go build -p=1 -o bin/migrate ./cmd/migrate` 与 `go build -p=1 -o bin/server ./cmd/server` 通过 |
| 真实 PostgreSQL | 本地 15.19，fresh apply 300 个非空迁移、重复 apply、check 均成功，users/security_secrets 均为0 |
| 只读数据库行为 | session 只读拒绝 DDL但 CheckMigrations 成功；仅 manifest SELECT 权限角色 InitEnt 成功且拒绝 secret INSERT |
| 实际程序启动 | 未实现 gateway role、check+setup、check 缺共享密钥均非0退出，未开放向导 |
| Terraform | 1.17.1 / aws provider 6.71.0，`fmt` / `init -backend=false` / `validate` / `test` 通过；7 passed，0 failed；全部 mock provider，无 AWS API |
| AWS 写入 | 未执行 apply/destroy、未创建资源、未推镜像/更改DNS |

注意：首次离线 config 测试曾因依赖缓存为空无法编译，之后依赖补齐后的真实包测试通过。未运行全仓所有测试、前端构建、真实上游/支付、PG16/RDS、ECS 故障和成本验收，不能据本表宣称完成云原生改造。
