# 实施状态与验证记录

更新时间：2026-11-03。此文件记录当前仓库真实能力，不把目标方案当作已交付。

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

## 第三批：managed构造、配置收敛与预生产实进程验证

前两批本地提交：`9a450028a`、`1976b4282`。本批目标是把默认关闭的独立角色推进到可显式opt-in的预生产验证，不是完整生产上线。

- BillingCache/Subscription/Email、五种OAuth session、ContentModeration、TimingWheel、UsageRecord autoscaler及三类本地配置订阅实现managed构造和显式Start/Stop；旧New保持兼容。
- Concurrency/UMQ后台清理可cancel/join；worker启用时禁用legacy跨进程前缀启动扫槽，避免误删活跃gateway租约/等待计数。
- ChannelMonitorRunner每30秒从PG配置收敛，支持有界ReconcileOnce、变更/删除、错误保留、无变化不重启、Stop等待poll和在飞检测；暂限单维护worker，没有leader租约。
- 生命周期按producer→consumer→store停止，同阶段逆序，防止store先关；V2Aggregator Stop等待循环退出。
- `RUNTIME_ENABLE_SPLIT_ROLES=true`才允许gateway/control/worker，且必须check模式、标准计费和共享JWT/TOTP；默认继续拒绝。只有gateway启PromptAudit，worker不启动插件进程。
- `tools/smoke_cloud_roles.py`启动2 gateway+control+worker，真实PG＋临时密码Redis，验证路由/鉴权、维护scope、正常SIGTERM退出和活跃槽位保留。最终二进制增强烟测通过：Gateway各19个本地启动项、Control24、Worker47；TokenRefresh/SchedulerSnapshot/ChannelMonitor仅Worker，启动后其他Gateway槽位与等待计数保留。脚本只接受loopback且以_test结尾、无账号/monitor/payment provider的已迁移测试库；日志脱敏。
- 新增managed队列/会话/订阅/审核/定时器/分阶段stop测试、配置门禁测试；CI接入真实进程烟测，尚未在GitHub执行。
- 恢复后五pkg session测试通过、聚焦managed/监控测试通过；曾发现runner半接线签名及仓储slog导入编译错误并修复。完整service回归发现旧白名单审计零值队列路径被新门禁拦截，已区分managed/legacy并重测通过。最终service完整包148.169秒、config0.632秒、repository7.098秒、cmd/server0.036秒、migrate0.017秒、server0.107秒、routes10.657秒通过；unit监控/定时器/Stop回归0.558秒通过；相关go vet和server build通过。
- 真实PG只读session/受限InitEnt在最终代码通过（0.052秒），users/security_secrets仍为0。订阅Stop新增测试曾因假设远端Redis立即观察TCP关闭而失败：改为验证callback已join/晚消息不再执行，不将网络关闭传播当同步承诺；`go test ./internal/repository -run TestManaged -count=10` 通过（0.030秒），仓储/config/server再回归通过（7.062/0.630/0.019秒）。Wire重生成可再现，SHA256 `666106f3e2f44003881873a3655d4d3c7c4fa80914477c85485466e5ad393e05`。

本批本地实现和上述验证已完成；没有创建AWS资源、没有推送。AWS预生产部署仍需专用role/state/费用批准、runtime DB/Secrets、镜像/migrator入口、ECS services与ALB接线，不具备一键生产上线条件。

## 第四批：当前EC2＋真实AWS数据层（2026-11-03）

用户明确授权创建所需资源及合理配置IAM；后续又指定先完成当前任务，再开始已授权的两份OAuth/5.6 Terra真实调用。本阶段没有导入账户、没有模型或支付provider调用。

- 复用当前EC2/VPC，新增两个isolated数据子网、安全组、RDS PostgreSQL16.13 Multi-AZ DB instance（db.t4g.medium/20GiB gp3）及Valkey7.2.6跨AZ主从（2×cache.t4g.small/CMD/TLS/ACL/noeviction）；没有新增NAT/ALB/ECS。
- 创建专用host/deployer IAM，root仅用于IAM bootstrap/策略管理，真实data/state通过assumed integration角色创建；远端S3加密/版本化/lock已使用。
- 真实CA verify-full、TLS、独立schema owner/migrator/runtime账户；300迁移成功，Runtime不能DDL/读取security_secrets/更改manifest；动态Secret与环境文件不进Git。
- 四个systemd非root服务已在127.0.0.1:18080–18083运行，app UID的IPv4/IPv6 IMDS被阻断；未enable开机自动启动。
- 真实Valkey并发槽Lua/等待计数/Billing Lua/PubSub、RDS事务幂等、两个Gateway共享Key认证均通过；临时用户/Key/上游账户数最终为0。
- 实际Control登录、余额兑换、套餐兑换、重复兑换拒绝、Key创建幂等、Key停用传播至双Gateway通过；不是支付渠道验收。
- Valkey受控切换：约36.37秒观察稳定，001→002，哨兵保留；RDS切换AWS事件间隔24.785秒，提交行保留。RDS观察脚本因AZ元数据滞后被停止并修正，未重复触发故障；客户端精确中断时长未测。切换后data/business复验通过。
- 证书/DSN配置、仓储和离线定价回归/相关vet/build通过；最终binary逐个systemd重启健康，启动窗口无ERROR；Terraform离线5项mock plan通过、真实plan为No changes。
- 临时资源创建、failover和RDS masterSecret读取权限已移除，IAM模拟确认拒绝；保留元信息只读、精确集成Secret读取和专用state/lock。

完整资源/费用、命令与已知限制见 [EC2_MANAGED_DATA_VALIDATION.zh-CN.md](EC2_MANAGED_DATA_VALIDATION.zh-CN.md) 和 `infra/ec2-integration/README.md`。基础按需费用约136.15美元/月（不含已有EC2及其它费用），资源保留持续计费；未取得通知地址，未配置预算邮件告警。前端/ECS/ALB/PITR/真实模型与支付仍未验。

## 尚未实现，禁止按已完成生产部署

- 独立角色仅实验opt-in：共享Wire图/权限最小化、DI失败全资源回收、部分缓存janitor、Control OAuth多副本、维护worker多副本lease仍未完成。AWS下一步见 [AWS_PREPRODUCTION_CHECKLIST.zh-CN.md](AWS_PREPRODUCTION_CHECKLIST.zh-CN.md)。
- 跨供应商统一候选调度、quota domain、持久请求/attempt/价格快照模型。
- 全平台刷新 CAS/租约强化、KMS 动态凭据加密。
- 完整持久预留、结算终态、账务 outbox/SQS 和补偿。
- ECS Task definitions/services、ALB/域名/证书、发布和长连接排空控制。
- 真实上游协议、支付沙箱及云故障验收。

## 云部署授权与当前边界

- 用户已确认 `us-west-2`、RDS PostgreSQL Multi-AZ DB instance，并明确授权创建当前集成测试所需资源及合理IAM。
- 当前EC2＋真实RDS/Valkey已部署并验收限定范围；专用IAM角色、加密版本化远端state及Secret bootstrap已落实，临时高权限已收紧。
- `infra/aws` 的未来ECS foundation仍未apply；不因集成阶段授权擅自扩建NAT/ALB/ECS、改DNS、推镜像或销毁数据。
- 当前四服务只loopback访问；Control/Worker各一实例，尚无生产多活保证。预算告警、外部域名/证书、ECS应用部署和生产运行权限仍需后续。

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
| AWS 写入（前3批） | 当时未创建资源；第四批按新增明确授权创建EC2集成数据层，见上方；未推镜像/更改DNS/销毁 |

注意：本表前3批是历史验证记录，不能覆盖第四批新增真实RDS16/Valkey结果。首次离线config缺依赖、后续回归失败和修复均保留说明。仍未运行全仓所有测试、前端构建、真实上游/支付、ECS或PITR恢复，不宣称完整云原生/生产验收。
