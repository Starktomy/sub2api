# Sub2API Cloud：AWS ECS 单层网关架构方案

- 状态：已确认的目标架构；不代表所有能力已经实现。2026-11-03已推进到显式opt-in的本地多角色预生产烟测；AWS部署前置清单见 [AWS_PREPRODUCTION_CHECKLIST.zh-CN.md](AWS_PREPRODUCTION_CHECKLIST.zh-CN.md)。
- 决策日期：2026-11-02。
- 唯一产品基座：Sub2API，初始 upstream commit `4a4a83ee5`。
- 本地开发仓库：`/root/ai/sub2api-cloud`，初始分支 `feat/ecs-cloud-foundation`。
- 用户指定后续推送目标：`https://github.com/Starktomy/sub2api`（origin）；采用本地分阶段提交，未经明确要求不推送。
- 参考仓库：CLIProxyAPI `7cad1cb7`；new-api `45746eee4`。
- 目标 AWS Region：**us-west-2**。
- 已确认数据库：**Amazon RDS for PostgreSQL Multi-AZ DB instance**（主实例＋跨 AZ 同步备用，不是三实例 DB cluster，也不是 Aurora）。
- 实施状态与验证命令见 [IMPLEMENTATION_STATUS.md](IMPLEMENTATION_STATUS.md)。

## 1. 产品目标与非目标

提供一个面向个人和企业客户的模型服务平台：

1. 将已获授权的 OAuth 订阅账号、供应商 API Key、服务账号、AWS IAM 资源统一管理和调度。
2. 按客户、模型、服务分组、资源池、套餐运营；支持余额、充值、订阅、用量、后台和审计。
3. 跨供应端选择必须遵守模型能力、会话、数据地域、授权、额度及安全约束，不能仅因名称相同就替换。
4. 在 ECS 上实现多 AZ、按负载扩容、有界缩容、可恢复任务和账务。
5. 只有一个推理入口、一套资源状态、一套调度与重试策略、一套客户账本；不串联第二套 AI 网关。

首期非目标：Kubernetes、跨 Region 多活写账本、Service Mesh、完整事件溯源重写、自己实现容器编排、所有模型协议无损互通、无限容量/零中断承诺。

## 2. 代码复用及许可规则

优先级：

1. Sub2API 已有能力满足需求，直接复用并补回归测试。
2. 缺失能力优先检查 CLIProxyAPI、new-api 的实现和边界。
3. 只做必要的适配器抽取或局部修正，不同时引入第二套账号管理器、调度器、HTTP 服务或账本。
4. 三者均不合适，再实现稳定领域接口下的新能力；记录未复用理由。

CLIProxyAPI 是 MIT。保留许可与来源声明、记录抽取 commit，并一并引入有价值的契约测试。其内置 executor 在 Go `internal/` 内，不能假设可直接 import；完整 Service 会创建自身服务和管理器，不作为本项目的内嵌启动入口。

new-api 是 AGPLv3，并有 NOTICE 要求。直接复制/链接其实现前，必须确认衍生作品的许可策略；不能以“参考”名义复制代码后忽略义务。没有许可决定时，只研究公开协议、领域边界和行为要求，独立实现，不直接导入 RelayKit/TokenKit 或其他 AGPL 代码。

保留 Sub2API 的许可证、版权和运营合规文档。软件开源许可不授予上游账号共享、订阅转售或地域使用权；只接入有权使用的资源，不实现绕过平台限制的能力。

## 3. AWS 服务选择

| 层 | 首期选择 | 边界 |
| --- | --- | --- |
| 容器运行 | ECS Fargate On-Demand | 关键网关不使用 Spot；以后可将可重试后台任务放 Spot |
| 入口 | ALB + ACM + WAF | 不默认引入 API Gateway 或第二个反向代理 |
| 关系数据库 | RDS PostgreSQL Multi-AZ DB instance | PG 是订单、账务、凭据版本及持久任务的权威来源 |
| 协调/缓存 | ElastiCache for Valkey，节点型，Cluster Mode Disabled，Multi-AZ | 一个主节点＋至少一个跨 AZ 副本；不是财务账本 |
| 任务消息 | SQS + DLQ | 至少一次投递，业务消费者必须幂等 |
| 对象存储 | S3 | 私有文件、归档、审计输出；不默认保存 prompt/完整响应 |
| 静态前端 | S3 + CloudFront OAC | API 首期直接走 ALB，不依赖 CDN 缓冲行为 |
| 镜像 | ECR | 不可变标签，部署固定 digest；保留许可证 |
| 密钥 | KMS + Secrets Manager | 动态账号凭据用信封加密；基础配置用 Secret 引用 |
| 监控 | OpenTelemetry + CloudWatch | 日志脱敏、有限基数指标、按请求关联 |
| 基础设施 | Terraform | 加密远端 state 和锁需先 bootstrap，不在 Git 保存 state |

### 3.1 为什么先 RDS DB instance

标准 PostgreSQL、当前 Ent/SQL 兼容路径明确、主备结构简单。订单、预留、结算主要依赖写节点，不能用只读副本分担事务写入。备用实例不服务读流量；故障切换仍可能中断连接，应用必须重连并处理提交结果不明。

启用加密、删除保护、自动备份/PITR、日志、存储监控，设置备份和维护窗口。应用和迁移账户分离，不用主账号运行网关。写操作及有 read-after-write 要求的检查访问主节点。

未来出现计算波动、读扩展、灾备等明确需求后再评估 Aurora PostgreSQL/Serverless v2；不以“云原生”名义预先迁移。Aurora 不消除钱包热点、事务锁或外部调用一致性问题。

RDS Proxy 首期可选，不默认依赖。先算集群连接预算并压测 Ent/驱动行为；会话 SET、通知和部分锁操作可能导致 pinning。schema migration 直接连接数据库，不经过事务复用代理。

### 3.2 为什么 Valkey 先关闭分片

当前 `repository/redis.go` 使用 `redis.NewClient`；并发控制有同时涉及账号、用户及 Key 的多 Key Lua。ElastiCache Serverless 始终使用 cluster protocol，多 Key 事务/Lua 需要同 hash slot。直接换地址会破坏现有语义。

首期采用节点型 CMD + Multi-AZ + automatic failover，连接 primary endpoint，启用 TLS/认证/静态加密。关键协调存储使用 `noeviction`、TTL、容量限制和 OOM 告警，不让缓存驱逐租约。不同 Redis DB 编号不是内存隔离。

协调接口与普通缓存接口从代码层分开，流量增长后拆独立集群。不能把所有 Key 塞到同一个 hash tag 后声称已获得水平扩展。未来分片需重新设计跨账号/用户/额度域的准入与补偿。

普通复制可能丢失最新租约，故障切换后有恢复屏障与保守准入。MemoryDB 不是首期依赖。ElastiCache durability 的版本/分片限制需单独验证，不能认为 CMD 配置天然支持持久日志。

## 4. 应用形态和运行角色

模块化单体代码库，按角色部署；不一开始拆十几个独立领域微服务。

| 角色 | 职责 | 对外接口 | 生命周期 |
| --- | --- | --- | --- |
| gateway | API 鉴权、准入、统一候选调度、直接访问上游、SSE/WS、持久计量提交 | 推理协议和 health/readiness | 可扩容，保守受控缩容 |
| control | 用户、租户、套餐、充值、后台、账号配置、OAuth callback、支付 webhook | 控制 API | 独立伸缩，不代理推理 |
| worker | 刷新、outbox、任务认领、补偿/对账、媒体任务、通知 | 最小健康接口或无公网接口 | 持久认领和有界停止 |
| migrate | 迁移、角色权限及显式 bootstrap | 无 | 发布前一次性 ECS Task |
| all | 兼容旧版本的单机启动 | 当前完整路由 | 仅本地/过渡，不视为角色拆分完成 |

不同角色拥有独立 Task Role 和最小数据库权限，可共享领域代码。只有 gateway→供应商位于推理转发链路。没有 control→第二网关→供应商的串联。

依赖构造不应隐式启动 goroutine；用明确生命周期注册启动/停止任务。现有 Wire 中大量 `Provide...Start()` 分阶段治理，不能只加 role 环境变量就声称隔离完成。尚未实现的角色启动必须明确失败。

运行实例不能 DDL、生成共享 JWT/TOTP 密钥或执行初始化 seed。独立 migrator 拥有 DDL 权限；runtime 只读验证 schema，再用共享配置启动。兼容旧部署可保留 `database.migration_mode=auto`，云运行用 `check`。schema 检查不能获取迁移 advisory lock、创建 tracking 表或写 bootstrap 数据。

## 5. 领域边界与数据模型

### 5.1 客户与运营

- Tenant/Customer：账单、数据及资源所有权；个人用户也有个人 Tenant。
- User/Membership：组织成员及角色。
- Project：可选企业内部边界，不要求第一期开放所有 UI。
- APIKey：访问身份、项目、模型白名单、预算与速率。
- Plan/Subscription：有效期、额度窗口、并发和权益；续订/升级/退款明确状态机。
- Wallet：可用、预留、已结算，固定精度金额。
- ServiceGroup：客户服务等级和资源权限，不代替 tenant 隔离。
- PriceBook/Version：客户售价、套餐扣减规则及生效版本。

每次请求固定账单归属、价格版本、权益快照。订阅额度优先或钱包回退由产品策略明确，不能在不同代码路径使用不同顺序。退款通过冲正记录，不改写历史流水。

### 5.2 供应资源

渐进演进现有 Account，而非立即重建所有账号表：

- Provider：执行器与供应商身份。
- Endpoint/Deployment：地址、区域、部署名、原生模型 ID、协议与数据策略。
- Account/Credential：OAuth/API Key/服务账号/IAM Role，版本、状态、过期及授权范围。
- QuotaDomain：组织、订阅、项目共享的实际 RPM/TPM/窗口配额。多个 Key 不自动等于多份额度。
- ModelBinding：公开逻辑模型与一个或多个合格供应端点的映射。
- CapabilityProfile：上下文、tools、thinking、图像、音频、流式、WS、续接和缓存等。
- ResourcePool：授权给服务分组的资源集合。
- RoutingPolicy：候选、优先级、亲和、健康、容量、成本与回退。

账号凭据和可检索非敏感元数据分离。共享资源必须显式授权，不能靠拼接分组名绕过租户检查。

## 6. 请求、调度和执行器契约

### 6.1 统一请求流程

1. 建立 request ID，识别租户/项目/Key，限制请求大小和参数范围。
2. 解析客户端协议与所需能力，保留原生扩展字段。
3. 检查客户/套餐/分组/模型/地区与数据策略。
4. 固定价格版本，持久创建 request，做必要额度预留。
5. 基于逻辑模型收集所有合格供应资源，而非先硬选平台。
6. 检查共享额度域、账号冷却、并发、亲和及传输约束。
7. 排序并原子占用短期容量，创建 attempt 记录。
8. 对应适配器直接调用上游，转发流并采集可用 usage。
9. 持久提交结算输入或未决状态，进行幂等结算。
10. 释放剩余预留和资源租约，更新健康/限额观察。

不持有数据库事务等待上游，不每 token 写账本。每个逻辑请求有独立 attempt；成本计入每次真实上游尝试，客户收费遵守公开规则，不能因内部回退意外多收费。

### 6.2 调度规则

先硬过滤：授权、能力、版本、地区、数据策略、认证类型、状态、额度、硬绑定。再按服务等级、亲和、健康、容量、成本、权重选择。第一期实现可解释的优先级/权重，不做不可观察的复杂学习算法。

保留现有账号内调度优势，主要修改 `TargetPlatform` 先决分流的边界。候选需要能包含 OAuth、直连 API、Bedrock 等不同认证资源；仅在语义可替换且客户允许时跨供应端回退。

软亲和用于缓存/性能，可逃逸；硬绑定用于只在原供应端有效的 response ID/缓存/会话。硬绑定失效必须显式报错、等待或经允许完整重放，不能伪装无损迁移。会话命名空间包含 tenant/project。

### 6.3 执行器

统一接口表达 capabilities、credential preparation、Execute/ExecuteStream、Refresh、Count/EstimateUsage、NormalizeError、QuotaSignals。执行器不挑另一个账号、不跨供应商重试、不直接扣客户余额。

不把所有请求统一降级成 Chat。原生协议专有能力、usage 原始语义和转换诊断应保留。每个适配器有版本化契约测试，区分 native/translated/unsupported。

### 6.4 重试

- 确认未发送：允许有限重试。
- 明确可重试拒绝：尊重 Retry-After、错误范围、重试预算。
- 已发送结果不明：标记 ambiguous/pending reconciliation，不盲目重放。
- 已输出部分流：不透明重启生成，不重复触发工具副作用。
- 无效参数/权限：不做无意义重试。
- 凭据过期：统一协调刷新后按协议允许范围重试。

全链路一处控制重试次数、总时长和抖动。DB 事务失败只重试可幂等本地动作，不包着上游调用重跑。

## 7. 账务、持久任务与支付

复用现有事务幂等结算、订单状态机和 Worker 同步兜底，不声称现有计费完全不可用。重点补齐上游消费后、结算输入持久化前的窗口，以及扣费、明细、后续任务的恢复。

需要 request、attempt、reservation、usage fact、pricing snapshot、settlement、ledger entry、outbox 和 reconciliation 状态。金额用固定精度；usage fact 保留来源/原始语义、计量可信度及未知分类。

客户收费、套餐扣减、上游实际/估算成本分别记录。OAuth 固定订阅成本与请求边际成本分开核算，不能默认成本为零。

本地 DB 事务原子提交幂等键、流水、额度/余额和 outbox。正常请求可直接结算；失败/积压用持久任务补偿。超时预留不能仅按墙钟自动退款：先查是否在执行、是否消费或处于未知状态。

Outbox→SQS 使用至少一次投递，消费者用业务唯一键防重复。消息携带任务引用，不携带可用凭据。完成业务事务后再 ACK；错误区分重试、永久失败、DLQ、人工处理。支持 lease/heartbeat、过期认领和有界并行；支付等关键任务还需 DB 扫描兜底，不能仅信队列。

支付 callback 在 control 验签、验证渠道/金额/币种/订单，持久记录后响应；重复 callback 不重复充值。充值、退款、拒付和已付款未履约有对账状态与后台工具。

PG outbox 解决本地写库与消息发布双写，不使上游生成与本地提交成为原子事务。外部结果不明进入 `pending_reconciliation`，不承诺端到端 exactly-once 或严格零透支，除非特定预留策略通过验证。

## 8. OAuth 凭据与安全

复用已有刷新 API、加锁后重读、outbox 与局部 CAS，强化而非重造：

- 所有入口收敛同一刷新协调；锁有 owner、超时和安全释放。
- 全平台凭据版本条件更新，拒绝旧持有者覆盖。
- 提前刷新并随机分散；临近过期按策略临时不可调度。
- Redis 不可用不无约束无锁刷新一次性 token。
- 上游轮换后本地持久化结果不明，有隔离/恢复/重新授权状态。
- 租约到期不保证旧外部刷新已停止；CAS 也不能撤销上游副作用。

授权 callback 的 state/PKCE/授权事务跨副本持久并单次使用。客户端/回调信任域和 CSRF 防护明确，登录 OAuth 与上游账号 OAuth 不混淆。

动态凭据 PG 密文＋KMS 信封加密，基础服务密钥放 Secrets Manager。解密只给必要角色，短期内存缓存，禁止明文导出、日志、SQS、无鉴权调试接口。KMS 轮换与 OAuth token 轮换是不同生命周期。

Gateway/Control/Worker 各自 Task Role。Execution Role 仅拉镜像、启动 Secret 注入等。Bedrock 使用 AWS SDK 默认凭据链/ECS Task Role；跨账号经授权 AssumeRole，不默认长期 AK/SK。

## 9. ECS、网络与弹性

### 9.1 网络

单 Region 至少两个 AZ；ALB/NAT 在 public subnets，Task 在 private app subnets，RDS/Valkey 在 isolated data subnets。应用无公网 IP，数据库/缓存无公网入口，SG 仅允许必要角色访问。各 AZ 独立出站避免单 NAT 故障点；NAT/EIP/跨 AZ 流量是显著成本，预算需显式审查。

S3 Gateway Endpoint；ECR/Logs/SQS/Secrets 的 Interface Endpoint 按访问量和费用决定。固定出口仅用于合规白名单/审计，不用于绕过限制。限制信任代理，只接受已知 ALB 入口；阻止源站直连伪造转发头。

### 9.2 初始服务

目标 Gateway 2、Control 2、Worker 2，跨 AZ 放置；migrate 每次发布一次。具体 CPU/内存、最大副本和连接池由压测决定。尚未拆分的角色不能上线为生产服务。

扩容关注活动流、排队时间、内存、连接池、FD、CPU、本地过载；Worker 看最老消息年龄、积压/执行器、耗时/重试。上游 429 或额度不足不是无限扩网关的理由。所有连接池预算计入发布期间双份副本和运维余量。

### 9.3 排空

先实现自动扩容和保守受控缩容。选定 Task→停止接新工作→ALB 摘流→协调 task protection→有界等待现有流/结算→释放租约→解除保护→停止。保护必须有截止时间，不能持续接新请求并无限续保护。

分开设置 ALB idle timeout、deregistration delay、应用 drain deadline、Fargate stopTimeout。Fargate 停止阶段最长等待 120 秒，长排空在 SIGTERM 前开始；SIGTERM 留给最终收尾。ALB HTTP/2 PING 不重置 idle timeout，按协议发送心跳和 flush，不给非 SSE 盲目插入字节。

WebSocket 显式登记、停止新 turn、按协议关闭；HTTP Shutdown 不自动管理所有升级连接。账号亲和不意味着已有连接可迁移，故障不可避免时支持可诊断重连/未决恢复，不承诺零中断。

## 10. 状态传播和故障策略

- PG 不可用：新收费请求若无法预留/持久记账则拒绝，不无限 fail-open；已运行请求按明确收尾策略处理。
- Valkey 不可用/切换：严格资源保守准入；丢失租约后有 epoch/恢复屏障和活跃请求登记/心跳，不能瞬间重新放满。
- SQS 不可用：outbox 保留，积压阈值触发背压；不能使用内存队列当唯一恢复来源。
- Worker 退出：停止认领，持久记录检查点/结果，再 ACK；过期任务可重新认领。
- 配置失效通知：PG 版本/outbox 是可靠来源；Pub/Sub 加速，周期校验修复丢消息。SQS 单队列竞争消费不是广播给所有实例。
- 凭据撤销/权限封禁：定义传播时限和敏感请求版本检查；不能无限依赖陈旧本地缓存。
- 预算和队列过载：有界排队和公平隔离，不让一个租户占满全部资源。

## 11. 部署、安全与可观测

Terraform 分 foundation 与应用部署。foundation 可先完成 VPC/RDS/Valkey/ECR/集群/队列/存储；应用服务在角色拆分、镜像、域名、证书、runtime DB 权限及 Secrets 就绪后才启用。无镜像占位服务或示例密码上线。

禁止 root provisioning。部署前确认 account、us-west-2、专用 IAM deploy role、plan、月费组成和删除保护。Terraform state 加密、版本化、锁、最小访问，不能含明文 Secret；RDS master password 用托管 Secret，runtime 不使用 master；Valkey 用外部创建/轮换的 ACL Secret，避免明文进入 state。

发布采用不可变镜像、构建/扫描、一次性迁移、逐步放量、旧版本排空。schema 走 expand→migrate→contract，兼容新旧实例；回滚应用不回滚当日账本或盲目恢复备份。定期做 PITR/恢复演练，具体 RPO/RTO 经演练再承诺。

监控：请求/attempt 跟踪、按供应商/模型的成功率和 TTFT、活动流、准入拒绝、租约、刷新失败/未决、账务 pending、outbox age、SQS age/DLQ、DB 锁/连接、Valkey memory/eviction/failover。避免 tenant ID/账号 ID 做无界指标标签，细粒度归因放受控日志。

不默认记录 prompt/原始响应；如需诊断，显式授权、加密、限期保存、审计访问。使用 CloudWatch retention、S3 lifecycle 与按月分区/归档限制数据成本。

## 12. 实施顺序和验收

### P0 基线

创建独立 fork；保留源仓库；记录方案、来源和许可；建立工具、测试与 AWS 安全边界。

### P1 运行与迁移基础

独立 migration/check 命令；runtime schema read-only check；角色语法/不支持门禁；配置生命周期；真实 DB 测试；逐个清理隐式任务启动。角色分离没有接线和测试前标记未完成。

### P2 统一资源与调度

模型绑定、quota domain、跨供应候选、能力过滤、可解释策略、一个重试所有者、硬/软亲和。先用少数认证/协议差异明显资源验证，再扩平台。

### P3 可靠账务和凭据

持久预留、计量终态/未决、流水/outbox、SQS 幂等、支付补偿、刷新租约/CAS、凭据加密。

### P4 ECS 基础设施与部署

Terraform foundation→专用部署角色→review plan/费用→创建资源→bootstrap runtime DB/Secret→构建推送镜像→migrate→应用服务→监控。

### P5 故障与弹性验收

多副本刷新同账号、token 轮换后写库前强杀、上游完成后结算前强杀、DB commit ambiguous、消息重复、支付重复/乱序、Valkey failover、长 SSE/WS 发布排空、额度并发耗尽、账号/权限紧急撤销、PITR 恢复。

通过标准是权限/资源/账务正确且异常可恢复，不只是 HTTP 200。没有真实上游账号或支付沙箱时明确列为待验，不伪造通过。

## 13. 初始实施环境记录

2026-11-02 只读检查：AWS CLI 当前使用账户 root 身份；默认 Region 未设置，用户已明确指定 us-west-2。仅以文档记录安全状态，不保存任何凭据。资源创建前必须换专用部署角色。

本机 2 vCPU、约 4 GiB RAM，初始没有 Go/Docker/Terraform/Node。Go 1.27.3 与 Terraform 1.17.1 通过官方发布及校验和安装到用户目录。构建限制并行，避免将硬件不足误判为代码失败。

## 14. 官方参考

- https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.MultiAZSingleStandby.html
- https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy-pinning.html
- https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/wwe-troubleshooting.html
- https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Replication.Redis-RedisCluster.html
- https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Durability.Limitations.html
- https://docs.aws.amazon.com/AmazonECS/latest/developerguide/load-balancer-connection-draining.html
- https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-scale-in-protection.html
- https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_definition_parameters.html
- https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html
