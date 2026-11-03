# EC2 多机镜像与离线契约验证（预生产、headless）

## 范围与未验证项

这些资产只准备本地镜像及现有 Sub2API 的独立 gateway/control/worker/migrate 入口，不创建 EC2/RDS/Valkey，不登录 registry、不推送 ECR，不修改现有 systemd 服务。独立角色仍是显式 opt-in 的预生产能力，不是生产就绪声明。

`deploy/Dockerfile.cloud` 复用现有 Go server、独立 `cmd/migrate`、迁移 SQL 和 pricing resources；**源码构建不带 frontend/embed，因此 control 只有控制 API，不含完整管理 UI**。没有额外网关或调度器。不包含 pg_dump/psql，不能把本镜像当作完整备份工具。需要完整 UI 时，应单独审核 frontend 构建/静态部署；不得将 headless 结果写为 UI 已验证。

本次新增文件本身不证明镜像成功构建。amd64/arm64 分别构建、分别测试；未执行的平台、真实多机联通、数据库权限、故障切换、任务接管和完整 UI 都必须单独记录，不能用脚本存在代替验收。

## 本地构建与 smoke

从仓库根目录执行，依赖 Docker BuildKit、git、bash；prebuilt 路径额外依赖 `file`。构建可能拉取基础镜像/Go modules，不属于离线验证，必须由操作者明确执行。下面命令不推送镜像。

```sh
# 首先只验证静态资产，不运行容器或连接网络。
bash deploy/tests/cloud-image-contract-test.sh

# 源码构建：固定 Linux 单平台，顺序编译两个二进制。
bash deploy/build_cloud_image.sh sub2api-cloud:local-amd64 linux/amd64
bash deploy/tests/cloud-image-contract-test.sh --image sub2api-cloud:local-amd64
```

默认构建限制 `GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20`、`go build -p=1`，可通过 `BUILD_GOMAXPROCS/BUILD_GOMEMLIMIT/BUILD_GOGC` 显式覆盖。Go 内存限制是软限制，不是整个 Docker build 的硬上限；不要与其他大编译并发运行。

为了避免重复昂贵编译，可以打包已经验证的 CGO0 Linux 二进制，目录中必须为 `server` 和 `migrate`：

```sh
# 只示范构建参数；已有相同参数/当前源码的产物时直接复用，不必重复执行。
mkdir -p /tmp/sub2api-cloud-prebuilt-amd64
(cd backend && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 \
  go build -p=1 -trimpath -ldflags='-s -w' -o /tmp/sub2api-cloud-prebuilt-amd64/server ./cmd/server && \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=1 GOMEMLIMIT=1400MiB GOGC=20 \
  go build -p=1 -trimpath -ldflags='-s -w' -o /tmp/sub2api-cloud-prebuilt-amd64/migrate ./cmd/migrate)
bash deploy/build_cloud_image.sh sub2api-cloud:local-amd64 linux/amd64 /tmp/sub2api-cloud-prebuilt-amd64
bash deploy/tests/cloud-image-contract-test.sh --image sub2api-cloud:local-amd64
```

脚本拒绝动态链接/错误架构产物。`file` 检查只证明 ELF/静态链接/架构，不证明源码来源、CGO 构建参数、版本或签名；操作者负责对应 commit、构建日志及 SHA256。prebuilt 也应使用无 embed 的上述构建参数，以保持本文 headless 契约。

脚本创建临时最小 context，仅拷贝 Git 已跟踪的 production Go 源码、显式 embedded assets、LICENSE、pricing JSON、Dockerfile 或指定两个产物，不把整个宿主目录交给 Docker。未跟踪的新 Go 文件不会进入构建；先审查并纳入 Git index。源码中的凭据同样不允许，allowlist 不是秘密扫描器。**不要直接运行 `docker build -f deploy/Dockerfile.cloud .`**；原有 repository .dockerignore 不代表这个 cloud 构建的最小上下文策略。

image smoke 的所有容器使用 `--network none`、只读 rootfs、固定 UID/GID 10001、drop ALL capabilities、no-new-privileges 和 tmpfs。验证独立 `migrate -help`、缺失/非法/all 角色拒绝、split 开关关闭拒绝、自动迁移拒绝、资源文件及写目录。不会运行真正 schema migration 或启动可联网 server。HTTP health 是存活检查，不是数据库 readiness，也不代表 worker 是 leader。

### arm64 计划

把平台替换为 `linux/arm64`，prebuilt 的 `GOARCH` 改为 `arm64` 并使用独立产物目录/标签。Dockerfile 在 BUILDPLATFORM 上运行 Go 并交叉编译，runtime 的 apk 需要原生对应主机或已配置模拟器。交叉编译成功不等于 arm64 运行成功；优先在原生 arm64 Docker host 执行同一 smoke。当前脚本只构建一个平台并加载本地镜像，不创建多架构 manifest，不发布 registry。

## 外部配置与多机 Compose

`deploy/ec2-cloud-compose.yaml` 没有 PostgreSQL/Redis/Valkey 服务，也没有可用凭据。操作者先在仓库之外创建权限受限的文件，不能复制进仓库、镜像或 Git：

- `CLOUD_ENV_FILE`：runtime PG user/password、host/port/dbname；Valkey primary endpoint、ACL username/password、port；同一集群共享显式 `JWT_SECRET` 和合法 `TOTP_ENCRYPTION_KEY`；标准运行模式。现有 config 环境变量使用 `DATABASE_*`、`REDIS_*`，例如 `REDIS_USERNAME`、`REDIS_PASSWORD`、`REDIS_ENABLE_TLS`。不要假定任意 `*_FILE` 被应用识别。
- `CLOUD_MIGRATOR_ENV_FILE`：单独迁移账户，只放迁移所需 PG 配置。migration 直连数据库，不用运行角色账户或主账户。
- `CLOUD_CA_FILE`：已验证的 PostgreSQL CA bundle，容器内只读 `/run/certs/postgres-ca.pem`。PG 强制 `verify-full`；Valkey 启用 TLS 且使用非分片 CMD primary endpoint。不要设置绕过证书校验。
- 不在 env_file 使用与结构化连接参数冲突的 `DATABASE_DSN`：migrator 优先 DSN，可能绕过 Compose 的 SSL 参数；本模板要求结构化 PG 配置。

这些文件必须通过宿主机权限保护，env_file 的值可被 Docker 管理权限用户读取，不是 Secret Manager 替代品。多个主机必须共享正确 JWT/TOTP，runtime 数据库不能 DDL 或读取 secret bootstrap 表；schema/共享 secret bootstrap 必须由单独受控流程完成，migrator 不负责应用初始化。

只将以下**非秘密指针变量**导出到操作 shell：`CLOUD_IMAGE`（本地 tag，发布另需授权；正式应固定 digest）、`CLOUD_ENV_FILE`、`CLOUD_MIGRATOR_ENV_FILE`、`CLOUD_CA_FILE`，全部文件指针为仓库外绝对路径。Compose 插值会检查整个文件，所以即使只选择 gateway 也要设置四者。不要将 `docker compose config` 输出放进日志，它可能展开秘密；静态检查只使用 `config --quiet`。

```sh
# 已设置上述变量且文件已预先安全创建后；本地解析，不 provision 数据层。
docker compose -f deploy/ec2-cloud-compose.yaml --profile gateway config --quiet

# 迁移默认仅 check；普通 up 不包含 migrate，意外启用 migrate profile 也不 apply。
docker compose -f deploy/ec2-cloud-compose.yaml --profile migrate run --rm migrate -check
# 只有发布审核明确允许应用迁移时才执行以下显式覆盖命令。
# docker compose -f deploy/ec2-cloud-compose.yaml --profile migrate run --rm migrate -timeout 10m

# 每台机器仅启动自己的 profile；不会创建其它主机。
docker compose -f deploy/ec2-cloud-compose.yaml --profile gateway up -d gateway
# 在其它主机分别选择 control 或 worker。
```

默认 gateway/control 绑定宿主 loopback，worker 不发布端口。多机入口需已有受控反向代理/私有连接、TLS 和安全组；只有审核后才通过 `GATEWAY_BIND_IP`/`CONTROL_BIND_IP` 指定私有地址，不要直接公开到互联网。本模板不创建负载均衡、DNS、IMDS 防火墙或 IAM；固定非 root UID 不阻止 EC2 metadata，必须另做宿主 IPv4/IPv6 IMDS 网络隔离。容器无法读取宿主凭据目录不代表它不能访问 metadata。

`/app/data` 与 `/tmp` 是临时 tmpfs，重建丢失。pricing fallback 只读资源保留；日志默认 stdout 且 Docker 日志轮转。需要 durable image upload/export/backup 时必须独立设计存储，不能将该模板当作完整数据持久化方案。

worker 固定 `RUNTIME_MULTI_REPLICA_WORKERS=true`，只放行经审计的六项维护子集并使用 PostgreSQL session leadership。TokenRefresh、SchedulerSnapshot、支付、备份等仍禁用，不能通过改 env 静默声称全部多副本安全；不要混跑 legacy all 或普通 full worker。详见 [WORKER_REPLICA_SAFETY.zh-CN.md](WORKER_REPLICA_SAFETY.zh-CN.md)。HTTP 健康不能作为任务领导权证据。

## 本地六容器集成 smoke

镜像契约检查后运行：

```sh
python3 -I tools/smoke_cloud_image.py --image sub2api-cloud:local-amd64 \
  --postgres-image postgres:18.1-alpine3.23 --redis-image redis:8.4-alpine \
  --keep-logs /tmp/sub2api-image-smoke-evidence
```

脚本只用已加载镜像，不build/pull；创建唯一internal网络和全新PG18/Redis8，数据层和应用均不发布端口，HTTP检查通过docker exec在应用容器内完成。独立migrator应用schema后启动2 gateway、2 control、2 guarded worker。凭据随机、私有文件传递，启动配置禁用远程pricing。验证路由/任务归属、冻结并杀死leader后standby实际暂停过期fixture，以及活跃槽位/等待计数保留；最后清理容器、匿名卷和网络。

这是单机Docker网络测试，不证明真实跨主机、TLS或AWS故障切换；两个Control仅验证路由，不验证OAuth共享session。此前process smoke另有session跨Control/重启覆盖，不能将其混记成本脚本结果。


### 2026-11-03 本机验收结果

- 官方 golangci-lint v2.14.0 最终 **0 issues**；完整 integration 与 unit 均通过（repository integration41.127s、service integration149.757s、repository unit10.110s、service unit251.324s）。
- prebuilt Linux amd64 静态 server/migrate 镜像构建通过，tag `sub2api-cloud:local-amd64`，本机image ID `sha256:ceab9d6e81bbfde21c6bd5620e0cc85a9d2c052c6cf998ab638ccde8997d4005`。未push registry；source-build目标与arm64运行尚未验证。
- 网络禁用的镜像契约通过；compose `config --quiet`通过。
- 六容器 smoke 首次因internal network不安装host publish映射、脚本假定binding非null失败；重试后改用容器内部HTTP探针，不开放端口。最终image-smoke-04 passed=true，独立迁移、六角色路由、worker-a SIGKILL→worker-b实际暂停fixture、live slot/wait保留均通过，临时容器/网络清理完成。provider_calls=0，OAuth session未在本脚本验证。
- 真实跨机器通信、TLS/SG/IMDS隔离、RDS运行权限与251迁移部署仍未验；此镜像headless，不含完整管理UI。SchedulerSnapshot/TokenRefresh仍禁用，不能直接宣称生产HA。
