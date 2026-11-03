#!/usr/bin/env python3
"""Explicit opt-in failover drill for the authorized EMPTY AWS integration stack.

No production endpoints are accepted. Runs one service failover at a time, checks
existing role processes plus prewritten sentinel data, and writes non-secret
results. This intentionally interrupts integration DB/cache traffic temporarily.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", choices=("valkey", "rds"), required=True)
    parser.add_argument("--authorize-integration-failover", action="store_true", required=True)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    meta = json.loads(args.metadata.read_text())
    if meta.get("db_identifier") != "sub2api-it-postgres" or meta.get("dbname") != "sub2api_integration_test" or meta.get("account_id") != "250088785444":
        parser.error("only the explicitly named Sub2API integration resources are allowed")
    if args.evidence.exists():
        parser.error("evidence must be a new file")
    settings = {}
    for line in args.env_file.read_text().splitlines():
        key, value = line.split("=", 1)
        settings[key] = json.loads(value)
    if settings.get("DATABASE_DBNAME") != meta["dbname"] or settings.get("DATABASE_SSLMODE") != "verify-full" or settings.get("REDIS_ENABLE_TLS") != "true":
        parser.error("expected integration-only TLS runtime settings")

    def aws(*command):
        result = subprocess.run(["aws", *command, "--region", "us-west-2", "--output", "json"], capture_output=True, text=True, timeout=60)
        if result.returncode:
            raise RuntimeError("AWS command failed: " + " ".join(command[:2]) + ": " + result.stderr[:700])
        return json.loads(result.stdout) if result.stdout.strip() else {}

    identity = aws("sts", "get-caller-identity")
    if ":assumed-role/sub2api-integration-deployer/" not in identity["Arn"]:
        raise RuntimeError("failover must use the dedicated integration role")
    pg_env = dict(os.environ, PGHOST=settings["DATABASE_HOST"], PGPORT="5432", PGDATABASE=meta["dbname"],
                  PGUSER=settings["DATABASE_USER"], PGPASSWORD=settings["DATABASE_PASSWORD"],
                  PGSSLMODE="verify-full", PGSSLROOTCERT=settings["DATABASE_SSLROOTCERT"], PGCONNECT_TIMEOUT="2")

    def sql(statement):
        result = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], input=statement, env=pg_env, capture_output=True, text=True, timeout=6)
        if result.returncode:
            raise RuntimeError("integration PostgreSQL unavailable during drill")
        return result.stdout.strip()

    def redis(*parts):
        host = settings["REDIS_HOST"]
        with ssl.create_default_context().wrap_socket(socket.create_connection((host, 6379), timeout=2), server_hostname=host) as sock:
            stream = sock.makefile("rwb")
            def issue(command):
                encoded = [str(value).encode() for value in command]
                stream.write(b"*"+str(len(encoded)).encode()+b"\r\n"+b"".join(b"$"+str(len(value)).encode()+b"\r\n"+value+b"\r\n" for value in encoded))
                stream.flush()
                header = stream.readline().decode().strip()
                if header.startswith("-"):
                    raise RuntimeError("integration cache temporarily unavailable")
                if header.startswith("$"):
                    size = int(header[1:])
                    if size < 0:
                        return None
                    value = stream.read(size).decode(); stream.read(2)
                    return value
                return header[1:]
            if issue(("AUTH", settings["REDIS_USERNAME"], settings["REDIS_PASSWORD"])) != "OK":
                raise RuntimeError("cache authentication failed")
            return issue(parts)

    def roles_healthy():
        for port in (18080, 18081, 18082, 18083):
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2) as response:
                if response.status != 200:
                    return False
        return True

    run_id = secrets.token_hex(8)
    sentinel = "integration-failover-"+run_id
    user_id = None
    key = "sub2api:integration:failover:"+run_id
    baseline = None
    if args.service == "valkey":
        group = aws("elasticache", "describe-replication-groups", "--replication-group-id", "sub2api-it-valkey")["ReplicationGroups"][0]
        if group["Status"] != "available":
            raise RuntimeError("cache is not stable before failover")
        baseline = next(m["CacheClusterId"] for m in group["NodeGroups"][0]["NodeGroupMembers"] if m["CurrentRole"] == "primary")
        if redis("SET", key, sentinel, "EX", 1800) != "OK":
            raise RuntimeError("could not write pre-failover cache sentinel")
        # Best-effort read via replica endpoint confirms this particular sentinel
        # was replicated. This does not upgrade asynchronous replication to RPO0.
        replica = group["NodeGroups"][0]["ReaderEndpoint"]["Address"]
        original = settings["REDIS_HOST"]
        settings["REDIS_HOST"] = replica
        try:
            if redis("GET", key) != sentinel:
                raise RuntimeError("cache sentinel was not visible on the replica before drill")
        finally:
            settings["REDIS_HOST"] = original
    else:
        db = aws("rds", "describe-db-instances", "--db-instance-identifier", "sub2api-it-postgres")["DBInstances"][0]
        if db["DBInstanceStatus"] != "available" or not db["MultiAZ"]:
            raise RuntimeError("RDS is not stable Multi-AZ before drill")
        baseline = db["AvailabilityZone"]
        user_id = sql(f"INSERT INTO users(email,password_hash,balance) VALUES ('{run_id}@failover.invalid','not-a-usable-login',7.5) RETURNING id;").splitlines()[0]
    started = time.monotonic()
    requested_at = datetime.now(timezone.utc)
    print("Starting dedicated integration", args.service, "failover; baseline:", baseline, flush=True)
    if args.service == "valkey":
        aws("elasticache", "test-failover", "--replication-group-id", "sub2api-it-valkey", "--node-group-id", "0001")
    else:
        aws("rds", "reboot-db-instance", "--db-instance-identifier", "sub2api-it-postgres", "--force-failover")
    errors = 0
    samples = []
    recovered = False
    try:
        deadline = started+900
        while time.monotonic() < deadline:
            elapsed = round(time.monotonic()-started, 2)
            ok = False
            try:
                if args.service == "valkey":
                    ok = redis("GET", key) == sentinel
                    state = aws("elasticache", "describe-replication-groups", "--replication-group-id", "sub2api-it-valkey")["ReplicationGroups"][0]
                    current = next(m["CacheClusterId"] for m in state["NodeGroups"][0]["NodeGroupMembers"] if m["CurrentRole"] == "primary")
                    stable = state["Status"] == "available"
                    completed = current != baseline
                else:
                    ok = sql(f"SELECT balance FROM users WHERE id={int(user_id)};") == "7.50000000"
                    state = aws("rds", "describe-db-instances", "--db-instance-identifier", "sub2api-it-postgres")["DBInstances"][0]
                    current = state["AvailabilityZone"]
                    stable = state["DBInstanceStatus"] == "available" and state["MultiAZ"]
                    events = aws("rds", "describe-events", "--source-type", "db-instance",
                                 "--source-identifier", "sub2api-it-postgres", "--duration", "30")["Events"]
                    completed_events = [event for event in events
                                        if "failover completed" in event.get("Message", "").lower()
                                        and datetime.fromisoformat(event["Date"].replace("Z", "+00:00")) >= requested_at]
                    # AvailabilityZone metadata can lag or remain unchanged.
                    # A post-request service completion event plus committed-data
                    # verification is stronger evidence than requiring AZ text.
                    completed = bool(completed_events)
                if ok and stable and completed and roles_healthy():
                    recovered = True
                    samples.append({"seconds": elapsed, "data_reachable": True, "reported_primary": current, "stable": True,
                                    "completion_basis": "primary_role_changed" if args.service == "valkey" else "aws_post_request_failover_completed_event"})
                    break
            except Exception:
                errors += 1
            samples.append({"seconds": elapsed, "data_reachable": ok})
            time.sleep(10)
        evidence = {"service": args.service, "baseline_primary": baseline, "recovered": recovered,
                    "observed_seconds": round(time.monotonic()-started, 2), "failed_probe_count": errors, "samples": samples,
                    "note": "Controlled integration sample only, not an availability SLA or a guarantee of zero cache data loss."}
        args.evidence.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(evidence, indent=2)); args.evidence.chmod(0o600)
        print(json.dumps(evidence, indent=2))
        if not recovered:
            raise RuntimeError("failover not recovered within the drill deadline; retain evidence and investigate")
    finally:
        if args.service == "valkey":
            try: redis("DEL", key)
            except Exception: pass
        elif user_id is not None:
            try: sql(f"DELETE FROM users WHERE id={int(user_id)};")
            except Exception: pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
