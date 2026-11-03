#!/usr/bin/env python3
"""Launch real cloud-role processes against an explicitly supplied LOCAL test DB.

Run with python3 -I. Never pass a production database: worker startup can execute
maintenance. The script creates a private, password-protected disposable Redis
and private runtime directories. It never provisions AWS or calls a model API.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(port, path):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=2) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--keep-logs", type=Path, help="Copy non-secret application logs to this new directory")
    parser.add_argument("--multi-replica", action="store_true", help="Run two controls and two guarded workers; verify leader termination takeover")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    backend = binary.parent.parent
    if not (backend / "resources/model-pricing/model_prices_and_context_window.json").is_file():
        parser.error("--binary must point to the backend/bin/server build with its resources directory")
    # Deliberately accept only the local fixture's libpq form, not arbitrary
    # remote URLs. No credentials are passed on a subprocess command line.
    dsn = os.environ.get("CLOUD_TEST_DATABASE_DSN", "")
    if not dsn:
        parser.error("CLOUD_TEST_DATABASE_DSN is required; use an isolated LOCAL test database")
    try:
        fields = dict(item.split("=", 1) for item in shlex.split(dsn))
    except ValueError:
        parser.error("CLOUD_TEST_DATABASE_DSN must be a local libpq key=value string")
    if fields.get("host") not in ("127.0.0.1", "localhost", "::1"):
        parser.error("only loopback test PostgreSQL is permitted")
    if not fields.get("dbname", "").endswith("_test"):
        parser.error("the explicitly supplied disposable database name must end with _test")
    for key in ("user", "dbname", "password"):
        if not fields.get(key):
            parser.error(f"local test DSN must provide {key}")
    redis_server = shutil.which("redis-server")
    redis_cli = shutil.which("redis-cli")
    if not redis_server or not redis_cli:
        parser.error("redis-server and redis-cli must be installed locally")
    # A loopback/name suffix alone is not enough: refuse a test database that
    # contains real accounts, active monitors or payment-provider credentials.
    psql = shutil.which("psql")
    if not psql:
        parser.error("psql is required to verify the test database is isolated")
    probe_env = dict(os.environ, PGHOST=fields["host"], PGPORT=fields.get("port", "5432"),
                     PGUSER=fields["user"], PGPASSWORD=fields["password"],
                     PGDATABASE=fields["dbname"], PGSSLMODE=fields.get("sslmode", "disable"))
    probe = subprocess.run([psql, "-X", "-At", "-v", "ON_ERROR_STOP=1", "-c",
                            "SELECT (SELECT count(*) FROM accounts) + (SELECT count(*) FROM channel_monitors) + (SELECT count(*) FROM payment_provider_instances);"],
                           env=probe_env, text=True, capture_output=True, timeout=10)
    if probe.returncode or probe.stdout.strip() != "0":
        parser.error("smoke requires an already migrated disposable database with no accounts, monitors or payment providers")
    if args.keep_logs and args.keep_logs.exists():
        parser.error("--keep-logs must name a new directory, not an existing target")

    processes = []
    results = []
    failed = None
    with tempfile.TemporaryDirectory(prefix="sub2api-role-smoke-") as temporary:
        root = Path(temporary)
        redis_password = secrets.token_hex(32)
        redis_port = unused_port()
        redis_config = root / "redis.conf"
        redis_config.write_text(
            f"bind 127.0.0.1\nport {redis_port}\nprotected-mode yes\n"
            f"requirepass {redis_password}\nsave \"\"\nappendonly no\ndir {root}\n"
        )
        redis_config.chmod(0o600)
        handles = []
        try:
            redis_log = (root / "redis.log").open("w")
            handles.append(redis_log)
            redis = subprocess.Popen([redis_server, str(redis_config)], stdout=redis_log, stderr=subprocess.STDOUT)
            processes.append(("redis", redis))
            deadline = time.monotonic() + 10
            while True:
                ping_env = dict(os.environ, REDISCLI_AUTH=redis_password)
                ping = subprocess.run([redis_cli, "-h", "127.0.0.1", "-p", str(redis_port), "PING"], env=ping_env, capture_output=True)
                if ping.returncode == 0 and ping.stdout.strip() == b"PONG":
                    break
                if redis.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError("disposable Redis did not become ready")
                time.sleep(0.1)

            def redis_command(*command):
                reply = subprocess.run([redis_cli, "--raw", "-h", "127.0.0.1", "-p", str(redis_port), *map(str, command)], env=ping_env, text=True, capture_output=True, check=True)
                return reply.stdout.strip()

            # Model another live gateway's slot and wait counters before the
            # maintenance process joins. A worker must not run the legacy
            # single-process startup sweep and delete them.
            slot_key = "concurrency:account:900001"
            wait_key = "wait:account:900001"
            now = int(redis_command("TIME").splitlines()[0])
            redis_command("ZADD", slot_key, now, "other-live-gateway:request")
            redis_command("SET", wait_key, 1, "EX", 600)
            redis_command("ZADD", "concurrency:account:active_index", now + 900, 900001)

            jwt_secret, totp_secret = secrets.token_hex(32), secrets.token_hex(32)
            instances = (("gateway-a", "gateway"), ("gateway-b", "gateway"), ("control", "control"), ("worker", "worker"))
            if args.multi_replica:
                instances = (("gateway-a", "gateway"), ("gateway-b", "gateway"), ("control-a", "control"), ("control-b", "control"), ("worker-a", "worker"), ("worker-b", "worker"))
            launched = {}
            expected_terminated = set()
            for instance, role in instances:
                data = root / instance
                data.mkdir()
                port = unused_port()
                config_file = data / "config.yaml"
                config_file.write_text(
                    "server:\n  host: 127.0.0.1\n  mode: release\n"
                    "pricing:\n  remote_url: ''\n  hash_url: ''\n"
                    f"  data_dir: {json.dumps(str(data / 'pricing'))}\n"
                    f"  fallback_file: {json.dumps(str(backend / 'resources/model-pricing/model_prices_and_context_window.json'))}\n"
                    "log:\n  output:\n    to_stdout: true\n    to_file: false\n"
                )
                env = dict(os.environ)
                env.update({
                    "CONFIG_FILE": str(config_file), "DATA_DIR": str(data), "TZ": "UTC",
                    "RUNTIME_ROLE": role, "RUNTIME_ENABLE_SPLIT_ROLES": "true",
                    "RUNTIME_MULTI_REPLICA_WORKERS": "true" if args.multi_replica and role == "worker" else "false",
                    "RUNTIME_SHUTDOWN_TIMEOUT_SECONDS": "10", "RUN_MODE": "standard",
                    "DATABASE_HOST": fields["host"], "DATABASE_PORT": fields.get("port", "5432"),
                    "DATABASE_USER": fields["user"], "DATABASE_PASSWORD": fields["password"],
                    "DATABASE_DBNAME": fields["dbname"], "DATABASE_SSLMODE": fields.get("sslmode", "disable"),
                    "DATABASE_MIGRATION_MODE": "check", "DATABASE_MAX_OPEN_CONNS": "8", "DATABASE_MAX_IDLE_CONNS": "2",
                    "REDIS_HOST": "127.0.0.1", "REDIS_PORT": str(redis_port), "REDIS_PASSWORD": redis_password,
                    "REDIS_DB": "0", "REDIS_POOL_SIZE": "16", "REDIS_MIN_IDLE_CONNS": "1",
                    "JWT_SECRET": jwt_secret, "TOTP_ENCRYPTION_KEY": totp_secret,
                    "SERVER_PORT": str(port), "LOG_OUTPUT_TO_FILE": "false",
                    "PLUGINS_DATA_DIR": str(data / "plugins"), "LOG_ENV": "test", "LOG_FORMAT": "json",
                    "SECURITY_TRUST_FORWARDED_IP_FOR_API_KEY_ACL": "false",
                })
                log_path = root / f"{instance}.log"
                log = log_path.open("w")
                handles.append(log)
                process = subprocess.Popen([str(binary)], cwd=backend, env=env, stdout=log, stderr=subprocess.STDOUT)
                processes.append((instance, process))
                launched[instance] = {"process": process, "port": port, "log": log_path, "env": env}
                deadline = time.monotonic() + 45
                while True:
                    if process.poll() is not None:
                        raise RuntimeError(f"{instance} exited before readiness (code {process.returncode})")
                    try:
                        status, body = request(port, "/health")
                        if status == 200 and json.loads(body).get("status") == "ok":
                            break
                    except (OSError, ValueError):
                        pass
                    if time.monotonic() > deadline:
                        raise RuntimeError(f"{instance} did not become ready within 45 seconds")
                    time.sleep(0.1)
                expected = {
                    "gateway": {"/v1/models": 401, "/api/v1/auth/me": 404, "/setup/status": 404},
                    "control": {"/v1/models": 404, "/api/v1/auth/me": 401, "/setup/status": 200},
                    "worker": {"/v1/models": 404, "/api/v1/auth/me": 404, "/setup/status": 404},
                }[role]
                checks = {}
                for path, want in expected.items():
                    status, _ = request(port, path)
                    if status != want:
                        raise RuntimeError(f"{instance} {path}: expected {want}, got {status}")
                    checks[path] = status
                text = log_path.read_text()
                events = []
                for line in text.splitlines():
                    try:
                        events.append(json.loads(line))
                    except ValueError:
                        pass
                startup = next((event for event in events if event.get("msg") == "experimental_runtime_started"), None)
                if startup is None or startup.get("runtime_role") != role:
                    raise RuntimeError(f"{instance} did not report its task ownership")
                tasks = startup["runtime_tasks"]
                for maintenance in ("TokenRefreshService", "SchedulerSnapshotService", "ChannelMonitorRunner"):
                    want_maintenance = role == "worker" and not args.multi_replica
                    if (maintenance in tasks) != want_maintenance:
                        raise RuntimeError(f"{instance} has incorrect maintenance ownership: {maintenance}")
                if role == "worker":
                    if redis_command("ZSCORE", slot_key, "other-live-gateway:request") == "":
                        raise RuntimeError("worker startup deleted another live gateway's concurrency slot")
                    if redis_command("GET", wait_key) != "1":
                        raise RuntimeError("worker startup deleted another live gateway's wait counter")
                results.append({"instance": instance, "role": role, "routes": checks, "started_task_count": len(tasks)})
                print(f"READY {instance}: HTTP routes and maintenance ownership verified", flush=True)
            if args.multi_replica:
                marker = "maintenance PostgreSQL session leadership acquired"
                def leaders():
                    return [name for name in ("worker-a", "worker-b")
                            if marker in launched[name]["log"].read_text()]
                deadline = time.monotonic() + 15
                while not leaders():
                    if time.monotonic() >= deadline:
                        raise RuntimeError("no maintenance leader acquired PostgreSQL session lock")
                    time.sleep(0.1)
                if len(leaders()) != 1:
                    raise RuntimeError("both workers reported initial leadership")
                leader = leaders()[0]
                standby = "worker-b" if leader == "worker-a" else "worker-a"
                fixture = subprocess.run([psql, "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], env=probe_env,
                    input="INSERT INTO accounts(name,platform,type,credentials,status,schedulable,auto_pause_on_expired,expires_at) VALUES ('replica-expired-fixture','openai','oauth','{}','active',true,true,NOW()-INTERVAL '1 minute') RETURNING id;",
                    capture_output=True, text=True, check=True, timeout=10)
                expired_id = int(fixture.stdout.strip())
                launched[leader]["process"].kill()
                launched[leader]["process"].wait(timeout=10)
                expected_terminated.add(leader)
                deadline = time.monotonic() + 20
                while marker not in launched[standby]["log"].read_text():
                    if launched[standby]["process"].poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError("standby worker failed to acquire after leader process death")
                    time.sleep(0.1)
                deadline = time.monotonic() + 10
                while True:
                    paused = subprocess.run([psql, "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], env=probe_env,
                        input=f"SELECT schedulable FROM accounts WHERE id={expired_id};", capture_output=True, text=True, check=True, timeout=10)
                    if paused.stdout.strip() == "f":
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError("standby acquired lock but did not pause expired fixture account")
                    time.sleep(0.1)
                subprocess.run([psql, "-X", "-q", "-v", "ON_ERROR_STOP=1"], env=probe_env,
                    input=f"DELETE FROM accounts WHERE id={expired_id};", capture_output=True, text=True, check=True, timeout=10)
                results.append({"check": "maintenance_leader_killed_and_standby_acquired", "leader": leader, "standby": standby, "expired_fixture_paused": True})
                # Controls are separate healthy processes. Provider callback
                # semantics are covered with fake-provider shared-store tests.
                # Here exercise real HTTP creation on A and state validation on
                # B, then after B restart, without any provider network call.
                def http_json(port, path, payload, token=None):
                    headers = {"Content-Type": "application/json"}
                    if token:
                        headers["Authorization"] = "Bearer " + token
                    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=json.dumps(payload).encode(), headers=headers)
                    try:
                        with urllib.request.urlopen(req, timeout=10) as response:
                            return response.status, json.loads(response.read())
                    except urllib.error.HTTPError as error:
                        return error.code, json.loads(error.read())
                password = secrets.token_hex(24)
                helper = root / "hash-password.go"
                helper.write_text('package main\nimport("fmt";"io";"os";"golang.org/x/crypto/bcrypt")\nfunc main(){b,_:=io.ReadAll(os.Stdin);h,e:=bcrypt.GenerateFromPassword(b,bcrypt.DefaultCost);if e!=nil{panic(e)};fmt.Print(string(h))}\n')
                hashed = subprocess.run(["go", "run", str(helper)], cwd=backend, input=password,
                    env=dict(os.environ, GOMAXPROCS="2", GOMEMLIMIT="1200MiB"), capture_output=True, text=True, check=True, timeout=90).stdout
                email = "replica-"+secrets.token_hex(8)+"@integration.invalid"
                fixture = subprocess.run([psql, "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], env=probe_env,
                    input=f"INSERT INTO users(email,password_hash,role) VALUES ('{email}','{hashed}','admin') RETURNING id;", capture_output=True, text=True, check=True, timeout=10)
                admin_id = int(fixture.stdout.strip())
                try:
                    status, data = http_json(launched["control-a"]["port"], "/api/v1/auth/login", {"email":email,"password":password})
                    if status != 200:
                        raise RuntimeError("control A real admin login failed")
                    token = data["data"]["access_token"]
                    status, _ = http_json(launched["control-a"]["port"], "/api/v1/admin/compliance/accept",
                        {"phrase":"我已阅读、理解并同意 Sub2API 部署与运营合规承诺","language":"zh"}, token)
                    if status != 200:
                        raise RuntimeError("disposable automated fixture compliance acceptance failed")
                    status, data = http_json(launched["control-a"]["port"], "/api/v1/admin/openai/generate-auth-url", {}, token)
                    if status != 200:
                        raise RuntimeError("control A authorization session creation failed")
                    session_id = data["data"]["session_id"]
                    payload = {"session_id":session_id,"code":"never-send-to-provider","state":"deliberately-wrong-state"}
                    status, data = http_json(launched["control-b"]["port"], "/api/v1/admin/openai/exchange-code", payload, token)
                    if status != 400 or data.get("reason") != "OPENAI_OAUTH_INVALID_STATE":
                        raise RuntimeError("control B did not see A session before state validation")
                    old = launched["control-b"]["process"]
                    old.terminate()
                    if old.wait(timeout=30) != 0:
                        raise RuntimeError("control B failed graceful restart")
                    restart_log = launched["control-b"]["log"].open("a")
                    handles.append(restart_log)
                    restarted = subprocess.Popen([str(binary)], cwd=backend, env=launched["control-b"]["env"], stdout=restart_log, stderr=subprocess.STDOUT)
                    processes.append(("control-b-restarted", restarted))
                    launched["control-b"]["process"] = restarted
                    deadline = time.monotonic()+30
                    while True:
                        try:
                            if request(launched["control-b"]["port"], "/health")[0] == 200:
                                break
                        except OSError:
                            pass
                        if restarted.poll() is not None or time.monotonic() >= deadline:
                            raise RuntimeError("control B restart failed")
                        time.sleep(0.1)
                    status, data = http_json(launched["control-b"]["port"], "/api/v1/admin/openai/exchange-code", payload, token)
                    if status != 400 or data.get("reason") != "OPENAI_OAUTH_INVALID_STATE":
                        raise RuntimeError("shared OAuth session did not survive control B restart")
                    results.append({"check":"control_A_session_seen_on_B_and_after_B_restart","provider_calls":0})
                finally:
                    subprocess.run([psql,"-X","-q","-v","ON_ERROR_STOP=1"], env=probe_env,
                        input=f"DELETE FROM users WHERE id={admin_id};",capture_output=True,text=True,timeout=10)

                for name in ("control-a", "control-b"):
                    if request(launched[name]["port"], "/health")[0] != 200:
                        raise RuntimeError("control replica lost health during worker takeover")
        except Exception as error:
            failed = str(error)
        finally:
            # Gateway/control stop producing work before the maintenance worker;
            # all application processes exit before the disposable Redis.
            for name, process in processes:
                if name == "redis" or process.poll() is not None:
                    continue
                process.send_signal(signal.SIGTERM)
            for name, process in processes:
                if name == "redis":
                    continue
                try:
                    code = process.wait(timeout=45)
                    if code != 0 and failed is None and name not in locals().get("expected_terminated", set()):
                        failed = f"{name} did not exit cleanly: {code}"
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    if failed is None:
                        failed = f"{name} failed to drain within 45 seconds"
            if processes:
                redis = processes[0][1]
                if redis.poll() is None:
                    redis.terminate()
                    try:
                        redis.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        redis.kill()
                        redis.wait()
            for handle in handles:
                handle.close()
            # Redact any accidental credential logging before exporting evidence.
            redact = [redis_password, fields["password"]]
            if "jwt_secret" in locals():
                redact += [jwt_secret, totp_secret]
            if args.keep_logs:
                args.keep_logs.mkdir(mode=0o700, parents=True)
            for path in root.glob("*.log"):
                text = path.read_text(errors="replace")
                for secret in redact:
                    text = text.replace(secret, "[REDACTED]")
                if args.keep_logs:
                    (args.keep_logs / path.name).write_text(text)
                if failed and path.name != "redis.log":
                    print(f"--- {path.name} (last lines) ---\n" + "\n".join(text.splitlines()[-18:]))
    print(json.dumps({"passed": failed is None, "results": results, "failure": failed}, indent=2))
    return 0 if failed is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
