# Aurora PostgreSQL 适配与切换演练

日期：2026-11-03。状态：迁移约束及离线证据检查已准备，真实 Aurora 演练未执行；不将脚本存在视为迁移成功。

## 当前基线

源为 us-west-2 RDS PostgreSQL16.13、db.t4g.micro、Single-AZ、20GiB gp3；Valkey为单cache.t4g.micro。两项实际available，RDS无pending修改。降配前两份手动snapshot已available。降配后accounts/users/usage_logs数量、余额汇总及两测试账号禁用状态与先前baseline一致，PG verify-full查询/Valkey TLS ACL PING通过；publication generation与maintenance generation一致，outbox0，三个角色healthy。源数据、账务及快照不得删除。

当前没有Aurora集群，不执行本文件即创建资源。真实演练需先审查账号、Region、专用角色、目标引擎兼容性、临时实例规格/费用上限范围及清理计划。版本支持必须在演练时重新核实；不得假定PG16.13可直接迁到任意Aurora版本。

## 不可放宽的约束

1. 地址、端口、TLS CA和凭据均由仓库外Secret/env提供；镜像不含凭据。不为迁移改动已应用SQL migration。
2. 所有现有应用DB连接仍走writer：账务、账号凭据、OAuth刷新状态、migration manifest、维护锁、generation、publication pointer/outbox都不走reader。reader routing不是此次适配的一部分。
3. hostname外观不能证明连接到writer。目标通过AWS已核实cluster endpoint身份及SQL只读状态验证；PG startup的read-only preflight不能执行DDL、加锁或bootstrap。
4. session advisory lock必须保留专用连接，连接失败或切换后重新选主并分配新generation；不使用事务池复用此会话。第一次演练直连writer，不新增RDS Proxy掩盖会话语义。
5. 同一时刻只有一个DB接受业务写入。停止所有旧gateway/control/worker及外部写入者；不是仅摘ALB或停止worker。排空请求和usage写入后才作最终数据同步。
6. 克隆DB会复制旧generation及publication指针。禁止源/目标同时运行Worker并共享Redis；独立演练使用隔离Redis namespace/实例，或完全停源应用后才切换。目标启动分配新generation后构建新publication；不能删除/重置generation来恢复。
7. 不把Redis当账本。缓存可重建不意味着OAuth session、幂等记录、槽位和wait可随意清空；逐项确定切换策略，禁止FLUSHALL/FLUSHDB。
8. 目标发生新写入后，禁止直接改回旧endpoint。先冻结目标写入，评估账务和凭据新变化，再制定反向同步/恢复；绝不双写或盲目重放结果不明的外部生成。

## 演练顺序与证据

A. 预检：读源引擎/扩展版本、extension allowlist、角色/权限、参数、schema manifest、表数量与账务汇总；目标版本的迁移支持由AWS官方文档/API核实。检查pg_locks、read-only模式、TLS、连接预算与专用session行为。

B. 选择方法：少量测试数据可采用snapshot迁移并停写；低中断路径仅在支持时使用Aurora迁移replica追平后提升；逻辑复制/DMS另审DDL、sequence、large object和权限同步，不假定自动完成。复制完整账号凭据的目标必须保持相同严格访问与加密，不打印导出内容。

C. 适配：在隔离环境运行现有迁移check与实际仓储集成契约，覆盖金额/幂等、OAuth pending/ambiguous、领导权丢失、stale generation、publication activation+exact ack、同generation Redis数据丢失恢复。先用合成fixture，不调用真实provider。

D. 切换：记录开始时间；停止入口新请求/排空在飞请求；停止所有旧写入者并验证；最终同步和数据/sequence核验；切换Secret/外部env，重建连接；先验证target writer与权限，再逐角色启动。检验一个有效leader、generation推进、快照激活、outbox、正常业务和用量落账。没有证据的检查不得标true。

E. 故障：目标writer故障切换时验证旧session取消/join、new generation和publication恢复；验证旧实例不能激活；注入网络COMMIT-response丢失只能在隔离fixture中，结果不明不得自动重放生成。

F. 结束：核验数据、禁用fixture并保留账务，记录RTO/观察窗口而不是宣称零停机。根据事先清理计划删除临时目标，保留必要最终快照；默认不删源库。下一次版本/规模变更仍需复验。

## 离线证据门禁

`python3 -I tools/test_check_database_cutover.py` 无网络测试；`python3 -I tools/check_database_cutover.py /外部路径/evidence.json`只检查输入声明的完整性，不查询DB或AWS，不执行或授权切换。不能自动把所有字段填true；必须由演练真实证据支撑。

该门禁并未接入生产发布执行器，也未强制阻止手工改env；当前属于可审阅runbook/离线检查，不是已完成运行时Aurora适配。

## 未完成

真实目标版本组合、集群与IAM配置、数据迁移、writer故障演练、真实网络COMMIT歧义、测量切换/回退耗时、费用及资源清理均待执行。当前只是为下一轮受控演练准备，不创建长期Aurora费用。
