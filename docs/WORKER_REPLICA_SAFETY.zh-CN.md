# EC2 多进程维护 worker 安全边界

本文件描述已实现边界，不代表全部维护任务已支持 HA。当前部署仍为单 EC2 多进程，不依赖 ECS。

## 启用与领导权

- 默认 `runtime.multi_replica_workers=false`，保留原单 worker 行为。只有显式启用 experimental split worker 和此开关才使用下述保守任务子集。
- 一个 dedicated PostgreSQL session 持有二元 advisory lock `(1937072690, 1)`。构造与 schema check 不拿锁、不启动协程；生命周期 Start 后才竞争。未获锁的 worker 为 standby。
- 获锁后启动允许的任务，每秒检查同一 session 在 `pg_locks` 中仍持锁；单次检查/获取超时 5 秒。连接损失或查锁异常即取消任务上下文、停止并 join；该实例不会重新启动其单次生命周期任务。
- 正常停机先 cancel、逆序 join，再解锁并返还连接。调用者的 Stop 遵守传入超时，超时不意味着任务已退出，不会因此主动解锁。Stop 返回错误、无法证明 join 完成时保留 session 到进程退出。数据库会在 session 真正结束后释放锁；网络分区的检测时间不保证。
- **不是 exactly-once，也不是全局 generation fence。** 数据库断开可能先于旧进程察觉，故新旧进程短暂并存仍可能发生。外部调用不可由 advisory lock 撤回，未有独立任务持久化 claim 的危险任务不启动。允许任务以原子条件 SQL / 幂等操作处理重复。
- 每个被禁用任务都有启动日志 `maintenance task disabled in multi-replica mode`，包含任务名和原因；允许任务有 `maintenance task admitted for PostgreSQL coordination`；真正 leader 日志为 `maintenance PostgreSQL session leadership acquired`。仅服务健康端口成功不能证明其为 leader。

## 允许清单

|任务|重复安全与退出依据|限制|
|---|---|---|
|AccountExpiryService|UPDATE 在写入时校验可调度、自动暂停及到期条件；上下文取消与 WaitGroup join|不是 owner generation 写入 fence；调度 outbox 写入原有边界保持不变|
|IdempotencyCleanupService|按到期时间条件 DELETE，外层 DELETE 再次校验到期条件，防止候选快照过期后删除被续期记录；取消与 join|过期记录清理，不提供业务 exactly-once|
|SubscriptionExpiryService|只执行 active 且 expires_at 到期的原子条件 UPDATE；取消与 join|此模式明确关闭提醒邮件；未实现 durable send claim|
|UserMessageQueueCleanup|Lua 删除前重新检查 PTTL，只删除无 TTL 的异常孤儿锁；取消与 join|索引修复为辅助信息，非财务账本|
|AuthCacheInvalidationWorker|PG SKIP LOCKED 持久化 claim、UUID worker ownership 条件 ack/retry；Redis DEL/pubsub 重复安全；Stop 取消并 join 整批|重复失效通知允许；不是 exactly-once 消息；仅协调此后台 worker，不修改请求侧行为|
|ConcurrencySlotCleanup|Lua 在写入时重查 regular/live/wait，保留更新的索引分数及原 TTL；清理协程取消并 join|仅周期清理；split worker 不运行 legacy 跨进程 prefix 启动清扫；独立主节点 Redis/Valkey（CMD），不声明跨 key cluster 支持|

## 明确禁用清单

|任务|尚缺能力或审计发现|
|---|---|
|TokenRefreshService|所有 provider 刷新及 reconciliation 的持久化凭据版本 fence 尚未统一验证|
|CNProviderBalanceCheckService|外部余额探测及账号修正缺 durable attempt fence|
|BatchImageCleanupService|对象存储删除无独立 durable claim|
|DashboardAggregationService|定时回调及启动 backfill 没有完整 join|
|UsageCleanupService|已有 claim 但任务进度/恢复缺 owner generation，Stop 仅取消定时器未 join；CreateTask 另有请求触发 runOnce 路径|
|OpenAICodexVersionSyncService / ClaudeCodeVersionSyncService|外部查询后设置写入缺版本 fence|
|ProxyExpiryService|源代理 UPDATE 有快照条件，但备用代理链目标由旧快照决定，未对并发目标变更做同事务 fence|
|PaymentOrderExpiryService|支付 reconciliation 会调用外部平台，现有 TTL leader lock 不等于 durable attempt claim|
|SchedulerSnapshotService|pending/exact-ack 与生命周期取消已修复；共享账号/metadata、group 生命周期及 bucket lock 尚缺完整 leader-generation fence|
|OpsAggregationService / ChannelMonitorV2Aggregator|聚合/backfill 写入没有统一 owner generation|
|OpsAlertEvaluatorService / OpsScheduledReportService|邮件或其他外部通知没有 durable delivery claim|
|OpsCleanupService|cron Stop 三秒后放弃等待，且 Reload 可重建 cron，不能证明停止后无回调|
|GeminiCachedContentService|有 mutation lease，但恢复还会回滚/删除上游，旧 holder 外部操作不能撤销，部分删除不是 token 条件|
|ScheduledTestRunnerService / ChannelMonitorRunner|真实上游模型/代理测试没有逐次持久化 claim|
|BackupService|备份和外部归档没有 durable run claim|
|UserPlatformQuotaUsageFlusher|共享额度 flush 的原子 claim / recovery / join 未完成审计|
|未知新增 worker|默认禁用，必须显式审计加入允许清单|

Process-local / request-local / gateway-local / control-local 不由此 coordinator 迁移。其分类本身不证明重复安全，构造时协程和请求侧维护入口仍须分别审计。不要同时运行 legacy `all` 或未开启此开关的 worker 与 HA 子集并声称全局只有一个维护者。

## 验证与未验证

新增单元测试覆盖双 coordinator standby、旧任务 join 前禁止主动移交、接管、锁丢失取消、不重启旧实例、Stop 失败保留 session、调用方 Stop 超时、未知任务默认拒绝以及 gate 未注入 PG 时失败。SQL mock 覆盖 dedicated connection、standby 返还连接、查锁与异常解锁丢弃连接。这些不是网络分区证明，也不覆盖真实 SMTP、支付、OAuth、对象存储或模型副作用。真实 PostgreSQL 进程 kill/standby 接管 smoke 的命令和结果由本次集成验证记录单独报告；未运行的能力不得写为通过。无新增 schema migration，未应用数据库迁移。

## 后续加固（2026-11-03，仍有阻断）

- 调度 outbox 改为扫描待处理行、先释放 dedup、成功后只确认本批精确 ID。自增 ID 是分配顺序而非提交顺序，不再用 watermark 决定扫描/删除范围；watermark 仅单调诊断信息，支持超过 Lua 精确整数范围的 int64。
- SchedulerSnapshot 启动 bootstrap/poll/rebuild 使用 owned context，StopContext 取消并等待；metadata revision 修复不再启动脱离生命周期的协程。
- **SchedulerSnapshot 仍禁用**：bucket epoch 不能排序 PG leader generation，共享账号/metadata 写入和 group retire/reopen 尚未完整 fencing，bucket lock release 也还不是 owner 条件删除。
- TokenRefresh 的多副本防护模式强制 durable API，禁用陈旧快照账号缓存覆盖、无 fence 的状态修正和 privacy/enrichment 后动作；Start/Stop 串行化。五个平台名的 stub 测试证明歧义后不重放，但不是五个真实 provider 的端到端证明。其他 provider 内部副作用尚需审计，**仍不加入允许清单**。
- 并发清理仓储单元测试通过，确定性第二 Redis 客户端交错测试重复 20 次通过；这里的第二客户端使用 miniredis，不等同真实 ElastiCache 故障演练。Docker PostgreSQL/Redis 的 outbox 晚提交、同 key 再生产、snapshot replay 三项集成测试通过（10.775s）。
- 额外确认的 Token 阻断：AntigravityOAuthService.RefreshToken 内部仍有最多四次调用及不可取消的 sleep；外层一次 executor 的 stub 断言无法覆盖内部上游重试。必须先统一 provider 单次尝试/取消语义，再考虑放行。
- GeminiOAuthService.RefreshToken 也存在同类内部四次重试和 sleep；不是只修 Antigravity 就能放行所有 provider。
- 实际调用链只读审计进一步确认：Gemini account refresh 有 alternate-client fallback、项目发现/OnboardUser、tier 查询；Antigravity account refresh 在轮换后继续 LoadCodeAssist/onboarding。维护刷新应只返回新 token 和保留的本地 metadata，不能因后续补全失败丢失新凭据。还须限制刷新 POST 的重定向及底层 transport replay；只设置应用 retry=0 不足以声称一次网络发送。未据此放行任务。

### Provider 单次刷新收敛进行中

维护标记下新增独立 HTTP 请求策略：fresh HTTP/1、禁用 keepalive/redirect/application retry，避免改动 legacy 共享客户端；Gemini/Antigravity 禁止内部重试、替换 client 和轮换后的外部补全，凭据 merge 保留本地 metadata。具体 HTTP 客户端第一轮本机假 provider 测试通过：Antigravity0.004s，四类 repository 客户端0.045s；覆盖 success/503/malformed/disconnect/307/pre-cancel。显式 retry=0 最终版本及 read 后 cancel 新用例、服务层 legacy 对照测试仍待最终结果。未调用真实 provider，未放开 TokenRefresh HA。先前“内部四次重试”阻断描述适用于旧路径，维护路径改造尚未完成整体验证。

### 完整 scheduler fencing 的后续实现契约（尚未实现）

不能只加 Redis generation marker：Valkey rollback 可恢复旧 marker/lease；PG check 后 Redis write 不是跨存储原子 fence。拟采用 PG 持久 generation+随机 owner、按 generation 隔离的 scheduler namespace、PG 选择 active generation；新 leader fresh rebuild 后发布 namespace。每个 payload/metadata/revision/member chunk、activate/retire/reopen、lease acquire/release 都需同 Lua 内校验原始 capability，missing guard 不得重建。Bucket API 改 owner lease，group transition 同时校验 group lease；outbox dedup release/exact ack 需在 dedicated PG session 的事务内核验锁和 generation。Gateway 不得拿 worker 写权限；保留 DB fallback、停止 fallback 发布并将请求冷却状态拆为窄 overlay。需要新 forward migration、权限/wire、reader namespace/memo 接线和旧 leader/lease expiry/Redis rollback/partial pipeline 确定性交错测试。

此方案也不承诺 PG 断连瞬时撤销在飞 Redis 命令；旧 namespace 在新 namespace 发布前仍有窗口，读侧 authority 与恢复策略必须明确。当前未加迁移、未改 reader/namespace、未开放 SchedulerSnapshot，不能把设计当作实现。

### Bucket/group 所有权首批实现（验证中）

所有三类快照发布 API 现在必须带 bucket lease；随机 owner、条件释放，allocation、payload/metadata/revision chunk、member chunk、activation 同 Lua 内验证当前 lease；epoch capture 不再初始化 Redis。Gateway fallback 在 DB load 前尝试 lease，busy 仍可 DB 返回但不发布。Group retire/reopen 要求原 group lease，脚本第一步验证 owner。新增 expired holder 不能改 successor keyspace/TTL 和两类 chunk 间过期用例。此批没有 PG generation、命名空间、outbox generation ack 或 gateway overlay 改造，SchedulerSnapshot 继续禁用。正在跑 unit/integration/build/vet，尚不把实现当通过。

所有权首批最终验证通过：repository0.370s、service0.223s、Docker实际HTTP durable/outbox6.731s、handler0.027s、build/vet、六进程smoke-04。上述只证明owner lease slice与保守六任务模式，不证明完整PG leadership fencing。

### PG 持久代次基础（进行中，默认未接线）

新增未部署的 forward migration251：singleton generation/随机owner，active_scheduler_generation 独立可空。显式 NewFencedMaintenanceLockRepository 在 dedicated session 已持有 advisory lock 后分配代次；默认构造器不访问新表。分配事务先检查同session exclusive锁，commit不明不返回capability并丢弃连接。短SQL guard在同session事务内验证锁、FOR UPDATE匹配代次和owner后才执行callback；禁止跨provider/network等待。不可变fence经context传给任务，Check/guard/release串行。尚未接outbox、namespace、active发布和reader，不能据此放行SchedulerSnapshot。runtime无DDL/seed，未应用真实RDS。
