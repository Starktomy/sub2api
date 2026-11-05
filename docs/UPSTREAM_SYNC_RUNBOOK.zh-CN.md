# 上游同步维护规范

确认日期：2026-11-04。适用于本fork后续Sub2API上游同步。

## 1. 默认策略

**先评估和分组适配，再通过正常merge保留上游祖先关系。** 不盲目点击Sync fork，不覆盖本地架构；也不为了把behind变成0而虚假声明已整合代码。

- 常规同步：在目标分支基础上创建集成分支，正常三方合并固定的上游SHA，在合并结果上保留本地适配、排除明确不采用的内容，验证后合并回目标。
- 默认无需先cherry-pick再merge。2026-11-04先分组移植、后正常merge，是为修复已经存在的移植历史，不是每轮必做两遍。
- 若只急需某个独立修复，可先cherry-pick并记录来源/依赖；此时behind仍可能存在，属预期。后续正常同步时检查重复应用和冲突。
- 若某项上游功能与架构不兼容，可在merge中明确适配或保留本地实现，记录排除理由及后续处置，不能暗中吞掉修改。
- 不使用`merge -s ours`、空合并或机械全选ours/theirs来清零behind。
- 历史通常只追加；正常同步不需要force push，不为同步上游重写作者或已有提交。

## 2. 权限与范围

评估/抓取不等于授权整合；整合不等于授权commit/push；push也不等于部署。遵守用户当次范围及AGENTS.md。

维护目标分支不固定为main：先确认当前开发线。Kiro独立分支与模块化分支不得因同步上游混合。

保留原始上游commit作者、版权、LICENSE、NOTICE。自己的适配提交使用项目配置的作者；Claude协作署名依当次会话约定。不要把上游作者批量改为自己。

## 3. 每轮先固定基线

要求工作区干净；有未提交工作先明确保存位置，不能直接reset/覆盖。

```bash
# 从仓库根目录执行。占位值必须替换，不可原样运行。
git status --short
git remote -v
git fetch upstream main

target=<目标分支>
upstream_sha=$(git rev-parse upstream/main)
local_sha=$(git rev-parse "$target")
base=$(git merge-base "$local_sha" "$upstream_sha")

git log --reverse --no-merges --oneline "$local_sha..$upstream_sha"
git diff --stat "$base" "$upstream_sha"
git diff --name-only "$base" "$local_sha"
git diff --name-only "$base" "$upstream_sha"
```

记录本地SHA、上游SHA、共同祖先、ahead/behind、来源提交及依赖。不能一直用最初fork基线统计新增，否则重复审计已合并内容。上游版本以本轮固定SHA为准，避免验证途中浮动。

可先`git merge-tree --write-tree "$local_sha" "$upstream_sha"`做离线冲突检查；其创建合并对象但不改分支。文本无冲突不代表行为兼容，更不代表测试通过。

## 4. 分组分析与架构门禁

按独立修复、协议转换、注册/权限、调度/快照、计费、前端和非功能资料分类，明确：直接采用、需适配、暂缓或排除。依赖/配套修复必须一起验证。

必须检查：

1. **模块上游**：显式provider binding、protocol/operation能力和fail-closed校验不得丢失；平台名不代表模块支持全部操作。
2. **调度**：唯一调度权威，DB+outbox→新namespace→fenced activation；保留epoch/lease与不可变快照。不原地修补active publication。
3. **投影**：新筛选字段须保留于full/slim，包括upstream_provider；避免丢字段静默退回默认。旧快照缺字段要设计重新发布及混合版本行为。
4. **供应者**：独立Host/Origin、API allowlist、audience、验证码用途、强制验证和无消费者奖励；共享用户禁用影响两侧。
5. **权限撤销**：区分禁用期间状态拒绝与永久撤销全部sessions；缓存TTL/DB失败行为明确，不把进程内失效宣传为跨实例即时生效。
6. **账务**：唯一客户账本、原子幂等、pending/ambiguous；未知用量不当作0，不能因本地提交不明重发外部生成。
7. **迁移**：不修改已应用SQL，检查上游与本地编号冲突，必要时新前向迁移；runtime schema check不执行DDL。

## 5. 推荐合并步骤

```bash
git switch "$target"
git branch "backup/pre-upstream-<唯一批次>" HEAD
git switch -c "integration/upstream-<唯一批次>"
git merge --no-ff --no-commit "$upstream_sha"
```

有冲突逐块核对来源/依赖，不能直接复制整份上游文件盖掉本地逻辑。正常合并会带入此前已pick的祖先，重复修改应核对最终tree。

### 排除赞助内容

目前用户策略为不引入赞助更新。逐项检查本轮赞助提交和文件，记录实际path；不将整个assets/README永久通配排除，因为其中可能包含功能说明、安全修复或法务要求。

对**确认只包含要排除的赞助变更**的文件，可恢复合并前HEAD版本：

```bash
git restore --source=HEAD --staged --worktree -- <逐个确认的赞助文件>
```

若文件同时有其他有用变更，只撤掉赞助hunk，保留其他修改。不得借此删除版权/许可/NOTICE。merge-parent会保留被排除上游提交的历史，内容不采用与历史包含是两回事。后续赞助更新仍可能再次发生冲突，每轮复核。

## 6. 验证与资源控制

在集成树上按组运行聚焦测试，保留本地回归，再做typecheck、build及需要的集成测试。数据库/Redis使用独立临时环境，不把测试指向正式库，不使用真实账号做常规离线回归。

此机器2vCPU/4GiB：**重型Go编译/链接严格串行**，`-p=1`；视内存采用GOMAXPROCS=1/2及GOMEMLIMIT。不要同时多组编译，即使每条-p=1也会总量超限。先检查磁盘/swap；只清理已识别可再生缓存，不动密钥、轮换凭据、marker或数据库。

记录每个命令、结果及未执行项：
- `[no tests to run]`不算覆盖；中断/超时不算通过。
- 重跑通过应保留首次失败原因；文件后续改变需重跑受影响用例。
- typecheck不等于build，build不等于服务运行，单元测试不等于真实PG/Redis/跨主机验收。
- 更改调度投影需验证模块绑定/新增约束同时保留，并验证旧publisher/fencing/回源路径。

合并后若内容树确实与刚刚验证的tree相同，可复用该tree测试记录，但仍核对合并冲突及最终diff，不笼统说重新跑过全量。

## 7. 提交、推送及祖先验证

用户授权后才完成commit/push。提交说明包含固定上游SHA、采用/排除项、保留的本地行为、实际测试及跳过项。

```bash
git diff --cached --check
git diff --check
git diff --name-only --diff-filter=U
# 上述检查完成，验证完成，且已获授权后：
git commit
# 回到目标并推进到验证后的集成提交；期间若目标前进则重新整合/验证。
git switch "$target"
git merge --ff-only "integration/upstream-<唯一批次>"
git merge-base --is-ancestor "$upstream_sha" HEAD
git rev-list --left-right --count "HEAD...$upstream_sha"
git status --short
git push origin "$target"
```

对本轮固定上游应显示behind=0；如果上游之后又新增提交，新的behind正常，不能强行作0。GitHub首页可能展示默认main，必须确认页面选中的分支。集成分支先经PR落地的项目，按评审流程替代直接推进目标。

## 8. 部署不是本流程的自动步骤

合并推送后仍需单独授权部署。调度字段上线顺序：服务端读写/投影→受fencing保护的新publication→验证全部reader→开放配置。不可修改active namespace。域名、镜像发布、AWS资源/迁移等遵守原部署权限边界。

## 9. 2026-11-04示例

- 本地适配：1678628cb，保留供应隔离与模块/档位投影。
- 正常merge：8a9618d4d，第二父为上游876b96d20；未使用ours策略。
- 仅排除已检查的赞助README/Logo变更，合并tree与本地已验证适配tree一致。
- 当前feat/modular-upstream-adapters相对当次上游40 ahead/0 behind；main未改变。
- 本次具体移植清单与测试记录：[UPSTREAM_PORTING_PLAN_20261104.zh-CN.md](UPSTREAM_PORTING_PLAN_20261104.zh-CN.md)。
