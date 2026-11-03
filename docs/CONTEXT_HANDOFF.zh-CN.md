# 会话压缩与接续摘要

更新：2026-11-03。原会话：`ffaa7526-ec8b-4ec7-9776-b09a873e7d32`。

## 已确认目标

- 以本仓库 Sub2API fork 为唯一基座；缺能力优先研究 CLIProxyAPI/new-api，但不串联第二层网关/调度器，遵守许可证。
- OAuth 订阅池与供应商 API 最终统一调度；支持客户、模型、分组、套餐、充值与后台。
- 目标 ECS Fargate（非 Kubernetes）、us-west-2；RDS PostgreSQL Multi-AZ DB instance；节点型 ElastiCache Valkey CMD。
- 用户调整实施顺序：先在当前 EC2 跑双 Gateway、Control、单 maintenance Worker，验证真实 AWS 数据层；暂不添加负载均衡或扩建 ECS/NAT。

## 当前事实与原会话断点

- 仓库 `/root/ai/sub2api-cloud`，分支 `feat/ecs-cloud-foundation`。
- 前三批提交：`9a450028a`、`1976b4282`、`f81fab32c`；第四批在原会话断在验收报告及本地提交收尾，未推送。
- 当前四服务通过 systemd 以非 root 运行，只监听 127.0.0.1:18080–18083，未 enable 开机自启。
- 真实 RDS/Valkey 已部署；300 迁移、TLS/权限隔离、跨连接 Lua/PubSub、事务幂等、双 Gateway 鉴权、登录/兑换/套餐/Key 幂等及撤销传播已有通过记录。
- 已执行一次 Valkey 和一次 RDS 受控切换；恢复后业务复验通过。Valkey 约36.37秒观察稳定；RDS 24.785秒是 AWS 事件间隔，不是客户端中断测量。
- RDS 观察器因 AZ 元数据滞后被停止，后修正脚本；修正后的触发路径未再次故障演练。
- 临时 create/failover/master-secret 权限已移除；应用 UID 禁止访问 IMDS。仍不是生产级按角色最小权限。
- 原记录最终临时用户/Key/上游账户数为0。没有模型调用、没有支付渠道调用。
- 基础资源估算约136.15美元/月，另有已有 EC2、Secret、日志等费用；保留资源继续计费，不自动销毁。

## 本次恢复后的实际检查

- 四个 `/health` 均返回 `{"status":"ok"}`。
- 后端配置、迁移、仓储聚焦包：通过（0.655/0.017/7.054秒）；Pricing 聚焦回归通过（0.082秒）。普通仓储测试不等同本次重新运行真实 AWS 合同测试。
- Terraform integration fmt/validate 与无 AWS 凭据的5项 mock plan 测试通过；未 apply、未再次触发故障。
- 首次 go test 在仓库根目录执行，因不含 go.mod 失败；切换 backend 模块后上述测试通过。

## 授权与下一步边界

- 用户已要求本地阶段提交；不推送，不发布镜像，不改 DNS，不销毁资源，不擅自扩建 ECS/ALB/NAT。
- 仓库外 `/root/ai/account.test` 两份 OAuth 账户获授权用于后续真实测试，但用户要求先完成本阶段任务、提交和报告。至本摘要写入时仍未导入，不展示凭据。
- 当前优先完成第四批本地提交和报告。之后真实调用须先确定实际模型 ID/可用能力及有界请求预算；不得凭“5.6 Terra”名称假定协议或可用性。
- 后续仍缺：统一跨供应端调度、持久请求/attempt/价格快照、刷新 CAS/租约、KMS 动态凭据加密、可靠预留/结算/outbox、Control/Worker 多副本治理、ECS 弹性/长连接排空。
- 不将有限集成验收描述为完整云原生或生产 SLA 验收。

## 权威文档与证据位置

- `AGENTS.md`：操作约束。
- `docs/CLOUD_ARCHITECTURE.zh-CN.md`：完整目标架构。
- `docs/IMPLEMENTATION_STATUS.md`：分批真实进度和历史测试。
- `docs/EC2_MANAGED_DATA_VALIDATION.zh-CN.md`：本阶段详细验收、费用、限制。
- `infra/ec2-integration/README.md`：资源、state、验证和运维边界。
- 私有证据：`/root/.local/state/sub2api-cloud/aws-integration/`；仅按需读取非敏感结果，不复制或提交环境文件、state、plan、Secret。
