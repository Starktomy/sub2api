#!/usr/bin/env python3
"""Bounded, explicitly authorized OAuth smoke against the named integration stack.

Never prints credentials or raw upstream bodies. Uses normal control login,
compliance acceptance (requires independent user consent), and official import.
Leaves disabled fixtures and financial records for review; never deletes usage.
"""
import argparse
import datetime
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
    parser.add_argument('--accounts', type=Path, required=True)
    parser.add_argument('--env-file', type=Path, required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--authorize-real-calls', action='store_true', required=True)
    parser.add_argument('--user-accepted-compliance', action='store_true', required=True)
    args = parser.parse_args()
    if args.evidence_dir.exists():
        parser.error('evidence directory must be new')
    args.evidence_dir.mkdir(mode=0o700, parents=True)
    settings = {k: json.loads(v) for k, v in (s.split('=', 1) for s in args.env_file.read_text().splitlines())}
    if settings.get('DATABASE_DBNAME') != 'sub2api_integration_test' or settings.get('DATABASE_SSLMODE') != 'verify-full':
        parser.error('only dedicated TLS integration database allowed')
    entries = [json.loads(line) for line in args.accounts.read_text().splitlines() if line.strip()]
    if len(entries) != 2 or not all(x.get('access_token') for x in entries):
        parser.error('expected exactly two authorized OAuth entries')
    pg = dict(os.environ, PGHOST=settings['DATABASE_HOST'], PGPORT='5432',
              PGDATABASE=settings['DATABASE_DBNAME'], PGUSER=settings['DATABASE_USER'],
              PGPASSWORD=settings['DATABASE_PASSWORD'], PGSSLMODE='verify-full',
              PGSSLROOTCERT=settings['DATABASE_SSLROOTCERT'], PGCONNECT_TIMEOUT='5')

    def sql(statement):
        result = subprocess.run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1'], input=statement,
                                env=pg, capture_output=True, text=True, timeout=20)
        if result.returncode:
            raise RuntimeError('integration SQL failed; details withheld')
        return result.stdout.strip()

    def http(port, path, method='GET', payload=None, token=None, stream=False):
        req = urllib.request.Request('http://127.0.0.1:'+str(port)+path,
            data=json.dumps(payload).encode() if payload is not None else None,
            method=method, headers={'Content-Type': 'application/json',
                                    'User-Agent': 'sub2api-authorized-integration-smoke/1'})
        if token:
            req.add_header('Authorization', 'Bearer '+token)
        try:
            with urllib.request.urlopen(req, timeout=90) as response:
                raw = response.read(2_000_000)
                if stream:
                    return response.status, raw.decode('utf-8', errors='replace')
                return response.status, json.loads(raw)
        except urllib.error.HTTPError as error:
            # Error bodies may contain upstream diagnostics: keep in memory only.
            raw = error.read(65536)
            try:
                return error.code, json.loads(raw)
            except ValueError:
                return error.code, {}

    def usage_summary(usage):
        if not isinstance(usage, dict):
            return None
        return {key: usage[key] for key in ('input_tokens', 'output_tokens', 'total_tokens')
                if isinstance(usage.get(key), int)}

    run = secrets.token_hex(6)
    admin_id = user_id = None
    accounts, keys, groups = [], [], []
    report = {'run_id': run, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'model': 'gpt-5.6-terra', 'generation_http_requests': 0,
              'generation_request_cap': 2, 'checks': [], 'passed': False}
    evidence = args.evidence_dir/'result.json'

    def save():
        evidence.write_text(json.dumps(report, indent=2))
        evidence.chmod(0o600)

    def require(status, data, stage):
        if status != 200:
            # Only local status and a fixed stage are emitted, never raw errors.
            raise RuntimeError(stage+' failed: HTTP '+str(status))
        return data.get('data', {})

    try:
        if sql('SELECT count(*) FROM accounts;') != '0' or sql('SELECT count(*) FROM users;') != '0':
            raise RuntimeError('requires initial empty integration accounts/users')
        password = secrets.token_hex(24)
        helper = args.evidence_dir/'hash-password.go'
        helper.write_text('package main\nimport("fmt";"io";"os";"golang.org/x/crypto/bcrypt")\nfunc main(){b,_:=io.ReadAll(os.Stdin);h,e:=bcrypt.GenerateFromPassword(b,bcrypt.DefaultCost);if e!=nil{panic(e)};fmt.Print(string(h))}\n')
        helper.chmod(0o600)
        result = subprocess.run(['/root/.local/bin/go', 'run', str(helper)],
            cwd=Path(__file__).resolve().parents[1]/'backend', input=password,
            capture_output=True, text=True, timeout=60,
            env=dict(os.environ, GOMAXPROCS='2', GOMEMLIMIT='1400MiB', GOTOOLCHAIN='local'))
        if result.returncode:
            raise RuntimeError('password hash generation failed')
        hashed = result.stdout
        admin_email = 'oauth-admin-'+run+'@integration.invalid'
        user_email = 'oauth-user-'+run+'@integration.invalid'
        admin_id = int(sql(f"INSERT INTO users(email,password_hash,role,balance) VALUES ('{admin_email}','{hashed}','admin',0) RETURNING id;"))
        user_id = int(sql(f"INSERT INTO users(email,password_hash,balance) VALUES ('{user_email}','{hashed}',1) RETURNING id;"))
        status, data = http(18080, '/api/v1/auth/login', 'POST', {'email': admin_email, 'password': password})
        admin_token = require(status, data, 'admin login')['access_token']
        status, data = http(18080, '/api/v1/admin/compliance/accept', 'POST',
            {'phrase': '我已阅读、理解并同意 Sub2API 部署与运营合规承诺', 'language': 'zh'}, admin_token)
        if require(status, data, 'authorized compliance acceptance').get('required', True):
            raise RuntimeError('compliance acceptance not recorded')
        report['checks'].append('user_authorized_compliance_recorded')
        for i, entry in enumerate(entries):
            group = int(sql(f"INSERT INTO groups(name,platform) VALUES ('oauth-{run}-{i+1}','openai') RETURNING id;"))
            groups.append(group)
            # Import only authentication metadata; never password, phone or 2FA.
            filtered = {k: entry[k] for k in ('access_token', 'refresh_token', 'id_token', 'chatgpt_account_id', 'email') if entry.get(k)}
            status, data = http(18080, '/api/v1/admin/accounts/import/codex-session', 'POST',
                {'content': json.dumps(filtered), 'name': 'oauth-integration-'+run+'-'+str(i+1),
                 'group_ids': [group], 'concurrency': 1, 'priority': 50,
                 'skip_default_group_bind': True, 'update_existing': False,
                 'credential_extras': {'model_mapping': {'gpt-5.6-terra': 'gpt-5.6-terra'}}}, admin_token)
            imported = require(status, data, 'official OAuth import')
            for item in imported.get('items', []):
                if item.get('action') == 'created' and item.get('account_id'):
                    accounts.append(int(item['account_id']))
            if imported.get('created') != 1 or imported.get('failed') or len(accounts) != i+1:
                raise RuntimeError('OAuth import did not create exactly one distinct account')
        report['checks'].append('two_distinct_accounts_official_import')
        report['fixture_ids'] = {'admin': admin_id, 'user': user_id, 'accounts': accounts, 'groups': groups}
        save()
        status, data = http(18080, '/api/v1/auth/login', 'POST', {'email': user_email, 'password': password})
        user_token = require(status, data, 'customer login')['access_token']
        for group in groups:
            status, data = http(18080, '/api/v1/keys', 'POST',
                {'name': 'oauth-smoke-'+run, 'group_id': group, 'quota': 0.25}, user_token)
            created = require(status, data, 'test key creation')
            keys.append((int(created['id']), created['key']))
        report['fixture_ids']['keys'] = [x[0] for x in keys]
        save()
        for i, (key_id, key) in enumerate(keys):
            streaming = i == 1
            payload = {'model': report['model'], 'input': 'Reply with exactly OK. Do not use tools.',
                       'instructions': 'This is an authorized minimal integration test. Reply briefly.',
                       'stream': streaming, 'store': False, 'max_output_tokens': 64,
                       'reasoning': {'effort': 'low'}}
            report['generation_http_requests'] += 1
            save()
            status, data = http(18081+i, '/v1/responses', 'POST', payload, key, stream=streaming)
            result = {'account_index': i+1, 'gateway_port': 18081+i, 'stream': streaming, 'http_status': status}
            if status == 200 and streaming and isinstance(data, str):
                events = []
                for line in data.splitlines():
                    if line.startswith('data: '):
                        try:
                            events.append(json.loads(line[6:]))
                        except ValueError:
                            pass
                completed = [x for x in events if x.get('type') == 'response.completed']
                result['completed'] = bool(completed)
                if completed:
                    response = completed[-1].get('response', {})
                    result['response_model'] = response.get('model')
                    result['usage'] = usage_summary(response.get('usage'))
                result['has_text_delta'] = any(x.get('type') == 'response.output_text.delta' for x in events)
            elif status == 200 and isinstance(data, dict):
                result['completed'] = data.get('status') == 'completed'
                result['response_model'] = data.get('model')
                result['usage'] = usage_summary(data.get('usage'))
                result['has_text_output'] = any(x.get('type') == 'message' for x in data.get('output', []))
            else:
                result['completed'] = False
                # Upstream message text is intentionally not persisted.
                if isinstance(data, dict):
                    err = data.get('error', {})
                    if isinstance(err, dict):
                        code = err.get('code')
                        if isinstance(code, str) and len(code) < 64 and all(c.isalnum() or c in '_-' for c in code):
                            result['error_code'] = code
            report.setdefault('requests', []).append(result)
            save()
            if not result.get('completed'):
                raise RuntimeError('generation did not complete; stop without automatic client retry')
        deadline = time.monotonic()+15
        while True:
            logs = json.loads(sql(f"SELECT coalesce(json_agg(t),'[]') FROM (SELECT account_id,api_key_id,model,upstream_model,input_tokens,output_tokens,total_cost,actual_cost FROM usage_logs WHERE user_id={user_id}) t;"))
            if len(logs) >= 2 or time.monotonic() >= deadline:
                break
            time.sleep(0.25)
        report['usage_logs'] = logs
        if len(logs) != 2 or {x['account_id'] for x in logs} != set(accounts):
            raise RuntimeError('expected exactly two durable usage rows, one per account')
        report['balance_after'] = sql(f'SELECT balance FROM users WHERE id={user_id};')
        report['checks'].extend(['account_1_nonstream', 'account_2_stream', 'both_gateways_real_generation', 'durable_usage_per_account'])
        report['passed'] = True
    except Exception as error:
        # All raised messages above are deliberately credential-free.
        report['failure'] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    finally:
        try:
            if accounts:
                sql('UPDATE accounts SET status=\'disabled\',schedulable=false WHERE id IN ('+','.join(map(str, accounts))+');')
            if user_id:
                sql(f"UPDATE api_keys SET status='inactive' WHERE user_id={user_id}; UPDATE users SET status='disabled' WHERE id={user_id};")
            if admin_id:
                sql(f"UPDATE users SET status='disabled' WHERE id={admin_id};")
            report['fixtures_disabled'] = True
        except Exception:
            report['fixtures_disabled'] = False
        save()
    print(json.dumps(report, indent=2))
    return 0 if report['passed'] and report.get('fixtures_disabled') else 1


if __name__ == '__main__':
    raise SystemExit(main())
