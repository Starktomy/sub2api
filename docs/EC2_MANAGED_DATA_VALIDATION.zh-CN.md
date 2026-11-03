# EC2 + 真实 AWS 数据层阶段验收

日期：2026-11-03（本机日期；AWS事件时间在文内使用UTC）。本报告描述已经执行并观察到的结果，不把预生产测试等同于生产SLA。

## 1. 用户确认的当前范围

- 拆分后的2 Gateway、1 Control、1 maintenance Worker暂时全部运行在当前EC2。
- 不添加负载均衡，不启动ECS，不创建NAT；先验证真实AWS PostgreSQL/Valkey和非模型业务。
- 用户明确授权创建所需资源和配置IAM，区域为 `us-west-2`。
- 用户后来授权 `account.test` 中两份OpenAI OAuth账号用于5.6 Terra真实测试，但随后明确要求先完成当前任务再开始。因此本阶段**没有导入OAuth账号，没有真实模型调用**。

## 2. 已部署拓扑

| 部分 | 实际状态 |
| --- | --- |
| 当前EC2 | `i-04225834295350402`，`us-west-2b`，原VPC `vpc-01b33479` |
| Control | systemd `sub2api-integration@control`，`127.0.0.1:18080` |
| Gateway A | `sub2api-integration@gateway-a`，`127.0.0.1:18081` |
| Gateway B | `sub2api-integration@gateway-b`，`127.0.0.1:18082` |
| Worker | `sub2api-integration@worker`，`127.0.0.1:18083`，仅1个maintenance实例 |
| 系统用户 | 四服务均为非root用户 `sub2api-it`，systemd限制文件写入路径 |
| RDS | `sub2api-it-postgres`，PostgreSQL16.13，db.t4g.medium，20GiB gp3，Multi-AZ DB instance，私网/加密/7天PITR/删除保护 |
| Valkey | `sub2api-it-valkey`，7.2（实际节点7.2.6），2×cache.t4g.small，CMD，跨AZ主从，TLS required、ACL、noeviction |
| 数据子网 | `172.31.128.0/24`（2b）、`172.31.129.0/24`（2a），路由表只有VPC-local |
| 网络入口 | RDS5432/Valkey6379只接受当前EC2 SG，cache另有self复制；无公网CIDR授权 |
| Terraform | 独立 `infra/ec2-integration`，复用原VPC但不接管原子网/EC2，S3加密/版本化远端state＋lockfile |

服务已经启动并保留供后续测试，systemd unit**没有enable开机自动启动**，避免未经验证的重启后自动运行。测试环境资源仍会产生费用；不自动销毁或删除备份。

启动：`systemctl start sub2api-integration@{gateway-a,gateway-b,control,worker}`。
停止：`systemctl stop sub2api-integration@{gateway-a,gateway-b,control,worker}`。
诊断：`journalctl -u sub2api-integration@gateway-a -o cat`（勿公开完整日志/环境文件）。

## 3. 凭据、IAM与数据库权限

- 初始AWS凭据为root；仅用于IAM bootstrap、绑定专用instance profile及策略收紧。真实数据资源和state桶通过新建 `sub2api-integration-deployer` assumed role创建，不使用root执行数据创建。
- EC2 host角色仅允许AssumeRole到本项目部署角色。未挪用其他项目角色，没有AdministratorAccess或任意PassRole。
- Terraform计划经IAM Policy Autopilot0.4.0生成基线，再移除不需要动作并限制账号、us-west-2、资源前缀。API依赖和刷新只读动作经官方授权参考核对。
- 部署完成后移除临时RDS/缓存创建、failover和RDS master-secret读取权限。最终IAM模拟：CreateDBInstance/RebootDBInstance/主Secret GetSecretValue/iam:CreateRole为implicitDeny。
- 日常仅保留数据元信息只读、精确的4个集成Secrets读取、专用state/lock对象权限。需要以后修改资源时重新按具体变更授权，不把create权限永久留给应用。
- 应用UID的IPv4/IPv6 IMDS访问由独立nft规则阻断，实际curl验证不可达；root运维角色仍可用。规则纳入systemd依赖并幂等重载，不改SSH/其他UID流量。
- Secret值在Secrets Manager和本机root-owned0600配置中，不进Git、不在命令行打印。操作目录 `/root/.local/state/sub2api-cloud/aws-integration` 为0700。
- `sub2api_it_owner` NOLOGIN；迁移账户 `sub2api_it_migrator` 可SET ROLE到owner；运行账户 `sub2api_it_runtime` 无SUPERUSER/CREATEDB/CREATEROLE及schema CREATE。
- Runtime可SELECT迁移清单，但不能修改；不能读取 `security_secrets`。共享JWT/TOTP显式注入，不走启动bootstrap。
- RDS使用官方区域CA和verify-full，Valkey使用系统CA+主机名验证，未启用InsecureSkipVerify。
- `account.test` 位于仓库外，仅检查结构并将权限从0644收紧至0600；未读取/输出凭据值到转录，未导入DB。仓库新增ignore规则防误提交。

限制：四服务目前同一OS UID/DB runtime账户；这是隔离集成阶段，不代表每角色最小DB/IAM权限已经交付。Valkey测试ACL为专用空集群 `on ~* &* +@all -@admin`，不是生产keyspace最小ACL。

## 4. 真实功能验收

### 数据迁移及连接

- 真实RDS16.13执行全部300个非空SQL迁移成功，重复/check路径正常。
- 独立迁移程序不启动HTTP、Redis、后台任务，不创建应用管理员。
- `pg_stat_ssl`确认实际会话encrypted；Runtime DDL、读取共享密钥表、修改manifest权限被拒绝。
- Valkey TLS1.3证书主机名验证、专用ACL认证、PING、Lua均通过；错误密码被拒绝。
- 修复原DSN直接拼接：所有文本安全libpq escaping，新增DATABASE_SSLROOTCERT；简单值格式和空密码省略兼容。

### 仓储真实合同

`MANAGED_DATA_LIVE=1` 的 `TestManagedDataLiveContracts` 使用命名测试DB/cache，仅写临时fixture，不flush全缓存。

通过项：

1. 两个独立TLS客户端共享账号并发Lua门禁，容量1时第二个被拒绝，释放后恢复。
2. 跨连接等待计数限制。
3. BillingCache Lua扣减后两个连接读值一致。
4. managed Pub/Sub跨连接通知与stop/join。
5. PG事务UsageBilling幂等：相同request/key重复Apply只扣一次。
6. 同一DB/Valkey Key在两个实际Gateway `/v1/usage` 认证成功。
7. Runtime不能CREATE TABLE，缓存无效凭据拒绝。

初次HTTP fixture缺分组返回403(group_unassigned)，补齐合法组后通过；没有修改鉴权来绕过该规则。切换/最终重启后再次整套通过（0.244秒）。

### 控制面真实HTTP业务

`tools/verify_managed_business.py` 创建临时普通用户/两组/兑换码，实际登录Control，再通过HTTP验证：

- 余额兑换、套餐兑换；
- 同一余额码重复兑换被拒绝，余额不二次增加；
- 已兑换套餐在active subscriptions可见；
- API Key创建Idempotency-Key重放返回同一个ID；
- 新Key被两个Gateway使用；
- Control停用Key后，两Gateway本地鉴权cache经Redis失效收敛并拒绝访问。

故障切换/最终重启后复验通过。模型调用=0，支付provider调用=0；兑换验证不代表Stripe/支付宝/微信真实支付回调已经验收。

## 5. 故障切换与退出

### Valkey受控主从切换

- TestFailover仅作用于 `sub2api-it-valkey`。
- 切换前主节点001（2b），后主节点002（2a）。
- 约36.37秒观察到available＋可读预写哨兵；10秒采样间隔中有2次失败探测。
- 哨兵在切换前已确认replica可读，本次恢复保留；不宣称异步复制具有RPO0。
- 四应用进程无自动重启；恢复后真实仓储和HTTP业务复验通过。

### RDS受控Multi-AZ切换

- 只执行一次 `RebootDBInstance --force-failover`，仅命名测试实例。
- AWS事件：2026-11-02T18:31:59.174Z started，18:32:23.959Z completed，事件间隔24.785秒。
- 预先提交余额7.50000000的哨兵行恢复后相同，已清理；最终主2a、备2b，available/MultiAZ=true。
- 原观察脚本只接受AvailabilityZone文本变化；事件已完成但字段短期滞后，人工停止该观察器（退出143）并用本次start/completion事件＋提交数据/业务复验收尾，没有再次触发故障。
- 已修复脚本：RDS要求请求后completion事件、稳定状态及数据可读，不强依赖AZ字段。**脚本修复后的触发路径只做语法检查，未再次演练**，不能把修复代码本身列成再次故障通过。
- 24.785秒是AWS事件间隔，不是精确客户端中断时间。当前无在飞真实生成，因此不能据此承诺生成请求无损恢复。

### 最终受控重启

安装最终binary后逐个systemctl restart，全部正常退出并恢复健康；观察恢复约0.24–0.27秒（空闲集成负载样本）。四服务NRestarts=0。离线价格模式初始化误报已修复，最终启动窗口无ERROR日志。

## 6. 回归与配置一致性

- `go test -p=1 ./internal/config ./cmd/migrate ./internal/repository -count=1`：通过（0.651/0.021/7.093秒）。
- `go test -p=1 ./internal/service -run TestPricing -count=1`：通过（0.082秒）。
- 相关 `go vet`、server/migrate build：通过。
- 新DSN测试曾因参照URL缺`//`/被authority覆盖host而失败，改为独立`postgresql:///?`驱动解析参照后通过，不降低字段转义断言。
- Terraform fmt/validate＋5项无凭据mock plan测试：通过。
- 真实Terraform refresh曾缺DescribeSecurityGroupRules，补官方元信息动作后发现force_ssl apply_method默认差异，显式pending-reboot与AWS一致；最终plan退出0：**No changes**。未因漂移执行无谓DB重启。
- 最终安装binary与工作仓库build SHA256一致；所有临时业务用户/API Key/上游账号数均为0。
- 未执行前端构建、全仓全部测试、ECS/ALB、PITR恢复、流式长连接与真实模型/支付测试。

## 7. 费用和保留状态

使用公开us-west-2按需价格、730小时月计算：RDS主备计算94.17美元/月；Valkey两节点37.38；20GiB gp3主备4.60；基础合计约**136.15美元/月，4.48美元/天**。

不含已有EC2、Secret、备份超额、CPU积分、流量/日志/税费，不是费用上限。当前RDS/Valkey及state/Secrets保留并持续计费；用户未要求销毁。尚未设置预算邮件告警（无确认通知地址）。

## 8. 接下来与明确暂停点

本阶段是“当前EC2运行四服务＋真实AWS数据层”的有限范围验收，不是完整方案所有功能完成。

用户授权的两份OAuth/5.6 Terra测试仅在本阶段任务、代码提交和报告全部收尾之后开始；本报告时仍未导入/未调用。后续先建立单独测试计划与有界请求预算，再覆盖nonstream/stream/工具/usage/账号亲和与回退。现有未完成的统一供应端调度、KMS动态凭据加密、可靠账务恢复、Control/Worker全能力多副本和ECS弹性仍留在实施清单。
