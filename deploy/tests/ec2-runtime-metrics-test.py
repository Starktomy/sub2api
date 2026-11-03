#!/usr/bin/env python3
"""Offline contract tests. No Docker, AWS, or network operations."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    'collector', Path(__file__).resolve().parents[1] / 'ec2-runtime-metrics.py')
C = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(C)
CONFIG = {'instance_id': 'i-04225834295350402', 'region': 'us-west-2'}


def container(role):
    memory, reservation, cpu = C.BUDGET[role]
    return {'name': '/' + C.PREFIX + role, 'running': True, 'oom': False,
            'health': 'healthy', 'memory': memory * 1024**2,
            'reservation': reservation * 1024**2, 'swap': memory * 1024**2,
            'nano_cpus': cpu, 'cpu_period': 0, 'cpu_quota': 0, 'pids': 256}


def docker(args, payload=None):
    if args[:2] == ['docker', 'ps']:
        return '\n'.join(C.PREFIX + r for r in C.ROLES)
    if args[:2] == ['docker', 'inspect']:
        return json.dumps(container(args[-1].removeprefix(C.PREFIX)))
    raise AssertionError('unexpected subprocess')


class CollectorTest(unittest.TestCase):
    def run_main(self, argv=(), **mocks):
        with patch.object(C, 'load_config', return_value=CONFIG), \
                patch.object(C, 'host_values', return_value=[('MemoryAvailableBytes', 1, 'Bytes'),
                    ('MemoryUsedPercent', 50, 'Percent'), ('RootDiskUsedPercent', 20, 'Percent')]), \
                patch.object(C, 'command', side_effect=docker), \
                patch.object(C, 'publish') as publish, \
                contextlib.redirect_stdout(io.StringIO()) as out, \
                contextlib.redirect_stderr(io.StringIO()) as err:
            with contextlib.ExitStack() as stack:
                for name, value in mocks.items():
                    stack.enter_context(patch.object(C, name, side_effect=value))
                result = C.main(list(argv))
            return result, publish, out.getvalue(), err.getvalue()

    def test_success_exact_series_and_single_batch(self):
        result, publish, _, _ = self.run_main()
        self.assertEqual(result, 0)
        publish.assert_called_once()
        data = publish.call_args.args[0]
        self.assertEqual(len(data), 17)
        keys = {(d['MetricName'], tuple((x['Name'], x['Value']) for x in d['Dimensions']))
                for d in data}
        self.assertEqual(len(keys), 17)
        self.assertEqual({x['Name'] for d in data for x in d['Dimensions']}, {'InstanceId', 'Role'})
        self.assertTrue(all(d['StorageResolution'] == 60 for d in data))

    def test_dry_run_never_publishes(self):
        result, publish, out, _ = self.run_main(['--dry-run'])
        self.assertEqual(result, 0)
        publish.assert_not_called()
        self.assertEqual(len(json.loads(out)['MetricData']), 17)

    def test_collection_error_no_false_healthy(self):
        for error in (C.CollectorError('secret'), ValueError('secret'), KeyError('secret')):
            result, publish, _, err = self.run_main(role_values=error)
            self.assertEqual(result, 1)
            data = publish.call_args.args[0]
            self.assertEqual({d['MetricName']: d['Value'] for d in data},
                             {'Heartbeat': 1, 'CollectionSuccess': 0})
            self.assertNotIn('secret', err)

    def test_failed_dry_run_no_publication(self):
        result, publish, out, _ = self.run_main(['--dry-run'], host_values=OSError())
        self.assertEqual(result, 1)
        publish.assert_not_called()
        self.assertEqual(len(json.loads(out)['MetricData']), 2)

    def test_publication_error_nonzero(self):
        result, _, _, err = self.run_main(publish=C.CollectorError('secret'))
        self.assertEqual(result, 1)
        self.assertNotIn('secret', err)

    def test_missing_containers_explicit_zero(self):
        with patch.object(C, 'command', return_value=''):
            roles = C.role_values()
        for values in roles.values():
            self.assertEqual({n: v for n, v, _ in values},
                             {'ContainerCount': 0, 'HealthyCount': 0,
                              'OOMKilledCount': 0, 'ResourceCapDrift': 1})

    def test_oom_and_health_and_drift(self):
        for field, value in [('oom', True), ('running', False), ('health', None),
                             ('health', 'starting'), ('health', 'unhealthy')]:
            def changed(args, payload=None):
                if args[1] == 'ps':
                    return C.PREFIX + 'worker'
                item = container('worker')
                item[field] = value
                return json.dumps(item)
            with patch.object(C, 'command', side_effect=changed):
                values = {n: v for n, v, _ in C.role_values()['worker']}
            self.assertEqual(values['HealthyCount'], 0)
            self.assertEqual(values['OOMKilledCount'], int(field == 'oom'))
        for field in ('memory', 'reservation', 'swap', 'nano_cpus', 'cpu_period', 'cpu_quota', 'pids'):
            def changed(args, payload=None):
                if args[1] == 'ps':
                    return C.PREFIX + 'gateway'
                item = container('gateway')
                item[field] += 1
                return json.dumps(item)
            with patch.object(C, 'command', side_effect=changed):
                self.assertEqual(dict((n, v) for n, v, _ in C.role_values()['gateway'])
                                 ['ResourceCapDrift'], 1)

    def test_scoped_inspect_no_secret_fields(self):
        with patch.object(C, 'command', side_effect=docker) as run:
            values = C.role_values()
        self.assertEqual(len(run.call_args_list), 4)
        for role, call in zip(C.ROLES, run.call_args_list[1:]):
            self.assertEqual(call.args[0][-1], C.PREFIX + role)
            self.assertIn('--format', call.args[0])
        self.assertNotIn('Config.Env', C.PROJECTION)
        self.assertNotIn('json .State}}', C.PROJECTION)
        for role in C.ROLES:
            sample = dict((n, v) for n, v, _ in values[role])
            self.assertEqual(sample['HealthyCount'], 1)
            self.assertEqual(sample['ResourceCapDrift'], 0)

    def test_timer_and_service_contract(self):
        directory = Path(__file__).resolve().parents[1]
        unit = (directory / 'ec2-runtime-metrics.service').read_text()
        timer = (directory / 'ec2-runtime-metrics.timer').read_text()
        self.assertIn('Type=oneshot', unit)
        self.assertIn('/usr/bin/python3 -I /opt/sub2api-runtime-metrics/', unit)
        self.assertNotIn('Requires=docker', unit)
        self.assertIn('AWS_SHARED_CREDENTIALS_FILE=/dev/null', unit)
        self.assertIn('OnCalendar=*-*-* *:*:00', timer)
        self.assertIn('Unit=ec2-runtime-metrics.service', timer)

    def test_malformed_or_raced_inspect_fails_closed(self):
        for response in ('not json', '{}', json.dumps({**container('worker'), 'oom': 'false'})):
            with patch.object(C, 'command', side_effect=[C.PREFIX + 'worker', response]):
                with self.assertRaises((ValueError, KeyError, C.CollectorError)):
                    C.role_values()
        with patch.object(C, 'command', side_effect=[C.PREFIX + 'worker', C.CollectorError()]):
            with self.assertRaises(C.CollectorError):
                C.role_values()

    def test_proc_memory_and_root_disk(self):
        disk = type('Disk', (), {'f_blocks': 100, 'f_bavail': 20})()
        with patch.object(Path, 'read_text', return_value='MemTotal: 1000 kB\nMemAvailable: 250 kB\n'), \
                patch.object(C.os, 'statvfs', return_value=disk) as statvfs:
            values = {n: v for n, v, _ in C.host_values()}
        self.assertEqual(values, {'MemoryAvailableBytes': 256000,
                                 'MemoryUsedPercent': 75, 'RootDiskUsedPercent': 80})
        statvfs.assert_called_once_with('/')

    def test_publish_secure_tempfile_and_cleanup(self):
        for fail in (False, True):
            paths = []
            def inspect_payload(argv, payload=None):
                self.assertEqual(argv[:3], ['aws', 'cloudwatch', 'put-metric-data'])
                self.assertEqual(argv[argv.index('--region') + 1], 'us-west-2')
                uri = argv[argv.index('--cli-input-json') + 1]
                self.assertTrue(uri.startswith('file:///'))
                path = Path(uri.removeprefix('file://'))
                paths.append(path)
                self.assertNotEqual(str(path), '/dev/stdin')
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertEqual(json.loads(path.read_text())['Namespace'], 'Sub2API/Runtime')
                self.assertIsNone(payload)
                if fail:
                    raise C.CollectorError('mock publication failure')
            with patch.object(C, 'command', side_effect=inspect_payload) as run:
                if fail:
                    with self.assertRaises(C.CollectorError):
                        C.publish(C.metrics(CONFIG, False))
                else:
                    C.publish(C.metrics(CONFIG, False))
                run.assert_called_once()
            self.assertEqual(len(paths), 1)
            self.assertFalse(paths[0].exists())

    def test_subprocess_timeout_and_redaction(self):
        for error in (subprocess.TimeoutExpired('x', 30, stderr='secret'),
                      subprocess.CalledProcessError(1, 'x', stderr='secret'), FileNotFoundError()):
            with patch.object(C.subprocess, 'run', side_effect=error):
                with self.assertRaises(C.CollectorError) as caught:
                    C.command(['docker', 'ps'])
            self.assertNotIn('secret', str(caught.exception))

    def test_secure_config(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            path.write_text(json.dumps(CONFIG))
            path.chmod(0o600)
            # Mock uid only so this test also runs as a non-root developer.
            original = os.fstat
            def root_stat(fd):
                info = original(fd)
                return type('Stat', (), {'st_uid': 0, 'st_mode': info.st_mode})()
            with patch.object(C.os, 'fstat', side_effect=root_stat):
                self.assertEqual(C.load_config(str(path)), CONFIG)
                path.chmod(0o644)
                with self.assertRaises(C.CollectorError):
                    C.load_config(str(path))
                path.chmod(0o600)
                for config in ({**CONFIG, 'instance_id': 'i-00000000000000000'},
                               {**CONFIG, 'region': 'us-east-1'}, {**CONFIG, 'extra': 'x'}):
                    path.write_text(json.dumps(config))
                    with self.assertRaises(C.CollectorError):
                        C.load_config(str(path))
                link = Path(directory) / 'link'
                link.symlink_to(path)
                with self.assertRaises(C.CollectorError):
                    C.load_config(str(link))

    def test_invalid_configuration_never_calls_commands(self):
        with patch.object(C, 'load_config', side_effect=C.CollectorError()), \
                patch.object(C, 'command') as run, \
                contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(C.main([]), 1)
        run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
