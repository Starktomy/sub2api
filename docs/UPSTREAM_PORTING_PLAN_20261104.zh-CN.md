# 当前模块化分支的上游移植分析

日期：2026-11-04。分组移植已提交推送为1678628cb；随后feat/modular-upstream-adapters正常合并upstream/main至876b96d20，合并提交8a9618d4d保留本地适配并排除赞助内容，内容树与已验证移植一致。未部署。后续常规同步遵循[上游同步维护规范](UPSTREAM_SYNC_RUNBOOK.zh-CN.md)，不必重复本次先pick后merge的历史修复过程。

## 固定比较范围

- 当前HEAD：`4c16f4dae4d2f5b1f643dbab7889c734e2ef3bc7`，无本轮Kiro适配。
- 共同祖先：`4a4a83ee5ec86b19d3a2dd9b6471de5128963bec`。
- 本轮fetch后的upstream/main：`251094436325175c9e0ab06e7b962d05369fd766`，不是上次的6f92090d7。
- 新增18提交，其中11个非merge：9个功能/修复、2个赞助更新；86文件、5353增行/184删行。
- `git merge-tree --write-tree HEAD upstream/main` exit0，无文本冲突。只产生离线合并对象，未改变工作树/分支。这不是测试或架构验收。

## 建议：分组移植，不直接整包merge

| 顺序 | 来源提交 | 方法 | 原因/验收门槛 |
|---|---|---|---|
| 1 | 17772947b | 优先pick候选 | Kimi并发403使用既有429短冷却；验证设置关闭/异常、配额403及调度可见性 |
| 2 | e47abd8f9 | pick候选 | CSV分页1000、同页429重试；不改客户账本；前端客户端与页面测试 |
| 3 | 522f1fb99 | pick后剔除无关依赖hunk | Grok agent_message规范化；保留供应商执行边界，不把文本encrypted_content解释成解密 |
| 4 | 01a1a78db → 835803a70 | 作为一个完整单元顺序pick | 后者依赖NeutralizeSystemIdentity；仅Antigravity处理，不误删任务、不污染原生模块 |
| 5 | a699c2bae | pick候选+模块回归 | 原始instructions呈现恢复与空值透传；保留本地Forward模块绑定/Responses策略门禁 |
| 6 | 721dfb282 | 安全适配后引入 | 注册邮箱精确黑名单/批量禁用涉及供应者独立注册与会话撤销；详见专项结论 |
| 7 | e452e41e4 → 6b565e45a | 两提交一起适配，最后验收 | 生图尺寸路由牵涉快照字段投影、不可变发布、模块能力、slot与粘性；不能单独发布首提交 |
| 可选 | e353c8322、251094436 | 不作为功能移植前置 | 仅README赞助资料/图片变更，单独决定；保留上游法务通知 |

推荐实施时建新的integration分支。保留原作者，适配提交由Starktomy署名并注明源commit/Claude协作；每组独立测试/提交。后续用户已明确授权分组整合，当前应用使用cherry-pick --no-commit保留来源清单，尚无新提交。原提交顺序及分组记录于上表；索引当前汇总上游代码，本地适配另有工作树变更，尚未整理成各组提交。

## 独立修复细节

### Kimi并发短冷却

ratelimit_cn_providers.go由长时间temporary-unschedulable改用apply429FallbackRateLimit。复用当前限流与调度通知，不新增第二调度。fallback关闭时不冷却；不会自动解除部署前留下的旧长封禁。验证并发文本匹配与额度不足/权限错误分开。

上游测试：TestHandleUpstreamError_KimiConcurrency403CooldownSettings、TestHandleUpstreamError_KimiQuota403IgnoresDisabledConcurrencyCooldown、TestHandleUpstreamError_KimiConcurrency403ExplicitRulesTakePriority。

### CSV导出

前端api/client.ts解析整数Retry-After；UsageView每页1000，遇429同页最多重试3次，等待1–60秒。HTTP-date不支持，跨域未暴露header时回退60秒。offset分页不保证导出期间新增记录的一致性，不宣称财务快照。用fake timers验证同页重试不重复追加、重试耗尽、非429/无效header、筛选参数。

### Grok输入

apicompat导出AgentMessageText，Grok sanitizer在自己的执行路径把agent_message转普通user文本。测试混合普通/委派消息、空文本及工具/图片邻接，保证其他模块不继承Grok变换。

单提交522f1fb99还改frontend/package.json和pnpm-lock.yaml，但当前到上游最终范围无这两个文件净变化。不要让单次pick带入无关临时依赖版本。

### Antigravity配套

首提交中和锚定的陈旧系统身份；第二提交在Responses转换前处理instructions/system/developer字符串及文本数组，保留任务。第二提交会修改请求对象，保持入口请求局部性。现有账号模块验证保留，不把供应商特定归一化放进全局converter。

### instructions透传/回显

上游在Forward开始记原instructions，在实际buildUpstreamRequest前同步，返回JSON/SSE时恢复客户端原值。当前Forward入口有validateUpstreamProviderBinding及validateCompatibleResponsesBody，buildUpstreamRequest也有重复边界保护，均不可用上游原文件覆盖。

仅回显呈现恢复，不改usage提取/成功终态/重试；未传instructions可能仍在上游补默认，只是客户端回显null。HTTP覆盖不等于WS覆盖。

重点测试：TestOpenAIResponsesInstructionsEchoRestoreHelpers、OAuthPassthrough_CodexBlankInstructionsForwardedAsIs、OAuthResponses_MultiLineInstructionsEchoKeepsSSEFraming，并联合upstream_provider_test.go和compatible_protocol_discovery_test.go。

## 专项：邮箱黑名单与供应注册

专项只读核查完成，未实施。应作为共享注册策略+Control管理功能适配，不替换本地注册实现：

- 把黑名单检查接入validateRegistrationEmailQuota，供应注册和发送验证码可自然共用。保留purpose proof、强制邮件验证、supplier audience及禁止消费者奖励。
- 已有邮箱OAuth用户登录只检查后缀；新用户创建检查黑名单。保存黑名单不等于禁用已有账号；已有活跃用户供应enroll也不属于注册，应保持这一差别。
- 匹配按规范化收件箱：Gmail点/加号/googlemail别名折叠，非Gmail点保留；SQL候选查询后仍需service精确校验。
- Control新增GET/PUT settings/registration-email-blacklist、POST users/blacklist-matches、POST users/batch-disable；保留upstream-providers路由。供应专用入口不得开放这些管理接口。
- 保存→查匹配→确认禁用是分离流程；batch-disable接受用户ID，不重新校验黑名单命中。供应者也是共享user，禁用影响消费者和供应者两侧，不仅是supplier profile。
- 缓存是进程内60秒TTL；不同Control/供应进程不会立即同步。冷启动无缓存且DB故障时上游允许注册，后续失败保留旧表。这不是严格fail-closed策略，需明确加固后才宣称立即全局封禁。
- 上游同事务禁用用户并永久吊销MCP grants；UPDATE排除管理员、已删除和已禁用用户，即使预览后晋升管理员也跳过。提交后InvalidateAuthCacheByUserID针对API-key auth缓存，不是清空全部refresh sessions；未调用RevokeAllUserSessions，也不改变token version。
- supplier JWT/refresh在disabled期间依靠用户active状态拒绝；RefreshTokenPair可能删除当前family，不代表批量禁用时撤销所有family。重新启用用户后，未过期access token及未被清除refresh family可能恢复可用，MCP grants则不会复活。若要求禁用后永久登出，需单独实现跨消费者/供应者全部会话撤销及重新启用回归，保留durable auth invalidation，不能宣传此上游提交已完成。
- 本地purpose proof在注册policy前消费，黑名单拒绝可能已消费验证码；需测试并明确，不误判为绕过。

测试入口：registration_email_blacklist_test.go（别名/TTL/DB失败/并发保存），auth_email_oauth_blacklist_test.go（已有登录/新建区别），user_repo_email_blacklist_integration_test.go（禁用与MCP竞态）。扩展supplier_signup_policy_test.go、supplier_token_policy_test.go、supplier routes allowlist及两进程策略缓存测试。此提交不含schema migration。

## 专项：生图路由与发布快照

- scheduler_cache.go只添加新extra投影键，保留epoch/lease fencing与不可变scheduler_publication。
- 旧slim快照缺档位字段，可能被解释为不受限；启用前发布新快照，验证混合版本/回源。
- create/edit控件与UpstreamProviderSelect共存，extra.upstream_provider不能丢。平台为OpenAI不等于模块支持图片。
- 保留slot、利润门禁、proxy quarantine fail-open、排队/取消释放及DB fallback。
- 粘性修复必须跟随首提交：首次pass前记录原绑定，不能溢出覆盖或复活已清除绑定；并发绑定读改写需回归。
- 未识别档位归一化为不受限，是路由配置，不是严格安全隔离。

测试：OpenAIImageSizeTiers、ExplicitSizeFiltersByTier、SpilloverToRestrictedAccountKeepsStickyBinding、SpilloverReplacesClearedStaleBinding、AllPassesQueuedKeepBindingOnReturnedAccount；联合repository/scheduler_publication*测试，覆盖旧publisher拒绝、full/slim、接管、Redis丢失、DB回源。前端组合模块选择+档位创建/编辑/批量测试。

## 执行前条件和验证记录

- 工作树在分析开始为干净；只fetch和离线merge-tree，未改运行服务/数据库/DNS。
- 机器可用空间约1.1GiB，串行低内存测试，不并发全量编译或大规模依赖安装。
- 已知上一阶段扩大service回归失败项TestCompositeGroupSchedulerHasAllCanonicalPlatformBuckets，涉及六个新增兼容平台；应基于本HEAD重新定位生产桶与断言，不能直接改期望掩盖。
- 需要先建立当前HEAD的聚焦基线，再每组测试：GOMAXPROCS=2、go test -p=1；前端使用已有Node/依赖，聚焦Vitest/typecheck。
- 当前已应用全部9个功能/修复提交，无文本冲突；两赞助提交未引入，frontend依赖文件无净变更。现有模块校验/供应者入口隔离/不可变发布代码保留。
- 供应者专项新增TestSupplierSignupBlacklist、TestSupplierSendSignupCodeBlacklist、TestSupplierDisabledUserCannotLoginOrRefresh及完整入口method/path allowlist断言，最终文件由主会话串行重跑TestSupplier及canonical bucket回归通过0.334s，覆盖agent最后修改断言；supplier handler/server早先通过0.450s/1.322s。
- 前端typecheck通过。首轮10文件中9文件、244断言通过，CreateAccountModal worker加载超时、无断言执行，独立重跑CreateAccountModal的58项随后通过，累计302断言通过；首轮超时保留记录，不称首轮全部通过。
- 已定位原复合平台桶失败为独立测试期望名单陈旧：生产schedulerSnapshotPlatforms复用domain.CompositePrecedencePlatformIDs已含六新平台，当前补齐显式测试名单，不改生产算法。
- handler及admin聚焦回归通过0.024s/1.250s；antigravity/apicompat全包测试通过0.025s/0.092s。供应者server/router聚焦测试亦通过。并发编译曾使swap接近满，已停止重复repository投影/fencing任务和server build，并要求停止供应者旧service编译/排队重跑，改串行验证；被停止项不算通过，最终投影修复需重新验。主service分组随后通过0.064s；协议包在该筛选命令无匹配测试，不计覆盖，另有全包通过记录。最终串行repository unit投影/publication/lease通过0.052s，模块及publication service通过0.048s，server build及diff检查exit0。
- 新增档位+显式模块同时保留的slim测试，发现本地原白名单缺ExtraKeyUpstreamProvider，已补投影避免静默回退legacy默认；该修复随独立投影测试验证，不更改不可变publication写入机制。最终server构建已通过；未执行真实PG/Redis integration、全后端测试、前端build或生产部署。

## 上线顺序与限制

服务端规则/投影先发布，随后通过现有发布领导权和epoch门禁刷新新publication，确认全部reader理解档位后才开放前端档位配置。不得手改active namespace。邮箱黑名单依旧60秒进程缓存，禁用靠active状态拒绝、MCP永久吊销，不宣称全会话永久撤销或立即跨实例生效。
