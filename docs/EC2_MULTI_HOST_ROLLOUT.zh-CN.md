# EC2 多机发布：执行范围与门禁

日期：2026-11-03。状态：用户已授权发布镜像、创建必要 AWS 资源，并明确允许 root 仅用于补齐所需 IAM 权限。已创建私有 ECR、发布既有验收镜像，并启动第二台 EC2；应用跨机验收与完整 fencing 尚未完成。本文件不是完成报告。

## 已核实的身份与阻塞

- 目标账号 `250088785444`，Region `us-west-2`。
- 通过仓库外 AWS config 成功 AssumeRole 到 `sub2api-integration-deployer`。
- 默认 AWS 凭证为 root；不得用于资源创建，不得借它绕过专用角色的拒绝。
- 初始专用角色调用 `ec2:DescribeInstances`、`ecr:DescribeRepositories` 均被 IAM 拒绝。用户随后明确授权 root 补齐 IAM；已新增受 Region 约束的发现权限与仅 `sub2api-cloud` 仓库发布权限。IAM 传播期间曾出现间歇拒绝，必须逐项验证，不通过 root 执行资源操作来绕过。
- 已用 root 创建 `sub2api-multihost-runtime` 角色及同名 instance profile，仅配置 SSM ManagedInstanceCore 与指定 ECR 仓库 pull，不允许 AssumeRole 为 deployer；运行 secret 权限尚未授予。
- 当前主机 `i-04225834295350402` 为 `t8i.medium`（2 vCPU/4 GiB），位于 `us-west-2b`。现有 SG `sg-0209d561b5a71eee0` 允许 IPv4/IPv6 全入站，禁止新主机复用；本轮尚未修改原 SG，应用当前 systemd 服务均 inactive。
- aws-mcp 已配置但启动失败（缺少 uvx）；本轮身份/权限检查使用 AWS CLI，不表示 MCP 未配置。

## 拟执行的最小范围（未执行）

1. 复用现有 VPC、RDS PostgreSQL Multi-AZ DB instance 和 TLS Valkey CMD，不复制数据层。
2. 一个私有 ECR repository `sub2api-cloud`，不可变标签，部署固定 digest；只发布经过本轮测试的 amd64 镜像，不包含凭据。不得更改整个 registry 的扫描配置影响其它项目。
3. 最多新增一台按需 x86_64 EC2，与当前主机构成两宿主验收环境；目标 2 vCPU/4 GiB、20 GiB 加密 gp3。实例类型、AMI、子网/AZ、SG、实例 profile 必须通过有权限的只读查询确认后选定。不使用 Spot。
4. 每台宿主运行 gateway/control/worker；应用无公网入口，管理通过 SSM，无新增 SSH 入站；测试经受控私网通路。不创建 ALB、NAT、ECS/EKS、DNS 或新的公网应用监听。
5. 新实例只获 ECR pull、必要 SSM 和精确运行 secret 读取权限，不复用能 AssumeRole 为 deployer 的现有 host profile。应用容器阻断 IPv4/IPv6 IMDS；部署角色与 runtime 凭据隔离。
6. 必要 SG 只按来源 SG 开放数据层端口，不开放公共数据库/cache。创建之前审查实际 SG 与路由。
7. 迁移与 runtime 用户分离；真实库应用新迁移前审核迁移清单、备份和权限。不能用 migrator 绕过运行角色权限。

## 已执行记录

- 私有 ECR `sub2api-cloud` 已创建，AES256、不可变标签。已发布 `it-6fad966dd-amd64`，index digest `sha256:ceab9d6e81bbfde21c6bd5620e0cc85a9d2c052c6cf998ab638ccde8997d4005`。这仍是 SchedulerSnapshot 禁用的既有 headless 验收版本。
- amd64 manifest `sha256:ecac8ae45ad6011fa298b2455e031043e229cfc7078d22f7c743d18a42e719b1` 的 ECR BASIC scan 返回 COMPLETE、findingSeverityCounts 为空；不代表源代码或全部语言依赖安全审计通过。直接扫描 index 曾返回 UnsupportedImageType，不能混记成功。
- 新主机 `i-07f2c0ba4744915e4` 已 running，私网 `172.31.51.219`、`us-west-2a`，Amazon Linux 2023 AMI `ami-0cc098d14c47f965b`、t8i.medium、20 GiB 加密 gp3、standard CPU credits。新 SG `sg-0befb673c8130af44` 无公网入站，仅允许原测试主机 SG 私网 TCP18080–18083；没有复用原全开放 SG。
- IMDSv2 required、hop limit1、IPv6 metadata disabled；runtime 无 AWS SDK 身份需求，host 通过 profile 拉取镜像。不把 hop limit 单独当完整隔离证据；已执行的防火墙与探针见下。
- 专用角色 EC2 dry-run 返回 DryRunOperation 后才执行 run-instances，使用固定 ClientToken 防止重复创建。root 只做 IAM；新增 SSM SendCommand 仅限新主机及 AWS-RunShellScript 文档。
- Docker25.0.18 安装成功，SSM Online；新主机通过 profile 拉取固定 digest，非root/只读/network-none migrator-help 返回 REMOTE_IMAGE_CONTRACT_OK。临时 Docker 登录文件通过 EXIT trap 删除。
- 新主机已安装 IPv4 DOCKER-USER 与 IPv6 FORWARD metadata 拦截规则，并通过sub2api-container-imds.service绑定Docker启动持久化；非root容器HTTP metadata探针返回CONTAINER_METADATA_BLOCKED。该探针只证明当前metadata请求未成功，IPv6 metadata本身disabled；不把单个GET探针解释为所有SSRF路径验证。原主机本轮新增DOCKER-USER拦截，持久化单独待验。
- 新主机只可读取三个精确 runtime/shared/valkey Secrets，不含 migrator。RDS/Valkey SG 新增来源为新主机 SG 的5432/6379两条规则。Terraform main.tf已加入可选additional_runtime_security_group_ids，私有tfvars同步；live rule IDs分别sgr-01150dcdbf513ef18、sgr-0ac1159dddf99c9e4，仍需导入现有远端state（未导入前禁止直接apply）。terraform fmt-check/validate通过，未执行apply。
- 新主机首轮runtime SELECT-only manifest check确实失败，报告待应用250_oauth_refresh_state.sql。随后审查250/251（新增表与singleton seed，不修改已应用迁移），确认RDS available/MultiAZ/7天备份及LatestRestorableTime正常，通过原主机专用migrator凭据执行现有已验收二进制apply与check，均exit0。没有在新主机配置migrator，252尚未应用。
- 新主机runtime SELECT-only RDS verify-full manifest复检exit0；原主机runtime权限核验为：新表访问允许、manifest INSERT拒绝、public CREATE拒绝。
- 两主机各启动gateway/control/guarded-worker，共六容器。原主机绑定loopback18180/18181，新主机绑定私网172.31.51.219:18080/18081，Worker不发布端口。跨AZ health200，gateway models401/control API404，control models404/auth-me401；两主机日志均确认experimental runtime启动。
- 首次原主机容器因systemd env双引号被Docker保留而exit64（split gate正确拒绝），只规范化独立测试env并重建失败容器，随后health200。
- 新主机Worker首先获得领导权，原主机为standby。STOP远端leader后插入无token/凭据的过期fixture，再SIGKILL远端leader；原主机取得领导权并实际暂停fixture，CROSS_HOST_WORKER_TAKEOVER_OK。fixture随后disabled，远端Worker已请求恢复为standby。没有真实provider/model/payment调用。
- 单独验证整机停止：先确认远端Worker再次获得领导权、原主机恢复standby，再通过仅限新实例的权限stop-instances。EC2实际stopped后重新启用同一合成过期fixture，原主机取得领导权并暂停fixture，返回WHOLE_HOST_STOP_TAKEOVER_OK；原主机gateway/control仍health200。fixture finally禁用。新主机恢复后SSM命令成功，Docker与metadata防火墙unit均active、DOCKER-USER规则存在，三角色恢复运行，跨AZgateway/control health再次200。角色目前为显式手动恢复，没有声称应用自动开机恢复。
- 这不是Control OAuth session共享、完整scheduler fencing或生产HA的验收；旧镜像SchedulerSnapshot/TokenRefresh仍禁用。

## 费用范围

新增费用仅限一台 EC2 按需实例、20 GiB gp3、一个用于出站的公网 IPv4、私有 ECR 存储/请求、必要日志/SSM/Secrets Manager 与跨 AZ 流量。现有数据层保持不变。AWS Pricing API 本轮核实：t8i.medium Linux $0.04992/小时；in-use IPv4 $0.005/小时；gp3 容量 $0.08/GB-month。按730小时，实例 $36.4416 + IPv4 $3.65 +20 GiB gp3约 $1.60 = **约 $41.69/月**，不是账单上限，不含其它流量/日志/镜像存储及税费；CPU credits 已 standard。不新增 NAT/收费接口 endpoint。公网 IPv4 不等于公网应用入口：新 SG 只允许指定原测试 SG 的私网探针端口，无公网入站。

不自动销毁现有资源；新测试资源的保留、停机与清理动作单独记录。停止 EC2 不停止 EBS/ECR 等费用。

## fencing 发布前验收门禁

- PostgreSQL 选择有效 scheduler generation，Redis 使用对应隔离 namespace，不能仅依赖可回滚 Redis marker。
- 旧 leader、旧 generation、旧 owner 不能发布当前 reader 可见快照；完整 snapshot、增量 account payload、member、group retire/reopen 与 gateway fallback 路径全部覆盖。
- outbox 领取/确认绑定持锁 PG session 和 generation；外部 Redis 操作不在 PG 事务内等待。
- reader/本地 memo 的 generation 选择、Redis 数据缺失/回滚、PG 不可用时的 fail-closed 或数据库 fallback 行为明确并测试。
- 新 generation 未完成构建不得被选为 active；取消/丢锁时不能继续激活；旧 namespace 垃圾回收不得删除当前 namespace。
- 先通过真实本地 PG/Redis 故障测试，再允许 SchedulerSnapshot 进入多副本维护任务；TokenRefresh 不因 scheduler 门禁解除而自动放行。

## 跨宿主验收（待执行）

固定镜像 digest；核验两主机不同 instance ID。验证角色路由、Control 共享单次 OAuth session（不调用真实 provider）、RDS verify-full、Valkey TLS、非 root/只读容器、IMDS 隔离、主 Worker 容器终止与宿主停止后的接管、活跃槽位保留、scheduler 旧 leader/Redis rollback fencing。所有日志脱敏，测试 fixture 与生产/已停用真实 OAuth 账号分离，不调用真实模型/支付。
