#!/usr/bin/env python3
"""Host-local, preloaded-image-only role supervisor and health-checked deployment.
No registry login/pull, remote execution, migration, or source build.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import time

STATE = Path('/var/lib/sub2api-roles')
UNIT = 'sub2api-roles.service'
LABEL = 'io.sub2api.host-roles'
ROLES = ('gateway', 'control', 'worker')
# memory MiB, soft reservation MiB, CPU cap, Go soft memory target MiB
BUDGET = {'gateway': (1024, 512, '1.0', 768),
          'control': (768, 384, '0.4', 512), 'worker': (512, 256, '0.3', 320)}
STOPPING = False
# Constant shell program: never interpolate release values or environment content.
# Write only into UID 10001's private tmpfs, then retain the image's role gates.
SAFE_START = ('umask 077; printf "%s\\n" \'server:\' \'  mode: release\' '
              '\'pricing:\' \'  remote_url: ""\' \'  hash_url: ""\' '
              '\'  data_dir: /app/data/pricing\' '
              '\'  fallback_file: /app/resources/model-pricing/model_prices_and_context_window.json\' '
              '\'log:\' \'  output:\' \'    to_stdout: true\' \'    to_file: false\' '
              '> /app/data/config.yaml; exec /app/cloud-entrypoint')


def command(*args, check=True):
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE).stdout.strip()


def secure_file(value, secret=False):
    path = Path(value)
    if not path.is_absolute() or path.is_symlink():
        raise ValueError('external files must be absolute regular non-symlink paths')
    path = path.resolve(strict=True)
    repo = Path(__file__).resolve().parent.parent
    if path.is_relative_to(repo):
        raise ValueError('configuration/secrets must be outside the repository')
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0:
        raise ValueError('external files must be root-owned regular files')
    if info.st_mode & (0o077 if secret else 0o022):
        raise ValueError('unsafe external file permissions')
    return str(path)


def validate(config):
    expected = {'image', 'env_file', 'ca_file', 'scheduler_publications', 'bind_ip',
                'gateway_port', 'control_port'}
    if set(config) != expected:
        raise ValueError('release requires exactly: ' + ', '.join(sorted(expected)))
    if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[0-9a-f]{64}', config['image']):
        raise ValueError('image must be repository@sha256 digest, never tag/image ID')
    if type(config['scheduler_publications']) is not bool:
        raise ValueError('scheduler_publications must explicitly be true or false')
    import ipaddress
    address = ipaddress.ip_address(config['bind_ip'])
    if address.version != 4 or not (address.is_private or address.is_loopback) or address.is_unspecified or address.is_multicast or address.is_link_local:
        raise ValueError('bind_ip must be an explicit private/loopback IPv4 address')
    ports = [config['gateway_port'], config['control_port']]
    if any(type(p) is not int or not 1024 <= p <= 65535 for p in ports) or ports[0] == ports[1]:
        raise ValueError('distinct unprivileged HTTP ports required')
    config['env_file'] = secure_file(config['env_file'], secret=True)
    config['ca_file'] = secure_file(config['ca_file'])
    for line in Path(config['env_file']).read_text().splitlines():
        key = line.partition('=')[0].strip()
        if key == 'DATABASE_DSN':
            raise ValueError('DATABASE_DSN can bypass structured TLS configuration')
    # Inspect only: no implicit pull. The exact digest must already be locally loaded.
    command('docker', 'image', 'inspect', config['image'])
    return config


def save(state):
    temporary = STATE / 'state.tmp'
    with temporary.open('w') as output:
        os.chmod(temporary, 0o600)
        json.dump(state, output)
        output.flush()
        os.fsync(output.fileno())
    os.replace(temporary, STATE / 'state.json')
    fd = os.open(STATE, os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def load():
    return json.loads((STATE / 'state.json').read_text())


def name(role):
    return 'sub2api-roles-' + role


def inspect(role):
    raw = command('docker', 'inspect', name(role), check=False)
    items = json.loads(raw) if raw else []
    return items[0] if items else None


def stop():
    # Never start saved/stopped containers; remove and recreate from the release.
    for role in reversed(ROLES):
        item = inspect(role)
        if item:
            if item['Config'].get('Labels', {}).get(LABEL) != 'true':
                raise RuntimeError('refusing to remove a foreign named container')
            command('docker', 'stop', '--time', '120', name(role))
            command('docker', 'rm', name(role))


def preflight():
    # Fail closed on unadopted containers; do not silently overlap a legacy worker.
    ids = command('docker', 'ps', '-q').split()
    if ids:
        for item in json.loads(command('docker', 'inspect', *ids)):
            env = item['Config'].get('Env', [])
            labels = item['Config'].get('Labels', {})
            # Independent, explicitly isolated supplier ingress is not a second
            # general Control or worker and is never managed by this supervisor.
            if (item.get('Name') == '/sub2api-supplier-control'
                    and labels.get('com.docker.compose.project') == 'deploy'
                    and labels.get('com.docker.compose.service') == 'supplier-control'
                    and 'RUNTIME_ROLE=control' in env
                    and 'SUPPLIER_PORTAL_ENABLED=true' in env
                    and not any(e in env for e in ('RUNTIME_ROLE=all', 'RUNTIME_ROLE=worker', 'RUNTIME_ROLE=gateway'))):
                continue
            if any(e in ('RUNTIME_ROLE=all', 'RUNTIME_ROLE=worker',
                         'RUNTIME_ROLE=gateway', 'RUNTIME_ROLE=control') for e in env):
                raise RuntimeError('running role containers must be drained/stopped before adoption')
    # The existing native integration units must not be running alongside containers.
    units = command('systemctl', 'list-units', '--state=active', '--no-legend',
                    'sub2api-integration@*.service')
    if units:
        raise RuntimeError('native integration units are still active')


def healthy(config):
    for role in ROLES:
        item = inspect(role)
        if not item or not item['State']['Running'] or item['State'].get('Health', {}).get('Status') != 'healthy':
            return False
        if item['Config']['Image'] != config['image']:
            return False
    return True


def wait_healthy(config, timeout=180):
    end = time.monotonic() + timeout
    while time.monotonic() < end and not STOPPING:
        if healthy(config):
            return
        time.sleep(2)
    raise RuntimeError('role health deadline exceeded')


def supplier_key_id():
    env = Path('/etc/sub2api-supplier/runtime.env')
    if not env.exists():
        return None  # Supplier support remains explicitly optional on other hosts.
    for line in env.read_text().splitlines():
        key, sep, value = line.partition('=')
        if sep and key == 'SUPPLIER_PORTAL_VAULT_KEY_ID' and re.fullmatch(r'[a-zA-Z0-9._-]{1,128}', value):
            return value
    raise ValueError('supplier active vault key ID is missing')


def start(config):
    preflight()
    vault_key_id = supplier_key_id()
    for role in ROLES:
        if STOPPING:
            raise RuntimeError('shutdown requested')
        memory, reservation, cpu, go_memory = BUDGET[role]
        args = ['docker', 'run', '-d', '--pull=never', '--name', name(role),
                '--label', LABEL + '=true', '--restart=no', '--network', 'sub2api-roles',
                '--user', '10001:10001', '--read-only', '--init', '--cap-drop=ALL',
                '--security-opt=no-new-privileges:true', '--pids-limit=256',
                '--memory', f'{memory}m', '--memory-reservation', f'{reservation}m',
                '--memory-swap', f'{memory}m', '--cpus', cpu, '--stop-timeout=120',
                '--log-driver=json-file', '--log-opt=max-size=10m', '--log-opt=max-file=3',
                '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777',
                '--tmpfs', '/app/data:rw,noexec,nosuid,nodev,size=128m,uid=10001,gid=10001,mode=0700',
                '--mount', f'type=bind,src={config["ca_file"]},dst=/run/certs/postgres-ca.pem,readonly',
                '--env-file', config['env_file']]
        environment = {
            'RUNTIME_ROLE': role, 'RUNTIME_ENABLE_SPLIT_ROLES': 'true',
            'RUNTIME_MULTI_REPLICA_WORKERS': 'true' if role == 'worker' else 'false',
            'RUNTIME_SCHEDULER_PUBLICATIONS': str(config['scheduler_publications']).lower(),
            'DATABASE_MIGRATION_MODE': 'check', 'DATABASE_SSLMODE': 'verify-full',
            'DATABASE_SSLROOTCERT': '/run/certs/postgres-ca.pem', 'REDIS_ENABLE_TLS': 'true',
            'SERVER_HOST': '0.0.0.0', 'SERVER_PORT': '8080', 'AUTO_SETUP': 'false',
            'DATA_DIR': '/app/data', 'CONFIG_FILE': '/app/data/config.yaml',
            'LOG_OUTPUT_TO_FILE': 'false', 'RUNTIME_SHUTDOWN_TIMEOUT_SECONDS': '90',
            'GOMEMLIMIT': f'{go_memory}MiB', 'GOMAXPROCS': '2'}
        for key, value in environment.items():
            args += ['-e', f'{key}={value}']
        if role != 'worker':
            args += ['-p', f'{config["bind_ip"]}:{config[role + "_port"]}:8080']
        if vault_key_id and role in ('worker', 'gateway'):
            args += ['--mount', 'type=bind,src=/etc/sub2api-supplier/keyring.json,dst=/run/vault/supplier-keyring.json,readonly',
                     '-e', 'SUPPLIER_PORTAL_VAULT_KEY_FILE=/run/vault/supplier-keyring.json',
                     '-e', 'SUPPLIER_PORTAL_VAULT_KEY_ID=' + vault_key_id]
            if role == 'worker':
                args += ['-e', 'SUPPLIER_PROBE_ENABLED=true', '-e', 'SUPPLIER_PROBE_INTERVAL_SECONDS=600']
        args += ['--entrypoint', '/bin/sh', config['image'], '-ec', SAFE_START]
        command(*args)
    wait_healthy(config)


def network():
    raw = command('docker', 'network', 'inspect', 'sub2api-roles', check=False)
    items = json.loads(raw) if raw else []
    if not items:
        command('docker', 'network', 'create', '--driver=bridge',
                '--opt=com.docker.network.bridge.name=sub2api0', 'sub2api-roles')
        raw = command('docker', 'network', 'inspect', 'sub2api-roles')
        items = json.loads(raw) if raw else []
    if not items:
        raise RuntimeError('dedicated container network absent after creation')
    item = items[0]
    if (item['Driver'] != 'bridge' or item.get('EnableIPv6') or
            item.get('Options', {}).get('com.docker.network.bridge.name') != 'sub2api0'):
        raise RuntimeError('dedicated IPv4 sub2api0 bridge required for metadata firewall')


def supervise():
    network()
    state = load()
    try:
        try:
            stop()
            start(validate(state['current']))
        except Exception:
            stop()
            if STOPPING or not state.get('pending') or not state.get('previous'):
                raise
            # Rollback only after all candidate processes (especially worker) stop.
            state = {'current': state['previous'], 'previous': state['current']}
            save(state)
            start(validate(state['current']))
            print('previous release restored after failed startup', flush=True)
        if state.get('pending'):
            state['pending'] = False
            save(state)
        while not STOPPING:
            if not healthy(state['current']):
                raise RuntimeError('role unhealthy; systemd will recreate current release')
            time.sleep(5)
    finally:
        stop()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['run', 'deploy', 'rollback'])
    parser.add_argument('release', nargs='?')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('host deployment requires root')
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    if args.action == 'run':
        supervise()
        return
    with (STATE / 'deploy.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        old = load() if (STATE / 'state.json').exists() else None
        if args.action == 'deploy':
            if not args.release:
                parser.error('deploy requires an external root-owned release JSON')
            candidate = validate(json.loads(Path(secure_file(args.release, secret=True)).read_text()))
        else:
            if not old or not old.get('previous'):
                raise RuntimeError('no previous release available')
            candidate = validate(old['previous'])
        command('systemctl', 'stop', UNIT)
        stop()
        preflight()
        save({'current': candidate, 'previous': old['current'] if old else None, 'pending': True})
        command('systemctl', 'reset-failed', UNIT)
        command('systemctl', 'start', UNIT)
        # The supervisor owns startup rollback; report candidate failure even if old is healthy.
        end = time.monotonic() + 400
        while time.monotonic() < end:
            state = load()
            if state['current'] != candidate:
                raise RuntimeError('candidate failed; supervisor is restoring previous release')
            if not state.get('pending', True) and healthy(candidate):
                print('all three role containers healthy; verify dependency/traffic/leadership gates before next host')
                return
            time.sleep(2)
        raise RuntimeError('deployment timed out; inspect local systemd journal, do not update peer')


if __name__ == '__main__':
    def terminate(_signum, _frame):
        global STOPPING
        STOPPING = True
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    try:
        main()
    except Exception as error:
        # Do not print subprocess stderr or configuration: these may contain secrets.
        print('host deployment failed: ' + (str(error) if not isinstance(error, subprocess.CalledProcessError)
                                           else 'local command failed (inspect protected journal)'), file=sys.stderr)
        sys.exit(1)
