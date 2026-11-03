#!/usr/bin/env python3
"""Host-local runtime gauges; stdlib only, no metadata lookup or secret reads."""
import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

NAMESPACE = 'Sub2API/Runtime'
REGION = 'us-west-2'
APPROVED_INSTANCES = {'i-04225834295350402', 'i-07f2c0ba4744915e4'}
ROLES = ('gateway', 'control', 'worker')
# Memory MiB, reservation MiB, NanoCPUs: supervisor contract, not live validation.
BUDGET = {'gateway': (1024, 512, 1000000000),
          'control': (768, 384, 400000000), 'worker': (512, 256, 300000000)}
PREFIX = 'sub2api-roles-'
# Project only the required fields. Never fetch Config.Env, health logs, or full inspect.
PROJECTION = ('{"name":{{json .Name}},"running":{{json .State.Running}},'
              '"oom":{{json .State.OOMKilled}},"health":'
              '{{if .State.Health}}{{json .State.Health.Status}}{{else}}null{{end}},'
              '"memory":{{json .HostConfig.Memory}},'
              '"reservation":{{json .HostConfig.MemoryReservation}},'
              '"swap":{{json .HostConfig.MemorySwap}},'
              '"nano_cpus":{{json .HostConfig.NanoCpus}},'
              '"cpu_period":{{json .HostConfig.CpuPeriod}},'
              '"cpu_quota":{{json .HostConfig.CpuQuota}},'
              '"pids":{{json .HostConfig.PidsLimit}}}')


class CollectorError(Exception):
    """Safe generic error; subprocess output must never reach the journal."""


def command(args, payload=None):
    try:
        result = subprocess.run(args, input=payload, text=True, capture_output=True,
                                timeout=30, check=True,
                                env={**os.environ, 'AWS_PAGER': '', 'AWS_MAX_ATTEMPTS': '1'})
        return result.stdout
    except (OSError, subprocess.SubprocessError):
        raise CollectorError('subprocess failed') from None


def load_config(filename):
    path = Path(filename)
    if not path.is_absolute() or path.is_symlink():
        raise CollectorError('invalid config path')
    resolved = path.resolve(strict=True)
    repo = Path(__file__).resolve().parent.parent
    if resolved == repo or repo in resolved.parents:
        raise CollectorError('config must be outside the repository')
    # O_NOFOLLOW plus fstat prevents final-component symlink swaps.
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd) as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0
                or stat.S_IMODE(info.st_mode) != 0o600):
            raise CollectorError('config must be a root-owned 0600 regular file')
        config = json.loads(stream.read(4097))
    if (set(config) != {'instance_id', 'region'} or config['region'] != REGION
            or config['instance_id'] not in APPROVED_INSTANCES):
        raise CollectorError('unapproved metric destination')
    return config


def host_values():
    fields = {}
    for line in Path('/proc/meminfo').read_text().splitlines():
        match = re.fullmatch(r'(MemTotal|MemAvailable):\s+(\d+) kB', line)
        if match:
            fields[match[1]] = int(match[2]) * 1024
    total, available = fields['MemTotal'], fields['MemAvailable']
    if not 0 <= available <= total or total <= 0:
        raise CollectorError('invalid memory sample')
    disk = os.statvfs('/')
    if disk.f_blocks <= 0 or not 0 <= disk.f_bavail <= disk.f_blocks:
        raise CollectorError('invalid disk sample')
    return [('MemoryAvailableBytes', available, 'Bytes'),
            ('MemoryUsedPercent', 100 * (total - available) / total, 'Percent'),
            ('RootDiskUsedPercent', 100 * (disk.f_blocks - disk.f_bavail)
             / disk.f_blocks, 'Percent')]


def role_values():
    names = command(['docker', 'ps', '--all', '--filter',
                     'name=^/sub2api-roles-(gateway|control|worker)$',
                     '--format', '{{.Names}}']).splitlines()
    if len(names) != len(set(names)) or any(n not in {PREFIX + r for r in ROLES}
                                            for n in names):
        raise CollectorError('unexpected container inventory')
    result = {}
    for role in ROLES:
        name = PREFIX + role
        healthy = oom = 0
        drift = 1  # A missing role cannot be certified as resource compliant.
        if name in names:
            item = json.loads(command(['docker', 'inspect', '--type', 'container',
                                       '--format', PROJECTION, name]))
            if (item['name'] != '/' + name or type(item['running']) is not bool
                    or type(item['oom']) is not bool
                    or item['health'] not in (None, 'starting', 'healthy', 'unhealthy')):
                raise CollectorError('invalid container state')
            for key in ('memory', 'reservation', 'swap', 'nano_cpus',
                        'cpu_period', 'cpu_quota', 'pids'):
                if type(item[key]) is not int:
                    raise CollectorError('invalid resource cap')
            memory, reservation, cpu = BUDGET[role]
            drift = int(not (item['memory'] == memory * 1024**2
                            and item['reservation'] == reservation * 1024**2
                            and item['swap'] == memory * 1024**2
                            and item['nano_cpus'] == cpu
                            and item['cpu_period'] == 0 and item['cpu_quota'] == 0
                            and item['pids'] == 256))
            healthy = int(item['running'] and item['health'] == 'healthy'
                          and not item['oom'])
            oom = int(item['oom'])
        result[role] = [('ContainerCount', int(name in names), 'Count'),
                        ('HealthyCount', healthy, 'Count'),
                        ('OOMKilledCount', oom, 'Count'),
                        ('ResourceCapDrift', drift, 'Count')]
    return result


def metrics(config, success, host=(), roles=None):
    timestamp = datetime.now(timezone.utc).isoformat()
    data = []
    groups = [(None, list(host) + [('Heartbeat', 1, 'Count'),
                                  ('CollectionSuccess', int(success), 'Count')])]
    groups += list((roles or {}).items())
    for role, values in groups:
        dimensions = [{'Name': 'InstanceId', 'Value': config['instance_id']}]
        if role:
            dimensions.append({'Name': 'Role', 'Value': role})
        for name, value, unit in values:
            data.append({'MetricName': name, 'Dimensions': dimensions,
                         'Value': value, 'Unit': unit, 'Timestamp': timestamp,
                         'StorageResolution': 60})
    return data


def publish(data):
    # CLIv2 on the target host cannot reliably read file:///dev/stdin.
    # NamedTemporaryFile creates an exclusive 0600 file and removes it even when
    # publication fails. It contains only bounded, nonsecret metric samples.
    try:
        with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8',
                                         prefix='sub2api-metrics-', suffix='.json') as stream:
            os.fchmod(stream.fileno(), 0o600)
            json.dump({'Namespace': NAMESPACE, 'MetricData': data}, stream,
                      allow_nan=False)
            stream.flush()
            command(['aws', 'cloudwatch', 'put-metric-data', '--region', REGION,
                     '--cli-connect-timeout', '5', '--cli-read-timeout', '15',
                     '--cli-input-json', 'file://' + stream.name])
    except (OSError, ValueError, TypeError):
        raise CollectorError('metric payload failed') from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', default='/etc/sub2api-runtime-metrics/config.json')
    parser.add_argument('--dry-run', action='store_true', help='collect only; never invoke AWS')
    args = parser.parse_args(argv)
    try:
        config = load_config(args.config)
    except (CollectorError, OSError, ValueError, TypeError):
        print('runtime collector: invalid secure configuration', file=sys.stderr)
        return 1
    success = True
    try:
        host = host_values()
        roles = role_values()
        data = metrics(config, True, host, roles)
    except (CollectorError, OSError, ValueError, KeyError, TypeError):
        success = False
        # No partial/false healthy samples; failure and liveness only.
        data = metrics(config, False)
        print('runtime collector: collection failed', file=sys.stderr)
    if args.dry_run:
        print(json.dumps({'Namespace': NAMESPACE, 'MetricData': data}, allow_nan=False))
    else:
        try:
            publish(data)
        except CollectorError:
            print('runtime collector: publication failed', file=sys.stderr)
            return 1
    return 0 if success else 1


if __name__ == '__main__':
    sys.exit(main())
