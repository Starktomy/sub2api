# 会话压缩与接续摘要

更新：2026-11-03。原会话：`ffaa7526-ec8b-4ec7-9776-b09a873e7d32`。

## 已确认目标

- 以本仓库 Sub2API fork 为唯一基座；缺能力优先研究 CLIProxyAPI/new-api，但不串联第二层网关/调度器，遵守许可证。
- OAuth 订阅池与供应商 API 最终统一调度；支持客户、模型、分组、套餐、充值与后台。
- 目标 ECS Fargate（非 Kubernetes）、us-west-2；RDS PostgreSQL Multi-AZ DB instance；节点型 ElastiCache Valkey CMD。
- 用户调整实施顺序：先在当前 EC2 跑双 Gateway、Control、单 maintenance Worker，验证真实 AWS 数据层；暂不添加负载均衡或扩建 ECS/NAT。

## 当前事实与原会话断点

- 仓库 `/root/ai/sub2api-cloud`，分支 `feat/ecs-cloud-foundation`。
- 前三批提交：`9a450028a`、`1976b4282`、`f81fab32c`；第四批在原会话断在验收报告及本地提交收尾，未推送。
- 当前四服务通过 systemd 以非 root 运行，只监听 127.0.0.1:18080–18083，未 enable 开机自启。
- 真实 RDS/Valkey 已部署；300 迁移、TLS/权限隔离、跨连接 Lua/PubSub、事务幂等、双 Gateway 鉴权、登录/兑换/套餐/Key 幂等及撤销传播已有通过记录。
- 已执行一次 Valkey 和一次 RDS 受控切换；恢复后业务复验通过。Valkey 约36.37秒观察稳定；RDS 24.785秒是 AWS 事件间隔，不是客户端中断测量。
- RDS 观察器因 AZ 元数据滞后被停止，后修正脚本；修正后的触发路径未再次故障演练。
- 临时 create/failover/master-secret 权限已移除；应用 UID 禁止访问 IMDS。仍不是生产级按角色最小权限。
- 原记录最终临时用户/Key/上游账户数为0。没有模型调用、没有支付渠道调用。
- 基础资源估算约136.15美元/月，另有已有 EC2、Secret、日志等费用；保留资源继续计费，不自动销毁。

## 本次恢复后的实际检查

- 四个 `/health` 均返回 `{"status":"ok"}`。
- 后端配置、迁移、仓储聚焦包：通过（0.655/0.017/7.054秒）；Pricing 聚焦回归通过（0.082秒）。普通仓储测试不等同本次重新运行真实 AWS 合同测试。
- Terraform integration fmt/validate 与无 AWS 凭据的5项 mock plan 测试通过；未 apply、未再次触发故障。
- 首次 go test 在仓库根目录执行，因不含 go.mod 失败；切换 backend 模块后上述测试通过。

## 授权与下一步边界

- 用户已要求本地阶段提交；不推送，不发布镜像，不改 DNS，不销毁资源，不擅自扩建 ECS/ALB/NAT。
- 仓库外 `/root/ai/account.test` 两份 OAuth 账户获授权用于后续真实测试，但用户要求先完成本阶段任务、提交和报告。至本摘要写入时仍未导入，不展示凭据。
- 当前优先完成第四批本地提交和报告。之后真实调用须先确定实际模型 ID/可用能力及有界请求预算；不得凭“5.6 Terra”名称假定协议或可用性。
- 后续仍缺：统一跨供应端调度、持久请求/attempt/价格快照、刷新 CAS/租约、KMS 动态凭据加密、可靠预留/结算/outbox、Control/Worker 多副本治理、ECS 弹性/长连接排空。
- 不将有限集成验收描述为完整云原生或生产 SLA 验收。

## 接续更新：OAuth 首轮已验

- 第四批收尾已提交 `7561bccb5`，压缩摘要提交 `9bd33c364`；均未推送。
- 用户随后明确接受合规承诺并授权记录，已通过官方接口记录；真实登录及导入两份OAuth完成。
- 2026-11-03首轮2次真实生成通过：账户1/Gateway A非流式，账户2/Gateway B流式，均 `gpt-5.6-terra`、31输入/5输出token。RDS两条用量；客户本地扣费共0.000244美元，不代表上游订阅成本。
- 本轮后账户、Key、测试用户全部停用；保留凭据于测试DB及账务证据，不删除历史账务。四服务仍健康。
- 所供敏感长值在本轮journal/audit精确匹配为0。尚未验证同池调度/回退、工具、会话亲和、刷新或在飞故障恢复。
- 上文“尚未导入/调用”为第四批历史暂停点，当前状态以上述更新及 IMPLEMENTATION_STATUS 第五批为准。

## 接续更新：同池与真实刷新已验

- 用户进一步要求同池、亲和、回退、工具及刷新；本批5次生成、1次串行刷新。完整结果见 `docs/OAUTH_POOL_VALIDATION.zh-CN.md` 和 IMPLEMENTATION_STATUS 第六批。
- 同组同Key调度、优先级反转后的跨Gateway亲和、echo工具往返、停调逃逸通过；关闭端口代理故障后，同请求账户2失败→账户1成功有日志证据。
- 原池脚本因journal时间格式采集断言失败而停止，原始passed=false保留；独立补查询确认回退，不重跑四次生成。
- 账户1官方refresh成功，access/refresh均变化、刷新后调用通过。新token在DB和root私有恢复文件，account.test未更新，绝不可重新导入旧文件覆盖。
- 最终两账户/测试Key/user均停用、原组与无代理状态恢复、故障代理inactive，账务保留，四服务健康。本批费用按本地配置0.001070美元。
- 管理员刷新尚无统一CAS；单次成功不代表并发刷新安全。尚未验证真实429、部分流恢复、WS或跨平台调度。

## 接续更新：刷新硬化与加密组件（未部署）

- 当前未提交改动：OpenAI统一管理员刷新、JSON/proxy CAS、owner租约Lua释放与lease内超时、受限管理员安全拒绝；Wire已生成。
- 新credentialcrypto包test/vet通过，仅抽象KMS信封组件，没有数据库/KMS适配或真实凭据迁移，不能称现有token已加密。
- 仓储CAS/lease聚焦单测0.041秒、管理员unit0.058秒、服务协调器聚焦unit0.159秒通过。PostgreSQL integration因Docker缺失跳过。server build通过，未安装/启动，最终状态见IMPLEMENTATION_STATUS第七批。
- 恢复会话后发现四集成服务均inactive，未enable自启；未启动或更新安装binary。本批无真实OAuth刷新、无模型调用、无AWS资源变更。
- 后续仍需：受限管理员事务外授权CAS、平台/type比较/持久fencing、lease failover治理、真实PostgreSQL条件写入、KMS适配/DB接线/迁移。

## 接续更新：用户要求1–3已实现并验证（未部署）

- Docker已安装可用29.9.2，不再按缺Docker跳过；最终PG18/Redis8.4容器集成通过5.618秒，包括权限撤销/轮换/策略/解绑、generation/owner、平台/type、迟到提交、本地幂等完成及显式歧义恢复。
- RDS真实TLS回滚夹具CAS已通过0.161秒，无DDL/真实账号修改。
- 受限通用refresh已支持事务外实时授权；提交时再次锁校验，带Key授权必走持久路径。split角色自动强制持久模式。
- 新250迁移只在Docker应用，未更新RDS或运行binary；四服务仍inactive。无新增真实模型调用/刷新。
- 不明结果禁止自动重新消费token；本地completion可幂等重试。显式恢复方法需停旧holder、重新授权、inactive/不可调度、changed refresh token及匹配generation，推进fence；无加密spool、无公开恢复CLI/HTTP。
- service/repo/handler单测、Wire/build/vet通过。用户授权完成后分批commit push；加密组件c80c6110f已推送，刷新及文档另批。最新完整状态见IMPLEMENTATION_STATUS第八批，取代第七批历史缺口。

## 接续更新：本机六进程与保守多副本（仍有任务受阻）

- 用户改为继续EC2，不部署ECS/Spot。实现split Control五provider Redis TTL会话、原子单次消费、无内存fallback；失败兑换需重开授权。
- Worker显式RUNTIME_MULTI_REPLICA_WORKERS=true后PG专用会话锁主备。白名单仅5类：账号过期、幂等清理、订阅过期禁邮件、等待队列、鉴权失效outbox；其余任务启动时明确禁用，不能称全部后台多副本完成。完整清单docs/WORKER_REPLICA_SAFETY.zh-CN.md。
- 当前机器独立本地PG15/临时Redis上2Gateway+2Control+2Worker真实烟测通过：SIGKILL leader后备用获锁且实际暂停过期fixture；A创建会话B可读，重启B仍可读，无真实provider调用。
- 六session/pkg测试通过；fake服务回调12竞争恰好1上游调用0.889秒，维护最终回归0.108秒，配置完整1.675秒、build/migrate/Wire/vet通过。
- 没改原systemd常驻服务，测试进程已全部退出；私有夹具/日志在/root/.local/state/sub2api-cloud/replica-test-f0b2bad6/，无AWS schema/资源变更。
- Redis数据丢失仍使未完成OAuth需重开，不承诺缓存故障会话持久无损；客户OIDC等cookie登录流未做全面安全审计。

## 权威文档与证据位置

- `AGENTS.md`：操作约束。
- `docs/CLOUD_ARCHITECTURE.zh-CN.md`：完整目标架构。
- `docs/IMPLEMENTATION_STATUS.md`：分批真实进度和历史测试。
- `docs/EC2_MANAGED_DATA_VALIDATION.zh-CN.md`：本阶段详细验收、费用、限制。
- `infra/ec2-integration/README.md`：资源、state、验证和运维边界。
- 私有证据：`/root/.local/state/sub2api-cloud/aws-integration/`；仅按需读取非敏感结果，不复制或提交环境文件、state、plan、Secret。

### 2026-11-03 后台维护继续加固

允许清单增加 ConcurrencySlotCleanup，共六项：原子 Lua 重查 regular/live/wait，保留更新索引及 TTL，split worker 不执行 legacy prefix sweep。SchedulerSnapshot 的 pending/exact-ack 与生命周期修复已实现，但全路径 leader fencing 未完成，仍禁用。TokenRefresh durable-only/后动作收敛仅为防护准备，真实 provider 内部审计未完成，仍禁用。详见 WORKER_REPLICA_SAFETY.zh-CN.md 和 IMPLEMENTATION_STATUS.md 的追加验证记录。不要将局部单测误记为全部后台任务已支持 HA。

最终修复两项 rebuild 测试的零值构造后，service 聚焦0.707s、repository聚焦0.918s、server build/vet全部通过。六进程 smoke-03 passed=true：kill worker-a→worker-b 实际暂停过期账号，陈旧索引下活跃槽位和等待计数保留，Control A session 在 B 与重启后可见，provider_calls=0；临时进程退出。下一阶段先 provider 真实调用链单次刷新（含 transport replay/redirect 与禁止轮换后 enrichment），再完整 scheduler leader fencing；均未放开 HA。

Provider 维护刷新已完成单次服务路径与隔离HTTP策略，legacy对照、五类具体HTTP客户端fake failure/redirect/cancel、重复3次及build/vet通过，详见 IMPLEMENTATION_STATUS。HA TokenRefresh仍未准入；完整scheduler fencing仍待实现，不能将bucket/group owner改造当作PG generation+namespace完整方案。
