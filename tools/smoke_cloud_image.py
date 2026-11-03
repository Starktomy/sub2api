#!/usr/bin/env python3
"""Offline local cloud-image integration smoke (Python standard library only).

Requires preloaded application, postgres:18-alpine and redis:8-alpine images.
Never builds/pulls, uses AWS, or accepts an existing database. Run with python3 -I.
Two gateways, two controls and two guarded workers share a fresh internal Docker
network and disposable PostgreSQL/Redis. This checks routing and worker takeover,
NOT shared OAuth sessions, provider behavior, TLS, or actual multi-host networking.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import time


ROUTES = {
    "gateway": {"/v1/models": 401, "/api/v1/auth/me": 404, "/setup/status": 404},
    "control": {"/v1/models": 404, "/api/v1/auth/me": 401, "/setup/status": 200},
    "worker": {"/v1/models": 404, "/api/v1/auth/me": 404, "/setup/status": 404},
}
LEADER_MARKER = "maintenance PostgreSQL session leadership acquired"


class SmokeError(RuntimeError):
    pass


def docker(*args, input=None, timeout=30, check=True):
    # Never include captured stderr or command arguments in exceptions: Docker
    # and application diagnostics can contain connection details.
    try:
        result = subprocess.run(["docker", *map(str, args)], input=input,
                                capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SmokeError("Docker command could not complete") from error
    if check and result.returncode:
        raise SmokeError("Docker command failed (inspect optional redacted logs)")
    return result


def wait_for(probe, description, seconds=60):
    deadline = time.monotonic() + seconds
    while True:
        if probe():
            return
        if time.monotonic() >= deadline:
            raise SmokeError("timed out: " + description)
        time.sleep(0.25)


def private_file(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        output.write(text)
    return path


def env_file(path, values):
    return private_file(path, "".join(f"{key}={value}\n" for key, value in values.items()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="Preloaded deploy/Dockerfile.cloud image")
    parser.add_argument("--postgres-image", default="postgres:18-alpine")
    parser.add_argument("--redis-image", default="redis:8-alpine")
    parser.add_argument("--keep-logs", type=Path, help="New directory for redacted container logs only")
    args = parser.parse_args()
    if args.keep_logs and args.keep_logs.exists():
        parser.error("--keep-logs must name a new directory")
    # Resolve local references to immutable local IDs before creating resources;
    # all docker run calls additionally use --pull=never.
    images = {}
    for key, image in (("app", args.image), ("pg", args.postgres_image), ("redis", args.redis_image)):
        if image.startswith("-"):
            parser.error("image references cannot start with '-'")
        result = docker("image", "inspect", "--format", "{{.Id}}", image, check=False)
        if result.returncode:
            parser.error("all three images must already be loaded locally; this tool never pulls")
        images[key] = result.stdout.strip()

    prefix = "sub2api-image-smoke-" + secrets.token_hex(6)
    names = []
    results = []
    failure = None
    network_created = False
    redactions = [secrets.token_hex(32) for _ in range(4)]
    pg_password, redis_password, jwt_secret, totp_secret = redactions
    old_handlers = {}

    def interrupted(signum, _frame):
        raise SmokeError(f"interrupted by signal {signum}")

    for signum in (signal.SIGINT, signal.SIGTERM):
        old_handlers[signum] = signal.signal(signum, interrupted)

    with tempfile.TemporaryDirectory(prefix=prefix + "-") as temporary:
        root = Path(temporary)
        pg = prefix + "-postgres"
        redis = prefix + "-redis"

        def start(label, options, image, command=()):
            name = prefix + "-" + label
            # Register before creation so partial docker run failures are cleaned.
            names.append(name)
            docker("run", "--detach", "--pull=never", "--name", name,
                   "--network", prefix, "--log-opt", "max-size=5m",
                   "--log-opt", "max-file=1", *options, image, *command)
            return name

        def logs(name):
            reply = docker("logs", name, check=False)
            return reply.stdout + reply.stderr

        def sql(statement):
            # Local Unix socket inside the disposable DB; password only exists
            # in env_file, never in argv, SQL text or the host process environment.
            return docker("exec", "-i", pg, "psql", "-X", "-qAt", "-U", "smoke",
                          "-d", "cloud_image_test", "-v", "ON_ERROR_STOP=1",
                          input=statement, timeout=20).stdout.strip()

        def redis_command(*command, check=True):
            # Expand the env_file credential inside the container, not in argv.
            return docker("exec", redis, "sh", "-c",
                          'export REDISCLI_AUTH="$SMOKE_REDIS_PASSWORD"; exec redis-cli --raw "$@"',
                          "redis-cli", *command, check=check).stdout.strip()

        def request(container, path):
            # Internal Docker networks may disable host port publication. Probe
            # loopback inside each app instead; no test ports need exposure.
            reply = docker("exec", container, "wget", "-S", "-O", "-", "-T", "2",
                           "http://127.0.0.1:8080" + path, check=False)
            status = None
            for line in reply.stderr.splitlines():
                fields = line.strip().split()
                if len(fields) >= 2 and fields[0].startswith("HTTP/") and fields[1].isdigit():
                    status = int(fields[1])
            if status is None:
                raise OSError("container HTTP not ready")
            return status, reply.stdout

        hardened = ["--user", "10001:10001", "--read-only", "--cap-drop", "ALL",
                    "--security-opt", "no-new-privileges:true",
                    "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
                    "--tmpfs", "/app/data:rw,noexec,nosuid,nodev,size=128m,uid=10001,gid=10001,mode=0700"]
        try:
            docker("network", "create", "--internal", prefix)
            network_created = True
            pg_env = env_file(root / "pg.env", {
                "POSTGRES_USER": "smoke", "POSTGRES_DB": "cloud_image_test",
                "POSTGRES_PASSWORD": pg_password,
            })
            start("postgres", ["--env-file", pg_env, "--network-alias", "postgres",
                               "--tmpfs", "/var/lib/postgresql:rw,nosuid,nodev,size=1g"], images["pg"])
            wait_for(lambda: docker("exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "smoke", "-d",
                                    "cloud_image_test", check=False).returncode == 0,
                     "PostgreSQL readiness")
            if not sql("SHOW server_version;").startswith("18."):
                raise SmokeError("fixture requires PostgreSQL 18")
            redis_config = private_file(root / "redis.conf", (
                "bind 0.0.0.0\nprotected-mode yes\nport 6379\n"
                f"requirepass {redis_password}\nsave \"\"\nappendonly no\ndir /data\n"))
            # The mounted 0600 config must be readable by Redis' explicit UID.
            # Root callers assign it to a nonroot UID; nonroot callers use their
            # own UID, avoiding world-readable config or chmod 0644 workarounds.
            redis_uid = os.getuid() or 10001
            redis_gid = os.getgid() if os.getuid() else 10001
            if os.getuid() == 0:
                os.chown(redis_config, redis_uid, redis_gid)
            redis_env = env_file(root / "redis.env", {"SMOKE_REDIS_PASSWORD": redis_password})
            start("redis", ["--user", f"{redis_uid}:{redis_gid}", "--read-only",
                            "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                            "--env-file", redis_env, "--network-alias", "redis",
                            "--tmpfs", f"/data:rw,nosuid,nodev,size=64m,uid={redis_uid},gid={redis_gid}",
                            "--mount", f"type=bind,src={redis_config},dst=/run/redis.conf,readonly"],
                  images["redis"], ["redis-server", "/run/redis.conf"])
            wait_for(lambda: redis_command("PING", check=False) == "PONG", "Redis readiness")
            if "redis_version:8." not in redis_command("INFO", "server"):
                raise SmokeError("fixture requires Redis 8")

            database = {"DATABASE_HOST": "postgres", "DATABASE_PORT": "5432",
                        "DATABASE_USER": "smoke", "DATABASE_PASSWORD": pg_password,
                        "DATABASE_DBNAME": "cloud_image_test", "DATABASE_SSLMODE": "disable"}
            migrate_env = env_file(root / "migrate.env", dict(database, RUNTIME_ROLE="migrate"))
            migrator = start("migrate", [*hardened, "--env-file", migrate_env, "--no-healthcheck"],
                             images["app"], ["-timeout", "5m"])
            exit_code = docker("wait", migrator, timeout=330).stdout.strip()
            if exit_code != "0":
                raise SmokeError("independent migration failed")
            results.append({"check": "independent_migration", "exit_code": 0})
            # No admin, OAuth, provider, payment or monitor fixtures are needed
            # for unauthenticated route isolation. Explicit shared secrets avoid
            # application secret bootstrap; migration does not create users.
            if sql("SELECT (SELECT count(*) FROM accounts) + (SELECT count(*) FROM users);") != "0":
                raise SmokeError("fresh migration unexpectedly created users or accounts")

            slot = "concurrency:account:900001"
            wait = "wait:account:900001"
            now = int(redis_command("TIME").splitlines()[0])
            redis_command("ZADD", slot, str(now), "other-live-gateway:request")
            redis_command("SET", wait, "1", "EX", "600")
            redis_command("ZADD", "concurrency:account:active_index", str(now - 1), "900001")

            def check_live_load():
                if not redis_command("ZSCORE", slot, "other-live-gateway:request") or redis_command("GET", wait) != "1":
                    raise SmokeError("worker removed another live gateway's slot or wait counter")

            launched = {}
            for label in ("gateway-a", "gateway-b", "control-a", "control-b", "worker-a", "worker-b"):
                role = label.split("-")[0]
                env = dict(database, RUNTIME_ROLE=role, RUNTIME_ENABLE_SPLIT_ROLES="true",
                           RUNTIME_MULTI_REPLICA_WORKERS="true" if role == "worker" else "false",
                           DATABASE_MIGRATION_MODE="check", DATABASE_MAX_OPEN_CONNS="8",
                           DATABASE_MAX_IDLE_CONNS="2", REDIS_HOST="redis", REDIS_PORT="6379",
                           REDIS_PASSWORD=redis_password, REDIS_DB="0", REDIS_POOL_SIZE="16",
                           REDIS_MIN_IDLE_CONNS="1", JWT_SECRET=jwt_secret, TOTP_ENCRYPTION_KEY=totp_secret,
                           SERVER_HOST="0.0.0.0", SERVER_PORT="8080", RUN_MODE="standard", AUTO_SETUP="false",
                           DATA_DIR="/app/data", CONFIG_FILE="/app/data/config.yaml",
                           PLUGINS_DATA_DIR="/app/data/plugins", TZ="UTC", LOG_ENV="test", LOG_FORMAT="json",
                           LOG_OUTPUT_TO_FILE="false", RUNTIME_SHUTDOWN_TIMEOUT_SECONDS="10",
                           SECURITY_TRUST_FORWARDED_IP_FOR_API_KEY_ACL="false")
                app_env = env_file(root / (label + ".env"), env)
                # Write config inside private tmpfs before exec, avoiding a bind
                # mount that would require host UID 10001 ownership for each app.
                setup = ('umask 077; printf "%s\\n" \'server:\' \'  mode: release\' '
                         "'pricing:' '  remote_url: \"\"' '  hash_url: \"\"' "
                         "'  data_dir: /app/data/pricing' "
                         "'  fallback_file: /app/resources/model-pricing/model_prices_and_context_window.json' "
                         "'log:' '  output:' '    to_stdout: true' '    to_file: false' "
                         '> /app/data/config.yaml; exec /app/cloud-entrypoint')
                name = start(label, [*hardened, "--env-file", app_env,
                                     "--entrypoint", "/bin/sh"], images["app"], ["-ec", setup])
                port = name  # request() probes container-local HTTP, not a host port.
                launched[label] = (name, port)

                def ready():
                    if docker("inspect", "--format", "{{.State.Running}}", name).stdout.strip() != "true":
                        raise SmokeError(label + " exited before readiness")
                    try:
                        status, body = request(port, "/health")
                        return status == 200 and json.loads(body).get("status") == "ok"
                    except (OSError, ValueError):
                        return False

                wait_for(ready, label + " HTTP health")
                for path, expected in ROUTES[role].items():
                    status, _ = request(port, path)
                    if status != expected:
                        raise SmokeError(f"{label} {path}: expected {expected}, got {status}")
                events = []
                for line in logs(name).splitlines():
                    try:
                        event = json.loads(line)
                        if isinstance(event, dict):
                            events.append(event)
                    except ValueError:
                        pass
                startup = next((e for e in events if e.get("msg") == "experimental_runtime_started"), None)
                if not startup or startup.get("runtime_role") != role:
                    raise SmokeError(label + " did not report runtime ownership")
                tasks = startup.get("runtime_tasks", [])
                if any(task in tasks for task in ("TokenRefreshService", "SchedulerSnapshotService", "ChannelMonitorRunner")):
                    raise SmokeError(label + " started forbidden maintenance")
                if role == "worker":
                    if not any("maintenance task admitted for PostgreSQL coordination" in line and "ConcurrencySlotCleanup" in line
                               for line in logs(name).splitlines()):
                        raise SmokeError(label + " did not admit atomic concurrency cleanup")
                    check_live_load()
                results.append({"instance": label, "role": role, "routes": ROUTES[role]})
                print(f"READY {label}: routes and maintenance ownership verified", flush=True)

            def leaders():
                return [label for label in ("worker-a", "worker-b") if LEADER_MARKER in logs(launched[label][0])]

            wait_for(lambda: bool(leaders()), "initial worker leadership", 20)
            elected = leaders()
            if len(elected) != 1:
                raise SmokeError("expected exactly one initial maintenance leader")
            leader = elected[0]
            standby = "worker-b" if leader == "worker-a" else "worker-a"
            # Freeze the leader before inserting so it cannot pause the fixture
            # in the insert-to-kill gap; only the successor can do that work.
            docker("kill", "--signal", "STOP", launched[leader][0])
            fixture = int(sql("INSERT INTO accounts(name,platform,type,credentials,status,schedulable,auto_pause_on_expired,expires_at) "
                              "VALUES ('image-expired-fixture','openai','oauth','{}','active',true,true,NOW()-INTERVAL '1 minute') RETURNING id;"))
            docker("kill", "--signal", "KILL", launched[leader][0])
            wait_for(lambda: LEADER_MARKER in logs(launched[standby][0]), "standby leadership after SIGKILL", 30)
            wait_for(lambda: sql(f"SELECT schedulable FROM accounts WHERE id={fixture};") == "f",
                     "standby pauses expired fixture", 15)
            check_live_load()
            for label, (name, port) in launched.items():
                if label != leader and request(port, "/health")[0] != 200:
                    raise SmokeError(label + " lost health during takeover")
            results.append({"check": "worker_sigkill_takeover", "leader": leader, "standby": standby,
                            "expired_fixture_paused": True, "live_slot_and_wait_preserved": True})
        except Exception as error:
            if isinstance(error, SmokeError):
                failure = str(error)
            else:
                # Report type and source line only, never exception values/locals.
                trace = error.__traceback__
                while trace.tb_next:
                    trace = trace.tb_next
                failure = f"smoke failed: {type(error).__name__} at line {trace.tb_lineno} (values omitted)"
        finally:
            # Finish cleanup even if the caller sends another interrupt.
            for signum in old_handlers:
                signal.signal(signum, signal.SIG_IGN)
            try:
                if args.keep_logs:
                    args.keep_logs.mkdir(mode=0o700, parents=True)
                    for name in names:
                        text = logs(name)
                        for secret in redactions:
                            text = text.replace(secret, "[REDACTED]")
                        private_file(args.keep_logs / (name + ".log"), text)
            except Exception:
                failure = failure or "could not preserve redacted logs"
            # Remove containers with anonymous volumes, including postgres image
            # declared volumes. No shared host data or named volumes are used.
            for name in reversed(names):
                try:
                    result = docker("rm", "--force", "--volumes", name, check=False, timeout=45)
                    if result.returncode and docker("container", "inspect", name, check=False).returncode == 0:
                        failure = failure or "container cleanup failed: " + name
                except SmokeError:
                    failure = failure or "container cleanup failed: " + name
            if network_created:
                try:
                    if docker("network", "rm", prefix, check=False).returncode:
                        failure = failure or "network cleanup failed: " + prefix
                except SmokeError:
                    failure = failure or "network cleanup failed: " + prefix
            for signum, handler in old_handlers.items():
                signal.signal(signum, handler)
    print(json.dumps({"passed": failure is None, "results": results, "failure": failure,
                      "oauth_sessions_checked": False, "external_provider_calls": 0}, indent=2))
    return 0 if failure is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
