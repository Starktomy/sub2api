#!/usr/bin/env python3
"""Explicitly authorized two-call smoke for the current primary; no refresh/import.

Uses unchanged stored OAuth credentials and existing account groups. Creates a
bootstrap admin in SQL, a customer and keys via Control HTTP, and retains ledger.
Evidence contains no credentials. Only the named private integration DB is allowed.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import warnings

PRIMARY = '172.31.16.78'
MODEL = 'gpt-5.6-terra'


def expiry_valid(value, now):
    try:
        expires = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
        return expires > now + dt.timedelta(minutes=30)
    except (AttributeError, TypeError, ValueError):
        return False


def publication_ready(publication, manifest_raw, payloads, account_id, group_id):
    """Pure validation; full credential-bearing payloads never leave memory."""
    if hashlib.sha256(manifest_raw).hexdigest() != publication['manifest_digest']:
        return False
    manifest = json.loads(manifest_raw)
    if (manifest.get('Generation') != publication['generation'] or
            manifest.get('ID') != publication['publication_id'] or
            manifest.get('Buckets', {}).get(f'{group_id}:openai:single') != [account_id]):
        return False
    for kind in ('slim', 'full'):
        key = f'{kind}:{account_id}'
        raw = payloads.get(key)
        if raw is None or hashlib.sha256(raw).hexdigest() != manifest.get('Payloads', {}).get(key):
            return False
        account = json.loads(raw)
        if (account.get('ID') != account_id or account.get('Status') != 'active' or
                account.get('Schedulable') is not True or account.get('Platform') != 'openai' or
                group_id not in account.get('GroupIDs', [])):
            return False
        for field in ('RateLimitResetAt', 'OverloadUntil', 'TempUnschedulableUntil'):
            value = account.get(field)
            if value and dt.datetime.fromisoformat(value.replace('Z', '+00:00')) > dt.datetime.now(dt.timezone.utc):
                return False
    return True


class Stack:
    def __init__(self, env_file):
        settings = {}
        for line in env_file.read_text().splitlines():
            if line and not line.startswith('#'):
                key, value = line.split('=', 1)
                settings[key] = value  # Docker env format, deliberately not shell/JSON.
        if (settings.get('DATABASE_DBNAME') != 'sub2api_integration_test' or
                settings.get('DATABASE_USER') != 'sub2api_it_runtime' or
                settings.get('DATABASE_SSLMODE') != 'verify-full'):
            raise RuntimeError('only dedicated TLS integration database accepted')
        self.settings = settings
        self.pg = dict(os.environ, PGHOST=settings['DATABASE_HOST'],
            PGPORT=settings.get('DATABASE_PORT', '5432'),
            PGDATABASE=settings['DATABASE_DBNAME'], PGUSER=settings['DATABASE_USER'],
            PGPASSWORD=settings['DATABASE_PASSWORD'], PGSSLMODE='verify-full',
            PGSSLROOTCERT='/etc/sub2api-roles/rds.pem', PGCONNECT_TIMEOUT='5')

    def sql(self, query):
        result = subprocess.run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1'],
            input=query, env=self.pg, capture_output=True, text=True, timeout=20)
        if result.returncode:
            raise RuntimeError('integration SQL failed; details withheld')
        return result.stdout.strip()

    def http(self, port, path, method='GET', payload=None, token=None):
        if port not in (18080, 18081) or 'refresh' in path or 'import' in path:
            raise RuntimeError('endpoint forbidden')
        request = urllib.request.Request(f'http://{PRIMARY}:{port}{path}', method=method,
            data=None if payload is None else json.dumps(payload).encode(),
            headers={'Content-Type': 'application/json',
                     'User-Agent': 'authorized-ec2-bounded-model-smoke/1'})
        if token:
            request.add_header('Authorization', 'Bearer '+token)
        try:
            with urllib.request.urlopen(request, timeout=90) as response:
                return response.status, json.loads(response.read(2_000_000))
        except urllib.error.HTTPError as error:
            # Never persist/print untrusted upstream error bodies.
            return error.code, {}

    def control(self, path, method='GET', payload=None, token=None):
        status, data = self.http(18081, '/api/v1/'+path, method, payload, token)
        if status != 200:
            raise RuntimeError('control operation failed: HTTP '+str(status))
        return data.get('data', {})

    def healthy(self):
        for port in (18080, 18081):
            status, data = self.http(port, '/health')
            if status != 200 or data.get('status') != 'ok':
                raise RuntimeError('primary unavailable; stop')

    def redis_get(self, key):
        s = self.settings
        command = ['redis-cli', '--no-auth-warning', '--tls', '--raw',
            '-h', s['REDIS_HOST'], '-p', s['REDIS_PORT'], '-n', s['REDIS_DB']]
        if s.get('REDIS_USERNAME'):
            command += ['--user', s['REDIS_USERNAME']]
        result = subprocess.run(command+['GET', key], capture_output=True,
            env=dict(os.environ, REDISCLI_AUTH=s['REDIS_PASSWORD']), timeout=10)
        if result.returncode:
            raise RuntimeError('publication Redis read failed; details withheld')
        return result.stdout.rstrip(b'\n')

    def wait_ready(self, account_id, group_id, timeout=60):
        deadline = time.monotonic()+timeout
        previous = None
        while time.monotonic() < deadline:
            self.healthy()
            publication = json.loads(self.sql('SELECT row_to_json(t) FROM scheduler_publication t;'))
            prefix = f"sched:publication:{publication['generation']}:{publication['publication_id']}:"
            ready = False
            try:
                manifest = self.redis_get(prefix+'manifest')
                payloads = {f'{kind}:{account_id}': self.redis_get(prefix+f'{kind}:{account_id}')
                            for kind in ('slim', 'full')}
                ready = publication_ready(publication, manifest, payloads, account_id, group_id)
            except (ValueError, KeyError, TypeError):
                pass
            after = json.loads(self.sql('SELECT row_to_json(t) FROM scheduler_publication t;'))
            if ready and publication == after:
                if previous == publication:
                    return {'generation': publication['generation'],
                            'account_id': account_id, 'group_id': group_id,
                            'manifest_and_payload_digests_verified': True,
                            'stable_observations': 2}
                previous = publication
            else:
                previous = None
            time.sleep(0.5)
        raise RuntimeError('publication readiness timeout; no model call sent')

    def accounts(self):
        return json.loads(self.sql("""SELECT json_agg(t) FROM (
            SELECT id,platform,type,status,schedulable,proxy_id,priority,
                credentials->>'expires_at' AS expires_at,
                (SELECT coalesce(json_agg(group_id ORDER BY group_id),'[]')
                 FROM account_groups WHERE account_id=accounts.id) AS group_ids
            FROM accounts WHERE id IN(1,2) ORDER BY id) t;"""))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', type=Path, default=Path('/etc/sub2api-roles/runtime.env'))
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--authorize-real-calls', action='store_true', required=True)
    args = parser.parse_args()
    if args.evidence_dir.exists():
        parser.error('evidence directory must be new')
    args.evidence_dir.mkdir(mode=0o700, parents=True)
    db = Stack(args.env_file)
    report = {'started_at': dt.datetime.now(dt.timezone.utc).isoformat(),
              'primary': PRIMARY, 'model': MODEL, 'generation_requests': 0,
              'generation_request_cap': 2, 'concurrency': 1, 'refresh_requests': 0,
              'passed': False}
    admin_id = user_id = None
    admin_token = None
    originals = None
    credentials_digest = None
    run = secrets.token_hex(6)

    def save():
        path = args.evidence_dir/'result.json'
        path.write_text(json.dumps(report, indent=2))
        path.chmod(0o600)

    def digest():
        # Compare persisted credentials without fetching their contents.
        return db.sql("SELECT md5(string_agg(credentials::text,',' ORDER BY id)) FROM accounts WHERE id IN(1,2);")

    def set_account(account_id, enabled):
        db.control(f'admin/accounts/{account_id}/schedulable', 'POST',
                   {'schedulable': enabled}, admin_token)
        db.control(f'admin/accounts/{account_id}', 'PUT',
                   {'status': 'active' if enabled else 'inactive'}, admin_token)

    try:
        db.healthy()
        originals = db.accounts()
        report['original_accounts'] = originals
        now = dt.datetime.now(dt.timezone.utc)
        if not originals or [a['id'] for a in originals] != [1, 2]:
            raise RuntimeError('only account IDs 1 and 2 accepted')
        for account in originals:
            if (account['platform'] != 'openai' or account['type'] != 'oauth' or
                    account['status'] != 'disabled' or account['schedulable']):
                raise RuntimeError('requires disabled non-schedulable OAuth fixtures')
            if not expiry_valid(account['expires_at'], now):
                raise RuntimeError('credential expired, missing expiry or near expiry; no refresh permitted')
            if len(account['group_ids']) != 1:
                raise RuntimeError('requires one existing group per account')
            group = int(account['group_ids'][0])
            if db.sql(f'SELECT count(*) FROM account_groups WHERE group_id={group};') != '1':
                raise RuntimeError('account group not isolated')
        if db.sql("SELECT count(*) FROM accounts WHERE id NOT IN(1,2) AND status='active' AND schedulable=true;") != '0':
            raise RuntimeError('unapproved schedulable accounts present')
        credentials_digest = digest()
        # libcrypt bcrypt is available on the host; no builds or package installs.
        with warnings.catch_warnings():
            warnings.simplefilter('ignore', DeprecationWarning)
            import crypt
        password = secrets.token_hex(24)
        hashed = crypt.crypt(password, crypt.mksalt(crypt.METHOD_BLOWFISH))
        if not hashed or not hashed.startswith(('$2a$', '$2b$', '$2y$')) or crypt.crypt(password, hashed) != hashed:
            raise RuntimeError('host bcrypt verification failed')
        email = 'model-admin-'+run+'@integration.invalid'
        admin_id = int(db.sql(f"INSERT INTO users(email,password_hash,role) VALUES ('{email}','{hashed}','admin') RETURNING id;"))
        report['fixture_ids'] = {'admin': admin_id}
        save()
        admin_token = db.control('auth/login', 'POST', {'email': email, 'password': password})['access_token']
        compliance = db.control('admin/compliance', token=admin_token)
        # Consent is administrator-scoped. The user explicitly says their prior
        # durable acceptance applies; require that evidence before recording it
        # for this disposable administrator through the normal HTTP endpoint.
        if compliance.get('required', True):
            version = compliance.get('version')
            if not isinstance(version, str) or not all(c.isalnum() or c in '._-' for c in version):
                raise RuntimeError('unexpected compliance version')
            prior = db.sql("SELECT count(*) FROM settings WHERE key LIKE 'admin_compliance_acknowledgement:%' "
                f"AND value::jsonb->>'version'='{version}';")
            if int(prior) < 1:
                raise RuntimeError('prior durable compliance acceptance unavailable')
            compliance = db.control('admin/compliance/accept', 'POST',
                {'phrase': compliance['ack_phrase_en'], 'language': 'en'}, admin_token)
            if compliance.get('required', True):
                raise RuntimeError('authorized prior consent not recorded for disposable admin')
        report['prior_durable_compliance_accepted'] = True
        groups = [int(a['group_ids'][0]) for a in originals]
        email = 'model-user-'+run+'@integration.invalid'
        created = db.control('admin/users', 'POST', {'email': email, 'password': password,
            'role': 'user', 'balance': 1, 'concurrency': 1, 'allowed_groups': groups,
            'restrict_public_groups': True}, admin_token)
        user_id = int(created['id'])
        report['fixture_ids']['user'] = user_id
        report['fixture_ids']['keys'] = []
        save()
        user_token = db.control('auth/login', 'POST', {'email': email, 'password': password})['access_token']
        for account in originals:
            account_id = int(account['id'])
            key = db.control('keys', 'POST', {'name': 'model-smoke-'+run+'-'+str(account_id),
                'group_id': int(account['group_ids'][0]), 'quota': 0.05}, user_token)
            report['fixture_ids']['keys'].append(int(key['id']))
            save()
            db.healthy()
            if not expiry_valid(db.accounts()[account_id-1]['expires_at'], dt.datetime.now(dt.timezone.utc)):
                raise RuntimeError('credential near expiry; no refresh permitted')
            set_account(account_id, True)
            readiness = db.wait_ready(account_id, int(account['group_ids'][0]))
            report.setdefault('publication_readiness', []).append(readiness)
            save()
            if db.sql("SELECT string_agg(id::text,',' ORDER BY id) FROM accounts WHERE status='active' AND schedulable=true;") != str(account_id):
                raise RuntimeError('active pool isolation failed')
            db.healthy()
            if report['generation_requests'] >= 2:
                raise RuntimeError('request cap reached')
            report['generation_requests'] += 1
            save()  # Count before transport: timeout/ambiguous response is never retried.
            status, data = db.http(18080, '/v1/responses', 'POST',
                {'model': MODEL, 'input': 'Reply with exactly OK.',
                 'instructions': 'Authorized brief integration test.', 'stream': False,
                 'store': False, 'max_output_tokens': 64, 'reasoning': {'effort': 'low'}}, key['key'])
            item = {'expected_account_id': account_id, 'http_status': status,
                    'completed': data.get('status') == 'completed',
                    'usage': {k: data.get('usage', {}).get(k)
                              for k in ('input_tokens', 'output_tokens', 'total_tokens')}}
            report.setdefault('requests', []).append(item)
            save()
            set_account(account_id, False)
            if status != 200 or not item['completed']:
                raise RuntimeError('generation failed or ambiguous; no client retry')
            deadline = time.monotonic()+20
            while True:
                logs = json.loads(db.sql(f"""SELECT coalesce(json_agg(t),'[]') FROM (
                    SELECT account_id,api_key_id,model,input_tokens,output_tokens,total_cost,actual_cost
                    FROM usage_logs WHERE user_id={user_id} ORDER BY id) t;"""))
                report['usage_logs'] = logs
                if len(logs) >= report['generation_requests'] or time.monotonic() >= deadline:
                    break
                time.sleep(0.25)
            if len(logs) != report['generation_requests'] or logs[-1]['account_id'] != account_id:
                raise RuntimeError('durable usage/account mismatch; stop')
            save()
        report['balance_after'] = db.sql(f'SELECT balance FROM users WHERE id={user_id};')
        report['credentials_unchanged'] = digest() == credentials_digest
        if not report['credentials_unchanged']:
            raise RuntimeError('stored credentials changed unexpectedly')
        report['passed'] = True
    except Exception as error:
        report['failure'] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    finally:
        cleanup_errors = []
        if admin_token:
            for account_id in (1, 2):
                try:
                    set_account(account_id, False)
                except Exception:
                    cleanup_errors.append('control account invalidation failed')
        # Never restore credentials or financial data. Other account fields were not modified.
        try:
            db.sql("UPDATE accounts SET status='disabled',schedulable=false WHERE id IN(1,2);")
            if user_id:
                db.sql(f"UPDATE api_keys SET status='inactive' WHERE user_id={user_id}; UPDATE users SET status='disabled' WHERE id={user_id};")
            if admin_id:
                db.sql(f"UPDATE users SET status='disabled' WHERE id={admin_id};")
            final_accounts = db.accounts()
            report['final_accounts'] = final_accounts
            report['accounts_restored_disabled'] = final_accounts == originals
            report['fixtures_disabled'] = all(db.sql(f"SELECT status FROM users WHERE id={i};") == 'disabled'
                for i in (admin_id, user_id) if i)
            if user_id:
                report['fixtures_disabled'] &= db.sql(f"SELECT count(*) FROM api_keys WHERE user_id={user_id} AND status!='inactive';") == '0'
            if credentials_digest:
                report['credentials_unchanged'] = digest() == credentials_digest
        except Exception:
            cleanup_errors.append('SQL cleanup verification failed')
        try:
            db.healthy()
            report['primary_healthy_after'] = True
        except Exception:
            report['primary_healthy_after'] = False
        report['cleanup_errors'] = cleanup_errors
        report['passed'] = bool(report['passed'] and not cleanup_errors and
            report.get('accounts_restored_disabled') and report.get('fixtures_disabled') and
            report.get('credentials_unchanged') and report.get('primary_healthy_after'))
        save()
    print(json.dumps(report, indent=2))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
