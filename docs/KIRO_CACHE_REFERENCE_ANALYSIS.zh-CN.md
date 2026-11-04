# Kiro 缓存与 token 算法参考核查

日期：2026-11-04。只读核查，不代表已采用或实现计费。

## 结论与来源

三个参考项目没有统一的官方缓存算法。原版kiro.rs主要估算输入输出；AIClient2API不报告实际缓存量；kiro2cc-proxy实现本地缓存模拟及credits反推，不能直接作为上游命中事实。

| 项目 | 缓存口径 | token口径 | 状态持久化 |
|---|---|---|---|
| kiro.rs | 没有完整缓存读写拆分 | 输入优先context百分比×窗口，否则本地/远程估算；输出估算 | 无缓存账本 |
| AIClient2API | 流式read/write固定0，非流省略；cache_control部分转cachePoint | Anthropic tokenizer，失败回退字符/4；流式可用窗口百分比减输出 | 无缓存命中账本 |
| kiro2cc-proxy | 完整上游字段优先，默认会话前缀模拟，可选断点账本及credits校准 | cl100k BPE，可选远程计数；全文输出计数 | 默认会话内存，断点模式快照 |

参考revision：kiro.rs `8fb3dd691d1cedf437bd6a4e9ef9f60b3f217fdd`；kiro2cc-proxy `e5387453c10a87e350fbe2d7c6505a1b128f5445`；AIClient2API `a000ac802d87fa06afe6134baaeea3ed350bd08e`。保留各自许可，未修改参考仓库。

## kiro2cc-proxy默认前缀算法

设输入I，前缀Praw=tokens(system+tools+除最后一条外messages)。只有本地会话冻结前缀命中才P=Praw，否则P=0。缓存read=min(P,I)，write=0，普通输入=I-read。

路径：`src/anthropic/handlers/helpers.rs:262-282`。会话需有历史，首次或稳定系统内容变化不算read。冻结键=session_id+系统内容SHA256前16位，不包含API Key/上游账号/模型。内存容量1024，无TTL，超限按HashMap迭代首项淘汰，不是严格LRU；重启清空。见converter/history.rs:220-256,342-362及converter/cache.rs:8-35。

### credits校准

已标定模型、I>=4096、实际credits C>0时：

```text
Rcredits=clamp(round((r_miss*I+r_out*O-C*10^6)/(r_miss-r_cached)),0,I)
R=min(P,Rcredits,I)
```

P<=0时read仍0；费率是模型经验硬编码，非官方缓存证明。桥接续流禁用反推。见`src/anthropic/model/usage/pricing.rs:164-233,251-272`及handlers/nonstream.rs:655-665。

## 可选cache_control断点账本

promptCacheAccounting默认false，开启后替代默认前缀估算，上游完整read+creation仍优先。

- 从原始body解析tools/system/message内容块及顶层ephemeral标记。ttl=1h为1小时，其他有效ephemeral为5分钟。
- tools→system→messages累计SHA256；只剔除块顶层cache_control，schema内部同名字段保留。deferred tools在tool_reference处展开。
- key隔离API Key ID（缺失anon）+映射模型+内容；不含session或Kiro账号。同Key跨会话/账号可模拟共享，不同Key不共享。
- Anthropic无标记时全普通输入；OpenAI入口可补顶层自动断点。
- 从末端断点回看找最长有效命中；最多当前加此前20块，共21个位置。
- read=命中累计tokens；后续达到门槛的断点增量分别写入5m/1h；普通输入=总量-read-write5m-write1h。
- 本地模型门槛：部分5.x为512，Opus4.7为2048，Opus4.6/4.5及Haiku4.5为4096，其他1024；不是已证实Kiro规则。
- 结果按最终输入量缩放并限制不超总量。

见`src/anthropic/cache/breakpoint.rs:64-108,168-175,218-370,495-537`及cache/simulation.rs:147-179。

命中TTL在请求前plan刷新，上游失败也可能续期旧条目；新写入在成功收尾commit。每30秒清过期并原子快照prompt_cache.json；20万上限，淘汰至18万。启动恢复未过期项，崩溃可能丢最近周期修改。见breakpoint.rs:495-657、main.rs:305-311、handlers/nonstream.rs:688-708、stream/context/mod.rs:1122-1140。

## 流式与非流式差异

nonstream客户端usage和落库read均可credits校准；stream客户端使用静态前缀，落库才校准，可能展示与管理记录不一致。两者仅在上游read+creation同时存在时使用完整上游对；stream该分支未调用nonstream clamp_to_total，不应宣称边界一致。见nonstream.rs:647-703、stream/context/mod.rs:283-321,1032-1041。

指纹模式按credential ID隔离，read=min(匹配累计tokens,floor(0.85*I))，write=I-read，普通输入=0；另有比例兜底。但正常入口始终传Some(prefix)，包括0，会压住这些后备分支，不能把模块声明当作默认实际路径。见cache/fingerprint.rs:177-229、cache/simulation.rs:111-138。

## 展示缩放不是计费真值

kiro2cc-proxy默认客户端输入/缓存为ceil(n*0.6657)，clientTokenPassthrough才1:1；内部落库不缩放。输出全文BPE，内部可含thinking，默认客户端可见输出不含thinking。estimated_cost是无缓存基线，credits_saved是基线换算减实际credits，不等同模拟read直接费用。见token.rs:49-54,120-129、stream/context/mod.rs:1023-1119、stream/calib.rs:93-111、model/usage/pricing.rs:111-129。用量api_key_usage.json与缓存账本独立。

## 其他两库

kiro.rs本地输入x=(西文字符+4*非西文字符)/4，再按区间乘1.5/1.3/1.25/1.2/1截断；代码分母4而非注释4.5。流式逐块计数有分块偏差，百分比换输入不减输出。见src/token.rs:74-102,186-244、src/anthropic/handlers.rs:657-664,726-742、stream.rs:1016,1227-1244。

AIClient2API用@anthropic-ai/tokenizer，失败ceil(JS字符串length/4)。流式输入max(0,round(窗口*百分比/100)-输出)，窗口custom配置→模型表→200000；非流输入请求前估算。流式缓存恒0，非流省略。工具标记转换cachePoint不能证明命中量。见src/utils/token-utils.js:62-120、src/providers/claude/claude-kiro.js:253-261,1477-1479,2424-2443,3325-3358,3554-3622。

检查范围内new-api/relay与CLIProxyAPI执行器/translator未发现原生Kiro，不把通用Anthropic usage转换当Kiro算法。

## 本平台采用边界

客户按平台token定价可复用现有本地tokenizer和模型价格，但本地计数应明确标记local_estimated；缓存命中不是tokenizer可推断的事实。

建议优先上游完整缓存数据；无报告时普通输入计费。如采用断点算法应定义为平台缓存优惠，隔离客户/Key、模型、策略版本，并明确账号切换规则、失败续期、并发与持久化。展示/日志/扣费使用同一冻结计数，不采用0.6657缩放、不以credits反推冒充上游缓存、不另建客户账本。
