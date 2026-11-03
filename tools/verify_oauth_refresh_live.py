#!/usr/bin/env python3
"""One serial official refresh plus one bounded post-refresh generation."""
import argparse
import datetime
import json
from pathlib import Path
import secrets
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from oauth_integration_support import Integration


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', type=Path, required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--authorize-real-refresh', action='store_true', required=True)
    parser.add_argument('--user-accepted-compliance', action='store_true', required=True)
    args = parser.parse_args()
    if args.evidence_dir.exists():
        parser.error('evidence directory must be new')
    args.evidence_dir.mkdir(mode=0o700, parents=True)
    db = Integration(args.env_file)
    run = secrets.token_hex(6)
    report = {'run_id': run, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'refresh_requests': 0, 'generation_requests': 0, 'passed': False}
    admin_id = user_id = None
    admin_token = None
    evidence = args.evidence_dir/'result.json'

    def save():
        evidence.write_text(json.dumps(report, indent=2)); evidence.chmod(0o600)

    try:
        if db.sql("SELECT count(*) FROM accounts WHERE id IN (1,2) AND status='disabled' AND schedulable=false;") != '2':
            raise RuntimeError('requires both prior accounts disabled')
        password = secrets.token_hex(24)
        hashed = db.hash_password(password, args.evidence_dir)
        admin_email = 'refresh-admin-'+run+'@integration.invalid'
        user_email = 'refresh-user-'+run+'@integration.invalid'
        admin_id = int(db.sql(f"INSERT INTO users(email,password_hash,role) VALUES ('{admin_email}','{hashed}','admin') RETURNING id;"))
        user_id = int(db.sql(f"INSERT INTO users(email,password_hash,balance) VALUES ('{user_email}','{hashed}',1) RETURNING id;"))
        report['fixture_ids'] = {'admin': admin_id, 'user': user_id, 'account': 1}
        admin_token = db.login(admin_email, password)
        status, data = db.http(18080, '/api/v1/admin/compliance/accept', 'POST',
            {'phrase': '我已阅读、理解并同意 Sub2API 部署与运营合规承诺', 'language': 'zh'}, admin_token)
        db.require(status, data, 'authorized compliance acceptance')
        before = json.loads(db.sql('SELECT credentials::text FROM accounts WHERE id=1;'))
        report['refresh_requests'] = 1
        save()
        status, data = db.http(18080, '/api/v1/admin/accounts/1/refresh', 'POST', {}, admin_token)
        after = json.loads(db.sql('SELECT credentials::text FROM accounts WHERE id=1;'))
        # Rotated tokens survive any later assertion failure, private and outside Git.
        recovery = args.evidence_dir/'refreshed-account-recovery.json'
        recovery.write_text(json.dumps({'account_id': 1, 'credentials': after})); recovery.chmod(0o600)
        report['refresh'] = {'http_status': status,
            'access_changed': before.get('access_token') != after.get('access_token'),
            'refresh_changed': before.get('refresh_token') != after.get('refresh_token'),
            'expires_at_present': bool(after.get('expires_at'))}
        response_text = json.dumps(data)
        report['refresh']['new_tokens_absent_from_response'] = not any(
            isinstance(after.get(k), str) and len(after[k]) > 20 and after[k] in response_text
            for k in ('access_token', 'refresh_token', 'id_token'))
        save()
        if status != 200:
            raise RuntimeError('official serial refresh failed: HTTP '+str(status))
        if not report['refresh']['access_changed']:
            raise RuntimeError('refresh returned success without changed persisted access token')
        for path, payload, method in [
            ('accounts/1', {'status': 'active', 'proxy_id': 0}, 'PUT'),
            ('accounts/1/schedulable', {'schedulable': True}, 'POST')]:
            status, data = db.http(18080, '/api/v1/admin/'+path, method, payload, admin_token)
            db.require(status, data, 'enable refreshed account')
        user_token = db.login(user_email, password)
        status, data = db.http(18080, '/api/v1/keys', 'POST',
            {'name': 'refresh-'+run, 'group_id': 41, 'quota': 0.1}, user_token)
        key = db.require(status, data, 'refresh test key creation')['key']
        time.sleep(2)
        report['generation_requests'] = 1
        save()
        status, data = db.http(18081, '/v1/responses', 'POST',
            {'model': 'gpt-5.6-terra', 'input': 'Reply with exactly OK.',
             'instructions': 'Authorized brief integration test.', 'stream': False,
             'store': False, 'max_output_tokens': 64, 'reasoning': {'effort': 'low'}}, key,
             headers={'session_id': 'refresh-'+run})
        report['generation'] = {'http_status': status, 'completed': data.get('status') == 'completed',
            'response_model': data.get('model'), 'usage': {k: data.get('usage', {}).get(k)
                for k in ('input_tokens', 'output_tokens', 'total_tokens')}}
        if status != 200 or not report['generation']['completed']:
            raise RuntimeError('post-refresh generation failed')
        deadline = time.monotonic()+15
        while True:
            logs = json.loads(db.sql(f"SELECT coalesce(json_agg(t),'[]') FROM (SELECT account_id,input_tokens,output_tokens,total_cost,actual_cost FROM usage_logs WHERE user_id={user_id}) t;"))
            if logs or time.monotonic() >= deadline:
                break
            time.sleep(0.2)
        report['usage_logs'] = logs
        if len(logs) != 1 or logs[0]['account_id'] != 1:
            raise RuntimeError('post-refresh durable usage mismatch')
        report['passed'] = report['refresh']['new_tokens_absent_from_response']
    except Exception as error:
        report['failure'] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    finally:
        try:
            if admin_token:
                for i in (1, 2):
                    status, data = db.http(18080, f'/api/v1/admin/accounts/{i}/schedulable',
                        'POST', {'schedulable': False}, admin_token)
                    db.require(status, data, 'disable account after refresh')
                    status, data = db.http(18080, f'/api/v1/admin/accounts/{i}',
                        'PUT', {'status': 'inactive', 'proxy_id': 0}, admin_token)
                    db.require(status, data, 'deactivate account after refresh')
            db.sql("UPDATE accounts SET status='disabled',schedulable=false,proxy_id=NULL WHERE id IN (1,2);")
            if user_id:
                db.sql(f"UPDATE api_keys SET status='inactive' WHERE user_id={user_id}; UPDATE users SET status='disabled' WHERE id={user_id};")
            if admin_id:
                db.sql(f"UPDATE users SET status='disabled' WHERE id={admin_id};")
            report['fixtures_disabled'] = True
        except Exception:
            report['fixtures_disabled'] = False
        save()
    print(json.dumps(report, indent=2))
    return 0 if report['passed'] and report.get('fixtures_disabled') else 1


if __name__ == '__main__':
    raise SystemExit(main())
