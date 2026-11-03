#!/usr/bin/env python3
"""Integration-only control/Gateway business checks with no model or payment calls.

Creates temporary user/group/redeem fixtures in the named RDS test database, logs
in through the real control API, redeems balance and a subscription, creates a key,
verifies both gateways, and revokes the key through control. Credentials stay in
memory; evidence contains only check names/status. Fixtures are deleted finally.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    if args.evidence.exists():
        parser.error("evidence must be a new file")
    values = {key: json.loads(value) for key, value in (line.split("=", 1) for line in args.env_file.read_text().splitlines())}
    if values.get("DATABASE_DBNAME") != "sub2api_integration_test" or values.get("DATABASE_USER") != "sub2api_it_runtime" or values.get("DATABASE_SSLMODE") != "verify-full":
        parser.error("only the dedicated TLS integration runtime is accepted")
    pg = dict(os.environ, PGHOST=values["DATABASE_HOST"], PGPORT="5432", PGDATABASE=values["DATABASE_DBNAME"],
              PGUSER=values["DATABASE_USER"], PGPASSWORD=values["DATABASE_PASSWORD"],
              PGSSLMODE="verify-full", PGSSLROOTCERT=values["DATABASE_SSLROOTCERT"], PGCONNECT_TIMEOUT="5")

    def sql(statement):
        result = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1"], input=statement, env=pg, capture_output=True, text=True, timeout=20)
        if result.returncode:
            raise RuntimeError("integration SQL failed; statement/credentials withheld")
        return result.stdout.strip()

    def http(port, path, method="GET", payload=None, token=None, idempotency=None):
        body = json.dumps(payload).encode() if payload is not None else None
        req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=body, method=method,
                                     headers={"Content-Type": "application/json"})
        if token:
            req.add_header("Authorization", "Bearer "+token)
        if idempotency:
            req.add_header("Idempotency-Key", idempotency)
        try:
            with urllib.request.urlopen(req, timeout=15) as response:
                return response.status, json.loads(response.read()), dict(response.headers)
        except urllib.error.HTTPError as error:
            try: data = json.loads(error.read())
            except ValueError: data = {}
            return error.code, data, dict(error.headers)

    run_id = secrets.token_hex(8)
    email = run_id+"@business.integration.invalid"
    password = secrets.token_hex(24)
    # Use the application's Go bcrypt helper through an operational tiny driver
    # rather than creating a login bypass or altering authentication middleware.
    helper = Path("/root/.local/state/sub2api-cloud/aws-integration/hash-password.go")
    helper.write_text('package main\nimport("fmt";"io";"os";"golang.org/x/crypto/bcrypt")\nfunc main(){b,_:=io.ReadAll(os.Stdin);h,e:=bcrypt.GenerateFromPassword(b,bcrypt.DefaultCost);if e!=nil{panic(e)};fmt.Print(string(h))}\n')
    helper.chmod(0o600)
    env = dict(os.environ, GOMAXPROCS="2", GOMEMLIMIT="1400MiB", GOGC="60", GOTOOLCHAIN="local")
    result = subprocess.run(["/root/.local/bin/go", "run", str(helper)], cwd="/root/ai/sub2api-cloud/backend", input=password, capture_output=True, text=True, env=env, timeout=60)
    if result.returncode:
        raise RuntimeError("fixture password hash generation failed")
    hashed = result.stdout
    checks = []
    user_id = standard_id = subscription_id = None
    codes = [secrets.token_hex(12), secrets.token_hex(12)]
    key_id = None
    login_token = None
    try:
        user_id = int(sql(f"INSERT INTO users(email,password_hash,balance) VALUES ('{email}','{hashed}',0) RETURNING id;"))
        standard_id = int(sql(f"INSERT INTO groups(name,platform) VALUES ('business-{run_id}','openai') RETURNING id;"))
        subscription_id = int(sql(f"INSERT INTO groups(name,platform,subscription_type,daily_limit_usd,weekly_limit_usd,monthly_limit_usd) VALUES ('subscription-{run_id}','openai','subscription',5,20,50) RETURNING id;"))
        sql(f"INSERT INTO redeem_codes(code,type,value) VALUES ('{codes[0]}','balance',5); INSERT INTO redeem_codes(code,type,value,group_id,validity_days) VALUES ('{codes[1]}','subscription',0,{subscription_id},7);")
        status, data, _ = http(18080, "/api/v1/auth/login", "POST", {"email": email, "password": password})
        if status != 200 or not data.get("data", {}).get("access_token"):
            raise RuntimeError(f"real control login failed: HTTP {status}, reason={data.get('reason')}")
        login_token = data["data"]["access_token"]
        checks.append("control_login")
        for code, kind in zip(codes, ("balance", "subscription")):
            status, data, _ = http(18080, "/api/v1/redeem", "POST", {"code": code}, login_token)
            if status != 200:
                raise RuntimeError(f"{kind} redeem failed: HTTP {status}, reason={data.get('reason')}")
            checks.append(kind+"_redeem")
        if sql(f"SELECT balance FROM users WHERE id={user_id};") != "5.00000000":
            raise RuntimeError("balance redemption did not credit exactly once")
        status, _, _ = http(18080, "/api/v1/redeem", "POST", {"code": codes[0]}, login_token)
        if status == 200 or sql(f"SELECT balance FROM users WHERE id={user_id};") != "5.00000000":
            raise RuntimeError("replayed redemption changed balance")
        checks.append("redemption_replay_rejected")
        status, data, _ = http(18080, "/api/v1/subscriptions/active", token=login_token)
        if status != 200 or not any(entry.get("group_id") == subscription_id for entry in data.get("data", [])):
            raise RuntimeError("redeemed subscription not visible through real control API")
        checks.append("subscription_visible")
        payload = {"name": "business-integration", "group_id": standard_id, "quota": 2}
        idem = "integration-create-"+run_id
        status, data, _ = http(18080, "/api/v1/keys", "POST", payload, login_token, idem)
        if status != 200:
            raise RuntimeError(f"control key creation failed: HTTP {status}, reason={data.get('reason')}")
        key = data["data"]["key"]; key_id = data["data"]["id"]
        status, replay, _ = http(18080, "/api/v1/keys", "POST", payload, login_token, idem)
        if status != 200 or replay.get("data", {}).get("id") != key_id:
            raise RuntimeError("key creation idempotency failed")
        checks.append("control_key_create_idempotency")
        for port in (18081, 18082):
            status, _, _ = http(port, "/v1/usage", token=key)
            if status != 200:
                raise RuntimeError(f"gateway {port} shared key failed: HTTP {status}")
        checks.append("both_gateways_shared_key")
        status, data, _ = http(18080, f"/api/v1/keys/{key_id}", "PUT", {"status": "inactive"}, login_token)
        if status != 200:
            raise RuntimeError(f"control key disable failed: HTTP {status}")
        deadline = time.monotonic()+10
        while True:
            statuses = [http(port, "/v1/usage", token=key)[0] for port in (18081, 18082)]
            if all(status in (401,403) for status in statuses):
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("key disable did not converge to both gateway L1 caches within 10 seconds")
            time.sleep(0.1)
        checks.append("control_revocation_reaches_both_gateways")
        print(json.dumps({"passed": True, "checks": checks, "model_calls": 0, "payment_provider_calls": 0}, indent=2))
        args.evidence.write_text(json.dumps({"passed": True, "checks": checks, "model_calls": 0, "payment_provider_calls": 0}, indent=2))
        args.evidence.chmod(0o600)
    finally:
        # Only fixtures created by this invocation; preserve unrelated rows.
        if user_id is not None:
            sql(f"DELETE FROM redeem_codes WHERE used_by={user_id} OR code IN ('{codes[0]}','{codes[1]}'); DELETE FROM user_subscriptions WHERE user_id={user_id}; DELETE FROM api_keys WHERE user_id={user_id}; DELETE FROM users WHERE id={user_id};")
        for group_id in (standard_id, subscription_id):
            if group_id is not None:
                sql(f"DELETE FROM groups WHERE id={group_id};")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
