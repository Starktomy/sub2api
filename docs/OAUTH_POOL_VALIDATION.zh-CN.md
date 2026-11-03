# OAuth 同池、亲和、回退、工具与刷新验收

日期：2026-11-03。范围：当前 EC2 上的两个实验 Gateway、Control、单 Worker，真实 RDS/Valkey，用户授权的两份 OpenAI OAuth 账户。不是完整生产或全部供应商验收。

## 实际结果

| 项目 | 实测证据 | 结论 |
| --- | --- | --- |
| 同池调度 | 两账户经 Control 绑定同一新测试组，同一客户 Key 的成功用量先后落账户1和2 | 通过基本候选选择与状态切换；未测权重公平性/容量压测 |
| 跨 Gateway 会话亲和 | 第一次工具请求落账户1；将账户2优先级调高、账户1调低后，同一 `session_id` 经另一 Gateway 续接仍落账户1 | 通过本次软亲和场景，不代表故障中会话无损迁移 |
| 工具调用 | 强制原生 Responses `echo`，实际返回一个 function_call，参数严格为 `{"text":"ping"}` | 通过；工具仅本地返回输入，无外部副作用 |
| 工具结果续接 | 完整回放首次output并附匹配call_id的function_call_output，跨Gateway得到包含ping的最终文本 | 通过；未使用OAuth HTTP不支持的previous_response_id |
| 停调后的逃逸 | Control停调账户1，同一session请求成功落账户2 | 通过发送前过滤/软亲和逃逸；单独不构成attempt失败回退 |
| 真正传输失败回退 | 给已绑定的账户2配置已确认未监听的localhost HTTP代理；同一请求日志记录账户2 `openai.upstream_failover_switching`（502）后账户1完成；RDS成功用量归账户1 | 通过请求发送前可确定失败的代理连接故障；不是OpenAI已收到请求后的拒绝、超时或部分流恢复 |
| 官方串行刷新 | `POST /api/v1/admin/accounts/1/refresh` 返回200，DB access和refresh token均变化、expires_at存在；成功响应无新token | 通过一次串行刷新；不宣称管理员刷新入口具备CAS或并发轮换安全 |
| 刷新后调用 | 账户1刷新后HTTP200/completed，20输入/5输出token，RDS对应usage | 通过 |

## 请求与费用

本批共5次真实生成、1次管理员手动刷新；没有压力请求、没有人为触发真实429、没有支付调用。

| 请求 | 成功账户 | input/output token | 本地客户费用USD |
| --- | --- | --- | --- |
| 首次工具调用 | 1 | 65 / 29 | 0.000478 |
| 工具续接与跨Gateway亲和 | 1 | 110 / 5 | 0.000280 |
| 停调逃逸 | 2 | 23 / 5 | 0.000106 |
| 代理故障重选 | 1 | 23 / 5 | 0.000106 |
| 刷新后生成 | 1 | 20 / 5 | 0.000100 |

RDS本批5条用量合计0.001070美元。金额来自本地价格配置，不是OAuth订阅上游实际成本。前一轮两次基础调用另计，本批不重复计算。

## 测试脚本故障与证据补齐

池脚本最初将ISO8601带微秒时间直接交给journalctl，未检查journal返回码；故障请求虽然成功，脚本未取到关联日志，因此原始result保留 `passed=false`，停止且未刷新。

随后以UTC格式化时间独立查询，找到同一request_id的账户2切换标记和账户1完成事件，保存 `transport-evidence-followup.json`，不伪改原始失败记录、不重复前4次模型请求。脚本已修正journal时间格式；完整脚本未再次运行，后续刷新由独立工具仅执行一次。

## 停用、凭据与安全边界

- 所有测试账户最终status=disabled/schedulable=false、proxy_id=NULL，恢复原分组；测试Key inactive、测试用户disabled，故障代理inactive。活跃Key数为0。
- Control更新已先传播inactive/停调，再用SQL恢复测试库的disabled标记；不存在把DB标记误当官方status枚举的问题。
- 保留真实用量和账务记录，不删除历史账务。
- 账户1refresh token已轮换。有效凭据保留在测试DB和root私有恢复文件中；原 `/root/ai/account.test` 未改写，不能重新导入旧文件覆盖有效版本。
- 对本轮audit和四服务journal检查当前access/refresh/id token的精确匹配均为0；这不是全面日志安全保证。
- 四服务health复查正常；不扩建ECS/ALB、不修改DNS、不推送。
- 手动刷新入口缺统一凭据版本CAS；后台锁测试通过不等于管理员入口有CAS。刷新还可能best-effort查询订阅/隐私配置，不能描述为只交换token。

## 验证工具与证据

- `tools/verify_oauth_pool_live.py`：本次池/工具/亲和/故障测试。工具请求上限8，实际运行4次后因日志采集断言停止。
- `tools/verify_oauth_refresh_live.py`：一次刷新与一次生成。
- `tools/oauth_integration_support.py`：限定loopback端口与命名TLS测试库，凭据不打印。
- 私有证据根目录：`/root/.local/state/sub2api-cloud/aws-integration/`，子目录 `oauth-pool-20261103-01` 与 `oauth-refresh-20261103-01`。含凭据的恢复文件不得公开或提交。
- `GOMAXPROCS=2 GOMEMLIMIT=1800MiB GOGC=60 go test -p=1 -tags=unit ./internal/service -run 'TestOpenAITokenProvider_|TestOpenAIAccountScheduler' -count=1 -timeout=3m`：通过，0.303秒。
- 首次未带unit标签的聚焦命令显示no tests to run，不能列为通过覆盖；补标签后的测试才是有效结果。

未覆盖：真实429/配额耗尽、已发送结果不明重试、部分流失败、并发刷新CAS、长连接/WS、供应商API与OAuth跨平台统一调度、模型公平性和负载性能。
