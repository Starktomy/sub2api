# 全平台采购成本优先（一期）

## 状态与启用

这是现有调度流程中的全局可选成本排序策略，不是第二个调度器。不改变客户账本、供应商结算或历史费用。

管理员在「设置 → 调度」开启「采购成本优先（全平台）」；默认关闭，设置键 `cost_first_routing_enabled`。**不依赖 OpenAI 高级调度开关**。支持部分更新，省略字段保留原值。设置保存更新本实例现有缓存，其他实例沿用现有设置缓存 TTL（5 秒）。全局开关本身没有新增迁移、依赖、provider 调用或金融账本；多来源路由需要下述 262 前向迁移。

## 支持的调度路径

- 共享 GatewayService：Claude/Anthropic、Gemini、Antigravity、TypeSafe，以及 Anthropic 平台下的 Bedrock 账号；包括混合候选池、模型路由候选池、负载选择、旧选择和等待回退。
- OpenAIGatewayService：OpenAI、Grok、OpenCode、OpenRouter、Kimi、智谱、DeepSeek、MiniMax、Command Code、Cline、SiliconFlow、Mistral、百炼、TokenHub、千帆等已登记兼容平台；包括高级权重选择、关闭高级调度时的旧选择、负载/失败回退和等待计划。

“全平台”表示不按 OpenAI 平台设置人为限制；不表示所有协议、所有价格维度或任意供应商均可互换。资源必须能服务当前同一逻辑模型请求，并通过原有模型、分组、协议、供应商请求能力、隐私、额度/RPM、健康、利润门、并发槽和新鲜账号复核。

## 采购价配置：复用已有渠道账号统计规则

在「渠道 → 账号统计定价规则」明确配置按账号 ID / 分组 ID 匹配的采购价。按现有顺序首个匹配模型规则生效；个别账号覆盖应排在通用分组规则之前。自有 OAuth、供应商 OAuth、第三方中转/API-key 均可独立定价，不共用一个 OAuth 倍率。

规则匹配的是**实际发往上游的模型**，沿用已有账号映射、OpenAI/Codex 归一化及 compact 映射、Antigravity 默认映射/默认 thinking 变体、Bedrock 模型/区域 profile 解析。已有请求上下文中确定的渠道/组合映射先保留。未确定的请求依赖维度不会被猜成免费。

现有界面单价单位与转换不变，后端 token 单价为每 token。例如输入每百万 1 美元、输出每百万 2 美元，即 `input_price=0.000001`、`output_price=0.000002`。这些规则同时仍用于后续账号统计，修改前应理解原有统计用途；不会重新定价历史记录。动态采购规则修改沿用渠道缓存失效/通知通路，不新增价格缓存。

## 可比较的费用单位

| 定价模式 | 一期比较口径 | 不可估算情况 |
| --- | --- | --- |
| token / 空模式 | 100 万未缓存输入 + 100 万输出 token 标准篮子 | 输入/输出缺任一价格、负值或非有限数 |
| per_request | 一次模型请求的明确 `per_request_price` | 依赖未知 token/尺寸的区间定价 |
| image / video | 一期不推测尺寸、数量、时长或分辨率对应档位 | 当前统一视为未知；不把图片价塞进 token 篮子 |

输入/输出均明确为零、按次价格明确为零均有效。缺失不等于零。token 区间使用现有区间选择器，按标准篮子 200 万总 token 选择；按次区间因未提供实际适用档位，不按固定 token 篮子猜测。

同一候选池如果同时含 token 与 per_request 定价，单位不同，整池不作成本高低判断，沿用原评分/优先级。不会拿“一次请求 0.1 美元”直接跟“200 万 token 3 美元”冒充同等工作。只存在一种可比较模式时，已知成本由低到高，未知排后；同成本（含明确零价）或均未知时，先选符合资格的软粘性账号，再用原有评分/优先级/负载/LRU等。

标准篮子不是实际请求费用预测，不保证最终最便宜。不预测实际输出长度、缓存、推理等级、服务层级、峰谷时段或真实长上下文占比。媒体及 `UseUpstreamTokenCost=false` 请求保持原有 opt-out，不伪造精确成本。

## 亲和、容量和失败回退

**软粘性只在成本相同或都未知时优先**：此时 eligible 的软会话/可迁移 previous-response 在评分、优先级和负载之前；不同成本时，成本优先于软会话粘性、可迁移 previous-response、订阅/OAuth 偏好、优先级和负载评分；较贵账号不能仅凭这些偏好抢先返回。不可迁移 previous-response 与 guardian 父账号硬亲和保留。compact 已确认支持/未知支持/快照重试仍按既有协议安全层级处理，不跨层级承诺最低价。

低价账号无可用槽、资格不符或发生失败，可按既有失败转移选择较贵且可用账号。等待回退沿用成本顺序及原容量/队列策略，不等待财务事务、不新增外部生成重试。

## 供应商费用与路由边界

供应商 `standard_cost` / `customer_charge` 手续费基准及 BPS 是结算政策，不是采购倍率。本期不自动换算；尤其 customer_charge 依赖实际客户账单、倍率与请求因素。管理员可以明确配置可比较的供应商净采购估价，并自行维护合同一致性。没有明确规则的供应商成本未知。自有 OAuth 订阅摊销同样由管理员明确配置，销售倍率、BillingRateMultiplier、OAuth 全局参考倍率、探测倍率及官方模型目录价均不作为采购价兜底。

候选仍经过原有 composite/platform 路由。没有显式多来源配置时，不按同名模型自动推断等价；原生与 Antigravity 的 mixed_scheduling 范围不变。

## 同一对外模型的显式多来源（本次新增）

管理员在「分组 → composite 路由」配置精确对外模型，并选择 `messages`、`responses` 或 `chat_completions` 端点。新增有序来源列表，每个来源包含具体平台、明确上游模型、启用状态；界面支持增加、删除、上下移动和启停，最多 16 项。非空列表至少启用一项。空列表保留旧单目标路由；非空列表首个启用来源投影到旧 `target_platform/upstream_model` 字段，已有客户端仍可读取。

例如对外 `gpt6luna`（仅为合成测试标识，不宣称真实模型存在），可明确声明 OpenAI `oauth-alias` 与 OpenCode `code-alias` 等价。账号映射再分别解析成 `oauth-upstream` / `code-upstream`。比较采购价时使用各候选的**平台及最终上游模型**，不是统一拿对外名称或首来源名称查价。OpenCode 采购价低时可以胜过自有 OpenAI OAuth。

来源仅限平台目录中共享 OpenAI handler 的具体平台（包含 OpenCode/OpenRouter/OpenAI；排除拥有独立会话语义的 Grok），不支持跨 handler 的 Anthropic/Gemini/Antigravity/TypeSafe 互换。多来源 `responses` 强制每个账号具备原生 Responses 能力，不沿首来源推导较弱的 chat 能力或降级加密状态/压缩。所有来源账号必须属于当前分组；没有来源级任意账号/其他分组授权。能力、供应商端点、健康、隐私、额度、利润、协议和新鲜账号复核仍由现有选择器执行。别名不会绕过这些门。

实现先读取各来源的现有调度快照并过滤候选，统一排序后才尝试并发槽；不会为比价预占每个来源的槽，也不会进行上游模型探测或试生成。一次扩展内复用每个平台的候选桶。成本开关关闭时按来源顺序，来源内部仍使用现有选择策略；开启时已知低价优先、未知后排，相同价/均未知时当前分组有效软粘性优先。混合单位不比较大小。低价无槽时尝试下个候选，全部繁忙则保留首个现有等待计划。

不可迁移 `previous_response_id` 必须有现有响应账号绑定，并限制到该 OpenAI 账号；绑定缺失时拒绝而非跨供应商重建状态。guardian 父账号同样限制候选。HTTP handler 在选号后应用选中平台和上游模型，保留对外模型上下文，并复用原转发与入账。选择比较不会调用生成；真正发送后的重试仍受原有安全策略和供应商 one-send 限制，不新增发送重放。

### 明确限制与迁移

- 新增前向迁移 `backend/migrations/262_composite_route_candidates.sql`，在原路由表增加 nullable JSONB `candidates`，旧行不变。已生成对应 Ent 字段；部署前验证与线上发布记录见下文。
- 多来源暂不支持 `any`、前缀路由、媒体、embeddings、count_tokens、原生 Gemini 或 Responses WebSocket。WebSocket 对多来源显式拒绝，不能默默只用首来源。
- 后台 preview 展示首启用来源及完整列表，不预占槽、不承诺当次运行时的最低价账号；实时资格与成本在请求调度时判定。
- 已在指定 `/tmp/supplier-pg-20261105-eADiDK` Unix socket、55439 端口的临时 PostgreSQL 15 中，以唯一隔离 schema 验证 262 幂等执行两次、迁移前旧行读取、真实 Ent Create/List/Update/Delete 及 OpenCode 主来源投影；测试删除其 schema。未操作线上数据库。

## 验证范围

聚焦测试覆盖全局开关/部分更新/缓存、token 与按次零价、缺价/未知、混合单位、模型映射、Claude/Gemini/Antigravity/TypeSafe 旧选择与负载选择、Bedrock/OpenRouter/OpenCode 实际模型采购价、OpenAI 高级/旧选择、供应商能力、软/硬亲和与原调度回归。无真实模型调用。


### 本次聚焦验证命令与结果

- `GOMAXPROCS=2 GOMEMLIMIT=1800MiB go test -p=1 -tags=unit ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin ./internal/server/routes -run 'TestComposite|TestCostFirst|TestExplicitPurchase|Test.*Supplier.*Endpoint' -count=1`：通过（另新增 repository roundtrip 用例在最终命令加入 `TestCompositeCandidate` 匹配）。
- `TestCompositeCandidatesGpt6lunaRoutingBoundary`：resolver → scheduler → forward projection → mock HTTP upstream，OpenCode 低价胜过 OAuth，最终发送 `code-upstream` 且只有一次发送；关闭成本模式选首来源；另外覆盖跨平台相同价/未知软粘性、零价、忙槽、外组排除、硬响应绑定及缺绑定拒绝。仅 mock transport，无真实模型调用。另见下述完整 HTTP middleware/production handler 补充验证。
- `TestCompositeCandidatesValidationRoundtrip` 与 `TestCompositeCandidateEntityRoundtrip`：严格来源验证、旧字段投影、JSON/Ent 转换。
- Node 24 下 `vue-tsc --noEmit`、改动文件 ESLint：通过；Vitest `GroupsView.duplicate.spec.ts` + `localeKeyCompleteness.spec.ts`：13/13 通过，包含来源增加/顺序/启停/删除/序列化。
- `GOMAXPROCS=2 GOMEMLIMIT=1800MiB go build -p=1 ./cmd/server`：通过。未运行全 service suite，不更改已知无关平台目录失败。


### 补充窄范围集成验证（已通过）

- `SUPPLIER_TEST_PG_SOCKET=/tmp/supplier-pg-20261105-eADiDK GOMAXPROCS=2 GOMEMLIMIT=1800MiB go test -p=1 -tags=unit ./internal/server/routes ./internal/repository -run 'TestCompositeCandidatesHTTPProductionHandler|TestCompositeCandidatePostgresCRUD' -count=1 -v`：通过。实际 PostgreSQL 迁移/CRUD 如上；HTTP 集成使用真实 composite middleware、production ChatCompletions handler、production scheduler/forwarder，只有外发 transport 和依赖 repository 为桩。
- `TestCompositeCandidatesHTTPProductionHandler/cheapest-once`：请求 `gpt6luna`，首来源 OpenAI OAuth、次来源 OpenCode；采购价使 OpenCode 获选，实际出站 `code-upstream` 一次，usage `RequestedModel=gpt6luna`。
- `.../failover-next-source`：OpenCode mock 返回 502，沿原安全失败策略转移 OpenRouter；出站账号顺序 `[2,3]`、模型 `[code-upstream,router-upstream]`，每次恰好一发，没有复用首来源模型/平台，最终只有一条成功 usage，public model 不变。这不授权供应商发送后重放。
- `GOMAXPROCS=2 GOMEMLIMIT=1800MiB go test -p=1 -tags=unit ./internal/service -run 'TestSupplierCompositeSource|TestSupplierNativeUnknownAndRejectedRequestsNeverReplay|TestSupplierRequiresSlotAndRejectsUnsupportedBeforeSend|TestSupplierPhysicalSendFenceCannotBeReused|TestCompositeCandidates' -count=1 -v`：通过。新增供应商来源 `supplier-alias → gpt-5.2` 验证 durable acceptance 的 `Model` 与物理外发一致，一次发送；既有未知结果/拒绝/提交模糊不会触发重放，能力与槽位缺失在发送前阻断。
- 验证拒绝 `any/images/embeddings/count_tokens/gemini` 多来源端点、前缀路由及 Anthropic/Gemini/Grok/TypeSafe/未知平台。范围仍是共享 OpenAI-compatible HTTP 精确文本路由，不扩展原生 Claude/Gemini。


## 2026-11-05 发布记录（执行前）

- 用户明确授权当前 `feat/dynamic-cost-routing` 分支全部预期变更的 commit、push 和本机集成环境部署；包含全局成本排序、多来源路由以及已部署的供应商完整邮箱/UI 变更，不包含凭据或构建产物。基线 HEAD `52d10748c`。
- 已执行 `git status --short`、`git diff --stat`、关键源码/迁移差异审查、`git diff --check` 和 79 个预期源码/文档文件的凭据模式扫描：通过。新增 go.sum 条目为现有 Ent 工具链依赖校验和，不新增 go.mod 运行依赖。
- 既有验证结果见上文。未声称全量测试通过；全 service suite 存在 3 个已知 legacy/platform 预期失败。发布中使用冻结提交重跑聚焦测试、前端类型检查并构建 server/migrate 和两个真实前端 bundle。
- 发布身份限定账号 `250088785444`、区域 `us-west-2`、专用 `sub2api-integration-deployer`；通过 `AWS_CONFIG_FILE=/etc/sub2api-roles/aws-deploy-config AWS_PROFILE=sub2api-deploy` 使用 AWS CLI。AWS MCP 未连接（uvx ENOENT），不依赖该服务，不使用 boto3。
- 四角色发布前均健康，原 digest `sub2api-cloud@sha256:654c8a3a0e2dd32c60c4881e1e07bd16218f0fbebd802739179f2aa725b3d240`。迁移清单应仅缺 `262_composite_route_candidates.sql`；261 已在上次部署应用，禁止改写已应用迁移。
- 本次仅发布，不启用 `cost_first_routing_enabled`，不改既有业务设置/路由/账号/资源，不调用真实上游生成或探测；不创建 AWS 资源、不发布 ECR、不改 DNS。多来源只支持共享 OpenAI-compatible HTTP 精确文本路由，不宣称原生 Claude/Gemini、WS 或媒体跨平台路由。
- 流程：冻结已提交源码；Python `subprocess.run(check=True)` 串行构建，Go `GOMAXPROCS=2 GOMEMLIMIT=1800MiB -p=1`，Node 24 / 2048 MiB；验证 index 和全部引用、保留 docs/legal 相对布局；备份后先迁移，双 runtime check-only 与 candidates SELECT，再按既有 supervisor 部署三角色与 supplier，hashed assets 先发布、index 原子替换、保留旧资源与挂载目录；四角色实际镜像/健康、认证 GET、HTTPS/SNI 所有 HTML/JS/CSS 字节校验后补记结果。
- 明确跳过真实推理、供应商 provider 探测、POST/usage 旧 smoke、浏览器交互；本次无需 PR。后续发布结果另一个正常提交记录，不改写历史。


### 执行结果：发布阻断，已安全回滚（2026-11-05）

**不是发布成功。** 实现提交 `0f28006009dd69f85ed507aa438067f794536e9c` 已通过 `git push origin feat/dynamic-cost-routing` 推送。新镜像曾启动并通过四角色健康检查，但认证路由列表 GET 返回 HTTP 500，发布总门禁失败，已恢复原四角色镜像和静态入口。未绕过失败继续发布。

- 隔离目录：`/tmp/sub2api-release-cost-20261105-qy1capku`；`git archive HEAD` 冻结源码，全量 4858 个已跟踪文件 SHA256 验证无改动。前端 node_modules 仅复用本地依赖，保留完整仓库 docs/legal 相对布局。Go embed 使用此前忽略的 fallback 静态文件；线上实际静态资源由独立前端构建后通过既有 OpenResty 挂载发布，不将 fallback 当成新管理 UI。
- 精确回滚备份：`/var/lib/sub2api-release-backups/sub2api-release-cost-20261105-qy1capku`，含当时 canonical release/state、supplier compose、supervisor、两个静态根的完整副本，不备份或输出 runtime env/keyring。
- 实际命令 `python3 -I /tmp/sub2api-release-cost-20261105-qy1capku/build.py`：内部使用 `GOMAXPROCS=2 GOMEMLIMIT=1800MiB CGO_ENABLED=0 GOPROXY=off go test -p=1 -tags=unit ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin ./internal/server/routes -run 'TestComposite|TestCostFirst|TestExplicitPurchase|Test.*Supplier.*Endpoint' -count=1`，五包全部通过；`go build -p=1 -o <release>/prebuilt/server ./cmd/server` 和 migrate 对应命令通过。Node24 / `NODE_OPTIONS=--max-old-space-size=2048` 下本地 `vue-tsc --noEmit`、两个配置的 `vite build --outDir <release>/consumer|supplier --emptyOutDir` 均 subprocess check=True 成功。
- 本次构建脚本随后因过严的供应商 JS 数量断言退出 1（运行中旧 Python 已加载旧断言，脚本文件后续修正不会改变运行中代码）。这不是构建 OOM 或成功状态被误判。单独重新核验 consumer 140 JS、supplier 4 JS、非空 index 和全部本地引用后才写 completion gate；未复用上次部署的新 UI 产物。后续 `python3 -I <release>/extra-tests.py` 通过：Vitest settings 44 + groups 10 + locale 3 = 57 项，21 个改动前端文件 ESLint，通过临时 PostgreSQL `TestCompositeCandidatePostgresCRUD` 和 mock `TestCompositeCandidatesHTTPProductionHandler` 两个子用例。未发生真实 provider 调用。
- `bash <release>/build-image.sh`：本地 Docker `--pull=false --network=none` 构建并本机 preload，未发布 ECR；尝试发布 digest `sub2api-cloud@sha256:2de89c57140e0a4d9276691c40abf8b1b519ebd3ad804ee6a962ca280afdac7e`。
- `AWS_CONFIG_FILE=/etc/sub2api-roles/aws-deploy-config AWS_PROFILE=sub2api-deploy AWS_DEFAULT_REGION=us-west-2 aws sts get-caller-identity`：确认账号 `250088785444`、assumed-role `sub2api-integration-deployer`。`python3 -I <release>/apply-migrations.py` 内通过 AWS CLI 将 migrator secret 仅放内存/env，精确检查 pending 仅 262，执行新 migrate，然后 `-check`；两种 runtime 身份 `-check` 和 `SELECT candidates FROM composite_model_routes LIMIT 0`、nullable JSONB 元数据查询全部通过。**262 已应用且保留**，未修改/回退已有迁移。
- `python3 -I <release>/finalize.py`：既有 supervisor deploy 三角色成功，supplier compose `up -d --no-deps --pull never supplier-control` 成功，四角色曾健康；hashed assets 先复制、index 原子替换、保留挂载根和旧 assets。OpenResty orphan warning 为预期，未使用 `--remove-orphans`。
- 认证 GET smoke：admin/owner 资源列表、owner detail/stats、401/404 隔离和凭据非泄漏通过；两条旧资源 email 仍为空，不回填、不改资源。新 `/api/v1/admin/settings` 暴露 `cost_first_routing_enabled=false`。随后 `/api/v1/admin/groups/1/composite-routes` 返回 **HTTP 500**（期望 200），因此整体 smoke 失败。根因尚未调查确认，不将其描述为已修复或已验证的线上路由能力。
- 总门禁自动调用 `python3 -I <release>/rollback.py`，恢复 supplier 原 compose、通过 supervisor deploy 精确原 release，再原子恢复旧静态入口；**四角色实际 `.Config.Image` 均为原 digest `sub2api-cloud@sha256:654c8a3a0e2dd32c60c4881e1e07bd16218f0fbebd802739179f2aa725b3d240` 且 healthy**。`state.current == canonical release`、pending=false；supplier 使用备份 compose。nullable 262 向前兼容保留，未做破坏性 schema rollback。
- `python3 -I <release>/check-business.py before|after`：全 settings 的 key/value digest 与现有路由旧字段 digest 相同，成本开关 false/默认 false；未保存设置明文。回滚后使用 `curl --resolve <hostname>:443:127.0.0.1 https://<hostname>/<path>` 正确 TLS/SNI 校验，app/admin 各 752 个、supplier 31 个备份 HTML/JS/CSS 文件全部字节相同，含 lazy chunks；新 bundle 的全量 HTTPS 验证因先前 smoke 失败没有执行，不宣称通过。
- 可复查结果：`<release>/final-result.json` 状态 `blocked_and_rolled_back`；对应 build/test/migration/rollout/smoke/rollback 日志留在隔离目录，未写凭据。未运行全 service suite；此前完整 suite 的已知失败为 `TestMatchingPlatforms`、`TestAccountTestService_ByInboundProviderSkipsMissingNativeEndpoints`、`TestPlatformListDerivedListsMatchLegacy`，不宣称 all-tests pass。
- 仍跳过真实推理、provider 探测、浏览器交互、旧 POST/usage smoke；未启用成本路由、未创建/修改路由、账号、用户或资源，无 AWS 网络/资源/DNS 变更。后续应先诊断修复路由列表 HTTP 500，再获得继续发布的指示；本次停在安全回滚状态。
