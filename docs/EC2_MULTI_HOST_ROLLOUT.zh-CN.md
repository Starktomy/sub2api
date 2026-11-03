# EC2 多机发布：执行范围与门禁

日期：2026-11-03。状态：用户已授权发布镜像、创建必要 AWS 资源，并明确允许 root 仅用于补齐所需 IAM 权限。已创建私有 ECR、发布既有验收镜像，并启动第二台 EC2；应用跨机验收与完整 fencing 尚未完成。本文件不是完成报告。

## 追加：双机生产基础验收（进行中）

用户随后要求先完成资源限制、监控、发布/回滚/开机恢复和容量测试；域名稍后提供。又明确要求SSH TCP22继续全网开放、先临时创建ALB测试，全部测试完成后可删除ALB及第二台EC2，前期保留单机。

- 原主机已安装官方SSM Agent并通过独立SendCommand；两主机SSM Online。曾撤销全网入站并保留操作IP/32，随后按用户新要求恢复IPv4 TCP22 0.0.0.0/0，仅SSH，不恢复全协议或IPv6全开放。
- 临时internal ALB sub2api-it-ha，SG仅接受原测试主机SG的HTTP80。两个Target Group分别Gateway/Control，Host api.test.invalid/admin.test.invalid分流，默认404；没有DNS发布、公网裸HTTP或证书冒充。ALB费用$0.0225/小时+$0.008/LCU小时。
- 两台现改用sub2api-roles.service托管gateway/control/worker，分别内存1024/768/512MiB与CPU1.0/0.4/0.3，pids256、禁swap、非root/readonly/tmpfs；Go软目标不等于预留。原测试容器已停止，旧容器不自动启动。
- 首次第二台安装因docker inspect不存在对象返回[]触发IndexError失败，恢复旧角色后修复并补测试，重试通过。原主机旧容器graceful stop较慢，ALB第二台持续服务。
- 新增host-local失败发布自动回滚实测通过：候选配置DB端点指向本机不可用端口，返回失败后supervisor恢复原配置，三角色healthy；不声称跨主机滚动编排完成。
- 第二台真实EC2 reboot后unit、metadata firewall、collector timer自动active，角色自动创建；最终Docker healthy确认仍单独验证。
- ALB两Target Group均有两healthy后端；第二台服务重启期间120次health请求全部200，P95 3.17ms。这不是模型吞吐。
- 原主机有界HTTP测试4/12/24并发各240次：4并发全部预期；12/24阶段各120次auth请求返回429（安全限流），health成功。脚本原始统计记为失败，不宣称容量全通过；无真实模型压测。
- host/role collector两台安装并成功PutMetricData，CloudWatch namespace Sub2API/Runtime，固定InstanceId/Role低基数。原主机ProtectHome导致/root内CLI不可访问，复制既有CLI到/opt并修复symlink后发布成功。两个collector离线15tests、role deploy13tests通过；已接CI。
- 已创建21条baseline metric alarms：RDS CPU/free storage/free memory、两个Valkey节点engineCPU/memory/evictions、两host status/heartbeat/collection与各role health。曾观察全部OK；故障测试期间可变。ActionsEnabled=false，无外部通知接收地址，不能算通知送达验收。34个常规custom series/两host，费用另计；阈值为初始基线非production容量承诺。
- 真实模型首次2请求：账户1成功、1条usage/customer cost $0.000100；账户2本地pool0/503，未建立上游调用。只读诊断指向启用后2秒早于5秒publication；正在补PG-selected manifest/payload readiness，剩余最多2请求。账户/Key/user均finally停用，无refresh/import，账务保留。
- 全部验收及证据保存前，不销毁临时ALB或第二台。当前资源仍计费；后续只清理本轮明确临时资源，不销毁RDS/Valkey/原EC2/ECR。

## 收尾与当前单机状态（2026-11-03）

- 第二台失败发布测试最终成功：AWS waiter先到达等待上限，实际SSM命令随后exit0；supervisor日志previous release restored，三角色healthy。真实reboot后最终REBOOT_ALL_ROLES_HEALTHY、collector Result=success，不只检查unit active。
- 模型readiness修复为两次稳定PG-selected publication观察，校验manifest/full/slim digest、group bucket成员及eligible状态后才发请求。7项聚焦unit tests通过。复验两账户均HTTP200，各input20/output5、各customer cost $0.000100。累计4次generation HTTP请求、3条成功ledger，$0.000300客户价格；首轮账户2本地503无usage，不宣称到达provider。凭据不变、无refresh/import；账户、fixture用户/Key最终禁用且published账户disabled，账务记录保留。
- 停止第二台全部角色后，原主机publication/maintenance generation=5/5、outbox0；ALB仅剩原主机时120次health全部200，P95 2.45ms。该测试不是持续模型容量上限，先前429限流结果不改写。
- 按用户明确授权已删除临时ALB sub2api-it-ha及两个Target Group；describe确认ALB为空。第二台i-07f2c0ba4744915e4已terminated，BlockDeviceMappings为空。临时SG sg-09623eef732d304f9与sg-0befb673c8130af44删除成功，RDS/Valkey及原主机上的临时来源规则已撤销；外部tfvars additional_runtime_security_group_ids恢复[]。
- 临时ALB策略、清理策略与第二台runtime角色/profile已删除，deployer新机launch/lifecycle/preparation/data-ingress权限撤销；保留原主机SSM/指标与必要运行权限。ELB AWS service-linked role保留，不把AWS共享服务身份当本项目临时资源删除。
- 原主机保留sub2api-roles.service与collector timer，三角色resource-capped健康、private gateway health200；保留15条RDS/Valkey/原主机基础告警查询均OK，外部通知仍未配置。公网仅SSH22按用户要求开放；没有DNS/公网HTTPS入口。
- 现已回到单EC2预生产测试形态，不再具备宿主故障冗余或ALB自动入口切换。RDS Multi-AZ/双节点Valkey与ECR保留继续计费；新增34series双机collector降为17常规series单机（历史CloudWatch数据按AWS保留），告警15条，自定义指标/告警费用仍存在。

## 已核实的身份与阻塞（以下保留早期执行记录，当前状态以上方追加为准）

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

## 2026-11-03 scheduler publication 实现与本地验收

- 实现提交 `aad3bd278`：显式 `runtime.scheduler_publications` 默认false，保持旧行为；新worker任务名SchedulerPublicationService，绑定实际取得的PG session，不解禁legacy SchedulerSnapshotService或TokenRefresh。
- 每次publication使用generation+随机ID的不可变namespace；PG指针固定manifest SHA256，manifest固定full/slim payload哈希。所有reader（含GetAccount）每次读取PG权威，不信Redis全局指针或旧本地memo；缺失/损坏回退DB，gateway不写快照。
- Account/Group通过同一read-only REPEATABLE READ事务复用既有仓储查询。PG短事务原子激活指针+exact-ID ack；其中无Redis网络调用，COMMIT结果不明不重用ID。
- 空outbox健康时只校验全部payload，不产生新publication。新generation或缺失数据重建。active不设TTL；PG先永久retire再清Redis，obsolete2分钟grace、abandoned staging5分钟；反复轮转清理处理Redis回滚后旧数据复活。
- 元数据不是硬有界：每次attempt保留PG lifecycle记录及Redis retirement marker；凭据payload可回收，故障/积压时回收延迟增长。未做production负载定容；真实网络注入COMMIT结果不明尚未验，仅sqlmock覆盖。
- 默认config/service/repository测试通过0.669s/148.721s/7.963s；unit-tag聚焦service0.319s/repository0.400s；server build/vet通过。最终真实PG18/Redis8 publication套件6.644s通过，包含RR并发更新、空outbox无写、full payload缺失恢复、stale leader、GC竞争及回滚复活、exact ack事务失败；精确CI golangci-lint v2.14.0为0 issues。
- 候选镜像重新构建及network-none加固契约通过，已发布it-aad3bd278-amd64，固定index digest sha256:d084e14bd2dcb0fd057bedf9a06ac4f27aa8394f735ee45901115e9bc01909b8。252已通过独立migrator apply/check exit0；runtime pointer/lifecycle所需权限均true，manifest写/schema CREATE仍false。
- 两主机六角色已切换该固定镜像并显式开启RUNTIME_SCHEDULER_PUBLICATIONS=true。原主机先获得generation1并发布，远端Worker为standby；SIGKILL原主机leader后远端激活generation2，返回CROSS_HOST_PUBLICATION_TAKEOVER_OK，PG pointer generation等于maintenance generation。远端日志确认新task/leader存在且无cycle error；原主机随后恢复standby。
- 真实Valkey定点故障：只删除PG当前publication的精确manifest键；远端leader在generation2发布新ID并恢复，返回REAL_VALKEY_PUBLICATION_RECOVERY_OK，outbox_before=0/outbox_after=0。未FLUSH、未删除其它缓存；这是缺失恢复，不冒称真实Valkey整库回滚。
- 旧六容器以previous名称停止保留用于受控回退，不自动启动或混跑。新版本跨AZhealth200及角色路由401/404隔离通过。TokenRefresh仍未放行。

## 跨宿主完整验收（部分已执行，剩余见上）

固定镜像 digest；核验两主机不同 instance ID。验证角色路由、Control 共享单次 OAuth session（不调用真实 provider）、RDS verify-full、Valkey TLS、非 root/只读容器、IMDS 隔离、主 Worker 容器终止与宿主停止后的接管、活跃槽位保留、scheduler 旧 leader/Redis rollback fencing。所有日志脱敏，测试 fixture 与生产/已停用真实 OAuth 账号分离，不调用真实模型/支付。
