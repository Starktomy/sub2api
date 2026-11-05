# 供应调用统计与双模式费用实施记录

日期：2026-11-05。用户明确要求显示每个资源的调用次数、输入/输出token；后台同时支持标准模型费用比例和客户实际计费金额比例；不做提现。

## 现网已修复

供应资源导入已默认auto_sync_enabled=true并入持久任务队列。此前部署worker未启用SupplierProbe且缺vault挂载，任务一直pending。现已通过主机监督器持久启用仅worker的SupplierProbe与只读vault挂载，独立供应Control重启恢复。资源实际状态fresh、已保存usage及last_probe_at。每30秒取任务，成功间隔600秒带±20%抖动，错误有界退避；401/403需重新授权，不自动刷新token/不生成推理。

原主机探测配置已同步到仓库deploy/ec2-role-deploy.py，并补充gateway只读vault挂载与精确supplier ingress例外；14项离线测试通过。新脚本尚未安装到主机，不把源码修改当成运行配置已切换。

## 两种费用模式

- standard_cost：现有模型标准费用TotalCost × ratio。
- customer_charge：现有实际计费金额ActualCost × ratio。包括订阅的实际消费计费金额，不等于现金支付。
- 比例按basis points整数0..10000，版本>=1，金额与现有账务一致保留8位小数。
- 不设置未经批准的默认分成比例。尚未配置时费用未知/待配置，不显示伪造已结算收益。
- 该比例表示供应费用/应计金额，不是额外向客户加收，也不是可提现余额。
- 每笔记录冻结mode、ratio、version、base和fee，规则变更不追溯重算。

## 统计口径

调用次数第一版=成功提交的计费用量记录数，不是每次外部尝试。额度探测、导入、模型列表查询不算推理调用。输入、输出、缓存读写token分项，避免total重复相加。失败/部分输出如有可信消费应单独pending，不虚构0或自动重发。

stats必须按资源/供应者owner过滤。不泄露客户ID、API Key、提示词或输出。历史事件保留，断开资源不删除旧统计。

## 关键实施顺序

1. 一资源一Account引用桥接，使用现有分组/并发/调度，不建第二账号池。
2. Account不存明文token。执行时从供应vault按resource/version/owner校验后解密；旁路OAuth refresh/shared token cache；过期/断开/未批准失败关闭。
3. OpenAI GetAccessToken与ChatGPT account header同时接引用解析；HTTP/WS和shadow都覆盖。
4. 普通admin入口管理资源接入、分组和费用策略；supplier域名只开放owner-scoped统计，不开放admin。
5. 复用UsageBillingRepository.Apply同一事务：dedup成功→现有客户effects→供应调用/费用事实→commit。不能聚合best-effort usage_logs充当应付财务真值。
6. 上游已有TotalCost/ActualCost和token分类，冻结供应policy；重复request/apiKey只记一次。提交不明只本地查询/重放同一账务命令，不重发模型。
7. supplier资源显示统计与费用状态；admin可选两模式/比例/版本，不实现提现或转余额。

## 后续用户确认与当前实施

用户进一步要求：校验成功后自动接入后台配置的对应分组，admin独立供应商管理入口集中所有供应配置。该需求已确认；分组必须显式配置、活跃且兼容OpenAI/composite，不随机选择现有组。

当前已实施（正在验收，尚未部署）：独立admin供应管理与版本CAS设置；256持久资源/Account绑定及探测版本验证；同事务分组同步及调度outbox；257不可变供应计费事实与现有客户扣费同事务提交；owner-scoped统计API。自动接入允许显式开启，但默认关闭，必须由管理员选择兼容分组与费用规则。现网仍仅自动套餐/额度探测及session前端精简已上线。

## 当前边界

资源桥接已编写但未部署。Account仅保存资源引用及非秘密上游账户ID；执行时重新验证当前策略、所有目标分组、owner、绑定与凭据版本、15分钟内成功探测且非未来时间、有效期和账户身份，随后解密。不得把调度快照当授权。供应资源暂不支持WebSocket（包括HTTP请求选择上游WS传输），以避免已认证连接池绕过撤销；仅支持逐请求重新校验的HTTP。simple mode及通用非OpenAI执行入口拒绝供应资源。

新表使用前向迁移，不修改254/255；真实数据库应用需单独确认。现网账户不盲目接入分组；未配置分组或价格应保持不可调度。

## 当前验证结果

session完整结构精简前端13测试、codexsession包测试及typecheck通过；精简后的供应静态页面已发布。新增admin页/配置与导入组合17测试、typecheck通过；页/路由/导航ESLint及locale完整性通过。2026-11-05后端TestSupplier(Fee|Operating|AdminInventory|ExecutionCredentials)聚焦测试通过0.023s，覆盖双模式计算、设置验证、投影脱敏及vault执行解析基础。较早重复编译因内存压力停止，不计通过；handler/routes整链编译另有后台验证尚待完成。未部署admin新API/UI，未开启接池或费用记账，未新增迁移。

## 验证门槛

- 密钥只读挂载/缺失fail-closed；Account/cache/export无token。
- stale snapshot断开仍拒绝，资源credential CAS、并发接入/重导入正确。
- 标准与实际计费模式、0比例、上限、8位舍入、NaN/Inf/负值拒绝。
- 并发dedup只一次、账务回滚时供应事件也回滚、重复结算不随新policy重算。
- 供应owner隔离、历史不被删除、无提现接口。

## 2026-11-05 当前增量验证（持续更新）

- `GOMAXPROCS=1 GOMEMLIMIT=700MiB GOPROXY=off go test -p=1 -tags=unit ./internal/repository -run '^TestSupplierUsageBilling' -count=1`：通过，0.019s（当时仅unit文件；之后新增PG测试需独立验证）。
- `python3 -I deploy/tests/ec2-role-deploy-test.py`：13项通过。脚本已保存worker探测配置并新增gateway只读vault挂载；普通Control不获vault。尚未同步到主机运行版本。
- 独立本机PG首轮因peer身份不匹配失败（root进程连接postgres），未执行测试逻辑；通过该测试集群专用pg_ident映射重跑，不放宽TCP或设置空密码。
- 费用policy在首次账务事务内冻结，不是发送上游前冻结。重复已提交请求不重读新规则。禁止因本地提交不明重发模型。
- admin设置要求显式version/auto_admission/fee_ratio_bps，零值不是缺省推断；保存采用CAS。尚未选择任何真实分组或分成比例。

### 增量复核

- 第一轮服务/路由回归：service通过0.462s、server通过0.017s；routes因另一份精确allowlist未加入新stats路径失败。已补齐该allowlist，重跑中；不把该轮称为全绿。
- 独立PG admissions/probe/financial tests通过0.965s；之后追加真实执行撤销、缓存投影和重导入轮换断言，需以最终重跑为准。
- 前端33项统计/设置测试及类型检查通过；新增admin实际接入列10项测试通过。尚未构建或发布新静态产物。
- 部署工具离线测试现14项通过，新增只允许精确命名、Compose项目/服务匹配且supplier模式开启的独立Control留存，不豁免普通/未知worker。
- supplier资源HTTP强制使用http_sse，WS入口及构建上游WS头拒绝；不让全局WS设置导致HTTP供应请求意外进入连接池。
- 同owner同上游身份重导入保留UUID/Account/binding，使用原AAD加密、资源及凭据版本CAS，清除旧验证并重置探测lease；在新探测成功前执行失败关闭。
- 新迁移256/257尚未获得该两项线上DDL的单独确认，尚未应用。已有255授权不能默认为新财务迁移授权。未启用真实自动准入或设置未经选择的分组/比例。

## 上线操作顺序（尚未执行）

1. 全部聚焦回归与二进制/双前端构建通过；先保留旧镜像、静态和release配置备份。
2. 单独确认256/257线上前向迁移；使用现有us-west-2专用integration deploy角色及migrator，不用root或runtime执行DDL。
3. 先应用迁移并用runtime只读check确认manifest与新表权限，再切换任何新gateway/control/worker。否则新客户账务对supplier_account_bindings的查询会因缺表回滚。
4. 本机预载镜像，不推ECR、不改DNS、不新增收费基础设施。安装已测试主机监督器，gateway/worker仅只读vault；consumer Control不挂vault；独立supplier Control保留原隔离认证与vault。
5. 更新consumer和supplier独立静态图，验证admin页面路由、owner stats鉴权、健康和自动探测。不得仅以health成功宣称真实推理/账务已验。
6. 自动准入默认关闭。管理员选择真实兼容分组、两模式之一及明确比例后才启用；没有选择时不代填运营决策。真实供应调用需在既有账号授权范围和限定用量内另验，不能伪造费用/统计。

额外边界：供应HTTP执行必须带认证中间件的分组上下文，且该组仍在当前供应策略内；避免策略改组后旧scheduler快照在旧组继续调用。无分组的管理直调失败关闭。

### 验证冻结点

最终聚焦PG回归（含轮换与投影）已通过1.091s；同轮server/routes及session包通过，但service/handler因编译期间源码新增ctxkey import，出现could not import/open空路径失败。该轮不计整体成功。停止源码修改后，重新串行执行PG、service/routes/handler和server+migrate构建，以新日志final2结果为准。

## 2026-11-05 用户再次要求上线后的执行记录

- 用户再次明确“上线”，此次范围包括必要的256/257前向迁移及现有EC2服务/静态更新，不新增基础设施、不发布ECR、不改DNS。
- 最终后端聚焦测试全部通过：service0.850s/server0.024s/routes0.026s/handler0.468s/admin0.047s/session0.003s；server及migrate静态二进制构建完成exit0。
- 本机镜像已构建并按digest预载：`sub2api-cloud@sha256:b7146a333a49e21e6cf0fd07a59522326026044b8748e053874a43662f2b5b5d`，未推送。
- 256/257已由专用integration deploy角色读取migrator凭据后成功应用；manifest只读检查及runtime新表读写权限检查通过，无TRUNCATE/schema CREATE。
- 双前端构建进行中；此记录点尚未切换服务和静态。旧服务健康。备份位于`/var/lib/sub2api-release-backups/supplier-20261105`。
- 运行策略尚未配置，自动准入默认关闭；未选择真实分组或分成比例，未执行模型生成。

### 上线验收完成

- 双前端2048MiB独立类型检查与consumer/supplier生产构建通过。四个角色切到上述digest并全部healthy；静态index与发布产物逐字节匹配。
- 通过真实HTTPS origin（curl --resolve到127.0.0.1，正确SNI及证书验证）检查admin.aquawhale.net/admin/suppliers和supplier.aquawhale.net/，HTML及6/2个入口资源全部200。之前误用8443宿主端口/缺SNI的两次检查失败，已按实际443映射及SNI修正；未改变TLS配置。
- 真实认证HTTP验收：admin策略/供应资源列表200（1条资源）；owner列表/统计200；未认证及跨consumer/supplier令牌401；supplier域admin路径404；未知资源stats404。首轮smoke误假设users.token_version列，查询失败；改为现有email+password_hash指纹算法后通过，不改用户或数据库。短期token只留内存、不落盘不输出。
- worker自动探测fresh、auto_sync=true、无错误、verified版本匹配且15分钟内更新。gateway/worker只读vault挂载验证通过，普通Control无vault。
- 自动准入未开启、bindings=0、supplier_usage=0。这是未选择目标分组及费用策略的预期安全状态，不等于已经真实供应推理成功。未生成模型请求、未伪造收益、未提现。
- Git未commit/push；镜像未推ECR；无DNS或新AWS基础设施变更。

## P0 供给授权与接单快照优化（2026-11-05，源码与聚焦回归完成，未上线）

用户确认已取得上游授权；本次按授权资源使用设计，不把该陈述扩展为其他供应商/套餐的通用许可。

### 已确定产品规则

- 导入入口按钮与表单提交按钮均为：授权全部可用额度并开始供给（最后复核已补齐入口文案断言）。持续授权覆盖当前及后续重置窗口，直到供应商撤回或授权到期；不预留散户自用额度。
- 操作：开始供给、暂停供给、更新授权、断开并删除凭据。暂停接单与只读额度探测分离；更新授权不先删除旧凭据，保持资源/Account及历史收入；新凭据验证成功后恢复供给。
- 平台独立禁供标记，供应商更新授权或开启供给均不能清除。解除平台禁供也不能凭空补同意或覆盖供应商暂停。
- 供应商保留调用/token统计，费用只展示累计总金额，不公开动态比例/内部成本/客户信息；后台逐请求冻结适用的供应计酬策略。无提现扩展。
- 五道检查复用单一网关与调度器：授权、凭据、模型/分组资格、所有适用额度窗口、现有并发槽位；未知额度不能按0占用/无限额度处理。

### 已实现并纳入回归的可靠性约束

- 新前向258保存显式同意版本、授权版本/到期/撤回及独立禁供；已有资源不静默补同意。
- 新前向259保存服务器生成请求/尝试标识、资源/供应者/Account、凭据与授权版本、计酬策略快照；不存prompt/output/headers/token/envelope。
- 发送前持久化接单及发送意图；数据库提交不明确时不发送。可信用量在异步队列之前持久化，构建的记账命令持久化后才执行既有UsageBillingRepository.Apply。
- 客户扣费、供应事实、幂等与任务完成同事务；后台恢复只重放同一条本地记账命令，不重选账号、不解密、不调用上游、不重新取价格计算。
- 已发送但没有可信用量、进程在用量落盘前退出、定价失败均保留待核对，不编造0消费；该边界不承诺从不存在的证据恢复精确token。
- 供应计酬mode/比例/version在接单时固定；模型客户侧全部价格表/倍数的预发送版本化不在本批最小快照中，不混称两者。
- 仅对完成持久化插桩的供应HTTP路径放行；未完成的WS/插件/媒体/其他兼容路径先拒绝，不影响普通非供应渠道。具体支持范围以最终验证记录为准。

上述源码已实现；验证范围及未完成项见后文。尚未应用线上258/259、未发布新镜像/前端、未进行真实上游调用、未提交推送。不把聚焦测试通过等同于已上线或真实供应推理成功。

### P0 当前验证进度

- 前端定向Vitest首轮因node未在PATH退出127；使用现有Node工具链后，10文件80测试通过。最终复验曾因从backend目录启动而退出127，改为frontend目录后10文件83测试通过（14.52s），含最后授权有效期与断开后更新授权修正。
- 最终前端命令：`cd frontend && PATH="$(cat /tmp/sub2api-node24-toolchain-path)/bin:$PATH" NODE_OPTIONS=--max-old-space-size=512 ./node_modules/.bin/vitest run --maxWorkers=1 --minWorkers=1 --no-file-parallelism src/supplier/__tests__ src/views/admin/__tests__/SuppliersView.spec.ts src/api/__tests__/supplier.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts`。
- 最终`NODE_OPTIONS=--max-old-space-size=2048 vue-tsc --noEmit`通过；最终供应资源/管理页/API/types、相关测试及en/zh locale范围ESLint通过（exit0）。
- `python3 -I deploy/tests/ec2-role-deploy-test.py`：14项离线测试通过（0.025s）；其中容器健康/回滚文字来自测试替身，不是本轮实际部署或回滚。
- 后端源码已冻结，统一串行验证。首轮因额度解析拆分留下未使用的encoding/json导入而编译失败，未执行断言；已删除该导入。第二轮repository（含实际PG测试）通过3.800s，handler通过7.065s，routes通过0.030s，middleware通过0.026s；service因新增测试替身未实现既有batch-image hold接口编译失败，已补齐替身后继续复验，不能将第二轮称为整链通过。数据库测试仅允许独立/tmp/supplier-pg-*本机socket，不操作线上RDS。
- 第三轮service通过5.695s、handler通过7.090s、routes/middleware/config通过；server精确allowlist因漏列新增start路径失败，已补齐。随后补强独立恢复worker注册与供应无个人预留阈值测试，冻结后再次统一复验。
- 最新冻结验证命令（日志`/tmp/supplier-p0-pg-final.log`及`/tmp/supplier-p0-backend-final.log`）：在backend目录设置`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off SUPPLIER_TEST_PG_SOCKET=/tmp/supplier-pg-20261105-eADiDK`，依次执行`go test -p=1 -tags=unit ./internal/repository -run 'TestSupplier' -count=1 -v`与`go test -p=1 -tags=unit ./internal/service ./internal/handler ./internal/server ./internal/server/routes ./internal/server/middleware ./internal/config -run 'Supplier|UsageBilling|OpenAI.*RecordUsage|OpenAI.*UsageRecordTask|OpenAI.*Responses|OpenAI.*Shadow|OpenAI.*Token|MaintenanceTaskPolicy|RuntimeLifecycle|OpenAI.*Schedul|ParentHealth|Advanced.*Schedul|BuildUsageBilling|ApplyUsageBilling|Quantiz|Normalize' -count=1`。最终通过：repository4.439s（9项PG测试实际执行，无skip）；service8.288s、handler7.144s、server0.025s、routes0.031s、middleware0.036s、config0.013s。PG覆盖准入、owner/CAS、probe、原子财务回滚、冻结policy/credential/auth归属、未知/冲突观测、并发Mark、并发撤回和并发恢复。
- `GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off go generate ./cmd/server`通过；Wire生成前后SHA256均为`0a4c0c71f1564021fb45b89fa39b3682a34526e2ebb8d328e0a88c7548289c83`，无go.mod/go.sum变更。
- `cd frontend && NODE_OPTIONS=--max-old-space-size=512 ./node_modules/.bin/vite build --config vite.supplier.config.ts --outDir /tmp/supplier-p0-ui-build-VEysQ8`通过（9.12s）；supplier-boundary插件通过，产物只写独立临时目录，未覆盖现网bind mount。Browserslist旧数据警告未阻断；本轮未联网更新依赖。consumer整体生产构建未重跑，admin源由最终typecheck/ESLint/Vitest覆盖。
- 最后入口文案复核将打开导入窗口按钮也统一为完整授权文案；同一前端命令复验10文件84测试通过（22.74s），变更组件/测试ESLint通过，供应UI重新构建到独立`/tmp/supplier-p0-ui-final-woh2it`通过（8.17s）。此小修只替换已存在i18n key并添加断言，最终typecheck完成于该文案修正之前。
- 额外`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off go test -p=1 ./internal/service -run 'TestMaintenance|TestRuntimeMultiReplica' -count=1`通过（0.099s）。随后相同资源限制下`CGO_ENABLED=0 go build -p=1 -o /tmp/supplier-p0-binaries-ww9Khw/server ./cmd/server`及`CGO_ENABLED=0 go build -p=1 -o /tmp/supplier-p0-binaries-ww9Khw/migrate ./cmd/migrate`均通过exit0；server SHA256=`e204a6886023f4f1b67368b74568e45e994b25fe3be59ec36f309ff8cae684dc`，migrate SHA256=`dd6988686e193974dd2639a1a579a264638ddd234289a1ad4163f9df167cdc70`。仅独立临时产物，未替换运行二进制。
- 最终`git diff --check`通过；已应用迁移256/257与go.mod/go.sum无修改。
- 本轮新增迁移258/259尚未应用线上；无真实模型请求、部署或提交。未执行真实HTTP全进程供应生成验收；gateway/parser集成测试使用合成凭据与内存上游替身，PG测试使用独立临时schema，不能称为真实供应调用。

### P0 执行范围与恢复边界

- 放行原生HTTP文本Responses及compact（`/v1/responses`、`/responses`、`/v1/responses/compact`、`/responses/compact`），JSON/SSE均插桩；允许客户端function/custom/namespace工具。不支持托管工具、媒体、WS、插件、passthrough、Chat/Messages及其他未插桩入口；普通非供应渠道不改变。
- 接单冻结供应者/资源/Account、客户/API Key/分组、映射后模型、凭据/授权/同意版本及供应计酬mode/比例/version。当前客户价格表和倍率仍在观测后按现有逻辑求值；命令一旦落盘则恢复绝不重新定价，不承诺完整价格表的预发送冻结。
- 每次物理发送必须先提交一次性发送标记；关闭重定向、可重放请求体与幂等头触发的传输自动重试。提交不明、上游错误及本地记账失败均不得触发供应生成重放。
- 完整终态用量即使后续解析/客户端传输失败仍可结算；未知、部分、冲突用量保留证据待核对。观测已落盘但未保存结算命令的崩溃窗口不会按新价格补算，超过48小时转review。没有上游证据不能承诺恢复精确token，也不能计成零。
- 已保存命令经数据库租约及有界退避重放；客户财务effects、供应事实、dedup、committed状态和修复标记同事务。认证缓存使用既有事务outbox；余额/订阅/API Key限额缓存删除采用持久两阶段修复，第二阶段至少延迟30秒。
- 恢复任务不得因为停止额度探测而停止清算。复核发现原注册条件跟随supplier_probe.enabled，已修正为独立、显式split/check-only worker生命周期；无需vault或上游客户端，普通all/gateway/control不因此启动新恢复任务。
- 供应资源在既有调度器内不继承普通账号90%/95%等提前暂停阈值，调度判定不触发自动额度卡重置；保留100%耗尽暂停和数据库所有适用窗口校验。普通账号仍遵守原阈值，无散户个人额度预留。
- 客户用户级平台限额增量尚未接入同一财务事务，因此配置此类限额的用户在供应发送前拒绝，不能以跳过持久额度更新换取可用性。此限制不改变普通非供应渠道。

### P0 持久状态含义

| 状态 | 已知事实 | 恢复动作 |
| --- | --- | --- |
| accepted | 接单快照已提交，尚无发送许可 | 不重发；长期悬挂转review |
| dispatched | 一次性发送意图已提交；不等同上游已收到或已消费 | 等待观测，未知消费不能补成0或自动生成 |
| observed | 完整可信用量已落盘，结算命令尚未保存 | 当前请求继续定价和Stage；崩溃后保留证据待核对，不按新价格补算 |
| pending | 不可变结算命令已保存，财务尚未确认提交 | 仅重放保存的Apply，重复执行依赖既有dedup |
| committed | 客户账务与供应事实已同事务提交 | 仅处理持久缓存修复，不再次加减钱 |
| review | 用量未知/部分/冲突、命令冲突或长期未完成 | 保留快照与观测；本批未新增人工裁定/补单UI，不宣称已经自动结清 |

供应商累计金额仅取已提交的supplier_usage；pending/review不凭估算加到总额。断开/撤回仅阻止后续发送与删除存储凭据，不删除既有快照、授权事件或历史收入。

## 2026-11-05 P0 用户授权发布与真实验收

- 用户明确要求真实上游生成验收、部署、commit、push。本次包含所需258/259前向迁移、现有EC2四角色及双静态更新，不新增AWS资源、不推ECR、不改DNS。
- 操作前STS核对账号250088785444、us-west-2专用sub2api-integration-deployer角色；专用migrator应用且检查258/259成功，runtime新表权限通过，仍无schema CREATE/TRUNCATE。
- 现网策略与此前记录相比已变化：version1、auto_admission=true、group43、customer_charge、100bps。本次保留既有策略，不代设或重置分成。
- 发布前备份目录`/var/lib/sub2api-release-backups/supplier-p0-20261105`；双前端重新类型检查及consumer/supplier生产构建通过；首个P0镜像`sub2api-cloud@sha256:a5bb92e4528b6a5b658ee952519354dff83856448b7047305e04993117289ba4`本机预载，四角色healthy。
- 真实HTTPS/SNI/证书检查：admin.aquawhale.net/admin/suppliers及supplier.aquawhale.net/与发布index逐字节相同，6/2个入口资源200。未认证401、跨域令牌401、supplier域admin404、owner stats200。
- 既有测试资源显式更新all_available_continuous_v1授权，保持UUID/Account/历史；只读额度探测及接池通过。上游观测套餐为free，主窗口明确可用、secondary明确不适用，不把null当缺失。
- 临时启用既有测试客户45和Key44（既有余额、无新增余额、不改quota/分组）；只发一次短文本Responses，用户模型gpt-5.1-codex-mini按既有normalize映射为gpt-5.3-codex，返回502。没有自动重试。finally已恢复客户disabled和Key inactive。
- 请求attempt `43e9fd8c-f38a-4ecc-8d5c-a558bcd018d5`持久state=review/reason=usage_unresolved，dispatched marker存在；观测usage_present=false/complete=false，无结算命令，不生成0消费结算。不能宣称真实生成或计酬验收通过。
- 只读上游models目录当前仅返回codex-auto-review（supported_in_api=true、visibility=hide）；这提示资源模型资格不匹配，但首轮未记录真实HTTP错误类别，不能仅据此断言唯一拒绝原因。未尝试未定价的隐藏模型、未更换其他凭据或规避上游权限。
- 补充固定上游拒绝类别+HTTP status的脱敏诊断，不记录正文、token或任意provider错误字符串；仍不允许自动重试。`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off go test -p=1 ./internal/service -run 'TestSupplier' -count=1`通过0.344s；server重新构建通过，SHA256=`8794b41dabfaa6394e53bcd080ad6836a85b419f25b12bfbebfbb4c20b748639`，migrate不变。
- 最终诊断补丁镜像本机预载`sub2api-cloud@sha256:eea8339dfd1e2364460e4bb99c6e2ababc36eb6e5fd50801fc854d56e43fbd70`；该补丁不追溯恢复首轮上游正文/状态，不执行第二次生成。最终四角色均运行该digest且healthy，部署脚本及第二轮鉴权隔离/owner统计smoke全部通过exit0。
- 数据库最终只读复核：本次Key44对应dispatch=1、review=1、committed=0、staged=0；该attempt供应事实=0。0表示没有已确认应计记录，不是对未知消费做0结算。worker日志确认SupplierBillingRecoveryWorker已获PostgreSQL协调准入。

## 手机验证与邮箱一致性门槛（2026-11-05，本轮未部署）

- 用户确认session.test未手机认证、account.test两账号已认证；随后明确要求以`/me.phone_number`非空判断手机验证，并一起校验邮箱。
- 三个授权样本只读对照：`GET https://chatgpt.com/backend-api/me`均200；未认证样本phone_number为空，已认证两样本均非空；上游email、Token profile邮箱及文件声明邮箱三者一致，上游用户id也匹配Token。未输出真实手机号/邮箱/Token。
- 两个已手机认证样本的models同样只返回codex-auto-review，纠正此前推测：该模型目录不能区分手机认证状态。amr的otp_phone/otp_email仅作认证方式线索，不作为唯一门槛。
- 用户进一步要求phone_verified仅首次检查：按每个凭据版本首次成功校验一次手机及邮箱；存在当前credential_version对应的verified_credential_version且两个身份标记为真时，日常仅刷新额度，不重复请求/me。暂停/恢复不清空证明；更新授权/轮换凭据清除旧usage和版本验证，必须重新检查。首次失败不算已验证，后续仍可重试。新增测试断言首次/me→quota、后续仅quota、凭据版本变化重新/me→quota；调整后的service/repository串行回归日志`/tmp/supplier-identity-once-tests.log`已通过；首次检查、后续quota-only及版本变化重新检查断言通过。
- 首次实现顺序：解密本资源Token→只读/me校验Account/User/邮箱及手机→只读额度查询→保存两个结论phone_verified/email_matched。phone字段null/空串/空白为phone_verification_required；缺失/类型错误/超时/非预期响应为identity_unknown；邮箱不匹配为identity_email_mismatch。401/403继续使用既有认证失败状态。
- Token顶层与profile邮箱、导入JSON顶层与user.email全部核对，trim后忽略大小写；不强制与供应门户邮箱相等。无需存储完整手机号或/me身份正文；不发送OTP、不刷新Token、不触发生成。
- 新前向260定义严格JSON布尔身份结论函数，共享凭据准入SQL覆盖Start、准入、接单及发送前复核。旧快照缺失标记失败关闭，必须重新探测，不补造历史验证。已接受请求的结算仍走冻结快照，不因新门槛否认既有消费。
- 前端10文件88项Vitest通过（18.25s），涵盖三个身份错误码的probe/availability显示及JSON邮箱冲突拒绝；变更组件/types/locales/测试ESLint通过。前端vue-tsc --noEmit通过。测试日志`/tmp/supplier-identity-frontend-tests.log`。供应独立UI生产构建通过（7.63s），仅输出`/tmp/supplier-identity-ui-BjH8Zv`，未覆盖现网静态；旧Browserslist数据警告未阻断，未安装新依赖。
- 后端统一串行命令：backend目录设置`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off SUPPLIER_TEST_PG_SOCKET=/tmp/supplier-pg-20261105-eADiDK`后执行`go test -p=1 -tags=unit ./internal/pkg/codexsession ./internal/repository ./internal/service ./internal/handler ./internal/server ./internal/server/routes -run 'Supplier' -count=1`；首轮parser0.003s/service0.370s/handler0.429s/server0.022s/routes0.023s通过，repository因畸形身份标记StartSupply返回JSON解析错误与测试预期业务错误不符而失败，后续用例恢复也受fatal断言影响；拒绝接单本身有效，测试已修正：畸形类型允许解析阶段拒绝（不要求一定到达SQL业务错误），missing/null/false仍要求业务拒绝；每个子测试t.Cleanup恢复合法快照避免串扰，生产错误映射未改。等待冻结后的最终重跑。日志`/tmp/supplier-identity-backend-tests.log`。
- server构建通过exit0：`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off CGO_ENABLED=0 go build -p=1 -o /tmp/supplier-identity-build-rPydJC/server ./cmd/server`；SHA256=`df34a1b5efcf05f1340de39a034653e63f9fba620784f84278a7be1a7c31ff65`。仅独立临时二进制，未替换运行服务。
- 本轮仅代码/本地验证；不沿用上轮上线/commit/push授权，不修改已应用258/259，不操作线上数据库。最终回归日志`/tmp/supplier-identity-backend-final.log`：parser0.005s、repository（实际临时PG）4.340s、service0.359s、handler0.429s、server0.028s、routes0.035s全部通过。最终git diff --check与新增/修改行凭据形态扫描通过；未部署、未应用线上260、未commit/push。

### 2026-11-05 首次身份验证发布授权

- 用户要求完成后commit/push/上线，本轮因此获准应用260及现有EC2发布；不新增基础设施、不推ECR、不改DNS、不做模型生成。
- 最终首次复用逻辑回归：service0.355s、repository含实际PG5.129s通过。前端88测试、ESLint/类型检查已通过，发布双前端与server/migrate重新构建中。
- STS已核对专用integration-deployer与账号250088785444；备份`/var/lib/sub2api-release-backups/identity-20261105`。部署工具14项离线测试通过0.036s。发布工作区`/tmp/sub2api-release-identity-20261105-lBViEf`。
- 计划只读线上验证已有未手机认证资源显示phone_verification_required且不可调度；保留原资源/收入，不触发模型生成。实际迁移/镜像/提交结果随后追加。

### 首次身份验证上线验收完成

- 实现提交`62d74e8d4`已推送origin/feat/modular-upstream-adapters。最终server/migrate构建通过；server SHA256=`e4d9eee33b9d6662f4b6520169dd9cf911a89155868be35c061945fb06b45c5b`，migrate SHA256=`1f3f27923fd13acb4da3499a55dc365cf6358df83112e17bb93e105bc8cef5b9`。
- 发布双前端类型检查与consumer/supplier构建通过。专用migrator确认唯一待应用260后执行成功，manifest只读检查及runtime严格身份函数false/true验收通过。
- 最终本机镜像`sub2api-cloud@sha256:0a989fb17622ee00ad21710a1cb889b742f56f90c2051b341b54373ed75c6d6a`已切换四角色，全部healthy；未推ECR、未改DNS、未新增基础设施。
- 线上只读/me探测确认既有未手机认证资源返回phone_verification_required，availability同码且不可调度；DB绑定Account disabled/schedulable=false/account_groups=0。既有总dispatch仍1，没有本轮新生成。资源/历史收入不删除，重试状态允许完成官方手机验证后自动重新检查。
- 最终鉴权/owner统计隔离smoke通过；真实HTTPS/SNI/证书及已发布index逐字节匹配通过，admin/supplier入口6/2个资产均200。原group43/customer_charge/100bps策略保留。
- 同凭据成功后的quota-only路径与更新凭据重查由合成HTTP集成测试覆盖；未将已认证account.test导入线上，也未做真实生成验证。发布结果记录与脚本保存在独立临时release目录，不含可用凭据。

## Supplier前端简化重构（2026-11-05，未部署）

- 按用户要求减少默认信息密度：移除Portal多层卡片与重复邮箱/入驻说明，桌面工作区max-w-4xl，手机端单列。资源卡默认显示账号、状态、单条可操作问题、累计费用与常用操作；开始/暂停互斥展示，待验证仍可暂停。
- 额度窗口、同步状态/时间、手动额度刷新与危险断开操作收进原生details，默认折叠；断开二次确认不变。完整授权说明移到页尾可展开区域，导入表单明确同意与凭据清空行为保留。累计费用保留原8位字符串，不新增内部比例/客户信息。
- 原资源API、30秒只读列表轮询、版本CAS、owner隔离、更新授权保留历史均不改变。原生details支持键盘展开，不引入弹窗或新增组件依赖。
- 首轮测试70项通过/1项失败：旧用例按第一个按钮定位额度刷新，重构后顺序改变。修正为按操作名定位并补充默认折叠、互斥主操作、去重通知断言；最终8文件73项Vitest通过9.87s，范围supplier/__tests__与locale完整性。变更组件/测试/中英文locale ESLint通过。
- 最终vue-tsc --noEmit及供应隔离Vite生产构建通过exit0，产物仅在独立/tmp/supplier-workspace-ui-*，未覆盖线上静态。git diff --check通过。未改后端、未新增依赖、未部署/commit/push。
- 环境未提供Chromium/Firefox或浏览器工具，未做真实浏览器截图/视觉验收；不将组件测试或静态构建称为截图验收。

### 前端简化版静态发布完成

- 用户随后明确要求部署。本次只发布supplier静态，不运行迁移、不重启后端、不改consumer静态、不commit/push。
- 使用已通过类型/测试/生产构建的`/tmp/supplier-workspace-ui-w9NqeR`产物。先备份当前supplier目录至`/var/lib/sub2api-release-backups/supplier-ui-20261105-*`，先复制hashed assets再原子替换index；保留旧hash资产，未重命名bind mount根目录。
- supplier.aquawhale.net的`/`与`/supplier/portal`通过真实HTTPS/SNI/证书验证，HTML与产物逐字节相同；全部JS/CSS（含懒加载Portal/Auth）逐字节匹配，入口index SHA256=`cbd69fb36cedc9268753ccf2cf465028a7e6ddf4204dca7682cdea2652de127c`。
- 四角色仍healthy且uptime未重置。未做真实浏览器截图/点击验收，不将HTTPS资源验证称作视觉验收。

## Supplier账号完整邮箱（2026-11-05，已实现/本地验证；后续部署结果见下）

- 资源DTO新增`email`，导入/更新授权持久化解析得到的`session.Email`；保留原`masked_email`字段和值以兼容旧客户端。supplier资源卡与admin供应库存优先显示完整邮箱，空/缺失时回退旧掩码，再回退未知提示；不改supplier用户资料等其他身份脱敏。
- 已核对SQL文件按名称排序：现有最大编号260，新添`261_supplier_resource_email.sql`（`TEXT NOT NULL DEFAULT ''`，幂等ADD COLUMN）。未改既有迁移；迁移只添加元数据列，不读凭据、不执行外部请求或secret bootstrap。发布前必须先通过专用migrator应用261，本轮未对运行库执行迁移。
- owner查询/版本CAS/admin权限边界不变。admin库存SELECT仅新增`r.email`，仍不读取envelope/token指纹/account_hint；admin-control不新增vault依赖。凭据密文仍仅含access_token/account_id，未加入邮箱或新增批量解密。
- 旧资源限制：只有masked_email无法恢复完整地址，现有加密envelope也没有独立email字段。本轮不解密全部vault、不从token猜测/回填、不新增/me请求；旧行email保持空并显示掩码，需供应者更新授权/重新导入后才能持久化完整邮箱。若新session本身无可解析邮箱，也不会编造地址。
- 后端覆盖导入/更新邮箱、JSON允许完整邮箱但不泄露凭据、PG插入/读取/旧行空值/重新授权更新、admin库存只读元数据与owner隔离；相关PG fixture均加入261，资源测试重复执行261验证幂等。前端新增两端完整邮箱优先、空/缺失兼容fallback，以及不渲染token/envelope/account_hint canary。
- 验证命令与结果（均串行）：
  - `cd backend && SUPPLIER_TEST_PG_SOCKET=/tmp/supplier-pg-20261105-eADiDK GOMAXPROCS=2 go test -p=1 ./internal/service ./internal/repository ./internal/handler -run 'TestSupplier' -count=1`：全部通过（service 0.328s/repository 4.841s/handler 0.419s）；仅使用既有临时Unix socket PG，测试创建并清理隔离schema，未接触运行库。
  - `cd backend && GOMAXPROCS=2 go test -p=1 ./migrations -count=1`：通过0.004s。
  - `cd backend && GOMAXPROCS=2 go build -p=1 -o /tmp/sub2api-supplier-email-server ./cmd/server`：通过exit0，未启动/发布。
  - frontend先`export PATH="$(cat /tmp/sub2api-node24-toolchain-path)/bin:$PATH"`；`NODE_OPTIONS=--max-old-space-size=512 ./node_modules/.bin/vitest run src/supplier/__tests__ src/views/admin/__tests__/SuppliersView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts`：9文件91项通过12.61s。首次尝试`pnpm exec vitest ...`因环境无pnpm退出127，随后使用已有本地Vitest，无安装依赖。
  - 同一Node PATH下`NODE_OPTIONS=--max-old-space-size=2048 ./node_modules/.bin/vue-tsc --noEmit`：通过exit0。
  - 同一Node PATH下`NODE_OPTIONS=--max-old-space-size=512 ./node_modules/.bin/eslint src/supplier/resourceTypes.ts src/supplier/Resources.vue src/supplier/__tests__/Resources.spec.ts src/api/admin/suppliers.ts src/views/admin/SuppliersView.vue src/views/admin/__tests__/SuppliersView.spec.ts`：通过exit0。
  - `git diff --check`：通过。保留本轮开始前既有supplier UI简化变更；未commit/push/deploy，未进行真实浏览器验收或外部账号请求。

### 完整邮箱上线验收完成（2026-11-05，未commit/push）

- 用户明确授权部署当前dirty tree；发布仅包含完整邮箱实现和此前supplier简化UI，未包含后续动态成本路由开发。冻结发布目录`/tmp/sub2api-release-email-20261105-7bZYuo`，源码/二进制/静态SHA256及脚本、日志均保存在该目录。构建开始时分支`feat/modular-upstream-adapters`；后续仅分支切换不改变冻结产物，不把新分支功能宣称为已部署。
- 精确现网备份`/var/lib/sub2api-release-backups/sub2api-release-email-20261105-7bZYuo`：`state-before.json`、`release-before.json`、`supplier-compose-before.yaml`、监督器、`consumer-before/`和`supplier-before/`。supplier备份是实际已发布简化UI（index SHA256 `cbd69fb36cedc9268753ccf2cf465028a7e6ddf4204dca7682cdea2652de127c`），不是identity旧静态。旧四角色digest `0a989fb17622ee00ad21710a1cb889b742f56f90c2051b341b54373ed75c6d6a`保留为previous。
- AWS MCP连接失败，改用现有AWS CLI；`AWS_CONFIG_FILE=/etc/sub2api-roles/aws-deploy-config AWS_PROFILE=sub2api-deploy aws sts get-caller-identity --region us-west-2 --output json`核验账号250088785444、专用`sub2api-integration-deployer`。没有新增AWS资源、ECR publish或DNS变更；运行密钥、JWT、数据库密码仅在内存，不输出env或登录文件。
- 串行后端构建：backend目录`GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off CGO_ENABLED=0 go build -p=1 -o "$release/prebuilt/server" ./cmd/server`及同环境`go build -p=1 -o "$release/prebuilt/migrate" ./cmd/migrate`。server SHA256 `d399429fbec1c4f1e266de76593ffc9c80e84e7fba81cf732328e59ff8e92009`，与已测试`/tmp/sub2api-supplier-email-server`一致；migrate `5f92f95541cbfc432bc76a0b4421a2e760f49252f88fdbf2c2fb8bbf635c97ed`。
- 最终后端复核：`SUPPLIER_TEST_PG_SOCKET=/tmp/supplier-pg-20261105-eADiDK GOMAXPROCS=1 GOMEMLIMIT=600MiB GOGC=30 GOPROXY=off go test -p=1 ./internal/service ./internal/repository ./internal/handler ./migrations -run 'TestSupplier|Test.*Migration' -count=1`全部通过（0.397s/4.668s/0.435s/0.007s）。`python3 -I deploy/tests/ec2-role-deploy-test.py`14项通过0.019s；最初在deploy顶层unittest discovery发现0项，不将其作为测试覆盖。
- 前端先设置`PATH="$(cat /tmp/sub2api-node24-toolchain-path)/bin:$PATH"`。`NODE_OPTIONS=--max-old-space-size=2048 ./node_modules/.bin/vue-tsc --noEmit`通过。supplier隔离构建`NODE_OPTIONS=--max-old-space-size=1024 ./node_modules/.bin/vite build --config vite.supplier.config.ts --outDir "$release/supplier" --emptyOutDir`通过6.37s。consumer最终从经哈希验证的`$release/frontend-source`（含原docs/legal相对导入和现有node_modules链接）运行`NODE_OPTIONS=--max-old-space-size=2048 ./node_modules/.bin/vite build --config vite.config.ts --outDir "$release/consumer" --emptyOutDir`通过1m16s；输出index798字节/140JS，supplier index371字节/4JS。没有安装新依赖或重叠重型构建。
- 失败如实记录：首次vue-tsc和consumer checker使用1536MiB堆而OOM；2048MiB类型检查通过。第一次隔离consumer因遗漏docs/legal相对导入失败，补齐原文后重建通过。早期shell复合命令错误地返回后续成功状态，实际consumer无index，部署预检拒绝（没有重启旧服务）；早期对旧后端smoke因缺少email字段失败、HTTPS因缺少consumer index失败。最终改为Python `subprocess.run(check=True)`、非空index和引用资产门禁，完整重跑通过；不以后台shell exit0代替实际产物/服务验收。Browserslist旧数据、Node shell调用弃用与大chunk警告非阻断。
- 已检查`deploy/build_cloud_image.sh`安全上下文流程，本次按既有release模板在本机固定基础digest镜像上仅COPY冻结server/migrate，`docker build --pull=false --network=none`，再用既有containerd本地digest标签方式预载。最终本地镜像`sub2api-cloud@sha256:654c8a3a0e2dd32c60c4881e1e07bd16218f0fbebd802739179f2aa725b3d240`。无registry登录/拉取/推送。
- `python3 -I "$release/apply-migrations.py"`使用冻结manifest确认唯一pending为`261_supplier_resource_email.sql`后apply成功；migrator `-check`及普通/supplier两个运行身份`-check`均通过，运行身份只读验证email列TEXT/NOT NULL/空默认值可读。只新增前向列，未回填、解密或改资源/账号。
- `python3 -I "$release/complete-rollout.py"`最终完整通过：监督器回滚感知部署三个角色，supplier compose `up -d --no-deps --pull never supplier-control`，等待四角色健康后复制hashed assets、最后原子替换index。bind mount根目录未重命名，保留旧hash文件。OpenResty是有意保留的compose orphan，未使用`--remove-orphans`。canonical `/etc/sub2api-roles/release.json`、`/var/lib/sub2api-roles/state.json`及supplier compose均持久化最终digest，pending=false；四角色Docker inspect精确digest且healthy。
- `smoke-email.py`只提取`smoke-identity.py`中`at=`以前的helper定义/只读用户查找，不运行其manual usage POST。admin/owner库存和详情均存在字符串email，禁止凭据字段/Token泄漏；本次观察两条旧资源均空email，按掩码fallback预期，不编造或回填。新资源`ad5d5421-996d-405d-ae97-ea2ee33533c9`为paused/quota_exhausted、phone_verified/email_matched=true、无绑定account；旧`c9f750fc-be3f-49d6-9a25-a155e05c53fb`仍disconnected。只报告观察值，不硬编码库存数量断言。owner stats200、admin token访问owner stats401、未知资源404，supplier admin路由404；原group43/customer_charge/100bps策略不变。
- `python3 -I "$release/verify-assets.py"`逐一执行等效`curl --silent --show-error --fail --noproxy '*' --resolve HOST:443:127.0.0.1 https://HOST/PATH`，验证证书、hostname/SNI、入口和supplier portal路由。app/admin各183个HTML/JS/CSS、supplier6个（含全部懒加载块）与冻结文件逐字节相同。consumer index SHA256 `e181ef6be977fe1b00fc42b7a34526ee766e46077cdc0bcbbd38781bcb46e4af`；supplier `de509e39ca285e0542d0c142f31e02f5154e9fb983c0076e66050ec75af50073`。
- 回滚资料：三角色监督器previous仍指向旧digest，可用既有`python3 -I /opt/sub2api-roles/ec2-role-deploy.py rollback`；supplier使用备份compose同一`up -d --no-deps --pull never supplier-control`；静态从备份先资产后index原子恢复。261为旧版兼容的新增列，回滚不DROP列，不删除数据。本轮实际无启动回滚，早期失败均在切换前。
- 跳过/禁止项：未进行外部generation、额度探测、usage POST、credential import或用户/账号/资源变更；未做真实非空邮箱导入验收（由本地测试覆盖）、浏览器截图/视觉点击验收；没有commit/push。最终日志`migration.log`、`rollout-verified.log`、`smoke-verified.log`、`https-verified.log`和`verified-complete`均在release目录，只有最终这些验收成功才记为完成。
