# Kiro 原生适配实施计划

日期：2026-11-04。分支：feat/modular-upstream-adapters。

## 状态与本批范围

用户已授权继续完善。P1协议内核已通过离线验证，用量事实层只读设计完成；当前实施P2的受控单次HTTP执行与加密凭据边界。尚无公共Kiro渠道、真实登录或商业供给；未接账务前不开放收费推理。不得因为新增DTO或注册名宣称已支持Kiro。

| 阶段 | 状态 | 交付 | 不包含 |
| --- | --- | --- | --- |
| P1 协议内核 | 已实现，离线验证通过 | Go DTO、增量EventStream/双CRC/0–9类header、复制访问器、事件解析及attempt状态 | 未接网络/账号/自动重试/SSE HTTP入口，不推断生成成功 |
| 用量事实层设计 | 设计完成，持久层实施中 | attempt屏障、追加usage事实、pending reconciliation三实体；预留前向迁移256 | 未通过最终验证、未应用正式迁移、不自动计费 |
| P2 单账号执行与加密凭据 | 持久接线及私有文本链已实现，最终验证中 | 加密引用、管理员JSON导入、持久刷新CAS、私有纯文本Messages转换/增量输出及原始metering journal已接线；迁移256/257待验收 | 公共收费Forward仍关闭；system/tools/images等输入拒绝；不伪造usage/成功终态；尚无真实调用 |
| P3 可计费用量与恢复 | 未实现 | 明确合同价格、可信来源、pending/ambiguous、幂等账务 | 不把模拟token/cache当供应商真值 |
| P4 刷新与管理 | 部分实施，后端最终验证中 | 刷新pending/CAS和admin导入已接线；前端133项测试、ESLint、typecheck、原站及供应构建通过 | 仅导入，模型/额度、持久状态/edit及完整生命周期未完成；不回写明文JSON，不读宿主登录缓存 |
| P5 其他入口转换 | 未实现 | 复用现有Chat/Responses转换到Kiro执行 | 不宣称Kiro原生Responses或完整状态保存 |

## 后续全链路实施与真实验证范围（2026-11-04）

用户已明确要求完成后续并授权使用外部 `/root/ai/kiro.test` 单账号真实测试。该文件不入Git/日志/文档，权限已收紧0600，结构为仅刷新材料的单对象。此处不保存其秘密或身份值。

本轮实施持久凭据及单次刷新/轮换CAS、受控单账号原生Messages转换/输出、管理JSON导入、发送屏障与用量待对账。迁移256保留用量journal，Kiro实体使用随后空闲编号；未验证前不部署/不应用正式库。

真实验证由主会话统一执行，agent不读取或调用真实fixture。先本地验证，再单次刷新，确认新凭据安全持久化后才允许最多32输出token的最小推理，不压测/并发/多区探测。导出无region时只按原项目默认us-east-1作一次明确尝试，不自动改用其他地区。客户端标识使用真实集成信息，不合成官方OS/设备或轮换身份；如上游不接受则报告阻塞，不绕过。

未登录不等于不刷新：JSON import可包含refresh-only材料，需宿主持久刷新后使用。不增加浏览器/device登录流程。真实上游权限、模型及计量结果未验证前不得宣称全部完成；无可信计量合同则公共收费流量保持门禁，不能用估算或零成本路径凑通。

## 真实验证结果（2026-11-04）

在独立本地PostgreSQL测试库应用完整迁移并复核，未改正式RDS。第一次调用因fixture当时为单元素数组，在本地导入前拒绝；凭据表及attempt均为0，未发送外部请求。原文件未改，仅将唯一对象提取到外部0600私有测试文件，独占marker分别保留。

随后执行唯一一次外部测试：region显式us-east-1，模型claude-sonnet-4.5，请求max_tokens32，30秒deadline，接收文本256字节即取消上限，无重试/换区/身份轮换。刷新成功，新凭据version从1至2、refresh_state=idle、加密envelope已持久保存；随后单次生成HTTP200、1个事件、观察到2字节文本。未打印文本/token/原始响应。

completion_verified=false，SDK返回未验证完成状态。journal验证send_intent=true、status=usage_unknown，事实source=none/quality=unknown/partial=true、reconciliation=pending/usage_unknown；无扣费。测试Account仍disabled且schedulable=false。这证明刷新与原生数据面确有响应，不证明完整SSE/工具/用量/收费渠道已完成。

私有PG状态、密钥、marker及加密数据库备份保存在仓库外受限目录，避免轮换材料丢失；不得删除marker或用旧fixture盲目再刷新。真实测试预算本次已使用，不以其作为长期自动调用许可。

前端133项测试、ESLint、typecheck、原站及供应构建通过。后端Kiro协议/service/repository/admin聚焦回归、实际本地PG刷新CAS/提交不明/取消后保存/ID与SSO分离，以及server/migrator/private-smoke构建通过；routes测试命令无匹配测试，不记录为路由断言通过。完整全后端测试及生产部署未执行。

## 参考仓库继续完善（实施中，未验收）

真实单次测试已验证刷新与响应，但不代表完整渠道。后续继续只用离线/独立临时库验证，未经再次明确范围不重用已消耗的真实测试预算。

| 切片 | 正在实施 | 保留门禁 |
| --- | --- | --- |
| 协议语义 | 已实现且包级离线验证：工具schema/use/result同类历史、唯一明确的省略ID/name后续分块；clean TransportEOF独立观察、终结sticky once | system、混合text/tool历史、images/thinking/cache/tool_choice等拒绝；不可无损表示不修复；EOF不等于成功 |
| 管理生命周期 | 持久状态、受限配置、暂停、明确重新导入恢复、手动模型/额度快照 | 不因租约到期重放旧refresh，不自动推理，不将目录当授权；收费路径仍阻塞 |
| 管理前端 | 安全状态/恢复/配置及显式查询操作，中英文提示来源/陈旧/未知 | 不展示秘密，不后台偷偷查询上游，不将pending当ready |
| 密钥配置 | 拆分Kiro专用vault配置，减少与SupplierPortal的隐含耦合 | 缺密钥失败关闭，不能回退明文 |

协议切片包测试/build/vet通过，覆盖率95.4%；FuzzConvertMessages 5秒67573次、FuzzDecoder 5秒47303次通过，包含文本+工具分块、cleanEOF、截断、异常、取消、sticky终结。主来源及补充来源固定revision；AIClient2API仅观察事件行为，未复制GPL代码。未读取真实fixture/key/数据库，不真实调用。

管理代码与接口仍在变化，尚未整体验收；公共计费合同/原子结算仍是独立缺口，不移植参考项目的估算、比例缩放或第二账本。

## 模型列表修复与真实诊断补充

新增加密profile_arn保存：导入保留、刷新缺失保留旧值/合法新值更新，按区域校验；旧schema1密文兼容。模型请求按存在的有效ARN加入请求体，不猜测补值。service TestKiro、vet、诊断工具build及diff检查通过。

本轮使用已轮换凭据做一次模型诊断，上游明确返回HTTP400，未刷新、未重试、未换区。当前未取得实际模型列表，不能将静态模型名称当作该账号列表。HTTP400只证明请求被上游拒绝，尚不能断言缺ARN或权限原因；需进一步白名单化上游错误代码/消息分类后定位，不记录原始响应或token。profile字段丢失为已修代码缺口，但不是已证实的本次400原因。

## 官方 CLI 安装与连接观察（2026-11-04）

- 从官方 stable manifest 下载 kiro-cli 3.0.0 Linux x86_64 zip，SHA256 校验为 `83dd96e00a75937958096da0436703562bc8c841bef66bde00a4caa5494d8be8`。只提取二进制至 `/opt/sub2api-tools/kiro-cli-3.0.0/kiro-cli`，未运行安装脚本；使用独立0700 HOME，不读取原测试JSON或轮换密钥。
- `login --help` 没有JSON导入选项；`chat --list-models --format json` 在空HOME自动进入设备登录，25秒超时结束，未完成登录、未发起生成、未取得模型列表。
- 用仅监听127.0.0.1、只转发官方服务443端口的临时CONNECT代理，进程级设置代理，15秒有界观察到 `oidc.us-east-1.amazonaws.com`。这是TLS连接目标元数据，不是解密HTTP抓包；未安装全局CA，未保存请求头/body/token，未捕获模型请求。
- 官方二进制静态字符串包含 `KiroControlPlaneBearerService.ListAvailableModels`、对应Smithy serializer、`maxResults`/`nextToken`，与旧参考代理的 `AmazonCodeWhispererService.ListAvailableModels` 不同。当前请求target已按此线索修改并增加对应测试断言；静态证据不等于已捕获实际请求，能否解决HTTP400仍需测试确认。
- 官方CLI的已认证模型抓包仍受登录阻塞；不通过伪造CLI凭据数据库或重新导入旧refresh材料绕过。此前安全错误分类测试及私有诊断工具构建成功。本次独立 `GOMAXPROCS=2 GOMEMLIMIT=800MiB GOPROXY=off go build -p=1 -o /tmp/kiro-observe-official-target ./cmd/kiro-observe-private` 成功；随后kind=models单次诊断返回 `credential_expired`，本地停止，没有外部模型请求、刷新或重试。target变更聚焦回归 `GOMAXPROCS=2 GOMEMLIMIT=1200MiB GOPROXY=off go test -p=1 ./internal/service -run 'TestKiro(Runtime|Observation)' -count=1` 及私有诊断工具build均exit0；不能将本次本地过期阻断记为模型修复成功。

## 官方公开源码交叉分析（2026-11-04）

用户允许改从CLI代码分析，暂未向CLI写入测试账号。公开仓库 `aws/amazon-q-developer-cli` 固定revision `15cc8f3cd18c4272925ce1c7053268eedff1ea0a`，是官方前身源码，不是已安装Kiro 3.0.0的同版源码，不混淆版本证据：

- `crates/chat-cli/src/api_client/mod.rs:277–309`：模型查询设置Origin::Cli、可选profileArn，并遍历paginator。
- `crates/amzn-codewhisperer-client/src/types/_origin.rs`：Origin::Cli序列化为 `CLI`，不是当前参考代理的 `KIRO_CLI`。
- `crates/amzn-codewhisperer-client/src/protocol_serde/shape_list_available_models_input.rs`：请求体字段origin、maxResults、nextToken、profileArn、modelProvider；未显示必须额外添加origin查询参数。
- `crates/amzn-codewhisperer-client/src/operation/list_available_models.rs:262`：旧版target确为 `AmazonCodeWhispererService.ListAvailableModels`；已安装3.0.0二进制则包含新的KiroControlPlaneBearerService target，不能把旧源码当作新版抓包。
- `crates/chat-cli/src/auth/builder_id.rs:501,657`：OIDC输出access_token存为BuilderIdToken并提供给Bearer identity。当前本项目models使用推理idToken，quota才使用SSO access token；这是另一个待实证的差异，并非已确认的失败根因。
- 3.0.0隔离SQLite有auth_kv表；二进制包含 `kirocli:odic:token` 与 `kirocli:odic:device-registration`。只检查结构，未写入凭据，未伪造client secret过期时间等缺失元数据，未执行额外刷新。

下一步应验证同一版本的origin/token契约，或在受控凭据交接下捕获真实模型RPC；不盲目混合两代协议、轮换身份或批量试探参数。目前仍未获得真实模型列表。

## 官方CLI账号导入与TLS解密结果（2026-11-04）

用户明确授权转换导入测试账号并解密模型请求。本地持久刷新一次成功，凭据version=4、refresh_state=idle；加密数据库继续持有唯一refresh材料。私有转换工具只导出短期OIDC access token（refresh_token=null），写入隔离CLI的auth_kv；未复用旧fixture、未交出第二份refresh权限、未伪造设备注册/过期时间。CLI `chat --list-models --format json` exit0，返回8个模型，default_model=auto。

首次TLS证书将CA直接作为服务端证书而握手失败。改为独立临时root CA签发CA:FALSE/serverAuth/SAN服务端证书后，CLI通过进程级SSL_CERT_FILE信任，TLS解密及真实转发成功，CLI exit0、上游HTTP200。代理只监听127.0.0.1，只允许management.us-east-1.kiro.dev:443；未修改全局CA，未持久化Authorization、token或完整原始包。协议证据已对profile参数脱敏。

实际捕获请求：

```http
POST /?origin=KIRO_CLI&profileArn=<URL编码的profile>
Host: management.us-east-1.kiro.dev
Content-Type: application/x-amz-json-1.0
X-Amz-Target: KiroControlPlaneBearerService.ListAvailableModels
Authorization: Bearer <REDACTED>

{"origin":"KIRO_CLI","profileArn":"<profile>"}
```

捕获profile与官方二进制嵌入字面值一致，是CLI默认服务profile而非测试账号身份。本实现仅在us-east-1且导入未提供profile时使用此已验证默认；其他region不猜值。Models改用OIDC SSO access token，profile同步传入query/body；保留固定端点、无隐式刷新/重试和响应白名单。

这推翻了将旧官方前身源码origin=CLI用于新版的猜测；仅新target不足以解决400，手工origin=CLI请求仍400。当前以同版CLI真实成功请求为准，不将所有差异分别宣称为已独立证明的唯一根因。

真实CLI返回模型：auto、claude-sonnet-4.5、claude-haiku-4.5、deepseek-3.2、minimax-m2.5、minimax-m2.1、glm-5、qwen3-coder-next。该结果不是生成权限或计费合同验证。Sub2API第一次后台回归因旧测试仍期望过期SSO时允许模型请求而失败，未进入build/真实查询。更新用例：models和quota均拒绝过期SSO；恢复有效SSO后401不刷新/不重试。随后 `GOMAXPROCS=2 GOMEMLIMIT=1000MiB GOPROXY=off go test -p=1 ./internal/service -run 'TestKiro(Runtime|Observation)' -count=1` 通过（0.020s），私有诊断build通过。单次真实Observe返回 `state=known entries=8 version=4 paused=true`，8个model ID与官方CLI一致，快照已持久保存。无生成/扣费/隐式刷新或重试。模型列表HTTP400问题在该us-east-1测试账号已实测解决；完整渠道/分页/公共结算仍不是本次验收范围。

## 额度、推理与安装身份抓包复核（2026-11-04）

- 官方CLI `/usage` 在隔离HOME完成，解密额度请求HTTP200：management.us-east-1.kiro.dev根路径POST，target=`AmazonCodeWhispererService.GetUsageLimits`，origin=KIRO_CLI及profileArn同时出现在query/body。当前quota实现已由参考IDE GET迁移到此RPC；聚焦回归/构建及单次真实查询链exit0，最终独立查询亦返回state=known、entries=1、version=4、paused=true。
- 官方CLI单次最小推理使用claude-haiku-4.5、仅要求回复OK、工具trust为空，代理对生成最多转发一次，阻断重试/后续生成。CLI exit0，runtime.us-east-1.kiro.dev根路径POST，target=`AmazonCodeWhispererStreamingService.GenerateAssistantResponse`，HTTP200；725字节EventStream，全部帧CRC有效。未保存prompt/生成文本/token。
- 推理帧顺序：initial-response(conversationId)、assistantResponseEvent(content/modelId)、metadataEvent(stopReason)、contextUsageEvent(contextUsagePercentage)、meteringEvent(unit/unitPlural/usage)。本次只保存字段结构，尚未记录stopReason值；不能据此直接新增成功终态。推理现有实现仍是参考IDE q端点，且未传播profile、未使用metadata stopReason，不能宣称已与CLI一致。
- 官方CLI请求UA是Rust SDK+实际Linux+appVersion3.0.0/AmazonQ-For-CLI，未观察到machine-ID请求头。SDK声明max=3；本平台继续max=1，不照搬重试生成。CLI自动注入了工具schema/环境和历史，本平台不伪造这些额外内容。
- 官方前身源码 `crates/chat-cli/src/telemetry/mod.rs:538–564`：遥测关闭返回固定禁用UUID；启用时优先合法环境override、持久数据库clientId、旧配置迁移，否则Uuid::new_v4并持久保存。隔离3.0.0数据库确认state键telemetryClientId。此为安装/遥测ID，不是硬件指纹，也不是OIDC clientId、请求invocation UUID。
- 按用户要求，同样UUIDv4规则已在仓库外0600文件生成本集成独立安装ID，生成一次/复用、不轮换；未复制官方CLI已有ID，未向模型/额度/推理请求添加未观察到的machine-ID字段，也未新增发送遥测。

本次真实CLI推理没有提供服务端输出上限参数，仅通过极短请求、40秒进程deadline和代理单次门禁限制；不是max_tokens参数语义验证。本次客户端遥测在CLI自身默认行为下发出SendTelemetryEvent，后续抓包应预先阻断该target；本平台不实现模拟官方遥测。

## 推理CLI对齐增量（2026-11-04，离线验收）

- 私有执行选择Endpoint=cli，仅允许已捕获us-east-1 runtime根路径；原IDE transport保留，不扩展未验证地区。
- CLI使用application/x-amz-json-1.0与GenerateAssistantResponse target，不带IDE agent-mode；保留真实集成UA、单次max=1、禁止GetBody重放，不冒充官方版本。
- 私有执行在持久刷新之后独立选择OIDC access token；SSO过期拒绝，不回退idToken。凭据profile绑定到请求，调用方冲突拒绝；journal摘要包含绑定后的实际请求。
- Messages用户输入补origin=KIRO_CLI。不添加虚构CLI启动历史/工具/环境；agentTaskType具体取值和maxTokens扩展仍待验证。
- 新增metadataEvent.stopReason类型化复制访问器，尚未把未知reason判为成功，CompletionVerified门禁不变。
- 新增CLI transport、SSO过期/profile冲突、metadata复制测试。首次完整聚焦service回归因旧IDE fixture无SSO而失败，补正确CLI access凭据后全部TestKiro通过0.024s；协议全包通过0.041s，私有观察工具build及diff检查通过。
- 本批没有新增真实生成调用。尚需适配端受控真实推理、stopReason枚举/后续事件顺序、SSE完整终态与用量合同验收；不能以官方CLI此前成功代替本批实现实测。

## 提交后平台真实推理（2026-11-04）

用户授权提交推送及通过本平台测试。基点f81fab32c4ab225413159c27644877ce6307b778之后36个提交在当前模块分支重写Author为starktomy、保留Claude co-author，内容树不变，使用明确旧远端HEAD的force-with-lease推送；旧历史保留本地backup分支。首个推送HEAD为a74a941e1faf31f567222086c8942c9389e5e633。

推送后使用本项目已提交的私有ExecutePrivate执行链、持久凭据/发送屏障/journal，对claude-haiku-4.5单次请求（32 max_tokens扩展、30秒、256文本字节取消界限）。返回HTTP400、events=0、text_bytes=0、completion_verified=false、transport_eof=false；无重试。没有通过公共HTTP Gateway/线上API测试，公共收费门禁保持关闭。这次证明本平台当前推理请求仍不匹配上游；不能把官方CLI先前HTTP200当本平台验收通过。具体拒绝原因尚待受限诊断，未输出认证材料或原始错误body。

## 平台推理400修复与真实复验（2026-11-04）

Author已按用户纠正统一为Starktomy、GitHub noreply邮箱，Claude协作署名保留，历史修正已推送。

推理增加只输出白名单code/field的HTTP诊断，单次诊断返回ValidationException，field=additionalmodelrequestfields。官方CLI成功请求未带此字段；本项目此前继承fork的maxTokens扩展被runtime拒绝。Messages转换不再输出该扩展，CLI transport也在发送前拒绝调用方传入该字段。max_tokens不再假称有服务端执行保证，私有执行保留30秒及256文本字节取消界限。

修正后本平台ExecutePrivate单次实测：HTTP200、events=5、text_bytes=2、transport_eof=true、completion_verified=false。执行链在发送前完成持久刷新至version=5/idle（加密备份已保留），无生成重试，未知用量仍待对账，公共HTTP收费Gateway未开放。模型为claude-haiku-4.5；不保存生成文本。协议/聚焦Kiro service测试通过0.053s/0.075s；其他后台大范围回归单独记录，不混作通过。

## 逐项完善：文本成功终态已实测（2026-11-04）

新版CLI静态词表与平台白名单探针确认metadata stopReason为大写END_TURN，不是旧猜测的小写end_turn。Attempt只在END_TURN、所有工具闭合、无后续内容/重复终态/异常且完整EOF时验证成功；context/metering允许在metadata之后到达。未知reason、截断、取消保持未验证。当前工具结束原因未实测，不开启其成功映射。

平台单次真实复验HTTP200、events=5、text_bytes=2、metadata_stop_reason=END_TURN、completion_verified=true、transport_eof=true。私有Messages桥补message_start、内容块收尾、message_delta/end_turn、message_stop；usage用空对象省略未知token，不伪造0，因此仍不是严格完整Anthropic usage契约。桥的完整序列离线回归通过，未新增公共路由。

聚焦协议/service测试通过0.037s/0.063s，覆盖缺marker/unknown、截断、未结束工具、重复marker及后续内容。已完成生成不等于账务完成：journal仍保持usage_unknown/pending，不收费。

结算勘查：现有UsageBillingRepository.Apply自开事务，不能先扣费再另事务标settled；需复用同库claimUsageBillingKey/applyUsageBillingEffects，在同事务冻结命令并更新journal。现有attempt无客户/API key/订阅归属，历史私有测试不能追溯扣费。当前定价无credits单价；不能把per_request/token价格冒充credit价格。下一阶段需显式收费依据，再实现前向迁移、发送前冻结客户/价格上下文及原子settled/幂等恢复；价格缺失继续blocked，公共收费门禁关闭。

## 收费决策（2026-11-04）

用户明确选择“暂不启用收费”。后续继续完善终态、协议及用量事实，不接客户扣费、不新增默认credit费率、不开放收费Gateway。保留现有pending/blocked记录；历史私有测试不追溯结算。原子结算设计作为后续显式启用收费时的前置，不在本轮擅自实施收费。

## 客户侧改为现有token计费（2026-11-04，覆盖前述暂不收费选择）

用户明确要求客户与其他平台一样按消耗token计费。采用现有模型token价格/倍率/客户账本，不新增credits单价、不按次收费。上游credits仅作为供应成本事实，不能自行换算成token。

官方前身Smithy元数据有tokenUsage：uncachedInputTokens、outputTokens、totalTokens以及可选cacheReadInputTokens/cacheWriteInputTokens。已补指针型解析、非负及安全整数检查，并把有效报告写入provider_reported token事实，保持partial直到完整终态；不把缺失视为0。新版实际抓包目前仅stopReason/context百分比/credits，未观察到tokenUsage，不能因此宣称按实际token自动扣费已完成。

既有私有测试无客户/API key/订阅归属，不能追溯扣费。还需客户上下文发送前冻结、完整token事实选择和价格版本、复用既有扣费effects的同事务settled、幂等/提交不明恢复测试。缺少可信token时保持usage_unknown/pending，不擅自启用本地估算计费；公共收费入口仍关闭。

## 当前增量离线复核（2026-11-04）

Kiro协议全包test通过0.040s，全部TestKiro service通过0.021s，协议包vet通过。FuzzConvertMessages 3秒23614次、FuzzDecoder 5秒76083次通过。无新增真实调用或扣费。参考项目缓存算法正在独立只读核查，不在结论返回前变更计费分类。

## 后续顺序：小米MiMo

用户要求Kiro全部完成且真实测试后再加入小米MiMo。本轮不并行实现MiMo，不预先宣称其协议/模型/计费支持。Kiro仍有公共结算、完整工具/管理/模型额度等缺口，真实smoke也尚未执行；不能以内部包通过替代该前置。后续MiMo按官方认证/协议/模型及用量契约核对，复用唯一模块体系。

## 架构边界

唯一Gateway/账号调度/客户账本继续由Sub2API负责。Kiro adapter接收已选定账号及凭据版本，只执行该次请求；不能隐藏切账号、轮换端点、重试生成或扣款。取消与部分输出失败必须向宿主表达。

凭据只在运行时受限解密，Account/Redis快照保存引用，不保存明文。Social、IdC/Builder ID与External IdP分别建模；IdC推理token和额度控制面token可能不同。刷新请求结果不明时隔离恢复，不重放旧refresh token。

不移植自动合成/轮换客户端身份、模拟缓存统计、token缩放、明文文件写回、完整请求dump。工具历史过滤、模型别名及max_tokens截断是语义变化，未经明确策略不静默继承。

## 参考来源与许可

- P1主要协议来源改为原库kiro.rs，revision 8fb3dd691d1cedf437bd6a4e9ef9f60b3f217fdd，MIT，Copyright 2026 hank9999。已按用户要求先克隆到独立目录并完成只读对比，再恢复既有授权范围实施。
- 补充参考kiro2cc-proxy，revision e5387453c10a87e350fbe2d7c6505a1b128f5445，MIT，Copyright 2026 Harllan He。fork继承的parser不能只保留fork版权而漏原作者；采用fork独有改进时逐项记录双来源。移植部分保留许可及文件级来源。
- AIClient2API：revision a000ac802d87fa06afe6134baaeea3ed350bd08e，GPLv3；用户已明确允许复制和使用，实际移植仍保留许可及适用归因。本P1不引入其整套运行时。
- 参考仓库只读，不运行程序、不导入测试账号、不读取宿主登录缓存。

## 原库与fork核查结果

原库已具备AWS EventStream帧、0–9类header、双CRC及大小上限。使用CRC32 ISO-HDLC，不因依赖注释误用CRC32C。fork的decoder在Recovering状态继续同chunk扫描是独有修复；P1采用坏帧终止，不引入恢复/跳帧策略。

原库顶层request包含conversationState与可选profileArn；fork新增additionalModelRequestFields，作为有来源的可选扩展，不代表完整converter已迁移。原库丢弃metering/unknown payload及部分assistant字段，P1不复制该信息丢失：原始计量、未知及异常事件须有界保留。

工具stop仅结束单个工具输入块。原库在网络错误及EOF后都生成结束事件，不能作为上游成功证明；P1必须区分EOF、截断、网络异常、工具块结束与未知整体完成状态。原库context百分比/长度估算、fork缩放及模拟cache均不转成provider-reported事实。

刷新不属于P1：原库IdC仅accessToken，fork区分idToken数据面和SSO控制面token，后续适配不能回退到单token假设。原库还存在API-key认证mode，但本批不注册/启用它，不由源码字段存在推导真实账号可用。

## 用量事实与待对账设计（只读方案，未实施）

建议三个实体，均不是新余额或第二账本：

| 实体 | 内容 | 不变量 |
| --- | --- | --- |
| usage_attempts | 网关operation/attempt身份、账号及凭据版本、请求摘要、发送屏障、响应状态、owner/generation | 动作前生成稳定ID；send_intent不证明已发送；屏障后结果不明禁止重发 |
| usage_fact_revisions | 追加observation、来源/置信度/完整性、原始单位、revision/supersedes、证据摘要 | 相同observation同内容幂等，异内容冲突；NULL与0区别；不存prompt/token或完整响应 |
| usage_reconciliation | pending/blocked/未来settled、选定facts、冻结价格合同和结算指纹、处理租约 | P1不收费；未来与既有dedup及财务effects同事务完成，不在外部另事务标settled |

现有UsageBillingCommand表示已定价扣费，不适合表示未知消费。现有pricing_missing零成本路径、legacy best-effort回退及usage_logs不能充当pending队列。新durable路径应显式拒绝这些回退；不借本设计未经测试修改旧路径。

短事务先记录attempt及pending，再CAS提交send_intent；确认提交后在事务外访问上游；结果追加facts。提交不明只查询/重放相同本地写入，不能再次生成。租约过期授予本地恢复处理权，不授予上游重发权。

原始provider tokens、provider credits/units、本地tokens_estimated及display_simulated分别记录；不因存在credits就换算token，不默认对估算或模拟值收费。Kiro metering有unit/unitPlural/usage和可选cache字段，但没有完整input/output token真值。多次metering是否累计/增量未确认时不求和。partial不自动变complete；total-only不伪造输入输出拆分。

成功无usage保持usage_unknown，缺价格保持blocked/pricing_missing，不认领零金额dedup。未来结算必须明确单位、来源、partial规则和价格版本；同一冻结命令幂等结算，事后修订只能追加事实，补退款另建批准流程。Rerank501门禁继续保留。

源码接点：service/usage_billing.go、repository/usage_billing_repo.go、service/gateway_usage_billing.go、service/openai_gateway_usage.go；可参考oauth_refresh_state的屏障与提交不明处理，但不把usage塞入凭据表或按账号串行所有推理。

验证提案：提交成功但返回错误、重启/重复事实/冲突、迟到owner、发送不明及partial均不增加fake provider调用；模拟/未批准估算/缺价格不能触发billing；未来财务阶段临时PG验证去重、原子settled及余额只变一次。目前这些新测试尚未实现。

## 验证门槛

协议层覆盖任意网络分块、多帧、两类CRC错误、头部类型/长度、截断EOF、超大帧、缓冲上限、错误不泄露payload。事件处理不能把未知事件当成功结束，usage保留原始来源，未报告与明确零区分。

P1不调用真实Kiro或其他上游，不修改DNS/AWS/正式数据库，不部署。

## P1 最终验证（2026-11-04）

实现文件全部位于backend/internal/pkg/kiro，现有Bedrock/service未改。支持有界reader任意分块/多帧、ISO-HDLC双CRC、0–9类header及重复/UTF-8/长度校验；校验失败sticky terminal，不跳坏帧恢复。帧内EOF为io.ErrUnexpectedEOF；错误不回显payload或底层reader敏感错误，访问器返回复制数据。

text/tool/context/metering DTO及未知/error/exception的有界原始payload/header保留；attempt状态输出文本/工具delta并校验工具块状态。不以EOF、tool stop、metering或未知事件推断整体成功。请求envelope支持conversationState/profileArn及有来源的additionalModelRequestFields，不重写历史。provenance.go记录参考文件与排除行为，LICENSE保留两作者MIT通知。

backend目录下，所有Go命令使用GOMAXPROCS=2 GOMEMLIMIT=512MiB GOPROXY=off GOTOOLCHAIN=local：`go test -p=1 ./internal/pkg/kiro`、覆盖测试、`go test -p=1 ./internal/pkg/kiro -run='^$' -fuzz=FuzzDecoder -fuzztime=5s -parallel=1`、包build/vet均通过；gofmt及diff检查通过。statement coverage97.0%，fuzz186858次通过。

race测试因CGO未启用且无gcc未执行成功；未安装编译器。未执行全backend build、全套回归、真实上游、网络或账号操作。未注册可运行Kiro渠道，尚无SSE/HTTP执行、认证、刷新、调度和财务接线。当前未commit/push/deploy。
