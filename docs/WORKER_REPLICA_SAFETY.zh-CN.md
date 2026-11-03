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
|ConcurrencySlotCleanup|已有取消及 join，但索引 reconcile 为读负载后无条件 ZADD/ZREM，可能覆盖新活跃索引；不能把幂等误当无陈旧写|
|SchedulerSnapshotService|已有 bucket write token；整个后台 outbox cursor、ack、全量 rebuild 的 owner generation 和取消尚未完整验证|
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
