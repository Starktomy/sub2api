# Runtime 角色拆分：生命周期迁移记录

本文件区分**已接线的模块能力**与**允许部署的进程角色**。角色路由/任务作用域测试通过，不等于完整 Gateway/Control/Worker 已可生产运行。

## 1. 提交基线

首批 ECS foundation / 独立 schema 工具已按用户要求提交为 `9a450028a`。origin 为 `https://github.com/Starktomy/sub2api.git`，没有 push。

本批继续：HTTP 路由隔离、显式 RuntimeLifecycle、Wire 接线及回归测试。未创建 AWS 资源。

## 2. 显式任务生命周期

新增 `service.RuntimeLifecycle`：

- Wire provider 只登记任务，不立即调用这些服务的 Start。
- 依赖图完整构造之后，server 显式调用 Lifecycle.Start。
- 任务声明 worker / request-local / gateway-local / control-local / process-local 归属。
- 默认 all 执行全部已登记任务，保留单体功能。
- Start/Stop 串行互斥，防重复；只停止成功启动的任务，按依赖登记的逆序停止。
- 重复名称、未知 scope、缺 Start/Stop、启动后追加登记在启动验证中报错。
- 部分启动失败由调用方执行 cleanup，已成功任务可反向退出；失败 Start 必须自行清理它内部的部分启动。
- 停止过程中一个任务报错，不跳过其他 stop。
- StartedTasks 提供非敏感诊断快照，不包含账户或凭据。

这不是新的账号调度器或消息队列，也不把本地闭包变成持久任务。

## 3. 已迁移 provider Start

以下服务从构造期启动迁移至显式登记（实际运行随依赖图可达性决定）：

- Worker 维护类：TokenRefresh、账号/代理/订阅过期、供应商余额巡检、版本目录同步、Dashboard 聚合、Usage 清理、Scheduler 快照/outbox、Ops 聚合/告警/清理/定时报表、幂等清理、Gemini 缓存恢复、定时测试、备份、共享配额 flusher、支付巡检、ChannelMonitor/V2 聚合、认证缓存 outbox、批量图片 worker、上游账单周期探测等。
- 本地/请求相关：Deferred 本地 last-used 批量刷写、日志 sink、操作审计、入口拒绝聚合、本地 Ops 设置刷新、API Key L1 失效订阅。
- 过渡 process-local：OpenAI 额度操作队列、Ollama 即时探测。这些仍接受本进程通知，不能仅因为名字像后台任务就移到远端 worker；需要先持久投递或拆开周期与本地分支。

关闭顺序先停止插件/PromptAudit 生产者，再排空本地 usage worker pool，再停止 managed tasks，然后清理尚未迁移的 eager 资源，最后 TimingWheel、Redis、DB。legacy cleanup 对 lifecycle 已管理任务不重复 Stop。

限制：某些旧 Stop 不接受 context 或不等待所有在飞操作。Lifecycle 不能强杀这些函数，当前 cleanup timeout 只是传递预算/诊断，不是严格进程关闭上限。

## 4. HTTP 角色边界

- all：保持当前登录、后台、支付、MCP、推理、静态前端等完整路由。
- gateway：仅推理协议、客户端遥测和健康；不挂登录/后台/支付/MCP/HTML。
- control：控制面、OAuth callback、支付和前端；不注册推理。
- worker：仅健康。
- 未知 role：路由层不开业务，进程配置校验仍应直接拒绝。

Gateway/worker 不读取 frame-src 页面配置或安装前端失效回调。Worker 不构建 WebSearch 配置。鉴权继续使用现有真实 middleware，不新增旁路。

## 5. 尚未解除的拆分门禁

`Runtime.ValidateRoleSupport()` **仍只允许 all**。只有完成以下工作并真实验证，才能逐个打开 split role：

1. **构造器隐式任务**：BillingCache、Subscription、Email、OAuth session store、ContentModeration、部分缓存/仓储仍有 eager pool/janitor/订阅。拆为无副作用构造与显式 Start，并覆盖失败 cleanup。
2. **TimingWheel**：go-zero New 即 auto-start，Start 仅日志；目前明确走 eager 资源最后清理。需要独立的 lazy/managed 工厂和错误回收，不能当作已消除副作用。
3. **Concurrency 清理**：已有 StartSlotCleanupWorker 无取消/Stop，需补可等待退出的生命周期；全局扫描与本地租约/缓存职责分开。
4. **Pricing**：Initialize 同时加载本地价格、远程请求、文件写入、启动更新。需要分离本地可用快照与远程同步，确保每个请求进程有价格，不简单 worker 独占。
5. **配置传播**：ChannelMonitor CRUD 当前只回调本进程 scheduler，worker 启动仅加载一次；必须加持久版本/outbox/定期重读，才能让 control 修改触达独立 worker。其他本地回调逐项核验。
6. **停止语义**：Dashboard 聚合取消未来两个 TimingWheel 键，但已启动 backfill 无 wait；V2Aggregator、QuotaFlusher等有在飞任务/禁用Stop副作用，需进一步规范。
7. **本地写入队列**：邮件、usage、billing、订阅维护、Deferred、moderation 的本地队列不自动跨进程可见；迁移到worker前必须换持久引用/任务，不迁移则明确每实例职责。
8. **依赖图与权限**：目前仍共享一个 Wire 图，不能仅靠路由判断声称最小依赖或最小 DB/IAM 权限已实现。服务应按角色构造可达依赖、验证启动不访问不必要 Secret/上游。
9. **状态与多副本**：刷新 CAS、租约恢复、持久账务、配置版本、长连接排空另有架构待办，当前不因此启用 ECS Service。

## 6. 验证要求

- Lifecycle 表驱动测试覆盖作用域、默认 all、构造无启动、成功一次、逆序停止、重复Stop、部分失败、取消和非法登记。
- Router 使用真实注册与路由表验证各角色；代表性无凭据请求继续被原鉴权拒绝，不调用真实供应商。
- Wire 重新生成并保持可再现；不手工修改 wire_gen.go。
- 原 server cleanup 测试和 service/route 回归不得跳过。
- 构建 service/migrator，运行配置/仓储/迁移回归，确认既有check模式和角色启动门禁不被绕过。

本批实际结果：角色路由/既有unit契约、Lifecycle聚焦、server/config测试、service完整包回归（147.444秒）、相关vet、server构建均通过；Wire重生成哈希一致。完整all进程外部依赖启动、前端/embed、真实split角色及跨进程任务、AWS资源未验。过程中发生过生成文件参数不同步的编译失败、以及并行编译OOM，修复生成接线/改串行后重跑成功，不将失败轮次记为通过。

实际命令和结果见 `IMPLEMENTATION_STATUS.md`，未运行的验证必须明示，不因本文件列出了要求就认定通过。
