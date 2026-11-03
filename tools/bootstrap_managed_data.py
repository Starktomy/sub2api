#!/usr/bin/env python3
"""Bootstrap a NEW, explicitly authorized Sub2API AWS integration database.

Run with python3 -I on the integration EC2, using its scoped assumed-role profile.
No usable credentials are printed or stored in the repository. This command does
not create/reset an application administrator; it separates schema owner/migrator
from the application runtime and stores generated credentials in Secrets Manager.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata", type=Path, required=True, help="Private JSON containing RDS/cache endpoints and exact secret ARN")
    parser.add_argument("--output-dir", type=Path, required=True, help="New private operational directory outside the repository")
    parser.add_argument("--migrator", type=Path, required=True)
    parser.add_argument("--ca-file", type=Path, required=True)
    args = parser.parse_args()
    meta = json.loads(args.metadata.read_text())
    if meta.get("account_id") != "250088785444" or meta.get("region") != "us-west-2":
        parser.error("unexpected account/region; this tool is scoped to the authorized integration environment")
    if meta.get("db_identifier") != "sub2api-it-postgres" or meta.get("dbname") != "sub2api_integration_test":
        parser.error("only the named disposable integration database is allowed")
    if not meta["db_host"].endswith(".us-west-2.rds.amazonaws.com"):
        parser.error("expected the actual us-west-2 RDS hostname for verify-full")
    if not meta["cache_host"].endswith(".cache.amazonaws.com"):
        parser.error("expected a private ElastiCache service hostname")
    if args.output_dir.exists():
        parser.error("output directory must be NEW; refusing to rotate existing credentials")
    args.output_dir.mkdir(mode=0o700, parents=True)
    ca = args.ca_file.resolve(strict=True)
    migrator_binary = args.migrator.resolve(strict=True)

    def aws(*command, input_json=None):
        cmd = ["aws", *command, "--region", meta["region"], "--output", "json"]
        if input_json is not None:
            # CLI v2 does not reliably read a file:///dev/stdin argument on this
            # host. A private, immediately removed request file keeps values out
            # of /proc/<pid>/cmdline and repository/history output.
            with tempfile.NamedTemporaryFile("w", dir=args.output_dir, prefix="secret-request-", suffix=".json") as request:
                os.fchmod(request.fileno(), 0o600)
                json.dump(input_json, request)
                request.flush()
                cmd.extend(["--cli-input-json", "file://"+request.name])
                result = subprocess.run(cmd, text=True, capture_output=True, timeout=60)
        else:
            result = subprocess.run(cmd, text=True, capture_output=True, timeout=60)
        if result.returncode:
            raise RuntimeError("AWS operation failed: " + " ".join(command[:2]) + ": " + result.stderr[:1000])
        return json.loads(result.stdout) if result.stdout.strip() else {}

    identity = aws("sts", "get-caller-identity")
    if ":assumed-role/sub2api-integration-deployer/" not in identity["Arn"]:
        raise RuntimeError("refusing database bootstrap without the dedicated non-root deployment role")
    master = json.loads(aws("secretsmanager", "get-secret-value", "--secret-id", meta["master_secret_arn"])["SecretString"])
    pg = dict(os.environ, PGHOST=meta["db_host"], PGPORT="5432", PGDATABASE=meta["dbname"],
              PGUSER=master["username"], PGPASSWORD=master["password"],
              PGSSLMODE="verify-full", PGSSLROOTCERT=str(ca), PGCONNECT_TIMEOUT="10")

    def sql(statement, env=pg):
        result = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], input=statement, env=env, text=True, capture_output=True, timeout=60)
        if result.returncode:
            # PostgreSQL may echo failed SQL with password literals. Do not log it.
            raise RuntimeError("database operation failed (details withheld; review private operational evidence)")
        return result.stdout.strip()

    active_tls = sql("SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid();")
    if active_tls != "t":
        raise RuntimeError("RDS session is not encrypted")
    occupied = sql("SELECT count(*) FROM pg_roles WHERE rolname IN ('sub2api_it_owner','sub2api_it_migrator','sub2api_it_runtime');")
    if occupied != "0":
        raise RuntimeError("integration database roles already exist; refusing reset/rotation")
    mig_password, runtime_password = secrets.token_hex(32), secrets.token_hex(32)
    statement = f"""
BEGIN;
CREATE ROLE sub2api_it_owner NOLOGIN;
CREATE ROLE sub2api_it_migrator LOGIN PASSWORD '{mig_password}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE sub2api_it_runtime LOGIN PASSWORD '{runtime_password}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT sub2api_it_owner TO sub2api_it_migrator;
GRANT sub2api_it_owner TO {master['username']};
ALTER ROLE sub2api_it_migrator SET role = 'sub2api_it_owner';
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO sub2api_it_owner;
GRANT USAGE ON SCHEMA public TO sub2api_it_runtime;
GRANT CONNECT ON DATABASE {meta['dbname']} TO sub2api_it_migrator, sub2api_it_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api_it_owner IN SCHEMA public GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO sub2api_it_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api_it_owner IN SCHEMA public GRANT USAGE,SELECT ON SEQUENCES TO sub2api_it_runtime;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
COMMIT;
"""
    sql(statement)
    credentials = {
        "migrator": {"username": "sub2api_it_migrator", "password": mig_password},
        "runtime": {"username": "sub2api_it_runtime", "password": runtime_password},
        "shared": {"jwt_secret": secrets.token_hex(32), "totp_encryption_key": secrets.token_hex(32)},
    }
    secret_arns = {}
    for purpose, value in credentials.items():
        value = dict(value, host=meta["db_host"], dbname=meta["dbname"], sslmode="verify-full") if purpose != "shared" else value
        reply = aws("secretsmanager", "create-secret", input_json={
            "Name": "sub2api/integration/"+purpose,
            "Description": "Authorized EC2 integration only; never use RDS master as runtime",
            "SecretString": json.dumps(value),
            "Tags": [{"Key": "Project", "Value": "sub2api-cloud"}, {"Key": "Environment", "Value": "integration"}],
        })
        secret_arns[purpose] = reply["ARN"]
    # Save recovery evidence BEFORE applying migrations; private operational
    # storage is outside the repo. Never reset roles on a second invocation.
    private = dict(meta, credentials=credentials, secret_arns=secret_arns)
    path = args.output_dir / "bootstrap.json"
    path.write_text(json.dumps(private, indent=2)); path.chmod(0o600)
    env = dict(os.environ, DATABASE_HOST=meta["db_host"], DATABASE_PORT="5432", DATABASE_DBNAME=meta["dbname"],
               DATABASE_USER="sub2api_it_migrator", DATABASE_PASSWORD=mig_password,
               DATABASE_SSLMODE="verify-full", DATABASE_SSLROOTCERT=str(ca))
    env.pop("DATABASE_DSN", None)
    for command in ([str(migrator_binary), "-timeout", "10m"], [str(migrator_binary), "-check", "-timeout", "30s"]):
        result = subprocess.run(command, env=env, text=True, capture_output=True, timeout=660)
        if result.returncode:
            raise RuntimeError("migration/check failed; roles and generated Secrets remain recoverable in the private output directory")
        print(result.stdout.strip())
    # schema_migrations is readable for validation but never writable by runtime.
    sql("GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO sub2api_it_runtime;\n"
        "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO sub2api_it_runtime;\n"
        "REVOKE INSERT,UPDATE,DELETE,TRUNCATE ON schema_migrations FROM sub2api_it_runtime;\n"
        "REVOKE ALL ON security_secrets FROM sub2api_it_runtime;\n")
    runtime_pg = dict(pg, PGUSER="sub2api_it_runtime", PGPASSWORD=runtime_password)
    if sql("SELECT count(*) FROM schema_migrations;", runtime_pg) != "300":
        raise RuntimeError("unexpected migration manifest count; review migration baseline")
    denied = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], input="CREATE TABLE must_not_create(id bigint);", env=runtime_pg, text=True, capture_output=True, timeout=15)
    if denied.returncode == 0:
        raise RuntimeError("runtime unexpectedly has DDL privileges")
    print("Verified RDS TLS, separate migrator/runtime accounts, 300 baseline migrations, runtime SELECT and DDL rejection. Secret values were not printed.")
    print(json.dumps({"db_host": meta["db_host"], "dbname": meta["dbname"], "secret_arns": secret_arns}, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print("Managed data bootstrap failed:", str(error), file=sys.stderr)
        raise SystemExit(1)
