# Sub2API 上游增量整合评估

日期：2026-11-04。当前分支feat/modular-upstream-adapters。仅完成分析，未merge/cherry-pick。

## 前置结果与范围

Kiro平台私有执行链已经单次真实验证HTTP200、5事件、2字节文本、TransportEOF=true。CompletionVerified仍false，公共HTTP收费入口未开放。修复提交7f21bec17、诊断脱敏测试0c2451769已推送。不能把这个结果扩展为商业渠道完成。

上游固定范围：基线`4a4a83ee5`至`upstream/main=6f92090d7`。新增14提交，包含8个非merge提交和6个merge提交，49文件，2697增行/157删行。离线三方检查未见文本冲突标记，但不代表架构兼容。未实际合并或执行上游测试。

## 结论

推荐选择性整合，不直接整包merge。先独立修复，最后将生图尺寸路由及粘性修复作为一个适配单元。所有整合保留唯一Gateway、调度权威、客户账本、不可变快照fencing及显式模块绑定。

| 提交 | 内容 | 建议 | 关键边界与验证 |
|---|---|---|---|
| `17772947b` | Kimi并发403改短冷却 | 优先cherry-pick候选 | 区分并发/余额/权限；关闭429 fallback时不冷却；验证scheduler可见性。旧长封禁不会自动解除 |
| `e47abd8f9` | CSV每页1000、429按Retry-After重试 | cherry-pick候选 | 同页重试不重复追加、重试耗尽/错误header/过滤器；offset不是一致性导出快照，不能当账务真值 |
| `522f1fb99` | Grok delegated agent_message规范化 | 选择性pick | 保留Grok执行边界；encrypted_content文本拼接不等于解密。单提交含package/lockfile附带变化，完整范围又无净变化，需单独处理 |
| `01a1a78db` | Antigravity陈旧系统身份归一化 | 与下一条配套pick | 仅Antigravity路径；普通厂商提及/任务指令不能误删；多system part与已有身份并存 |
| `835803a70` | 保留带任务的身份前导 | 依赖上一条 | Responses转换前处理system/developer字符串/数组；不改用户消息及其他模块；注意修改输入对象语义 |
| `a699c2bae` | 空instructions透传及响应回显恢复 | pick候选，流回归后 | 保留Forward模块门禁；回显仅呈现层，不改变usage/终态/重试；HTTP JSON/SSE/SSE-to-JSON均测，不能宣称WS覆盖 |
| `e452e41e4` | 生图按1K/2K/4K档位选号 | 需适配后整合，最后做 | scheduler_cache白名单、immutable publication、模块能力和账号UI联动；不能仅按OpenAI平台标签推定支持图片 |
| `6b565e45a` | 生图溢出与原粘性绑定 | 必须与上一条同时落地 | 选槽前读取原绑定，避免溢出覆盖/复活已清除绑定；并发session绑定操作非原子，需专门回归 |

六个merge提交未发现额外remerge-resolution变化，不重复pick。

## 生图路由必须适配的架构接点

1. `backend/internal/repository/scheduler_cache.go`：仅增加extra投影键，不覆盖现有epoch/lease门禁。
2. `backend/internal/repository/scheduler_publication.go`：full/slim payload均须包含档位；发布内容不可原地修改。旧slim缺字段可能解释为不受限，启用限制前应刷新发布快照，验证混合版本。
3. `backend/internal/service/openai_account_scheduler.go`及`openai_gateway_scheduling.go`：保留slot/排队/利润门禁、proxy quarantine、DB fallback、粘性及接管语义；不得恢复Gateway侧随意修改缓存。
4. `backend/internal/service/upstream_provider.go`及account_test_service.go：显式模块绑定未知/不兼容必须fail-closed；尺寸不支持的400诊断不能掩盖模块资格错误。
5. Create/Edit/Bulk账号UI：保留UpstreamProviderSelect和extra.upstream_provider。平台级档位控件不证明该模块支持图片，清除限制需真正删除旧JSONB值。
6. 未识别配置被上游解释为不受限，因此它是路由偏好配置，不应宣传为fail-closed安全隔离。

验证矩阵：硬档位/auto/省略尺寸，legacy/advanced scheduler，slot释放/取消/排队溢出，previous-response与session粘性，利润门禁，full/slim roundtrip，旧publisher拒绝、接管、Redis丢失和DB回源，400永久不支持与503临时不可用，generation/edit/async/Responses图片工具，模块选择+档位创建编辑批量测试。

## 建议整合顺序

1. Kimi短冷却。
2. CSV导出。
3. Grok输入规范化，分离无关依赖变更。
4. Antigravity两个提交顺序落地。
5. instructions回显，检查流式终态和partial usage不变。
6. 生图路由与粘性跟进一起适配，验证快照发布与模块选择器。

本次请求为分析，不自动开始上述merge/pick。后续每组独立提交，记录原提交来源和实际测试；保留上游作者及许可证，不将上游作者改成自己的作者。

## 本地额外风险与测试状态

- Kiro协议包测试、TestKiro聚焦service及私有工具build通过，协议包vet通过。
- 较早后台扩大到全部service的命令失败，实际失败`TestCompositeGroupSchedulerHasAllCanonicalPlatformBuckets`：六个兼容新平台与复合调度桶断言不一致。尚未确定仅断言陈旧或生产桶确有缺失，不能直接改期望掩盖；需独立核查并修复。
- 因测试链使用`&&`，该失败后台链未进入真实生成。之后独立受控平台调用HTTP200是另一条有marker的执行，二者不可混淆。
- 分析结束时磁盘0可用。仅删除按时间筛选的旧Go可再生缓存约813MB，未删源码、密钥、marker或数据库；避免在低空间状态大规模编译/合并。
- 未跑上游新功能测试、完整前端/后端验收或线上部署。
