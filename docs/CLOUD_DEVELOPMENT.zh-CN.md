# Cloud fork 开发与第一批验证

## 仓库和范围

- 本地路径：`/root/ai/sub2api-cloud`。
- origin：`https://github.com/Starktomy/sub2api.git`。
- upstream：`https://github.com/Wei-Shaw/sub2api.git`。
- 当前开发分支：`feat/ecs-cloud-foundation`。
- 不对源仓库做修改；按用户要求进行本地阶段提交，未明确要求时不 push。

完整架构见 `CLOUD_ARCHITECTURE.zh-CN.md`；真实完成度见 `IMPLEMENTATION_STATUS.md`。

## 独立数据库命令

在 `backend/` 构建：

```sh
GOMAXPROCS=2 go build -p=1 -o bin/migrate ./cmd/migrate
GOMAXPROCS=2 go build -p=1 -o bin/server ./cmd/server
```

连接从环境读取，密码不要放命令行参数或 shell 历史。推荐由 Secret 注入以下变量：

- `DATABASE_HOST`
- `DATABASE_PORT`（默认 5432）
- `DATABASE_USER`
- `DATABASE_PASSWORD`
- `DATABASE_DBNAME`
- `DATABASE_SSLMODE`（命令默认 `verify-full`；测试本地数据库才显式设 `disable`）

也可用 `DATABASE_DSN` 提供 libpq DSN/URL；此时调用方负责 TLS 配置。RDS 证书通过运行镜像的可信 CA 或受控证书配置提供。

```sh
# 应由专用 DDL 角色执行，不启动服务、Redis 或后台任务。
./bin/migrate -timeout 10m

# 仅 SELECT schema_migrations，不能修复或应用迁移。
./bin/migrate -check -timeout 30s
```

迁移命令复用现有 SQL 迁移、校验和兼容白名单、advisory lock 与非事务迁移处理，不修改旧迁移。执行迁移后自动再验证一次 manifest。

命令只处理 schema，不创建应用管理员，不写 JWT/TOTP，不生成 config 文件。管理员/共享密钥及 runtime 数据库权限的显式 bootstrap 尚未实现，不能将 schema 成功当作生产初始化完成。

## Runtime 数据库模式

- 默认 `DATABASE_MIGRATION_MODE=auto`：保持旧启动行为，允许自动迁移和 bootstrap，适合已有单机/本地测试。
- `DATABASE_MIGRATION_MODE=check`：只读校验 manifest，不 DDL、不获取迁移锁、不运行 secret bootstrap 或 simple-mode seed；必须预先完成迁移并显式配置共享 JWT/TOTP。
- 其他值启动失败，不提供 `skip` 或静默忽略错误。

check 模式读取以下共享配置（真实值由 Secret 注入，不写入版本库）：

- `JWT_SECRET`：符合现有配置长度要求。
- `TOTP_ENCRYPTION_KEY`：符合现有加密密钥要求，所有副本一致。

check 模式不因容器缺少 `.installed` 或 config 文件而开放安装向导。`--setup` 与 check 模式组合被拒绝。

manifest 校验允许数据库额外存在后续迁移，以便 expand/contract 滚动发布。它不能证明任何破坏性新 schema 都兼容旧程序；发布流水线仍需验证新旧版本契约。

## Runtime 角色

`RUNTIME_ROLE` 接受 `all/gateway/control/worker`，默认 `all`。独立角色默认被拒绝；仅隔离预生产可以显式设 `RUNTIME_ENABLE_SPLIT_ROLES=true`，同时必须 `DATABASE_MIGRATION_MODE=check`、`RUN_MODE=standard` 及显式共享JWT/TOTP。已验证角色实际启动和后台scope，但仍是共享Wire图，不是生产最小权限/高可用验收。

`RUNTIME_SHUTDOWN_TIMEOUT_SECONDS` 控制真实 HTTP Shutdown 的等待预算，默认 90 秒，上限 110 秒。它不实现 SIGTERM 前排空，不迁移 WebSocket，也不保证全部 cleanup 在 ECS 120 秒停止阶段内完成。生产长连接生命周期仍为待办。

真实迁移范围与剩余边界见 [CLOUD_RUNTIME_MIGRATION.zh-CN.md](CLOUD_RUNTIME_MIGRATION.zh-CN.md)。预生产限定单Control、单维护Worker；Gateway可用两实例验证共享状态。尚未给维护Worker或Control实现全能力跨实例状态与lease，不能任意扩容。

### 真实角色进程烟测

先完成local test数据库迁移并通过私有环境提供 `CLOUD_TEST_DATABASE_DSN`（loopback、dbname以`_test`结尾，不得传生产库），安装本地redis-server/redis-tools后：

```sh
python3 -I ../tools/smoke_cloud_roles.py --binary bin/server
```

脚本启动双Gateway、单Control、单Worker，创建密码保护的临时Redis和临时配置，验证健康/鉴权/路由边界、日志中的任务归属及Worker不误删其他Gateway槽位，然后SIGTERM等待退出。脚本会执行维护逻辑，故只接受显式本地测试库，不调用AWS、不发真实模型/支付请求。`--keep-logs`仅写入新建目录，凭据先脱敏。

## 聚焦测试

```sh
GOMAXPROCS=2 go test -p=1 ./internal/config
GOMAXPROCS=2 go test -p=1 ./internal/repository -run 'Test(CheckMigrations|InitializeSchema|InitEntCheck|InitEntPostgres)' -count=1
GOMAXPROCS=2 go test -p=1 ./cmd/migrate -count=1
```

真实 PostgreSQL 验证：使用独立测试数据库，将 `CLOUD_TEST_DATABASE_DSN` 通过私有环境文件或 Secret 注入；先运行迁移，再运行：

```sh
DATABASE_DSN="$CLOUD_TEST_DATABASE_DSN" ./bin/migrate
DATABASE_DSN="$CLOUD_TEST_DATABASE_DSN" ./bin/migrate
DATABASE_DSN="$CLOUD_TEST_DATABASE_DSN" ./bin/migrate -check
GOMAXPROCS=2 go test -p=1 ./internal/repository -run '^TestCheckMigrationsPostgresReadOnly$' -count=1
GOMAXPROCS=2 go test -p=1 ./cmd/migrate -count=1
```

`TestCheckMigrationsPostgresReadOnly` 将数据库 session 设置成只读，先证明 DDL 被拒绝，再运行 schema 检查。`TestInitEntPostgresCheckHasNoBootstrapWrites` 创建仅能查询 manifest 的临时角色，证明实际 InitEnt check 初始化不需要读取/写入密钥表，再验证密钥写入被权限拒绝。未提供 DSN 时这些测试明确 skip。

`TestDatabaseConnectorSpecialPassword` 会在明确提供的本地测试库中创建临时登录角色，验证特殊字符密码的 DSN quoting，然后删除临时角色。不要把生产数据库 DSN 传给此测试。

所有真实上游、支付和云资源集成验收均需单独执行；没有凭据和测试环境时不能声称通过。

## 当前EC2＋真实托管数据测试

用户已授权创建集成数据层，`infra/ec2-integration`已实际部署；四角色systemd运行、RDS/Valkey TLS/权限/共享状态、非模型HTTP业务与受控切换结果见 [EC2_MANAGED_DATA_VALIDATION.zh-CN.md](EC2_MANAGED_DATA_VALIDATION.zh-CN.md)。`account.test`仅检查格式/设0600，未导入或调用模型；按用户要求先收尾本批。

证书配置：`DATABASE_SSLMODE=verify-full`＋`DATABASE_SSLROOTCERT=/opt/sub2api-integration/certs/rds.pem`；Valkey `REDIS_ENABLE_TLS=true`，使用主机名认证及专用ACL。不要在故障时改成disable或跳过证书验证。

真实fixture验收：`TestManagedDataLiveContracts`需显式`MANAGED_DATA_LIVE=1`及命名RDS集成库/缓存设置，默认skip，不使用生产资源；运行环境由root私有文件加载，不把密码写命令参数。业务脚本`tools/verify_managed_business.py`只产生/清理临时fixture，无模型/支付调用。故障脚本需显式授权参数，仅作用命名测试资源；不要为复验健康而重复触发。

## 后续ECS部署前置条件

- Region 固定 `us-west-2`。
- 使用专用部署角色，不使用 AWS root。
- 先 review foundation plan、账号、参数与费用范围，后执行资源创建。
- `infra/aws` 的 init/validate/mock tests 不需要创建资源。
- 不默认启动尚未拆分的应用角色，不发布占位镜像。
- ECS services、域名/ACM、runtime DB 账户、加密 Secret、远端 state backend、发布和排空机制仍需后续实现与批准。
- 分阶段实机启动条件见 [AWS_PREPRODUCTION_CHECKLIST.zh-CN.md](AWS_PREPRODUCTION_CHECKLIST.zh-CN.md)，不要把本地多角色启动通过当作AWS部署已完成。
