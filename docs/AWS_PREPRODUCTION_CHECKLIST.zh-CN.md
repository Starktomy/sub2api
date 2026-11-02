# AWS 预生产实机启动清单

更新时间：2026-11-03。本文件回答“角色拆分完成后能否在 AWS 实机启动”。

## 结论与边界

完成进程级隔离和本地烟测之后，可以开始**封闭预生产环境**的 AWS 部署工程，不等于现仓库已经能一键 terraform apply 并对外收费。

当前角色是显式 experimental opt-in：2 Gateway、1 Control、**1 maintenance Worker**。Gateway可多实例验证；Control授权会话及Worker全量lease尚未完成，二者不任意扩容。没有真实上游和支付沙箱验收前不接公共收费流量。

## A. 本地软件验收

- [x] 独立 schema migration/check CLI。
- [x] 默认阻止 split role，显式opt-in要求check模式、标准计费与共享JWT/TOTP。
- [x] managed后台任务、请求本地队列、取消/等待、路由边界和原鉴权契约。
- [x] ChannelMonitor从持久配置周期收敛，错误保留、变更/删除处理。
- [x] 2 Gateway + Control + Worker连接真实PG和临时密码Redis，健康/路由/任务scope/正常退出已跑通。
- [x] 最终增强槽位四角色烟测及受影响完整回归通过；订阅关闭时序专项复测结果见IMPLEMENTATION_STATUS。
- [ ] 真实RDS PostgreSQL16 / Valkey7.2 TLS环境验证（本地PG15.19/Redis7.0不能替代）。
- [ ] 模型协议、计量和支付沙箱；强杀/断网/数据库commit结果不明的恢复。

## B. 身份和预算（需要用户/AWS管理员）

- [ ] 不使用现有root作为日常部署身份；提供专用IAM role及允许的account ID。
- [ ] us-west-2、stage环境命名、Owner/CostCenter、CIDR不冲突。
- [ ] 审查至少两个NAT/公网IPv4、RDS主备、Valkey两节点、ALB、Fargate、日志/Secret/KMS费用，并设置预算告警。
- [ ] 加密、版本化、有锁的Terraform远端state backend，独立bootstrap并限制访问。
- [ ] 明确需要内部ALB还是公网TLS入口；公网入口需受控域名与ACM证书，且限制预生产访问。

## C. 现有 Terraform foundation

`infra/aws`已经定义并离线mock验证，但**尚未部署**：

- 两AZ网络与isolated数据子网、同AZ NAT；
- RDS PostgreSQL Multi-AZ DB instance、备份/PITR/加密/删除保护；
- Valkey CMD主从、TLS/noeviction、禁用的预留ACL；
- 空ECS集群、ECR、SQS/DLQ、KMS、私有S3。

没有自动取得部署授权。只在账户、角色、state和费用经过确认后执行真实plan，再批准apply。

## D. 还需实现的应用部署接线

- [ ] ECS适用镜像：明确server/migrate入口、非root、资源与文件目录、RDS CA、license、固定digest、无自动setup和自更新。
- [ ] 运行配置/Secrets：JWT、TOTP、DB migrator/runtime用户、Valkey ACL用户与密码；不能用DB master启动应用。
- [ ] 一次性bootstrap管理入口：创建runtime DB角色/权限、应用管理员（带不可重复误重置保护）；不写凭据进state或Git。
- [ ] ECS Task execution role和每角色Task role；最小Secret/KMS/SQS/S3/Bedrock授权。
- [ ] Gateway/Control/Worker Task definitions及services，private task subnets、端口/健康检查、IAM、日志、部署策略。
- [ ] ALB/target groups/规则/HTTPS、安全组、WAF和可信代理；Control与Gateway并列入口，不串联AI网关。
- [ ] 迁移任务在应用发布前完成；runtime migration_mode=check。
- [ ] 单Control/单Worker避免滚动重叠的stage部署策略；Gateway支持有界排空后再缩容。
- [ ] CloudWatch告警、DLQ/账务pending/outbox age、DB/cache容量、Secret rotation、恢复流程。

## E. 实机顺序

1. 非root身份/state bootstrap/预算审查。
2. foundation plan审查→获批后创建。
3. bootstrap DB用户/Secret/Valkey最小ACL。
4. 构建扫描镜像→获批后push ECR，固定digest。
5. 专用migrator ECS Task执行schema，再独立bootstrap应用管理员。
6. 先启动1 Control和1 Worker，再启动2 Gateway；仅限受控预生产访问。
7. 复核role实际task日志和DB/Redis权限、路由鉴权；确认Worker重启不清掉Gateway槽位。
8. 接入少量有授权的真实测试资源，验收工具/stream/usage/额度、支付sandbox。
9. 测试SIGTERM、ECS部署、cache切换、DB切换、任务重投和账务对账。

只有这些证据满足后，再讨论正式生产流量和更积极的自动缩容。当前仍未交付完整统一供应商资源调度、KMS动态账号凭据加密及可恢复账务的全部目标，不能把“云上能启动”写成“完整产品已完成”。
