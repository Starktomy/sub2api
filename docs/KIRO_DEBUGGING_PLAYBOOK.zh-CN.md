# Kiro 调试方法与实证记录

日期：2026-11-04。适用仓库：Sub2API Cloud，分支 feat/modular-upstream-adapters。

本文记录可复用的方法、失败经验及已验证协议，不保存凭据、私人profile ARN、设备ID、提示词或完整响应。实施状态详见 [KIRO_ADAPTER_PLAN.zh-CN.md](KIRO_ADAPTER_PLAN.zh-CN.md)。

## 1. 原则：先证据，后实现

证据优先级：同版本官方客户端实际成功请求 > 同版本官方源码 > 不同版本官方源码 > 第三方实现 > 猜测。

- 静态模型目录不是账号实时模型列表；CLI成功也不自动证明我们适配器成功。
- HTTP400只证明请求被拒绝，不直接证明权限不足或缺profile。
- 抓到CONNECT/SNI只证明连接目标，不等于解密请求。
- 字段存在不等于必填；一次成功不能证明每个修改都是独立必要条件。
- 旧Amazon Q源码与Kiro 3.0.0存在协议差异，不能拼接两代协议。
- 对外宣称要区分：实现、离线测试、真实上游、生产部署。

## 2. 凭据安全与轮换

1. 用户明确授权测试账号和调用范围。
2. JSON仅从仓库外0600文件读取，不打印值、不进入Git或审计body。
3. refresh-only导入先加密保存；Account仅持引用。
4. 外部刷新前提交持久pending/CAS屏障；轮换结果加密提交后才能推理。
5. 刷新结果不明不能因超时再次使用旧refresh token。
6. 已轮换账号的数据库是最新真值，不能重新导入旧fixture。
7. 临时CLI只导入短期access token，refresh_token=null；保留一个刷新owner。
8. 每次真实操作使用O_EXCL独占marker，不覆盖；保留密钥和加密数据库备份。

本次测试凭据持久版本已至4、refresh_state=idle。保存过期检查阻断记录，不把本地阻断当成外部失败或成功。

## 3. 官方CLI安装与隔离

- 从官方stable manifest定位版本与下载地址，下载后按官方SHA256校验，再执行二进制。
- 本次版本：kiro-cli 3.0.0，Linux x86_64。
- zip SHA256：`83dd96e00a75937958096da0436703562bc8c841bef66bde00a4caa5494d8be8`。
- 只提取二进制，不盲目运行install.sh。
- 下载包、解压目录、自己编写的脚本分别使用独立目录；Python使用`python3 -I`。
- HOME独立0700，工作目录为空且受控，不读取项目hooks/MCP/宿主登录缓存。
- 先读help。`chat --list-models --format json`可查询模型而不生成；未登录时它会自动启动设备登录。

本次CLI无官方JSON导入命令。经用户明确授权，检查SQLite schema与源码后向隔离auth_kv写入转换后的access-only对象；这是调试用途的内部存储转换，不是承诺长期稳定的导入API。

## 4. 无凭据输出的静态分析

二进制字符串可定位：target、端点、schema字段、存储键和源码路径。仅输出白名单协议线索，不输出整段缓存或敏感文件。

已找到：

- `KiroControlPlaneBearerService.ListAvailableModels`
- `kirocli:odic:token`
- `kirocli:odic:device-registration`
- `SSL_CERT_FILE`及代理支持

官方公开前身源码：`aws/amazon-q-developer-cli`，固定revision `15cc8f3cd18c4272925ce1c7053268eedff1ea0a`。必须记录它不是Kiro 3.0.0同版源码。

## 5. 从CONNECT观察到TLS解密

### 第一步：只观察连接元数据

临时HTTP CONNECT代理仅监听127.0.0.1，对CLI进程设置HTTPS_PROXY；只允许明确官方域名443。

结果只能报告host，不能说抓到了HTTP包。未登录只看到OIDC；导入后看到management。

### 第二步：准备正确证书链

首次错误：直接把CA证书作为服务器证书，TLS握手失败。

成功方式：

1. 临时独立root CA，basicConstraints=CA:TRUE，keyUsage含keyCertSign。
2. root签发服务端leaf：CA:FALSE、extendedKeyUsage=serverAuth、keyUsage=digitalSignature/keyEncipherment。
3. SAN列出实际捕获域名；本次是management/runtime/q的us-east-1端点。
4. 用openssl verify检查用途与链。
5. 系统CA集合加临时root形成独立trust bundle；仅给CLI进程设置SSL_CERT_FILE。
6. 不改全局CA、不关闭上游TLS验证；代理到真实上游仍校验官方证书。

### 第三步：选择性记录

只记录方法、端点、白名单header、body字段结构、HTTP状态、EventStream事件名/字段形状及CRC结果。

禁止持久化Authorization、Cookie、token、clientSecret、提示词、生成文本、完整请求包。profile参数脱敏；UA可能含身份后缀，输出前也需脱敏。

代理需限制body/response大小、连接超时、总时间和转发次数。生成最多转发一次，阻断重试及后续工具回合。后续测试应预先阻断SendTelemetryEvent等非必要遥测；本次观察到官方CLI自行发送该类遥测，本平台不模拟它。

## 6. 同版本CLI实测协议

### 模型列表

```http
POST https://management.us-east-1.kiro.dev/?origin=KIRO_CLI&profileArn=<URL编码profile>
Content-Type: application/x-amz-json-1.0
X-Amz-Target: KiroControlPlaneBearerService.ListAvailableModels
Authorization: Bearer <OIDC access token>

{"origin":"KIRO_CLI","profileArn":"<profile>"}
```

profile同时放在query和body；无账号profile时，官方CLI使用二进制内置的默认服务profile。实现仅对已验证us-east-1使用该默认，不猜其他地区。

错误经验：只替换target仍不够；旧源码origin=CLI与新版真实请求不一致。应以成功抓包为准，不继续盲试枚举。

实测：官方CLI及Sub2API均返回8个模型：auto、claude-sonnet-4.5、claude-haiku-4.5、deepseek-3.2、minimax-m2.5、minimax-m2.1、glm-5、qwen3-coder-next。Sub2API快照known、entries=8。

### 额度

与模型相同的management端点、POST、query/body origin和profile，但target为：

```text
AmazonCodeWhispererService.GetUsageLimits
```

不再使用参考IDE的`GET q.../getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST`作为CLI契约。

响应包括nextDateReset、subscriptionInfo、usageBreakdownList、overageConfiguration和userInfo。保存需白名单，不存userInfo身份。数值指针区分未报告与0，不擅自reset或更新套餐权限。

实测：官方CLI HTTP200；Sub2API按捕获修正后返回`kind=quota state=known entries=1 version=4 paused=true`。

### 推理

```http
POST https://runtime.us-east-1.kiro.dev/
Content-Type: application/x-amz-json-1.0
X-Amz-Target: AmazonCodeWhispererStreamingService.GenerateAssistantResponse
Authorization: Bearer <OIDC access token>
```

body结构包含conversationState（conversationId、history、currentMessage、chatTriggerType、agentTaskType）和profileArn。userInputMessage含origin/modelId/context。CLI自动注入工具schema、环境和启动历史；转换器不应为了“像CLI”而编造这些内容。

本次单次claude-haiku-4.5请求要求只回复OK，工具trust为空；CLI exit0、上游HTTP200，725字节EventStream。注意不信任工具不等于请求不包含工具schema。

观察到的事件：

1. initial-response：conversationId。
2. assistantResponseEvent：content、modelId。
3. metadataEvent：stopReason。
4. contextUsageEvent：contextUsagePercentage。
5. meteringEvent：unit、unitPlural、usage。

全部帧CRC有效。尚未保存stopReason枚举值，不能仅凭字段名就启用成功终态。现有Sub2API推理仍需从IDE路径对齐到已捕获CLI协议；本次成功是官方CLI成功，不是已完成本项目全部推理改造。

### 平台推理400的实际根因与修复

后续平台自身请求返回400，新增有界、白名单错误code/field诊断后定位为ValidationException/additionalmodelrequestfields。此前第三方fork的additionalModelRequestFields.maxTokens扩展在官方runtime路径被拒绝。

修复：转换器不再输出扩展；CLI transport在发送前拒绝此字段，避免无声丢弃调用方语义。max_tokens未获得上游强制限制保证，受控私有调用必须独立限制时间/接收字节；公共收费入口继续关闭。

复验使用平台ExecutePrivate而非官方CLI：HTTP200、5事件、2字节文本、clean TransportEOF；CompletionVerified仍false，不借此伪造完整SSE或可信token结算。这说明“HTTP可响应”和“完整商业渠道完成”是不同验收项。

## 7. 安装身份算法

官方前身源码`crates/chat-cli/src/telemetry/mod.rs:538–564`：

```text
遥测关闭 → 固定禁用UUID
合法环境override → 使用override
数据库已有clientId → 复用
旧配置有合法ID → 迁移并保存
否则 → 随机UUIDv4，保存一次
```

隔离CLI 3.0.0 state确认键`telemetryClientId`。本集成已按UUIDv4规则生成独立安装ID，仓库外0600保存，不复制CLI现有值、不按请求轮换。

必须区分：安装/遥测clientId、OIDC注册clientId、每次请求invocation UUID、conversationId和IDE machine ID。抓包未观察到模型/额度/推理请求有machine-ID header，不能把安装UUID硬塞进去，也不把kirors的refresh-token哈希算法误称为官方CLI算法。

## 8. 保留本系统安全边界

- CLI声明SDK max=3，本系统仍单次max=1；不复制自动生成重试。
- 使用真实集成身份，不伪造官方二进制/设备；协议兼容不等于冒充官方产品。
- CRC是IEEE/ISO-HDLC，不是CRC32C。
- clean EOF不自动等于整体成功；tool stop也不是整体成功。
- 原始provider credits与token估算、context百分比、模拟cache分开；不将其转换为虚构账务真值。
- metering多个事件是否增量/累计需明确，不能直接相加。
- 无冻结价格/原子结算时维持pending/blocked；测试账号保持paused，公共收费路径关闭。
- 一套Gateway、调度和客户账本，不引入第二网关或账本。

## 9. 验证清单

- [x] 官方工具校验安装、隔离HOME。
- [x] 最新凭据持久轮换，CLI access-only导入。
- [x] TLS解密，不改全局CA。
- [x] 官方CLI模型、额度及单次推理成功抓包。
- [x] Sub2API模型和额度真实快照。
- [x] EventStream协议包测试。
- [ ] 推理端点/token/profile/schema全面对齐及本项目真实回归。
- [ ] stopReason枚举与完整SSE终态契约。
- [ ] 分页、完整工具历史、多模态等覆盖。
- [ ] 可信用量价格合同与既有账本原子结算。
- [ ] 全量测试、浏览器验收和生产部署。

本轮命令：`GOMAXPROCS=2 GOMEMLIMIT=1000MiB GOPROXY=off go test -p=1 ./internal/service -run 'TestKiro(Runtime|Observation)' -count=1`；私有观察工具`go build -p=1`；`GOMAXPROCS=2 GOMEMLIMIT=700MiB GOPROXY=off go test -p=1 ./internal/pkg/kiro -count=1`。协议包通过0.049s；quota最终工具build及真实查询成功。后台额度聚焦回归、构建及真实查询链随后收到完成通知，exit0。上述均非全量后端测试或生产部署。
