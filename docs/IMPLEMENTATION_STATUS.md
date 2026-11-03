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

## 第五批：授权 OAuth 真实调用首轮（2026-11-03）

- 第四批已本地提交 `7561bccb5`；上下文摘要提交 `9bd33c364`。未推送。
- 用户明确阅读并接受部署与运营合规承诺 v2026.06.10，授权为隔离测试环境记录确认；通过真实管理员登录和官方合规接口完成，不绕过门禁。
- `tools/verify_oauth_live.py` 经官方 Codex session 导入接口接入两份独立 OAuth 账户；只传认证必要字段，不导入原文件中的密码、电话、2FA 等无关字段。
- 首轮仅2次生成HTTP请求：账户1经Gateway A非流式、账户2经Gateway B流式；均HTTP200/completed，响应声明 `gpt-5.6-terra`，流式收到文本delta。
- 每次31 input/5 output token；RDS各落一条usage，分别关联正确账户。客户余额从1降到0.99975600，合计0.000244美元；这是本地配置价格，不代表OAuth上游实际成本。
- 测试后两账户status=disabled/schedulable=false、两Key inactive、两用户disabled，保留用量和账务记录。真实SQL核对通过；四服务health正常。
- 对本轮服务journal和audit_logs检查所供access/refresh/id token、密码等长敏感值的精确匹配均为0；不等同于全面日志安全审计。
- 私有证据：`/root/.local/state/sub2api-cloud/aws-integration/oauth-smoke-20261103-01/result.json`；不提交原始凭据或运行态配置。
- 本轮尚未验证工具调用、会话亲和、同一池双账号调度/回退、上游刷新、在飞故障恢复、支付或ECS。两账户分别置于独立测试组，因此不能据此声称统一池调度已验收。
- 运行后对工具的usage证据序列化进一步收敛到3个token计数字段；该小改动仅语法检查，不为重验脚本再消耗模型请求。

## 第六批：同池/亲和/工具/传输回退/真实刷新（2026-11-03）

- 用户明确要求验证以上能力；本批实际5次生成、1次官方串行刷新。
- 同组同Key两账户候选选择、跨Gateway同session在优先级反转后仍亲和账户1、真实echo工具参数与完整回放结果续接均通过。
- 停调账户1后同session落账户2，验证发送前过滤；再给账户2配置关闭端口代理，同一请求日志确认账户2传输失败后重选账户1，最终usage归账户1，区分于仅预过滤。
- 池脚本最初journal时间格式导致证据采集失败并停测；原始passed=false保留。独立修正查询保存同请求回退证据，不重复4次调用；脚本修正只做语法验证。
- 随后独立工具执行一次账户1官方刷新，access/refresh token均实际变化并持久化，成功响应未包含新token；刷新后生成与usage成功。
- 本批5条usage本地客户费用共0.001070美元，不代表OAuth上游成本。最终两账户disabled/不可调度/无代理、原组恢复，测试Key/user停用、故障代理inactive；四服务健康。
- 当前凭据在本轮audit/journal精确匹配为0。账户1新refresh token在DB及root私有恢复文件，原account.test未覆盖，不可用旧文件回滚。
- unit标签令牌提供器/调度器测试通过0.303秒；未带标签那次no tests to run不计覆盖。管理员手动刷新缺统一CAS，不据单次成功宣称并发刷新安全。
- 详见 [OAUTH_POOL_VALIDATION.zh-CN.md](OAUTH_POOL_VALIDATION.zh-CN.md)。未测真实429、在飞/部分流恢复、WS、并发刷新或跨平台统一调度。

## 第七批：刷新协调与信封加密基础（代码阶段，未部署）

- 两个普通管理员OpenAI刷新入口接入共享OAuthRefreshAPI，不再在刷新后无条件UpdateAccount；锁占用返回409，缺少协调器/持久结果返回503。
- OpenAI刷新成功按完整旧credentials和proxy做数据库条件写入，与scheduler outbox同一SQL原子提交；拒绝旧结果覆盖并发凭据变更。成功后重读持久状态，不恢复并发停用状态。Grok原有严格平台/type边界保留。
- Valkey统一刷新路径使用随机owner租约及Lua比较删除，失败停止刷新；尝试超时限制在租约内。旧缓存接口/其他legacy刷新路径仍存在，不宣称所有入口已完成租约治理；Valkey主从复制也不提供严格分布式互斥保证。
- 手动刷新重读后若凭据已变化，不再次消费旧refresh token。没有持久CAS能力时在调用上游前拒绝。
- 受限管理员OpenAI刷新因旧ScopedAdminWrite持有事务，当前安全拒绝（503），不绕过权限；仍需实现事务外授权与条件提交的完整服务路径。
- 新增credentialcrypto纯Go基础包：整份JSON AES-256-GCM、每文档独立DEK、账号绑定AAD/KMS context、严格版本/大小校验、失败不回退明文。只有抽象KMS接口，**未接真实KMS、数据库、配置开关、迁移或运行时**，不能宣称现有凭据已加密。
- Wire已重新生成。加密包test/vet通过；仓储条件写入与lease单测通过0.041秒；管理员聚焦unit通过0.058秒。服务层聚焦unit（OAuthRefreshAPI/OpenAI CAS/owner lease/manual snapshot）通过0.159秒；`go build -p=1 -o bin/server ./cmd/server`通过，未安装或启动新binary。
- PostgreSQL integration标签测试编译成功但Docker缺失而跳过，未真实执行SQL CAS。本轮没有调用真实OAuth、轮换token、应用schema变更或创建AWS资源。
- 中断恢复后发现四集成systemd服务均inactive（此前未enable自启）；没有启动或更新已安装binary，不能沿用上批健康结论。并行编译造成swap压力，相关测试被主动停止后转串行，不记为测试断言失败。
- 条件写入接口尚未比较expected platform/type（两种合格类型间并发切换仍有边界），未使用数据库递增fencing版本；lease与JSON CAS不能代替全平台持久刷新状态机、歧义提交恢复。

## 第八批：真实CAS、事务外授权与持久fencing（已验证代码，未部署）

- 第七批中的Docker缺失、受限刷新拒绝、平台/type和持久fencing缺口已由本批后续实现替代；历史记录保留，不代表当前仍缺同一代码能力。
- 真实RDS verify-full/TLS回滚夹具通过：CAS成功、旧凭据/代理不匹配拒绝、保留停用状态、outbox失败时凭据回滚。测试0.161秒，未写真实账户、无DDL。
- 受限管理密钥刷新走事务外授权流程：上游调用前短事务校验，Begin/Complete再次锁定并校验实时key/creator/policy/account/有效group membership；密钥轮换或撤销、策略修改、分组解绑、创建者停用会拒绝提交。全权自定义管理Key也校验；专用OpenAI受限路由仍未授予，只开放原有通用accounts.refresh权限路径。
- 新迁移250_oauth_refresh_state.sql：仅generation、随机owner、pending/completed/ambiguous、输入/输出指纹和租约元信息，不另存token。完整平台/type/credentials/proxy快照在成功提交时比较；账号凭据、completed和scheduler outbox原子提交。
- split角色Wire自动强制持久刷新，配置文件role也不能绕过；legacy all可用OAUTH_REFRESH_DURABLE_ENABLED=true启用。带管理Key授权的刷新强制持久路径，不能落回legacy CAS。缺schema在调用上游前失败。
- pending提交后才调用OAuth；过期pending不自动接管。上游结果不明隔离；提交结果不明仅重试同owner/generation、同输出的本地完成操作，不再调用上游，重复完成不重复outbox。
- 显式ResolveOAuthRefreshAmbiguous需inactive/不可调度、匹配generation和完整账号快照、非空且已变化的refresh token，递增generation、拒绝旧holder迟到提交；恢复前仍必须停旧holder并重新授权。不能靠改metadata或proxy解除隔离。
- 尚无加密恢复spool：若上游已轮换但进程在持久化前丢失响应，不能重建token；安全行为是隔离并重新授权，不宣称外部exactly-once。尚无公开恢复HTTP/CLI；恢复方法供后续受控运维入口使用。
- Docker现在可用29.9.2；PG18.1+Redis8.4临时容器实际执行CAS/授权撤销/状态机/显式恢复，最终通过5.618秒；最初套件外层事务导致嵌套事务/FK失败，改为容器非事务client后通过，不改生产事务保证。
- service聚焦unit0.119秒、repository聚焦0.059秒、handler最终0.025秒通过；Wire/build/相关vet通过。新SQL迁移只在临时Docker数据库执行，**未应用RDS、未安装/启动新binary**，四服务仍inactive，无真实OAuth刷新或模型调用。
- 按用户要求分批commit/push：独立加密基础包已提交c80c6110f；其余刷新状态机/权限代码及报告随后单独提交。不合并主分支。

## 第九批：当前EC2多Control与保守维护主备（部分任务完成）

- 用户要求继续当前EC2测试，不部署ECS、不用Spot。保持一个代码库，新增显式runtime.multi_replica_workers门禁，默认false且只允许experimental worker。
- split角色的Claude/OpenAI/Gemini/Antigravity/Grok上游OAuth session接共享Redis TTL后端；写失败明确报错、无内存fallback。回调验证后原子领取/删除再发上游，失败兑换也消费session；legacy内存模式同时改为单次消费语义。Redis session含PKCE verifier JSON，依赖TLS/ACL，不是应用层加密。
- 维护主备使用专用PG session advisory lock；构造无抢锁，standby等待，leader丢锁取消并join，旧实例不重启，join失败不主动释放session。网络分区不是绝对fencing；因此只运行审计白名单。
- 当前白名单5类：账号过期、幂等记录清理、订阅过期（禁邮件提醒）、等待队列孤儿清理、鉴权缓存失效outbox。幂等清理外层DELETE补到期条件，防陈旧候选误删续期记录。其余token刷新/调度快照/模型测试/通知/支付/备份等明确禁用并日志列原因，详见WORKER_REPLICA_SAFETY.zh-CN.md。**没有完成全部后台任务多副本化**。
- 六进程实机测试：同一EC2启动2Gateway+2Control+2Worker，独立本地PG15测试库+临时密码Redis。全部health/角色路由通过；只有一个初始leader；SIGKILL后standby获锁并实际暂停过期账号夹具；Control A HTTP创建OAuth session，B验证错误state（证明已找到session），B重启后仍可读。测试结束所有进程退出，provider/model/payment调用=0。
- 共享session六pkg unit通过；维护service聚焦0.196秒、repo0.100秒；配置完整包1.675秒、Wire生成/server+migrate build/相关vet通过。最终假上游跨service成功回调/12并发单次兑换/重建实例/TTL/故障测试通过0.889秒；维护尾改动回归通过0.108秒。广范围service unit编译曾因资源协调终止，不计通过。
- 首轮六进程smoke仅接管锁，通过后补真实过期任务行为并再跑通过。私有脱敏日志在/root/.local/state/sub2api-cloud/replica-test-f0b2bad6/smoke-02/；测试数据用专用role/DB，没有改真实AWS账号或RDS schema。
- 登录侧OIDC/WeChat/LinuxDo等初始state/verifier依赖浏览器cookie，不是进程map；pending identity为PG、passkey为Redis。未做这些登录流全面replay/隐私审计，不把目标搜索当全面Control验收。
- 原systemd集成部署未更新，仍inactive；本轮是临时本机多进程测试，未建立常驻六服务、未创建AWS资源。

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

## 后台多副本第二轮加固（2026-11-03）

- 周期 ConcurrencySlotCleanup 已通过原子 Lua 陈旧写修复，加入 PostgreSQL 维护协调允许清单（共六项）；split worker 仍不执行 legacy prefix startup sweep。验证边界见 WORKER_REPLICA_SAFETY.zh-CN.md。
- SchedulerSnapshot 已修复 outbox ID 分配/提交乱序丢事件、精确确认、取消/join；仍缺 leader-generation 全路径 fencing，因此 HA 模式继续禁止启动。
- TokenRefresh 新增 durable-only 防护、禁止无 fence 后动作；平台 stub 歧义隔离测试通过（service 0.113s），真实 provider 内部路径尚未完成审计，继续禁用。
- Docker PG18.1/Redis8.4 outbox 三项集成测试通过（10.775s）。测试启动出现一次 ryuk handshake reset 警告，但数据库/Redis正常就绪，三项实际断言均执行并通过。
- 新一轮综合回归命令首次在仓库根目录失败（cannot find main module），没有执行断言；已切换 backend 模块重跑。无 AWS 资源变更、真实模型/刷新调用或永久服务更新。

本轮已执行的聚焦命令（均在 backend 下，GOMAXPROCS=2）：

```sh
go test -p=1 -tags=unit ./internal/service -run 'TestTokenRefreshMaintenance|TestOAuthRefreshDurable' -count=1 -timeout=3m
# PASS 0.113s
go test -p=1 -tags=integration ./internal/repository -run 'TestSchedulerOutboxLateLowerIDRemainsPending|TestSchedulerOutbox_ListPendingAndReleaseDedup_AllowsSameKeyWhileEventInFlight|TestSchedulerSnapshotOutboxReplay' -count=1 -v
# PASS 10.775s，真实 disposable Docker PG/Redis
go test -p=1 ./internal/repository -run 'TestConcurrencyIndex|TestLiveLease' -count=20
# PASS 1.681s，miniredis 确定性交错，不是 AWS failover
```

综合 service 编译首次重跑触发严重 swap（编译进程 RSS 约 3.2GiB、swap 约 1.6GiB，未进入断言），主动终止（exit 144）。后续串行验证使用 `GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20`，覆盖 scheduler/service/cleanup、仓储回归、build、vet、六进程 smoke；最终结果另附，不将启动命令等同通过。

最新综合回归在进入断言后失败：`TestSchedulerSnapshotServiceFullRebuildCoalescesConcurrentRequestsIntoTrailingRun` 的直接零值构造未初始化新 runCtx，`coalesceFullRebuild` panic（service 0.659s）。已将两项 rebuild coalescing 测试改为正式构造器并注册 Stop 清理，保持生产取消检查，不通过删掉检查掩盖问题；正在重跑。该失败中断了后续仓储/build/vet/smoke，不能记为已执行。

修复后聚焦回归：service 0.707s PASS（scheduler snapshot/outbox/rebuild、maintenance、runtime lifecycle、token maintenance/durable、concurrency cleanup），repository 0.918s PASS（scheduler、concurrency index、live lease）。使用上述低内存串行参数；build/vet/六进程 smoke 结果待后续记录。

最终验证通过（2026-11-03）：上述串行链 exit 0；`go build -p=1 -o bin/server ./cmd/server`、`go vet -p=1 ./internal/service ./internal/repository ./cmd/server` 均通过。`python3 -I tools/smoke_cloud_roles.py --binary backend/bin/server --multi-replica --keep-logs <private-new-directory>`（DSN 仅环境传入，实际由私有 launcher 调用）通过：2 gateway＋2 control＋2 worker 路由隔离；两 worker 均接纳原子并发清理；kill worker-a 后 worker-b 获锁并实际暂停过期 fixture；旧索引分数已过期的活跃槽位与等待计数保持；Control A OAuth session 在 B 及 B 重启后可查询，provider_calls=0。日志位于私有 `replica-test-f0b2bad6/smoke-03`；临时角色进程全部退出。未部署真实 AWS 或修改永久 systemd。

## Provider 单次维护刷新（2026-11-03）

已实现 shared refreshpolicy 标记，四类 req 客户端维护专用 HTTP/1/fresh connection/no redirect/no application retry，Antigravity clone transport 同样隔离；不修改 legacy 客户端。Gemini/Antigravity 维护路径一次 refresh、无 alternate client、无轮换后 enrichment，merge 保留既有元数据。TokenRefresh HA 仍禁用，尚缺 actual-service→durable-state→真实HTTP fake 的全链路接管验证等准入审计。

验证（backend 下，GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20）：
- `go test -p=1 -tags=unit ./internal/service -run 'Test(Gemini|Antigravity)(MaintenanceRefresh|LegacyRefresh)|TestTokenRefreshMaintenance|TestOAuthRefreshDurable' -count=1 -timeout=3m` PASS 3.335s。
- `go test -p=1 ./internal/pkg/antigravity ./internal/repository -run 'TestMaintenanceRefreshSingleRequest|TestMaintenanceConcreteOAuthClientsSingleRequest' -count=1 -timeout=3m` PASS 0.005s/0.048s；server build PASS。
- 最终新增429/308/read后cancel/legacy-client隔离用例重复3次：`go test -p=1 ./internal/pkg/antigravity ./internal/repository -run 'TestMaintenanceRefreshSingleRequest|TestMaintenanceConcreteOAuthClientsSingleRequest|TestMaintenanceRefreshClientDoesNotMutateLegacy' -count=3 -timeout=3m` PASS 0.019s/0.169s。
- refreshpolicy 包 PASS0.003s；`go vet -p=1 ./internal/pkg/refreshpolicy ./internal/pkg/antigravity ./internal/repository ./internal/service` PASS。

均为loopback假provider/假client，没有真实OAuth刷新、模型调用、AWS部署或schema变更。完整scheduler fencing仍未实现，下一批先bucket/group owner-safe slice，不解除任务门禁。

另新增 `TestAccountRepoSuite/TestOAuthRefreshConcreteHTTPDurableNoReplay`，拟覆盖真实 disposable PG durable barrier→实际 OpenAI service/refresher→具体 HTTP client→loopback fake provider，success/disconnect 后重建协调器不重放。第一次命令 `go test -p=1 -tags=integration ./internal/repository -run 'TestAccountRepoSuite/TestOAuthRefreshConcreteHTTPDurableNoReplay' -count=1 -timeout=4m -v` 在并发进行的 scheduler lease API 迁移中编译失败（undefined SchedulerBucketLease），没有进入断言；等待接口稳定后重跑，不记录为测试通过。

TLS补充回归发现严格维护的 Chrome preset 即使 ForceHTTP1，ALPN仍协商h2，导致服务端读到HTTP/1前言后EOF。`TestMaintenanceRefreshForcesHTTP1OverTLS/impersonated`失败（仓储0.364s），中断后续链。已在维护专用ClientHello限定ALPN=http/1.1并移除h2 application settings，legacy客户端不变；重跑中，不删除失败用例。

第二种共享自定义ClientHello spec尝试仍在impersonated TLS用例失败（local tls internal error，仓储0.372s）；已改成每连接独立uTLS握手状态修改ALPN，先单独验证TLS，不将未通过修复写作完成。两轮仓储输出未显示lease断言失败，但整条链均失败，后续service/integration/build/vet尚未执行。

每连接uTLS版本首次单测失败为callback收到hostname却按host:port解析；兼容hostname后TLS单项 PASS0.034s（plain/impersonated，两次请求，h2-enabled loopback）。最终完整链已重新启动。此前TLS失败均保留记录，未以绕过TLS校验的生产配置修复；自签证书跳过校验仅存在于本地测试客户端。

所有权首批当前验证：repository unit PASS0.370s；service聚焦独立重跑PASS0.223s。原后台service编译残留主动终止exit144后，仅运行剩余链。integration-tag先因scheduler_cache_integration_test残留redis未使用import编译失败，已移除重跑；无断言执行。build/vet/smoke-04仍待最终结果。

真实 Docker PG/Redis 集成最终PASS6.731s：`TestAccountRepoSuite/TestOAuthRefreshConcreteHTTPDurableNoReplay` success/disconnect 两项均执行，重建协调器后上游计数仍1；`TestSchedulerSnapshotOutboxReplay`及`TestSchedulerOutboxLateLowerIDRemainsPending`通过。不是stub-only覆盖，不使用真实provider。handler/build/vet/smoke-04仍待结果。

最终剩余链exit0：Docker6.731s、handler warmup0.027s、server build、相关vet均PASS；smoke-04 passed=true，kill worker-a→worker-b实际暂停过期fixture，live_slot_and_wait_preserved=true，Control A session在B及重启后可见，provider_calls=0，临时进程退出。结合此前repository unit0.370s/service0.223s，owner-lease首批可提交。没有PG generation/namespace/outbox generation fencing实现，仍禁用SchedulerSnapshot/TokenRefresh HA。无迁移或AWS部署。

## PG持久领导代次基础（2026-11-03，opt-in未接生产wire）

新增migration251，仅迁移seed且幂等不重置代次；runtime没有DDL/seed。显式fenced构造器持session advisory lock后分配PG generation+随机owner，commit不明丢弃连接不启动任务；默认六任务模式不访问新表。不可变fence传至任务context，短SQL guard在同session事务内验exclusive锁、FOR UPDATE匹配owner/generation；Check/guard/release串行，不能以PG preflight+Redis write冒充原子fence。outbox/namespace/active发布/reader尚未接入，SchedulerSnapshot继续禁用。

验证：`go test -p=1 ./internal/repository -run TestMaintenance -count=1 -timeout=3m` PASS0.086s；service同regex PASS0.052s；`go test -p=1 -tags=integration ./internal/repository -run TestMaintenanceGeneration -count=1 -timeout=4m -v` Docker PG实际断言PASS5.852s（递增、伪造owner、错误session、失锁、旧代次重获锁仍拒绝、active不发布）。后补session测试/build/vet结果待最终记录。新迁移未应用真实RDS；新二进制check要求数据库具备新迁移，禁止直接替换永久服务。

最终PG基础链exit0，server build通过；后补 `go test -p=1 ./internal/repository -run 'TestMaintenance|TestAllocateMaintenance|TestWithMaintenance|TestFencedMaintenance' -count=1 -timeout=3m` 与 repository/service vet通过。此批没有运行新版六进程（新增migration251未同步到旧私有fixture），Docker fresh迁移实际验证已通过。默认运行仍使用旧构造器，未启用新能力。

## Session-bound scheduler outbox（2026-11-03，验证中）

新增显式adapter绑定仓储maintenance session：dedup release/pending扫描、精确ID ack均在同session.withGeneration事务验exclusive锁和row owner/generation；commit失败不返回events。旧pool仓储只重用scanner，默认wire不变。RequiresMaintenanceFence任务在legacy或无coordinator时拒绝，不允许静默降级。仓储聚焦PASS0.042s；service/Docker/build/vet尚在运行。Redis namespace/active-reader未实现、SchedulerSnapshot继续禁用，未部署AWS。

Session-bound outbox最终链PASS：repository0.042s、service0.098s、Docker PG generation+fenced outbox5.948s、server build/vet均通过；最终继承capability拒绝用例另行重跑通过。outbox未接默认wire、Redis namespace/reader未实现，仍不开放SchedulerSnapshot。后续用户要求修复CI并准备多机器镜像，先修CI再做本地image/role/migrator测试，不发布ECR/创建新AWS资源。

## CI修复与多机镜像准备（2026-11-03，进行中）

已定位fork CI：Cloud foundation health-cmd单引号导致runner docker参数拆分（迁移前失败），改double quote；CI integration因committing夹具泄漏污染列表/唯一性断言，harness新增迁移基线snapshot+checked cleanup，仅disposable容器，保留schema_migrations/atlas_schema_revisions/maintenance_generation且不重置sequence；scheduler-neutral outbox断言改前后差值。已修正lint checked assertions/Close/Rollback、unused、deprecated字段；callback-drain mutex保留并窄范围说明。

精确golangci-lint v2.14.0官方包checksum验证通过。首轮发现新增测试等9项剩余问题，已修正，最终lint仍待结果。完整 `GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 go test -p=1 -tags=integration -count=1 ./...` PASS（repository41.127s、service149.757s、handler39.838s）。最终unit/image链仍运行中，不能写CI已绿。最初镜像预构建与集成排队竞态并发编译，已主动停止镜像编译exit144，后续使用单条串行链。

新增headless cloud Dockerfile（server+migrate）、最小build context脚本、外部配置compose（非root10001、readonly、drop caps、no-new-privileges、migrator默认check）、离线contract和六容器internal network smoke脚本。静态contract、bash/Python语法、compose config --quiet通过；实际镜像构建/运行仍待结果。没有registry push、新AWS资源或真实跨机器验证，arm64未验。见EC2_MULTI_HOST_IMAGES.zh-CN.md。

CI修复最终本机验收：golangci-lint v2.14.0 0 issues，完整unit exit0（repository10.110s/service251.324s），完整integration exit0。CI修复3f39bed33已push，远端CI41597223057与Cloud foundation41597223046运行中，Security Scan通过；不能写远端CI已绿。

headless amd64 prebuilt镜像构建及network-none契约通过，local tag sub2api-cloud:local-amd64。六容器烟测前3次失败（binding null TypeError/等待publication超时；独立迁移均成功），修为internal network容器内HTTP探针后image-smoke-04通过：6角色、独立migrator、杀主接管实际暂停fixture、活跃槽位/等待计数保留，临时资源清理，provider_calls=0。没有真实多机/TLS/arm64/UI验证、registry发布或AWS部署。
