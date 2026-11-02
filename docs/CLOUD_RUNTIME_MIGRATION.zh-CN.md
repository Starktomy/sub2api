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

关闭顺序先停止插件/PromptAudit 生产者，managed tasks按producer→consumer→store分阶段退出（阶段内逆序），最后清理未托管资源和Redis/DB。TimingWheel在维护producer退出后、依赖store关闭前停止并等待callback；legacy cleanup对已托管任务不重复Stop。

限制：某些旧 Stop 不接受 context 或不等待所有在飞操作。Lifecycle 不能强杀这些函数，当前 cleanup timeout 只是传递预算/诊断，不是严格进程关闭上限。

## 4. HTTP 角色边界

- all：保持当前登录、后台、支付、MCP、推理、静态前端等完整路由。
- gateway：仅推理协议、客户端遥测和健康；不挂登录/后台/支付/MCP/HTML。
- control：控制面、OAuth callback、支付和前端；不注册推理。
- worker：仅健康。
- 未知 role：路由层不开业务，进程配置校验仍应直接拒绝。

Gateway/worker 不读取 frame-src 页面配置或安装前端失效回调。Worker 不构建 WebSearch 配置。鉴权继续使用现有真实 middleware，不新增旁路。

## 5. 第三批：默认关闭的预生产 split-role opt-in

`RUNTIME_ENABLE_SPLIT_ROLES=true` 显式启用 gateway/control/worker；默认 false，仍拒绝独立角色。实验角色必须 `DATABASE_MIGRATION_MODE=check`、标准计费模式、显式 JWT/TOTP；启动前拒绝 auto/安装向导。它用于隔离预生产验证，不是生产可用性或权限最小化保证。

已落实：

1. BillingCache、Subscription、Email 新增 managed 构造/Start/Stop，旧 New 自动启动兼容。本地闭包仍本进程消费，避免迁往worker后丢任务。Subscription取消并等待Redis订阅，关闭Ristretto；email在排空预算后取消但等真实worker退出（SMTP可能超预算）。
2. 五类OAuth session store新增 managed构造、幂等Start、Stop cancel/join；cleanup仅control启动，后台token refresh与授权会话清理分开。XAI Redis会话同时提供managed构造。
3. TimingWheel构造不再启动go-zero loop，Lifecycle.StartManaged才创建且可报告错误；停止等待已派发callback。本轮保留旧New eager兼容。
4. Concurrency/UMQ清理可取消和等待，Worker不再执行会删除其他进程前缀的legacy启动扫槽；日常仅按过期清理。加入单元回归及真实Redis槽位烟测。
5. Pricing.Initialize从provider构造移至每进程显式生命周期，Stop幂等；每个推理进程仍有本地价格/热重载。远端价格目录与本地加载的进一步拆分仍待办，预生产烟测显式关闭远程同步，使用内置快照。
6. ChannelMonitorRunner新增有界ReconcileOnce与每30秒PG配置重读：新增/更新/禁用/删除收敛，失败保留，unchanged不重复探测；Stop取消poll/在飞检查并等待。本地control不挂worker scheduler callback。**无跨worker leader，预生产维护worker只启一个**。
7. ContentModeration本地审核队列可显式启动、Stop拒绝入队并排空；旧零值构造路径保留缓冲兼容。审核retention cleanup暂仍随本地请求角色启动，未宣称cluster独占。
8. Channel/ErrorPassthrough/TLSProfile配置订阅改为managed构造，Redis transport返回可等待stop，回调有关闭屏障。Pub/Sub丢失恢复与启动订阅失败重连仍需进一步强化。
9. Lifecycle停止按producer→consumer→store阶段，同阶段逆序；先停维护/本地生产者，再排空usage，再停billing/subscription/email/日志等依赖；V2Aggregator Stop开始等待loop退出。

真实烟测已运行两个gateway＋control＋单worker，共用PG和临时密码Redis。四进程健康、鉴权/缺失路由、关键维护scope归属、正常SIGTERM退出通过。没有用户/凭据bootstrap写入；没有调用真实模型或支付服务。完整回归与最终增强烟测结果见状态文件。

### 尚未完成的生产边界

- 仍共享一个Wire图，部分本地缓存janitor/readonly预热仍在构造期；DI中途失败的全资源回收和最小DB/IAM依赖图未交付。
- 维护worker暂限单副本，所有巡检/版本同步/通知并非均有集群lease；不能允许多维护worker滚动重叠后承诺无重复。
- OAuth授权会话仅部分provider有Redis后端；Control预生产限单副本，其他provider跨实例/重启授权继续可能失败。无需token刷新cleanup不代表授权状态已持久化。
- Dashboard后台backfill取消/等待、QuotaFlusher禁用Stop副作用与并发flush、部分旧HTTP/SMTP超时仍需治理，当前非严格ECS停止时间保证。
- KMS动态凭据加密、全平台token CAS/租约、可恢复账务、资源quota domain和跨供应商统一候选未完成。
- ECS task definitions/services、ALB/域名/证书、非root deploy role、runtime DB角色/Secrets、remote state、镜像构建和发布/长连接排空仍需下一阶段。

## 6. 验证要求

- Lifecycle 表驱动测试覆盖作用域、默认 all、构造无启动、成功一次、逆序停止、重复Stop、部分失败、取消和非法登记。
- Router 使用真实注册与路由表验证各角色；代表性无凭据请求继续被原鉴权拒绝，不调用真实供应商。
- Wire 重新生成并保持可再现；不手工修改 wire_gen.go。
- 原 server cleanup 测试和 service/route 回归不得跳过。
- 构建 service/migrator，运行配置/仓储/迁移回归，确认既有check模式和角色启动门禁不被绕过。

第二批结果：角色路由/既有unit契约、Lifecycle聚焦、server/config测试、service完整包回归（147.444秒）、相关vet、server构建均通过。第三批结果：managed/会话/配置/仓储回归、service完整包（148.169秒）、unit监控/停止/定时器、相关vet/build通过；最终二进制2Gateway+Control+Worker真实PG/临时Redis烟测通过，含活跃槽位保留和正常退出。AWS、RDS16/Valkey7.2TLS、前端/embed、真实上游/支付、长流排空/故障恢复及多维护worker未验。失败与修复轮次如实记录于状态文件，不因后续成功而省略。

实际命令和结果见 `IMPLEMENTATION_STATUS.md`，未运行的验证必须明示，不因本文件列出了要求就认定通过。
