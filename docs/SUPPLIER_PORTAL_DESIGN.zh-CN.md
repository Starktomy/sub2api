# 供应者门户与容量结算方案

- 日期：2026-11-03。
- 状态：整体仍为设计提案；邮箱自助开通MVP正在feat/control-supplier-portal实现，未部署，未验证商业供给许可。
- 最新范围调整：供应者门户身份无需人工审核，复用用户邮箱注册/登录，验证码开通；不因门户开通自动激活上游供给、结算或付款。以下涉及人工审核的设计仅用于后续供给权利与付款，不作为注册门槛。
- 首期：评估获许可的 Codex 供给；Claude 后续独立评估。
- 基础：现有 Sub2API Cloud，一个推理 Gateway、一套资源调度器、一个客户账本。

## 1. 决策摘要

首期整合进现有 Control 代码库，但供应者使用独立域名、独立前端产物和供应专用 API 入口，与消费者站点隔离。复用底层服务与用户记录不等于共用对外路由、会话或权限；允许同一台 EC2 上运行独立的供应 Control 实例，不新增第二套推理 Gateway 或调度器。供应者身份、资源所有权、权限和应付账本必须独立，不另建客户钱包。当前邮箱会员基础仍使用共享用户鉴权，尚未完成独立入口及令牌隔离，不能视为已满足此边界。

同一用户可以同时具有客户与供应者身份，但权限、客户余额和供应者应付不可混用。Control负责申请、审核、授权连接、资源管理、收益查询和提现审批；Gateway产生可信调用事实；Worker执行幂等计提与对账；高权限付款执行器后续按需隔离。

当前先实现邮箱自助注册/登录、验证码开通与本人资料，无人工注册审核；独立供应入口隔离纳入本分支后续实现。资源授权、供给权利核验、暂停撤销和影子结算属于后续阶段，不直接上线“自动收号、自动探测、自动付款”。可拆供应控制入口与付款执行器，但不复制网关、调度和客户计费。

## 2. 上线前置：供给权利与提供方许可

拥有个人订阅、能完成 OAuth、能实际调用模型，都不自动证明允许向第三方出租、转售或托管该容量。供应者勾选承诺也不能替代提供方许可。

本方案仅适用于提供方明确允许的委托、商业转售或 API 容量供给。若个人消费订阅不获许可，则不激活其交易供给；可先做申请审核，或改用获许可 API 容量、本人自用第三方接入产品。

公开资料初步检索涉及账号共享与转售限制，但原规划时官方页面直接抓取受403限制，仅取得搜索摘要；没有完成适用地区、条款版本或商业授权核验。不得将本文当作法律意见或已获许可证明。

每份供给授权至少记录：

- 权利人、提供方、产品、适用地区及经营主体。
- 使用依据、合同/公开条款版本、证据引用和有效期。
- 是否允许第三方客户调用、商业收费、平台托管凭据。
- 允许模型、调用主体、数据用途、地域和容量边界。
- 审核人、复核时间、撤销/到期状态。

调度门禁：权利核验通过、账号控制权核验通过、授权有效、凭据有效、模型合格、容量允许、风险检查通过，缺一不可。不能使用未经许可的私有接口、绕过限额或借用不属于本产品的OAuth客户端来补齐门禁。

## 3. 模块与部署边界

| 模块 | 职责 |
| --- | --- |
| 供应者门户 | 申请、材料、授权连接、权益来源、暂停、收益、结算与争议 |
| Control | 供应者鉴权、所有权检查、审核、授权回调、合同报价和付款审批 |
| 唯一 Gateway/调度器 | 供给资格过滤、模型能力、共享配额域、发送前授权复核 |
| Worker | 权益复核、获许可凭据维护、计量对账、应付计提及补偿 |
| PostgreSQL | 权利证据、计量事实、价格快照、应付分录和付款状态的权威来源 |
| 独立付款执行器（后期） | 最小支付权限，执行已审批的幂等付款，不参与推理 |

不采用：供应者门户代理推理、第二套账号池调度器、供应者自报用量即付款、把供应者收益直接写入客户余额。Redis只用于缓存/协调，不是结算账本。

### 3.1 独立域名及 API 安全边界（2026-11-03 新增要求，待实现）

- 供应者域名指向供应专用 Control 监听入口/实例；后端按显式白名单注册路由，而非注册全部路由再隐藏菜单。未知 API 返回 404，不能落入 SPA HTML 回退。
- 当前范围只允许必要的供应身份认证、邮箱验证与本人供应资料 API；不暴露消费者余额、API Key、充值、订单、用量、管理员、MCP、支付回调或推理接口。认证端点逐项列出，不直接复用整组 RegisterAuthRoutes。
- 供应者独立前端入口、根组件、路由和构建产物，不打包消费者/管理员页面，不启动客户订阅或公告轮询。注册登录后的目的地限供应路由，不接受任意返回 URL。
- 复用底层用户身份，但供应者 access/refresh 会话必须用途隔离；两种入口双向拒绝对方令牌。可使用独立签名密钥及会话命名空间，或严格验证 audience/issuer 并给现有消费者入口补拒绝规则；仅新增 audience 而消费者继续忽略它不构成隔离。刷新、2FA及注销同样不能跨用途转换。
- 默认同源 API；跨源时仅允许显式供应者 origin。浏览器存储按 origin 隔离，不使用父域共享 Cookie；后续若引入 Cookie，使用 host-only Secure/HttpOnly 及 CSRF 防护。
- 域名/Host 路由只负责入口分流，不作为用户权限依据。内部端口不公网开放，反代限定目标及可信转发来源；未知 Host 拒绝，不能通过修改 Host 选择高权限入口。
- 供应者只能访问自己拥有的资源；默认业务 admin 也不自动获得付款执行权。供应入口与管理入口即使使用同一代码库，也不共享对外权限。
- 验收包括路由矩阵、消费者/管理员/推理 API 的 404、双向令牌与刷新拒绝、独立前端构建检查、未知 Host 与 CORS 拒绝。域名、DNS、TLS 和公网部署须另行配置；目前未创建或修改任何相关 AWS 资源。

现有共享用户路由上的供应会员代码只是中间基础，不得直接作为独立供应域名的公网后端。

## 4. 订阅等级、模型、窗口余量

三者独立建模，不用“连接成功”推导全部权益。

| 来源 | 可表达 | 不能推导 |
| --- | --- | --- |
| 文档化且授权范围内的官方API | 接口明确提供的权益/额度 | 未授予的转售权 |
| 官方身份/权益声明 | 声明明确包含的身份与权益 | 未声明的模型/精确余量 |
| 合法调用响应 | 某时刻可用性、用量及限流观察 | 完整订阅容量 |
| 用户材料/申报 | 用户声明的套餐及保留额度 | 自动成为调度或付款依据 |
| 平台累计用量 | 本平台观察到的消耗 | 用户在其它客户端的全部消耗 |

字段附带：`source_kind`、`source_ref`、`observed_at`、`expires_at`、`verification_status`、`scope`、`unit`、`unknown_reason`。缺少可靠数据时显示未知，过期时标陈旧，不显示无限或100%剩余。

目前未确认存在适用于任意第三方、可读取所有个人套餐等级、模型权限和精确剩余额度的公开API。实现前必须逐接口验证，不将现有代码技术可达性视为使用许可。

现有 `openai_oauth_service.go` 的套餐补全涉及ChatGPT backend-api及隐私设置修改，不能直接继承为供应者权益探测流程。新流程禁止隐式修改隐私设置，收费探测默认关闭；需要真实连接测试时单独说明成本并取得授权。

共享订阅、组织或项目下的多凭据归入同一 `quota_domain`，不能重复计算供给。供应者本人仍使用账号时，平台无法仅凭本地计数保证实时剩余；支持个人保留预算、保守并发和陈旧数据降级，不宣传精确可售token总量。

## 5. 身份与凭据接入

分别核验：门户登录主体、上游账号控制权、对外供给权利。不能仅按邮箱相同认定归属。

- 使用提供方支持、且本产品有资格使用的授权方式；state、PKCE、回调白名单和单次消费按实际协议实施。
- 同一上游身份去重，重复归属进入审核。
- 不收集账号密码、2FA种子或浏览器会话文件作为接入方式。
- 凭据服务端加密入库；供应者门户无解密/导出权，不进入日志、审计正文、客服或队列。
- 授权撤销先阻止新请求并使亲和/缓存失效；在飞请求按已约定政策处理，不能等下次刷新。
- 刷新结果不明时隔离并要求恢复/重新授权，不重放旧refresh token。

现有credentialcrypto组件尚未完整接入运行时凭据存储。正式托管第三方供应者凭据之前，必须完成加密上下文绑定、轮换、恢复和最小解密权限验收。同一代码库不等于共享秘密或数据库权限。

## 6. 拟新增领域模型

以下不是已实现表，名称可随实现调整：

| 实体 | 关键内容 |
| --- | --- |
| Supplier / SupplierMembership | 合同主体、组织成员、权限、风险、收款资料引用 |
| SupplyGrant | 供给许可、范围、证据、审核和有效期 |
| SupplierResource | supplier与现有Account关联、归属、quota domain |
| EntitlementSnapshot | 套餐、模型、窗口、余量及来源时效 |
| SupplyOfferVersion | 模型、单位、币种、采购单价、限额、部分失败规则 |
| Request / Attempt | 逻辑请求、每次实际尝试、授权/价格快照及发送状态 |
| UsageFact | 可信计量、上游请求标识、语义、修订及未知原因 |
| SupplierAccrual / Journal | 幂等应付计提、借贷分录及冲正关联 |
| Payout / Dispute | 付款状态、幂等键、渠道证据、争议及冻结 |

资源状态：`draft → verification_pending → approved → active`。

旁路状态：`paused`、`suspended`、`reauthorization_required`、`revoked`、`expired`。只有active且全部门禁通过才能发送请求；已撤销资源不能因亲和继续接单。

## 7. 定价与可靠计量

MVP采用按模型、按可验证usage的固定版本化单价，不先做竞价。

结算金额为各类合格计量单位乘对应合同单价，再按约定调整。明确input/output、cache read/write、reasoning是否包含在output，避免重复计数；规定部分流、取消、失败、内部重试、测试流量、币种精度、舍入、手续费与税费。

客户售价与供应采购价独立快照。客户费用、账号倍率、OAuth月费不能直接当供应者应得金额。客户未收费不代表没有平台承担的上游采购成本。

每个attempt持久化授权与报价版本、供应资源、请求/实际模型、发送事实、上游request ID、usage来源及可信度。不能仅靠异步日志、内存回调或每天SUM usage_logs打款。

| 情形 | 结算处理 |
| --- | --- |
| 确认未发送 | 按统一预算允许重选，不计使用费 |
| 明确拒绝且无消费证据 | 记录原因，按合同处理 |
| 成功、usage可靠 | 按快照计提 |
| 部分流、usage可验证 | 按合同计提部分使用，不透明重启 |
| 已发送后超时/断流、usage不明 | pending_reconciliation，不估成零或直接付款 |
| DB提交结果不明 | 查幂等结果，只恢复本地提交，不重新生成 |

客户逻辑请求与供应attempt分开，内部回退不得意外重复收客户款；每个实际供应消耗按合同独立归责。

## 8. 应付子账与付款状态机

保留唯一客户账本，新增供应者应付子账，使用NUMERIC/decimal及按币种平衡的双分录。

- 计提：借供应采购费用，贷待释放应付。
- 解冻：借待释放应付，贷可支付应付。
- 提现占用：借可支付应付，贷付款处理中应付。
- 付款确认：借付款处理中应付，贷现金/支付清算。

退款、欺诈追回、纠错追加冲正，不能改历史分录；客户善意退款不必然扣供应者收益，按合同归责。

状态：`observed → validated → accrued_held → payable → payout_pending → paid`，另有`pending_reconciliation`、`disputed`、`reversed`。

幂等键覆盖attempt、计量修订和报价版本。可靠本地事务落结算状态及财务outbox，再幂等计提并定期数据库对账。付款请求超时须使用原幂等键查询，不盲目再次打款；回调验签、去重和乱序处理。

## 9. 风控与隐私

- 防止盗号、重复供给、伪造usage、供应者与客户自买自卖、盗卡消费后提现。
- 平台可信计量及提供方证据为准，不接受自报即付款。
- 新供应者低限额、延迟释放、滚动准备金；付款审批与执行分权。
- 收款主体核验，收款变更二次验证及冷静期；税务/地区/付款渠道另审。
- 冻结须有原因、范围、期限和申诉渠道。
- 供应者只能看自己的计量与结算证据，不能看客户prompt、响应、身份和Key。

## 10. 现有代码接入点（实施前复核）

- `backend/internal/service/openai_account_scheduler.go`：现有调度选择，增加供应资格及发送前复核，不另建调度器。
- `backend/internal/service/usage_billing.go`、`backend/internal/repository/usage_billing_repo.go`：现有客户幂等计费，不等于供应者应付已实现。
- `backend/internal/service/gateway_usage_billing.go`、`backend/ent/schema/usage_log.go`：用量链路与持久字段，后续接attempt/UsageFact及财务outbox。
- `backend/internal/service/oauth_refresh_state.go`：持久刷新屏障与歧义隔离。
- `backend/internal/pkg/credentialcrypto/codec.go`：加密基础，运行时集成仍为上线前置。
- `frontend/src/router/index.ts`、`backend/internal/server/routes/user.go`：可复用路由/鉴权组织，但供应者必须独立权限与资源归属检查。

## 11. 分阶段实施与验收

### P0 供给可行性

核实提供方许可、合格授权方式、权益/计量来源、经营主体和付款法律税务方案。无许可证据资源无法激活；个人订阅不获许可则不开放该供给模式。

### P1 邮箱自助门户与入口隔离

邮箱注册/登录、验证码自助开通、本人资料，无人工注册审核。独立域名对应供应专用前端和 API 白名单入口；测试双向令牌/刷新隔离、消费者与管理 API 不可达、验证码用途隔离、单次消费、邮箱变更重新验证、重复开通及资料版本冲突。不自动接单或付款。

后续资源接入再实现权利材料、归属、来源展示、暂停和撤销，测试跨租户越权、重复归属、过期、回调重放、撤销传播。供给权利核验不是门户注册审核。

### P2 小规模获许可供给

共享quota domain、资格硬过滤、durable attempt与价格快照。未知额度不能显示充足，撤销不能被亲和绕过，收费探测默认关闭。

### P3 影子应付与付款沙箱

先计算但不支付，对照原始事实与客户账务。覆盖重复事件、强杀、提交不明、部分流、冲正、付款超时及回调重放；未决款不可提现。

### P4 有限开放

白名单供应者、固定价格、低限额、人工审批，稳定后扩大。Claude独立审核授权及能力接口。

## 12. 待决定与明确未完成项

1. 提供方允许哪种商业供给，适用哪些主体/地区？
2. 是否存在本产品可用的官方授权、权益及计量接口？
3. 无精确余量时是否接受未知与保守准入，不销售保证token容量？
4. 支付单位、失败/未知消费、客户拒付由谁承担？
5. 收款、身份、税务和地区限制如何满足？
6. 第三方凭据托管前完成加密与恢复，建议不可跳过。

本文件未授权开发或上线交易、导入账号、调用真实模型、修改AWS资源或转账。具体实施按另行确认范围推进。

## 13. 当前实施记录（2026-11-03）

### 邮箱会员基础：已提交、已本地验证、未部署

提交 `0f66938a1`，分支 `feat/control-supplier-portal`：

- 新增前向迁移 `253_supplier_profiles.sql`，本人资料、唯一用户绑定、并发开通幂等、版本 CAS；当前用户邮箱变更后必须重新验证。
- 供应者验证码与普通注册验证码用途隔离，原子单次消费；发送与开通接口按用户/IP限流，Redis不可用时拒绝，严格拒绝外部设置归属/状态等字段。
- 门户会员自助开通无需人工审核，不产生上游供给、调度、收益或付款权限。
- 后台聚焦命令：`SUPPLIER_TEST_PG_SOCKET=<临时本地 socket> GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 go test -p=1 ./internal/service ./internal/repository ./internal/handler ./internal/server/routes ./internal/server/middleware ./cmd/server -run TestSupplier -count=1 -v`：17项通过，实际执行本地 PostgreSQL 15.19 并发测试；`cmd/server` 编译及默认测试 vet 通过。Wire生成、`git diff --check` 通过；此批未单独执行完整 `go build`。
- 前端聚焦124项通过；Node24环境 `pnpm typecheck`、`pnpm build` 通过，构建有非阻断的Browserslist/Node弃用/大chunk/混合导入警告。
- 本地临时PG已停止并清理；未发送真实邮件、调用模型或操作AWS，未应用迁移到真实RDS。

### 独立供应入口：已实现，本地测试与两套生产构建通过，未部署

用户要求先提交会员基础后实现独立供应域名、专用 API 和会话隔离。本批实现独立前端构建和供应 Control 入口，保持一个 Gateway/调度器；完整验证结果待测试完成后追加。不得将上一批共享JWT和共享前端测试作为独立入口验收。

当前代码设计（开发中，非部署说明）：

- `supplier_portal.enabled` 显式选择专用 Control 进程，要求 split Control 与数据库 check-only 模式。
- 必须配置精确 `supplier_portal.host`、匹配的 HTTPS `supplier_portal.origin`、与消费者密钥不同且至少32字符的 `supplier_portal.jwt_secret`；密钥只通过外部秘密配置传入，不写入Git。`registration_enabled` 独立控制供应者注册。
- API-only 后端不提供消费者SPA，只显式挂载邮箱注册/验证码、登录/2FA、刷新/退出、最小身份、最小公开配置和本人供应资料；其他接口不注册。消费者入口移除原共享供应路由。
- 前端 `pnpm build:supplier` 使用独立模块图，产物为 `frontend/dist/supplier/index.html` 及其静态资源，不是 `dist-supplier`。构建有消费者模块导入守卫；部署时静态站点与供应API保持同源。
- 反代必须保持配置的Host，且只将API请求转发到供应专用监听端口；未知API不得回退到前端HTML。静态站点独立的SPA路由回退不应用于API。当前不增加正式反代配置或开放端口。
- 如果配置了尚未支持的强制登录协议/captcha策略，供应流程拒绝并展示不可用，不能静默跳过既有安全策略。

后端隔离批本地验证：

- `GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 go test -p=1 ./internal/config ./internal/service ./internal/repository ./internal/handler ./internal/server/routes ./internal/server/middleware ./internal/server ./cmd/server -run 'TestSupplier|TestCloudRole|TestRuntimeHTTPRole|TestCloudGateway|TestCloudWorker|TestCloudCommon' -count=1 -v`：32个顶层测试通过，1项现有本地PG opt-in测试因未提供socket跳过；此批无schema变更。上一批实际PG验证不能代替本批跳过项。
- `-tags=unit` 下既有 RefreshToken/GenerateToken 与供应测试通过（0.329s）；独立 `go build -p=1 -o /tmp/supplier-isolated-server ./cmd/server`、上述8包显式 `go vet -p=1`、Wire生成及 `git diff --check` 均通过。
- access token 使用供应 audience及独立签名，覆盖双向拒绝、过期令牌旧刷新路径及签名边界；refresh用户/家族索引、TOTP pending/setup/attempt/stepup、邮箱proof缓存分别隔离。
- 供应者注册要求供应用途邮箱证明，不赠额、不授订阅、不初始化返利；已有用户登录仍要求原有TOTP。仅记录注册来源及登录时间不属于消费者赠送权益。
- 限制：供应专用监听进程仍复用Control DI与生命周期，并非依赖图/数据库权限已完全最小化。尚无真实反代、TLS、域名或公网端到端验证。

前端隔离批本地验证：149项测试/18文件通过；Node24下串行 `pnpm typecheck && pnpm build:supplier && pnpm build` 全部exit0。供应构建99模块/5.26s，产物 `frontend/dist/supplier/index.html`，manifest动态页面仅Entry/Auth/Portal，消费者模块导入守卫通过；原站1098模块/1m18s构建通过，i18n 3项通过。无依赖或lockfile变化。原站构建输出至已有backend embed目录，不覆盖供应产物。

真实域名尚未提供，本批不修改DNS、TLS、AWS或正式部署。

### Session JSON 与窗口接入：前后端本地测试/构建通过，未接真实上游及正式数据库

用户要求复用Sub2API既有能力，不另写导入器或探测器。已定位管理员导入 `backend/internal/handler/admin/account_codex_import.go`、前端 `CreateAccountModal.vue` 的 `handleOpenAIImportCodexSession`、`OpenAIQuotaService.QueryUsage` 及现有窗口调度字段。

实施检查发现：现有Account凭据字段仍为明文JSON；`credentialcrypto`只有独立codec，尚未运行时接线。既有窗口QueryUsage还可能经tokenProvider刷新，并额外查询reset-credit details。因此不直接将管理员handler或完整QueryUsage开放给供应者。

本批最小集成边界：

- 抽取复用已有session解析逻辑，新增供应者受限接口与所有权校验，不复制整套管理员导入流程。
- 新增前向迁移254（最终文件名待实现），供应资源加密信封绑定资源UUID、供应者和用途；不同时保存明文副本，不写既有明文Account，不进入调度池。
- 复用credentialcrypto；显式本地密钥包装器只用于本地/测试选项，不冒称AWS KMS。外部密钥文件严格权限校验；没有密钥时导入拒绝，不自动生成或降级明文。正式托管仍需密钥轮换/恢复和部署验收。
- 在现有quota service内增加明确只读窗口入口，复用客户端及解析，固定上游地址，使用短暂解密材料；不调用自动刷新、reset credit查询/消耗、隐私修改或收费模型请求。
- 供应门户只展示本人资源、套餐/窗口观察值与时间，未知和陈旧明确标记；支持暂停/断开，不宣传自动续期或精确可售token总额。
- 验证仅本地mock/loopback与临时数据库，不调用真实ChatGPT/Codex，不操作AWS或正式数据库。此段为实施决定，不是已完成或验证通过记录。

后端接入批验证（2026-11-03）：

- `SUPPLIER_TEST_PG_SOCKET=<临时socket> GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 go test -p=1 ./internal/pkg/credentialcrypto ./internal/pkg/codexsession ./internal/config ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin ./internal/server/routes ./internal/server/middleware ./internal/server ./cmd/server -run 'TestSupplier|TestLocalWrapper|Test.*Codex' -count=1 -v`：534个顶层测试通过，0跳过。涵盖原管理导入回归、本地PG254重复迁移/归属/CAS/碰撞/断开重导入、加密篡改/上下文互换/key ID/旧key、loopback只读单GET且无重置/刷新/重定向/token回显。
- 当前服务端独立build、相关包显式vet、Wire生成、diff空白检查通过；临时PG已停止清理。未访问真实上游/邮件/AWS，未应用正式数据库迁移。
- 密钥配置：`SUPPLIER_PORTAL_VAULT_KEY_FILE` 为绝对路径的外部Linux本地密钥文件，root或当前uid所有、0600、路径及文件禁止符号链接；内容结构为keys映射（key ID至64位十六进制），不在文档提供可用密钥。`SUPPLIER_PORTAL_VAULT_KEY_ID` 选择当前写入key。缺少/无效配置返回503，不降级明文。
- 支持文件保留旧key ID后重启，实现读旧写新；不支持热加载、自动重包裹或完整轮换/灾备流程，不等同AWS KMS部署。
- 限制：导入身份/套餐声明未经上游验证，成功只读快照的套餐优先显示；逆向的legacy Account创建尚不查询供应vault，不保证全局排他性。导入只在短事务内锁表查重，锁等待500ms/语句2s；列表只返回最近100条，尚无分页。所有资源paused且不关联Account/调度。

本机反代暂存验证：新增 `deploy/openresty-local-compose.yaml` / `openresty-local.conf`，Alpine OpenResty固定digest，仅发布127.0.0.1:8088，非root/readonly/128MiB。消费者页面200/API401，Gateway health200，供应页面200，未知Host404；供应API因专用进程未部署返回502，没有回退普通Control。首次临时目录readonly启动失败已修复，nginx -t通过。不代表真实TLS、DNS或新版session UI验收。

资源前端最终验证：完整159项测试/19文件通过，套餐申报/观测标签后补测试11项通过；串行 `pnpm typecheck && pnpm build:supplier && pnpm build` 全部exit0，供应102模块/5.31s、原站1m53s，i18n3项通过。供应产物仍为 `frontend/dist/supplier/index.html`，manifest仅Entry/Auth/Portal且消费者模块守卫通过。重建后经运行中OpenResty本机测试Host访问新版供应index为200，nginx -t再次通过；仅验证静态文件可服务，不是浏览器交互或真实供应API端到端验证。后台专用进程尚未部署，API仍不可用。

### 持久自动窗口任务（2026-11-04）：已实现、本地验证，未部署

新增255迁移及PG持久队列；Worker明确opt-in，默认10分钟±20%抖动，30秒补偿扫描、每批2条顺序处理。短事务认领后执行只读查询，结果按资源版本、凭据版本、lease owner/generation CAS提交；60秒租约、20秒上游超时。429尊重Retry-After（上限24小时），瞬时失败有界退避，401/403/过期停止并要求重新连接；暂停/断开取消任务。旧资源默认不开同步，新导入入队；同步恢复不代表恢复调度。邮件配置状态仅表示配置齐全，不证明投递成功。

后台最终40个顶层测试通过、0失败/跳过，真实临时PG执行255重复迁移、租约接管/拒绝旧结果、版本CAS、401隔离、暂停取消/恢复、429计划及到期断言；另6项热重跑通过。当前build、显式vet、Wire、diff检查通过，临时PG已清理。无真实probe或邮件。前端165项测试通过；最终 `pnpm typecheck && pnpm build:supplier && pnpm build` 全部exit0（供应102模块/5.35s、原站1m21s、i18n3项通过），模块边界守卫及diff检查通过。源码已冻结。此批自动probe状态UI与Worker代码已验证，不代表线上Worker已启用或自动入池已完成。

### 当前部署状态（2026-11-04）

四域名HTTPS证书与Cloudflare回源可用；供应专用Control本地固定image ID运行，迁移253/254已通过独立migrator应用，255尚未应用。供应公开配置200、未登录资料401、admin及推理路径404；IMDS访问阻断。专用健康检查使用供应Host和最小公开配置，不能沿用通用/health。邮件尚未配置，真实邮箱注册不可用；后台自动Worker未启用、自动入池未实现。

凭据、私有环境、密码文件及密钥均在仓库之外，不记录其内容。当前用户要求先分批提交/推送供应分支，再从其新建模块化上游分支；不将后续架构工作与此批验证混在一起。

## 参考

- https://openai.com/policies/terms-of-use/
- https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan
- https://help.openai.com/en/articles/20001542-using-your-chatgpt-plan-in-other-apps-and-sites

上述官方页面需在实施前取得当前有效原文并审核；初步检索摘要不构成商业许可确认。
