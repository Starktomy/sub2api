# 模块化上游：边界与首批接线

日期：2026-11-04。分支：`feat/modular-upstream-adapters`。

状态：全部既有非OAuth模块接线及六个简单上游Chat切片已完成本地聚焦验收；OpenRouter/Mistral无状态Responses及三家模型发现扩展已通过本地验收；六家原生Messages未实现。Rerank执行受账务前置阻塞。模块化后端未部署、未真实调用提供方，完整状态见下表。

## 实现状态总表（2026-11-04）

本表为本分支当前状态入口。**已实现≠已真实调用≠已部署**；“本地通过”仅表示下述聚焦测试/build/vet，不表示全套回归或所有模型均可用。模块化及本批六新增上游尚未提交/推送/部署；线上仍是此前版本。并发开发中的新能力只有最终验证后才更新为通过。

| 类别 | 项目/模块 | 状态 | 实现到什么程度 | 验证/部署 | 剩余工作或限制 |
| --- | --- | --- | --- | --- | --- |
| 核心 | 上游注册表与模块契约 | 已实现 | 构建时注册、重复/未知ID拒绝；协议、认证策略、执行family、三态能力 | 后端聚焦/build/vet通过；未部署 | 不是动态插件或全新通用执行器；复杂逻辑仍复用原执行器 |
| 核心 | 管理员模块目录 | 已实现 | GET `/api/v1/admin/upstream-providers`，只返回安全元数据 | handler安全测试通过；未部署 | 操作清单是有条件的宿主入口，不是授权/全模型支持保证 |
| 核心 | 账号创建/编辑/批量/extra校验 | 已实现 | 显式模块绑定与平台/类型一致，错误拒绝；旧账号保持兼容 | 后端及前端聚焦通过；未部署 | 旧未绑定账号不强制迁移 |
| 前端 | 模块选择与中央平台目录 | 已实现 | 自动唯一匹配、旧绑定保留、凭据不清空，目录由后端生成 | 最新231项测试、typecheck、原站/供应构建通过；未部署 | 不根据品牌推定所有协议能力 |
| 现有上游 | OpenAI compatible | 已模块化 | API Key；Responses/Chat路由、override/probe优先级，复用执行器 | 本地通过；新接线未部署 | 未知探测沿用旧默认不是能力证明 |
| 现有上游 | Kimi | 已模块化 | PayG/Coding、既有Chat/Responses/Anthropic profile与认证桥接 | 本地通过；未部署 | 非文本能力不因文本profile自动获得 |
| 现有上游 | 智谱 GLM V4 | 已模块化 | 既有Chat/Anthropic profile，保留模式及路径 | 本地通过；未部署 | 无原生Responses声明；旧智谱协议不做 |
| 现有上游 | DeepSeek | 已模块化 | 既有Chat/Responses/Anthropic及特殊路径/规则 | 本地通过；未部署 | 独立Search/Fetch仅记录、不做 |
| 现有上游 | MiniMax | 已模块化 | 既有文本多协议、PayG/Coding | 本地通过；未部署 | 新增TTS/原生图片/海螺视频不做 |
| 现有上游 | OpenCode Go/Zen | 已模块化 | 既有模型规则选择协议，保留不支持模型检查 | 本地通过；未部署 | 不代表每个模型都支持三协议 |
| 现有上游 | Command Code | 已模块化 | 既有多协议/model catalog规则 | 本地通过；未部署 | catalog观察值不代表真实调用验证 |
| 现有上游 | Cline | 已模块化 | 既有Chat路径与钱包/冷却行为 | 本地通过；未部署 | 不新增原生Responses |
| 现有上游 | Anthropic API Key | 已模块化 | Messages、既有Key/Bearer认证 | 本地通过；未部署 | OAuth/setup-token不迁移 |
| 现有上游 | Anthropic Vertex | 已模块化 | 服务账号认证、rawPredict/streamRawPredict桥接 | 本地通过；未部署 | 无模型自动发现 |
| 现有上游 | Bedrock | 已模块化 | Claude Invoke/EventStream；Bearer、静态/STS、operator credential_file | 本地通过；未部署 | 不是全模型Converse；Nova/Mantle新增路径不做；此前真实Bedrock生成被账号准入阻断 |
| 现有上游 | Gemini API Key | 已模块化 | 原生推理、countTokens、既有模型发现/批量图片 | 本地通过；未部署 | 原生embedding/Imagen/Veo新增执行器不做 |
| 现有上游 | Gemini Vertex | 已模块化 | SA与Express模式、既有推理和受限批量图片桥接 | 本地通过；未部署 | Express不支持catalog sync/batch，SA沿用原限制 |
| 现有上游 | Grok API Key | 已模块化 | 既有文本、媒体、搜索、语音/realtime执行入口 | 本地通过；未部署 | 不迁移订阅OAuth生命周期 |
| 现有上游 | Antigravity兼容API Key | 已模块化 | 第三方兼容网关，原Messages/Gemini执行 | 本地通过；未部署 | 非官方CloudCode OAuth |
| 现有上游 | Antigravity upstream | 已模块化 | 既有静态passthrough认证及执行family | 本地通过；未部署 | 发现/测试不自动等同apikey模式 |
| 现有上游 | TypeSafe/System One | 已模块化 | 原生SystemOne非流式执行 | 本地通过；未部署 | 不支持流式及模型同步 |
| 横向能力 | Images/Embeddings/Seedance/Grok媒体/批量图片 | 已补模块校验 | 使用原执行器；执行前拒绝错误显式绑定 | 扩展回归通过；未部署 | 不是新增这些能力，不扩张各平台原有入口限制 |
| 新增上游 | OpenRouter | Chat/无状态Responses/模型发现完成 | `/api/v1`，PayG/Bearer，无默认归属头；有界分页 | 本地验收通过；无真实调用/部署 | 原生Messages未实现；不支持状态续接/保存/后台执行 |
| 新增上游 | SiliconFlow | Chat切片完成；扩展核查中 | `/v1`，PayG/API Key、Chat Completions | Chat本地通过；无真实调用/部署 | 三项扩展逐家核查；不包含专用Rerank/音频/图片参数 |
| 新增上游 | Mistral | Chat/无状态Responses/模型发现完成 | `/v1`，PayG/API Key，Responses public preview | 本地验收通过；无真实调用/部署 | 原生Anthropic未实现；不支持状态续接/保存/后台执行 |
| 新增上游 | 百炼兼容模式 | Chat切片完成；扩展核查中 | `/compatible-mode/v1`，PayG/API Key | Chat本地通过；无真实调用/部署 | 原生多模态embedding、图片/视频任务不做 |
| 新增上游 | 腾讯TokenHub | Chat切片完成；扩展核查中 | `/v1`，PayG/Bearer | Chat本地通过；无真实调用/部署 | 不含腾讯TC3；三项扩展待官方证据与接线 |
| 新增上游 | 百度千帆V2兼容 | Chat/模型发现完成 | `/v2`，PayG/API Key、实时目录 | 本地验收通过；无真实调用/部署 | Responses/Anthropic未开放；不含旧V1 AK/SK及复合key |
| 扩展 | 六上游Responses | 部分完成 | OpenRouter/Mistral无状态Responses已接线，其余四家未开放 | 后端聚焦/build/vet及前端239项/typecheck/双build通过；未部署 | 发送前拒绝状态续接/保存/后台字段；Mistral为public preview |
| 扩展 | 六上游Anthropic Messages | 部分完成，本地验收通过 | OpenRouter/百炼/TokenHub流式及非流式；SiliconFlow CN仅非流式；Mistral/千帆未开放 | 后端聚焦测试/build/vet通过；前端245项/ESLint/typecheck/原站及供应构建通过；未真实调用/部署 | Bearer与x-api-key按provider选择；百炼独立base与模型白名单；SiliconFlow发送前拒stream，包含转换/测试入口 |
| 扩展 | 六上游自动模型发现 | 部分完成 | OpenRouter/Mistral/千帆V2实时目录已接线；另外三家仍关闭 | 本地测试/构建通过；未真实调用或部署 | 查询当前凭据、无共享cache；分页异常不返回partial；目录不是可调用验证 |
| 高复用 | 通用Rerank | 未完成，暂不开放 | 仅输入契约及501拒绝，无上游请求；前端可开启选项撤回 | 拒绝路径测试通过；无实际执行 | 需持久pending/ambiguous消费事实与对账，再接标准执行/计费 |
| 账务前置 | 未知用量待对账journal | 未实现 | 已确认现有已知金额扣费接口不足 | 未验证/部署 | 不将缺usage当0，不自动重发；另立账务切片 |
| 工具 | new-api差集盘点 | 已完成静态研究 | 59类型：14主要覆盖/22兼容配置/21原生缺口/1占位/1容器；11任务插件不重复计数 | 只读源码核查 | 不等于已迁入new-api独有渠道 |
| 工具 | new-api转换器 | 未实现 | 已完成离线分析+保守生成方案评估 | 无工具运行验证 | 倾向半自动骨架/差异报告，复杂协议人工适配 |
| 后置 | OAuth/setup-token/session模块迁移 | 本轮不做 | 保留Sub2API原有可用路径 | 原实现非本轮迁移 | 不得误称已重构OAuth生命周期 |
| 仅记录 | 旧PaLM/百度V1/旧智谱/Moka等 | 不实施 | 仅差集记录 | 无新增验证 | 用户明确排除旧协议 |
| 仅记录 | Azure原生/TC3/讯飞WS/Ollama原生/Workers AI原生 | 不实施 | 标准兼容面可复用；不新增专有认证执行器 | 无新增验证 | 用户明确排除特殊协议 |
| 仅记录 | Dify/Coze/AdvancedCustom引擎 | 不实施 | 仅差集记录 | 无新增验证 | 不新增工作流/Bot/任意转换引擎 |
| 仅记录 | MJ/Suno/Sora/Veo/Kling/Vidu/Hailuo/Jimeng/Replicate等专用任务 | 不实施 | 仅差集记录，已有Grok/Seedance不删除 | 无新增验证 | 不新增第二任务网关或调度器 |
| 发布 | 当前模块化分支提交/推送 | 尚未执行 | 分支`feat/modular-upstream-adapters`，包含已测代码及后续进行中扩展 | 本批尚未提交/推送 | 不混淆之前供应分支已push的提交 |
| 发布 | 模块化后端部署/真实提供方验收 | 尚未执行 | 未发布新版Gateway/Control执行模块 | 无真实调用 | 已生成的前端文件不代表新版后端已部署；运行中静态挂载可能看到构建文件，不算完整发布 |

### 六上游扩展证据与实施进度（公开文档核查中，非已接线）

以下为公开官方资料的中间核查结果，最终URL与运行契约测试待补；不能据此把上表“进行中”改为已支持，也不是使用真实Key验证。

| 上游 | Responses证据 | Anthropic证据 | 模型发现证据 | 当前实施边界 |
| --- | --- | --- | --- | --- |
| OpenRouter | 官方存在`/api/v1/responses`，无状态约束 | 官方存在`/api/v1/messages`，Bearer | 官方`/api/v1/models` | 优先接入；store/previous_response_id限制须发送前拒绝，尚未验收 |
| Mistral | 最新官方`/v1/responses` public preview，旧new-api stub不能作为不支持结论 | 未确认 | 官方`/v1/models` | Responses仅明确兼容的无状态子集；模型发现优先，尚未验收 |
| SiliconFlow | 未确认 | 官方.com域有messages，但当前.cn域证据待核对 | 官方.com域有models，.cn待核对 | 不跨域套用证据，保持当前配置拒绝未证协议 |
| 百炼 | 官方compatible-mode Responses存在 | 官方apps/anthropic Messages存在 | 独立`/api/v1/models`，分页`output.models`非标准 | 当前地域/旧host与路径需核对，不直接拼标准models |
| TokenHub | 旧域Responses为服务端Chat转换，不是native | 新地域域名资料待核实认证与迁移 | 新地域目录资料待核实 | 不将转换当原生；不静默更换账号域名 |
| 千帆V2 | models元数据列Responses，专页仍待核查 | metadata列Anthropic不等于执行证明 | 官方`/v2/models`已确认 | 优先模型发现，推理协议证据不足不全开 |

### 协议扩展接线进展（前后端本地聚焦验收通过，未部署）

OpenRouter/Mistral无状态Responses实际出站路径及状态字段发送前拒绝已实现；OpenRouter/Mistral/千帆V2实时模型发现已实现。service初验0.444s、server build及service/routes vet通过；最终聚焦及legacy golden回归6.238s通过、diff检查通过，后端已冻结。前端最终Node24.22.2串行验证exit0：typecheck、原站build（i18n3项、vue-tsc、Vite59.69s）、供应build（5.28s、边界守卫）全部通过，8文件239项测试16.01s通过，diff检查通过。此扩展切片本地聚焦验收完成；未执行全套回归、真实provider或部署验收。发送前拒绝store:true、previous_response_id、conversation、background，复用既有Bearer执行及stream/usage处理，不新增生成重试或账务路径。

OpenRouter官方明确offset/limit分页；实现只向固定endpoint发送重建参数，next仅用于验证同origin/path及白名单query，offset须按当前返回条数递增。页数/条目/累计大小有界；失败、空或超限不返回partial、不回退静态配置列表。无共享模型缓存，每次使用当前账号凭据；目录结果不是可调用或授权证据。OpenRouter Messages仍未实现；SiliconFlow/百炼/TokenHub仍保持Chat-only，千帆仅新增模型发现。暂无真实provider调用或部署。

### 官方核查来源（2026-11-04）

本次扩展截至调查终态尚未修改源代码，Chat-only版本仍是最后通过验收的切片。进一步核查已找到SiliconFlow CN Messages文档和千帆Responses专页，但不因此自动开放。随后开始实现OpenRouter受限协议/模型发现、Mistral模型发现及可确认的无状态Responses、千帆模型发现；其他待核查组合继续关闭。

- OpenRouter：[Responses](https://openrouter.ai/docs/api/reference/responses/overview)、[Messages](https://openrouter.ai/docs/api/api-reference/anthropic-messages/create-messages?explorer=true)、[Models](https://openrouter.ai/docs/api/api-reference/models/get-models)。
- Mistral：[Responses public preview](https://docs.mistral.ai/studio/conversations/responses.md)、[Models](https://docs.mistral.ai/api/endpoint/models)。
- SiliconFlow：[CN Messages](https://api-docs.siliconflow.cn/docs/api/messages-post)、[国际站Models](https://docs.siliconflow.com/en/api-reference/models/get-model-list)。国际站列表不直接作为CN可用证据。
- 百炼：[Responses](https://help.aliyun.com/zh/model-studio/compatibility-with-openai-responses-api)、[Messages](https://help.aliyun.com/zh/model-studio/anthropic-api-messages)、[非标准分页Models](https://help.aliyun.com/en/model-studio/list-models)。
- TokenHub：[旧域Responses转换限制](https://cloud.tencent.com/document/product/1823/133813)、[地域协议参考](https://intl.cloud.tencent.com/jp/document/product/1300/80632)。
- 千帆：[Models](https://cloud.baidu.com/doc/qianfan-api/s/Dmba8k71y)、[Responses](https://cloud.baidu.com/doc/qianfan-api/s/vmhejnuy8)。

## 架构原则

保留唯一Gateway、账号调度和客户账本。模块只描述提供方差异，不能自行重选账号、重试生成、扣客户款、刷新未知状态凭据或创建第二套资源池。

优先包装现有Sub2API实现。用户于2026-11-04明确确认new-api许可问题已解决，允许直接复用；如实际移植，必须保留来源版本、版权、LICENSE、NOTICE及适用UI署名，不将授权理解为抹除开源声明。当前首批代码未复制new-api代码，未修改兄弟仓库。新增模块不要求实现所有协议，未实现/未知能力不能宣称支持。

## 首批已实现

- `backend/internal/pkg/upstream/registry.go`：构建时注册表，确定性ID顺序、重复ID拒绝、显式未知ID拒绝，无动态插件。元数据复制；selector要求不可变及并发安全。
- 最小 `ProtocolSelector` 与三态 `Support`：只做协议选择，不耦合Account、凭据、URL、传输、调度或计费。
- `backend/internal/pkg/openai_compat/provider.go`：`openai-compatible`内建模块包装已有能力helper，不重写协议转换。
- `backend/internal/service/upstream_provider.go`：将账号extra归一化为无秘密路由提示。
- 实际Gateway的Responses/ChatCompletions判定和账号连接测试共用selector；ChatCompletions、Messages、Responses及WS入口校验显式绑定。WS只有绑定校验，没有新增WS模块。

可选后端字段 `accounts.extra.upstream_provider = "openai-compatible"`，仅适用于OpenAI API-key账号。字段缺失保持原行为；空值、非字符串或未知显式ID拒绝。原生OAuth及模型profile优先路由不迁移，不因模块化强行走兼容路径。

路由顺序保持：账号强制Responses/ChatCompletions设置 > 探测结果 > 未知时沿用Responses旧默认。旧默认不等于已证明上游支持；streaming与模型发现仍声明unknown，不据此广告能力。

## 验证及限制

使用 `GOMAXPROCS=2 GOMEMLIMIT=1200MiB GOGC=30` 串行验证：注册表与兼容包测试（含并发只读）、service聚焦unit回归通过，覆盖路由优先级、显式绑定拒绝、原生OAuth/stream fallback、账号连接测试与原有重试行为。较广聚焦运行0.724s，最终service回归0.142s。

`go build -p=1 -o /tmp/sub2api-modular-upstream-server ./cmd/server`、`go vet -p=1 ./internal/pkg/upstream ./internal/pkg/openai_compat ./internal/service`、`git diff --check`通过。首次高内存编译主动停止，后以限内存参数重跑通过。race检查因CGO禁用且缺C编译器未运行，不声称race-clean。未跑全套unit/integration及真实提供方测试。

本批没有UI、迁移、动态插件、新提供方专用执行器、认证/刷新框架、模型目录框架或账务变化。未部署，未提交/推送本批模块代码。

## 本轮范围扩大：全部现有非OAuth上游（实施中）

用户要求先将Sub2API已有全部非OAuth上游模块化，DeepSeek、MiniMax、OpenCode为明确示例；OpenAI/Grok/Claude等OAuth及session路径后置，保持既有实现。其他API-key、静态签名及凭据文件认证路径按实际支持矩阵纳入，不凭品牌名称推断认证方式。

验收以真实入口为准：模块目录/配置校验、认证与协议路由桥接、连接测试应一致；通用兼容模块覆盖纯地址/Key差异，专用模块包装已有执行器，不创建重复调度/账务或额外生成重试。后台消费同一安全模块目录，不返回凭据值，编辑旧账号不得清空已有秘密或静默变更profile。

清点已完成：13个实际platform ID、7个多协议profile，仍在接线实现，不将首批单模块测试视为“所有非OAuth完成”。不部署、不真实调用provider、不修改AWS。

### 现有非OAuth覆盖清单（源码盘点，非上线能力承诺）

| 族 | 本轮需覆盖的既有路径 |
| --- | --- |
| OpenAI兼容 | OpenAI API Key，Responses/Chat、覆盖模式与探测默认；保留Ollama Cloud等host差异 |
| 多协议profiles | Kimi、Zhipu、DeepSeek、MiniMax、OpenCode Go/Zen、Command Code、Cline，保留profile modes、model规则及自定义base优先级 |
| Anthropic | API Key/Bearer兼容、Vertex service_account，不含OAuth/setup-token |
| Gemini | AI Studio API Key、Vertex service_account及Express API Key，三者发现能力不相同 |
| Bedrock | Bearer API Key、静态/STS SigV4、operator credential_file；仅已有Anthropic InvokeModel族，不虚称通用Converse |
| Grok | API Key专用文本及媒体/搜索/语音/realtime；不迁移订阅OAuth生命周期 |
| Antigravity | API Key兼容网关与upstream静态passthrough；二者测试/发现覆盖不同，不迁移官方OAuth |
| TypeSafe | System One原生非流式协议，不按网关family名称误当Anthropic |
| 非文本操作 | 既有OpenAI Images/Embeddings、Ark/Seedance任务、Grok媒体与音频、Gemini/Vertex批量图片；不能只给文本入口注册模块 |

明确不支持的发现路径继续返回不支持：Bedrock、Anthropic Vertex、Vertex Express、TypeSafe等不能因为统一目录而伪称具备模型自动发现。OpenCode/Command Code以映射后的实际模型及规则选择协议；DeepSeek Responses特殊路径、Cline钱包/冷却、各方usage和工具流式语义保留。

已有 `platformCatalog.builtin.json` 及后台domain catalog继续作为品牌与表单基础，安全模块目录提供认证/执行与能力，不再手工维护第二套厂商清单。每项操作需映射实际执行器或明确不支持，模块ID存在不等于全部操作已迁移。

### 全量接线进展（最终验证中）

当前实现17个模块覆盖13个平台：7个profile模块自动复用既有providerProfiles，另有openai-compatible、anthropic-apikey、anthropic-vertex、bedrock、gemini-apikey、gemini-vertex、grok-apikey、antigravity-apikey、antigravity-upstream、typesafe。模块输出认证策略、执行family与协议策略，由既有dispatch消费；非文本入口加入显式绑定校验，复用既有执行器。

管理员目录 `GET /api/v1/admin/upstream-providers` 返回安全描述，无凭据及账号配置值。Operations表示已接线宿主入口，受Conditions及原账号/模型规则限制，不是授权白名单或完整能力保证。Vertex服务账号可发现模型，Express模式不可；TypeSafe仍非流式；Bedrock仍限定既有InvokeModel路径。

管理创建/编辑表单复用同一目录；新非OAuth账号唯一匹配时自动绑定，旧账号编辑保持未绑定，未知或错误绑定可见且拒绝静默回退。OAuth/session保持原路径。

后端已冻结并完成验证：扩展原生/client回归8.982s通过；最终模块、协议golden、SystemOne及写入校验回归5.940s通过；安全目录handler测试0.022s通过。`GOMAXPROCS=2 GOMEMLIMIT=1200MiB GOGC=30`下串行server build、registry/compat/service/admin/routes显式vet、diff检查通过。create/update/extra patch/bulk及执行前校验显式绑定；model-sync及非文本入口保留现有能力限制。未跑全套回归或真实provider，race受CGO/编译器限制未运行。

前端最终Node24.22.2串行验证exit0：typecheck通过；原站build通过（i18n3项、vue-tsc与Vite59.80s）；供应build通过（5.17s，模块边界守卫通过）；4文件158项聚焦测试通过（13.69s），改动文件ESLint及diff检查通过。存在非阻断Browserslist陈旧、Node DEP0190、既有混合导入/大chunk警告。前后端本批本地聚焦验收完成，不等于全套回归、真实提供方或正式部署验收。本批是模块策略、凭据策略与已有执行器接线，不是全新通用执行框架；go.mod/go.sum、Account模型、调度与账务未变化。

## 渠道差集的范围收敛（2026-11-04）

用户明确要求：特殊协议和旧版协议仅记录，不实施。本轮不因new-api存在对应目录而扩大开发范围；后续新增渠道优先复用现有标准兼容接口。以下为规划范围，不表示已实现。

### 仅记录、不实施

- 旧版协议：Google PaLM、百度文心V1、旧智谱invoke/JWT、MokaAI专用接口，以及旧兼容聚合品牌的单独执行器。
- 特殊认证/专用文本协议：Azure deployment/api-version、腾讯TC3、讯飞签名WebSocket、Ollama原生/api与NDJSON、Cloudflare Workers AI原生路径等。其可复用标准兼容入口的部分不因此被禁用，但不新增专用适配。
- 工作流/机器人协议：Dify ChatFlow、Coze Bot会话，以及Advanced Custom等任意转换/路由引擎。
- 专用媒体/任务协议：Midjourney代理、SunoAPI、Sora、Veo/Vertex视频、Kling、Vidu、Hailuo、Jimeng CV、Replicate predictions、百炼原生图片/视频等新增执行器。
- 已有渠道的特殊扩展：DeepSeek独立Search/Fetch、厂商原生多模态embedding、Imagen predict、厂商专用TTS/音频/WebSocket、Bedrock Nova/Mantle等。

上述列表是本次差集新增工作的排除项，不删除Sub2API已有对应能力（例如Grok媒体、Seedance、Vertex/Bedrock已有执行路径），也不撤销已完成的非OAuth模块化接线。若将来需要某项，另行明确范围。

### 高复用能力及简单上游：已授权，实施中

用户随后要求“先补高复用能力，然后加入那些常见的简单方便的模型上游”。本批优先实现标准兼容Rerank入口，再扩展常见API Key兼容渠道；不因该要求恢复上述特殊/旧协议开发。

Rerank对已有候选文档按查询相关性重排，不负责首次检索或生成。标准入口拟归一化query/documents/top_n与结果index/score；不同模型分数不可直接比较。必须复用唯一调度、权限和客户账本；usage缺失不得当零消费，价格/计量不支持时明确拒绝，不能先上线不计费的成功通路。首批只标准Bearer JSON兼容协议，厂商专有操作另行排除。

Rerank上线阻塞已确认：现有RecordUsage的pricing_missing路径可能记零，billing repo缺失可走legacy best-effort；UsageBillingCommand只有已知token/金额，没有“上游已执行但usage缺失”的持久pending/ambiguous消费记录。本轮不改造整个账本，撤回未完成Rerank执行草稿及前端可开启选项，仅保留明确拒绝、不调用上游的契约入口。不得将151项中间前端测试视作Rerank已支持。后续先完成持久待对账事实/恢复设计再开放Rerank；不为赶工推定零消费或重发。

候选简单上游：OpenRouter、SiliconFlow、Mistral、百炼OpenAI兼容模式、腾讯TokenHub、千帆V2标准兼容部分。逐个核实路径/auth/protocol差异后复用执行器与中央目录；候选不等于已实现或真实验证。不默认声明Responses、图片、模型发现等额外支持。已转为优先推进这些简单上游profile/前端配置。

六上游已实现：`openrouter`（api/v1）、`siliconflow`（v1）、`mistral`（v1）、`bailian`（compatible-mode/v1）、`tokenhub`（v1，Bearer非TC3）、`qianfan_v2`（v2）。仅payg/API Key/Chat Completions，显式Responses/Anthropic/adaptive及复合旧key拒绝；不默认添加OpenRouter归属头。model_discovery=false，前后端禁止模型同步；不宣称余额、原生媒体或其他协议能力。总计23模块/19平台，平台目录由Go契约测试生成，不手工维护第二份。

后台最终profile/catalog、六实际路由、模块与legacy golden回归6.961s通过；Rerank输入限制及501拒绝测试0.024s通过（无上游、无账本）；domain/pkg契约、server build、相关vet和diff检查通过。首轮六路由测试fixture将key强制改为sk-test导致断言失败，修复夹具后通过，未以修改真实路径掩盖问题。未真实调用提供方或部署。前端最终Node24.22.2串行验证exit0：typecheck、原站build（i18n3项、vue-tsc、Vite54.94s）、供应build（5.22s、隔离守卫）均通过，7文件231项测试15.83s通过；ESLint/diff通过。此为六上游Chat-only切片终态，后续协议扩展另行验收。

## 后续切片（尚未实现，受上述范围约束）

1. 明确模型/协议/工具/流式/usage语义的能力描述与账号观察证据，不能仅按模型名称等同能力。
2. 在现有HTTP安全客户端内抽取请求构造与签名小接口；SSRF、重定向、地域/模型allowlist仍由宿主执行。
3. 将现有流式解析、usage、错误分类渐进包装，保留已发送/部分流/结果不明语义；模块不能私自触发第二次生成。
4. 注册更多有实际差异的提供方模块。仅base URL及API key不同的上游优先复用兼容模块，避免每个品牌复制相同代码。
5. Control展示注册模块及其明确能力，账号创建时校验绑定；旧账号维持兼容。

下一批以真实可调用路径和契约测试推进，不先创建大量无消费者接口，也不一次搬迁整个Gateway。
