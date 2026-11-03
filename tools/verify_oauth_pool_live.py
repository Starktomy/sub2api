#!/usr/bin/env python3
"""Bounded real OAuth pool checks. Only authorized named integration fixtures."""
import argparse
import datetime
import json
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time

# -I intentionally excludes the script directory. Only add this trusted source
# directory, never the supplied accounts or evidence directories.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from oauth_integration_support import Integration


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', type=Path, required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--authorize-real-calls', action='store_true', required=True)
    parser.add_argument('--user-accepted-compliance', action='store_true', required=True)
    args = parser.parse_args()
    if args.evidence_dir.exists():
        parser.error('evidence directory must be new')
    args.evidence_dir.mkdir(mode=0o700, parents=True)
    db = Integration(args.env_file)
    run = secrets.token_hex(6)
    report = {'run_id': run, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'model': 'gpt-5.6-terra', 'request_cap': 8, 'generation_http_requests': 0,
              'checks': [], 'requests': [], 'passed': False}
    admin_id = user_id = group_id = key_id = None
    admin_token = key = None
    evidence = args.evidence_dir/'result.json'

    def save():
        evidence.write_text(json.dumps(report, indent=2))
        evidence.chmod(0o600)

    def control(path, payload, method='PUT'):
        status, data = db.http(18080, '/api/v1/admin/'+path, method, payload, admin_token)
        return db.require(status, data, 'control '+path)

    try:
        rows = json.loads(db.sql("SELECT json_agg(t) FROM (SELECT id,status,schedulable FROM accounts ORDER BY id) t;"))
        if [x['id'] for x in rows] != [1, 2] or any(x['status'] != 'disabled' or x['schedulable'] for x in rows):
            raise RuntimeError('requires exactly the two disabled prior OAuth smoke accounts')
        password = secrets.token_hex(24)
        hashed = db.hash_password(password, args.evidence_dir)
        admin_email = 'pool-admin-'+run+'@integration.invalid'
        user_email = 'pool-user-'+run+'@integration.invalid'
        admin_id = int(db.sql(f"INSERT INTO users(email,password_hash,role,balance) VALUES ('{admin_email}','{hashed}','admin',0) RETURNING id;"))
        user_id = int(db.sql(f"INSERT INTO users(email,password_hash,balance) VALUES ('{user_email}','{hashed}',1) RETURNING id;"))
        admin_token = db.login(admin_email, password)
        status, data = db.http(18080, '/api/v1/admin/compliance/accept', 'POST',
            {'phrase': '我已阅读、理解并同意 Sub2API 部署与运营合规承诺', 'language': 'zh'}, admin_token)
        if db.require(status, data, 'authorized compliance acceptance').get('required', True):
            raise RuntimeError('compliance confirmation missing')
        group_id = int(db.sql(f"INSERT INTO groups(name,platform) VALUES ('pool-{run}','openai') RETURNING id;"))
        report['fixture_ids'] = {'admin': admin_id, 'user': user_id, 'group': group_id, 'accounts': [1, 2]}
        user_token = db.login(user_email, password)
        status, data = db.http(18080, '/api/v1/keys', 'POST',
            {'name': 'pool-'+run, 'group_id': group_id, 'quota': 0.1}, user_token)
        created = db.require(status, data, 'pool key creation')
        key_id, key = int(created['id']), created['key']
        report['fixture_ids']['key'] = key_id
        save()
        original_groups = {i: json.loads(db.sql(f"SELECT coalesce(json_agg(group_id),'[]') FROM account_groups WHERE account_id={i};")) for i in (1, 2)}
        report['original_groups'] = original_groups
        save()
        for i in (1, 2):
            control(f'accounts/{i}', {'group_ids': original_groups[i]+[group_id], 'priority': 0 if i == 1 else 100, 'status': 'active'})
            control(f'accounts/{i}/schedulable', {'schedulable': True}, 'POST')
        # Bounded settling interval for the existing snapshot worker, not an SLA.
        time.sleep(3)
        session = 'pool-'+run

        def generate(label, port, session_id, additions=None):
            if report['generation_http_requests'] >= report['request_cap']:
                raise RuntimeError('generation request budget exhausted')
            payload = {'model': report['model'], 'input': 'Reply with exactly OK.',
                       'instructions': 'Authorized integration test. Reply very briefly.',
                       'stream': False, 'store': False, 'max_output_tokens': 128,
                       'reasoning': {'effort': 'low'}, 'prompt_cache_key': session_id}
            payload.update(additions or {})
            report['generation_http_requests'] += 1
            save()
            status, data = db.http(port, '/v1/responses', 'POST', payload, key,
                                  headers={'session_id': session_id, 'X-Request-ID': label+'-'+run})
            entry = {'label': label, 'http_status': status, 'session': session_id,
                     'completed': isinstance(data, dict) and data.get('status') == 'completed'}
            report['requests'].append(entry)
            save()
            if status != 200 or not entry['completed']:
                raise RuntimeError(label+' generation failed: HTTP '+str(status))
            # Every call is serial, so the latest new row identifies this call.
            deadline = time.monotonic()+15
            while True:
                logs = json.loads(db.sql(f"SELECT coalesce(json_agg(t),'[]') FROM (SELECT id,account_id,request_id,session_id,input_tokens,output_tokens,total_cost,actual_cost FROM usage_logs WHERE user_id={user_id} ORDER BY id) t;"))
                if len(logs) >= report['generation_http_requests'] or time.monotonic() >= deadline:
                    break
                time.sleep(0.2)
            if len(logs) != report['generation_http_requests']:
                raise RuntimeError('durable usage row count mismatch')
            entry['usage'] = logs[-1]
            if entry['usage']['session_id'] != session_id:
                raise RuntimeError('usage session not preserved')
            save()
            return data, entry['usage']['account_id']

        tool = {'type': 'function', 'name': 'echo', 'description': 'Return the input text unchanged.',
                'parameters': {'type': 'object', 'properties': {'text': {'type': 'string'}},
                               'required': ['text'], 'additionalProperties': False}, 'strict': True}
        original_input = [{'role': 'user', 'content': 'Call echo with text ping, then report the returned text.'}]
        first, selected = generate('tool-call', 18081, session,
            {'input': original_input, 'tools': [tool], 'tool_choice': {'type': 'function', 'name': 'echo'}})
        calls = [x for x in first.get('output', []) if x.get('type') == 'function_call']
        if len(calls) != 1 or calls[0].get('name') != 'echo':
            raise RuntimeError('expected one actual echo function call')
        arguments = json.loads(calls[0]['arguments'])
        if arguments != {'text': 'ping'}:
            raise RuntimeError('echo function arguments mismatch')
        other = 3-selected
        report['initial_selected_account'] = selected
        report['checks'].append('real_function_call_arguments')
        # Keep both eligible; make the other account preferable, then cross gateways.
        control(f'accounts/{selected}', {'priority': 100})
        control(f'accounts/{other}', {'priority': 0})
        time.sleep(3)
        continuation = original_input + first['output'] + [
            {'type': 'function_call_output', 'call_id': calls[0]['call_id'],
             'output': json.dumps({'text': arguments['text']})}]
        second, continued_account = generate('tool-result-affinity', 18082, session,
            {'input': continuation, 'tools': [tool], 'tool_choice': 'none'})
        text = ''.join(c.get('text', '') for item in second.get('output', [])
                       for c in item.get('content', []) if c.get('type') == 'output_text')
        if 'ping' not in text.lower() or continued_account != selected:
            raise RuntimeError('tool continuation or cross-gateway affinity failed')
        report['checks'].extend(['tool_result_full_replay', 'cross_gateway_affinity_despite_priority_change'])
        control(f'accounts/{selected}/schedulable', {'schedulable': False}, 'POST')
        time.sleep(2)
        _, escaped = generate('unschedulable-sticky-escape', 18081, session)
        if escaped != other:
            raise RuntimeError('unschedulable sticky did not escape to other account')
        report['checks'].append('pre_send_filter_and_soft_affinity_escape')
        # Restore the initially selected account, but pin the current sticky account
        # to a confirmed closed localhost proxy. No request reaches OpenAI via it.
        control(f'accounts/{selected}/schedulable', {'schedulable': True}, 'POST')
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            closed_port = probe.getsockname()[1]
        status, data = db.http(18080, '/api/v1/admin/proxies', 'POST',
            {'name': 'closed-proxy-'+run, 'protocol': 'http', 'host': '127.0.0.1',
             'port': closed_port, 'fallback_mode': 'none'}, admin_token)
        proxy = db.require(status, data, 'closed test proxy creation')
        report['test_proxy_id'] = int(proxy['id'])
        control(f'accounts/{other}', {'proxy_id': report['test_proxy_id']})
        time.sleep(2)
        _, recovered = generate('transport-failover', 18082, session)
        if recovered != selected:
            raise RuntimeError('transport failure did not select healthy account')
        journal_since = datetime.datetime.fromisoformat(report['started_at']).strftime('%Y-%m-%d %H:%M:%S UTC')
        journal = subprocess.run(['journalctl', '--since', journal_since,
            '-u', 'sub2api-integration@gateway-b', '-o', 'cat', '--no-pager'],
            capture_output=True, text=True, timeout=20)
        relevant = [line for line in journal.stdout.splitlines() if 'transport-failover-'+run in line]
        report['transport_evidence'] = {
            'switch_marker_for_request': any('openai.upstream_failover_switching' in line for line in relevant),
            'closed_proxy_error_for_request': any('connection refused' in line.lower() for line in relevant),
            'failed_account': other, 'successful_account': recovered}
        if not report['transport_evidence']['switch_marker_for_request']:
            raise RuntimeError('missing request-correlated failover switch evidence')
        report['checks'].append('real_transport_attempt_failure_then_reselection')
        # Restore network configuration before the serial, one-time refresh.
        control(f'accounts/{other}', {'proxy_id': 0})
        control(f'accounts/{other}/temp-unschedulable', None, 'DELETE')
        control(f'accounts/{other}/schedulable', {'schedulable': False}, 'POST')
        before = json.loads(db.sql(f"SELECT credentials::text FROM accounts WHERE id={selected};"))
        # Tokens are compared in memory; evidence contains booleans only.
        status, data = db.http(18080, f'/api/v1/admin/accounts/{selected}/refresh', 'POST', {}, admin_token)
        report['refresh'] = {'account_id': selected, 'http_status': status}
        after = json.loads(db.sql(f"SELECT credentials::text FROM accounts WHERE id={selected};"))
        report['refresh'].update({'access_changed': before.get('access_token') != after.get('access_token'),
            'refresh_changed': before.get('refresh_token') != after.get('refresh_token'),
            'expires_at_present': bool(after.get('expires_at'))})
        private = args.evidence_dir/'refreshed-account-recovery.json'
        # A rotated refresh token must survive even if a later assertion fails.
        private.write_text(json.dumps({'account_id': selected, 'credentials': after}))
        private.chmod(0o600)
        save()
        if status != 200:
            raise RuntimeError('serial OAuth refresh failed: HTTP '+str(status))
        _, refreshed_account = generate('after-refresh', 18081, 'refresh-'+run)
        if refreshed_account != selected:
            raise RuntimeError('post-refresh request used unexpected account')
        report['checks'].append('official_serial_refresh_persisted_and_generation_succeeds')
        report['balance_after'] = db.sql(f'SELECT balance FROM users WHERE id={user_id};')
        report['passed'] = True
    except Exception as error:
        report['failure'] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    finally:
        try:
            if admin_token:
                for account_id in (1, 2):
                    control(f'accounts/{account_id}/schedulable', {'schedulable': False}, 'POST')
                    restore = {'status': 'inactive', 'proxy_id': 0, 'priority': 50}
                    old_groups = report.get('original_groups', {}).get(account_id)
                    if old_groups is not None:
                        restore['group_ids'] = old_groups
                    control(f'accounts/{account_id}', restore)
                    control(f'accounts/{account_id}/temp-unschedulable', None, 'DELETE')
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
