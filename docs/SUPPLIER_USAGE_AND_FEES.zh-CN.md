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
