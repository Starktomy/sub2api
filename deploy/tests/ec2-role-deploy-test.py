#!/usr/bin/env python3
"""Offline tests: no Docker, systemd, network, or cloud operations."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / 'ec2-role-deploy.py'
spec = importlib.util.spec_from_file_location('host_deploy', SCRIPT)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class Contracts(unittest.TestCase):
    def config(self):
        return {'image': 'registry.example/sub2api@sha256:' + 'a' * 64,
                'env_file': '/external/runtime.env', 'ca_file': '/external/ca.pem',
                'scheduler_publications': True, 'bind_ip': '127.0.0.1',
                'gateway_port': 18080, 'control_port': 18081}

    def test_digest_and_bind_gate(self):
        with patch.object(m, 'secure_file', side_effect=lambda p, **k: p), \
                patch.object(Path, 'read_text', return_value='JWT_SECRET=not-real\n'), \
                patch.object(m, 'command'):
            self.assertEqual(m.validate(self.config()), self.config())
            for key, value in [('image', 'registry.example/app:latest'),
                               ('image', 'sha256:' + 'a' * 64),
                               ('bind_ip', '0.0.0.0'), ('bind_ip', '8.8.8.8'),
                               ('scheduler_publications', 'true'), ('gateway_port', 80)]:
                config = self.config()
                config[key] = value
                with self.assertRaises(ValueError):
                    m.validate(config)

    def test_dsn_rejected(self):
        with patch.object(m, 'secure_file', side_effect=lambda p, **k: p), \
                patch.object(Path, 'read_text', return_value='DATABASE_DSN=forbidden'):
            with self.assertRaises(ValueError):
                m.validate(self.config())

    def test_start_security_and_budgets(self):
        calls = []
        with patch.object(m, 'preflight'), patch.object(m, 'wait_healthy'), \
                patch.object(m, 'command', side_effect=lambda *a, **k: calls.append(a)):
            m.start(self.config())
        self.assertEqual(len(calls), 3)
        for role, args in zip(m.ROLES, calls):
            text = ' '.join(args)
            for required in ['--pull=never', '--restart=no', '--read-only',
                             '--cap-drop=ALL', '--user 10001:10001',
                             '--security-opt=no-new-privileges:true',
                             'DATABASE_MIGRATION_MODE=check',
                             'DATABASE_SSLMODE=verify-full',
                             'RUNTIME_SCHEDULER_PUBLICATIONS=true',
                             f'RUNTIME_ROLE={role}', '--memory-reservation']:
                self.assertIn(required, text)
            self.assertEqual(args[-3], self.config()['image'])
            self.assertEqual(args[-2:], ('-ec', m.SAFE_START))
            self.assertIn('CONFIG_FILE=/app/data/config.yaml', args)
            self.assertEqual(args[args.index('--entrypoint') + 1], '/bin/sh')
            self.assertEqual('-p' in args, role != 'worker')
        self.assertEqual(sum(b[0] for b in m.BUDGET.values()), 2304)
        self.assertLessEqual(sum(float(b[2]) for b in m.BUDGET.values()), 1.75)

    def test_safe_config_constant(self):
        import subprocess
        subprocess.run(['sh', '-n'], input=m.SAFE_START, text=True, check=True)
        for line in ['remote_url: ""', 'hash_url: ""',
                     'fallback_file: /app/resources/model-pricing/model_prices_and_context_window.json',
                     '> /app/data/config.yaml; exec /app/cloud-entrypoint']:
            self.assertIn(line, m.SAFE_START)
        self.assertNotIn('$', m.SAFE_START)
        self.assertNotIn('`', m.SAFE_START)

    def test_deploy_waits_for_pending_commit(self):
        candidate = self.config()
        pending = {'current': candidate, 'previous': None, 'pending': True}
        committed = dict(pending, pending=False)
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(m, 'STATE', Path(directory)), \
                patch.object(m.sys, 'argv', ['deploy.py', 'deploy', '/external/release.json']), \
                patch.object(m.os, 'geteuid', return_value=0), \
                patch.object(m, 'secure_file', return_value='/external/release.json'), \
                patch.object(Path, 'read_text', return_value=json.dumps(candidate)), \
                patch.object(m, 'validate', return_value=candidate), \
                patch.object(m, 'command'), patch.object(m, 'stop'), \
                patch.object(m, 'preflight'), patch.object(m, 'save'), \
                patch.object(m, 'load', side_effect=[pending, committed]) as load, \
                patch.object(m, 'healthy', return_value=True) as healthy, \
                patch.object(m.time, 'sleep') as sleep:
            m.main()
            self.assertEqual(load.call_count, 2)
            healthy.assert_called_once_with(candidate)
            sleep.assert_called_once_with(2)

    def test_absent_container_inspect(self):
        for output in ('', '[]', '[]\n'):
            with self.subTest(output=output), patch.object(m, 'command', return_value=output):
                self.assertIsNone(m.inspect('worker'))
                m.stop()
                self.assertFalse(m.healthy(self.config()))

    def test_absent_network_inspect(self):
        network = {'Driver': 'bridge', 'EnableIPv6': False,
                   'Options': {'com.docker.network.bridge.name': 'sub2api0'}}
        for output in ('', '[]', '[]\n'):
            with self.subTest(output=output), patch.object(
                    m, 'command', side_effect=[output, 'network-id', json.dumps([network])]) as command:
                m.network()
                self.assertEqual(command.call_args_list[1].args[:3],
                                 ('docker', 'network', 'create'))
        with patch.object(m, 'command', side_effect=['[]', 'network-id', '[]']):
            with self.assertRaisesRegex(RuntimeError, 'absent after creation'):
                m.network()

    def test_stop_order_and_no_start_old(self):
        calls = []
        with patch.object(m, 'inspect', return_value={'Config': {'Labels': {m.LABEL: 'true'}}}), \
                patch.object(m, 'command', side_effect=lambda *a, **k: calls.append(a)):
            m.stop()
        self.assertEqual(calls[0], ('docker', 'stop', '--time', '120', m.name('worker')))
        self.assertTrue(all(c[1] in ('stop', 'rm') for c in calls))
        with patch.object(m, 'inspect', return_value={'Config': {'Labels': {}}}):
            with self.assertRaises(RuntimeError):
                m.stop()

    def test_rollback_stops_candidate_before_previous(self):
        events = []
        current, previous = self.config(), dict(self.config(), scheduler_publications=False)
        m.STOPPING = False
        def start(config):
            events.append(('start', config['scheduler_publications']))
            if config['scheduler_publications']:
                raise RuntimeError('candidate unhealthy')
            m.STOPPING = True
        try:
            with patch.object(m, 'network'), \
                    patch.object(m, 'load', return_value={'current': current, 'previous': previous, 'pending': True}), \
                    patch.object(m, 'validate', side_effect=lambda c: c), \
                    patch.object(m, 'stop', side_effect=lambda: events.append(('stop',))), \
                    patch.object(m, 'save'), patch.object(m, 'start', side_effect=start):
                m.supervise()
            self.assertEqual(events, [('stop',), ('start', True), ('stop',),
                                      ('start', False), ('stop',)])
        finally:
            m.STOPPING = False

    def test_conflicting_worker_fails_closed(self):
        def command(*args, **kwargs):
            if args[:3] == ('docker', 'ps', '-q'):
                return 'old'
            return json.dumps([{'Config': {'Env': ['RUNTIME_ROLE=worker']}}])
        with patch.object(m, 'command', side_effect=command):
            with self.assertRaises(RuntimeError):
                m.preflight()

    def test_health_is_all_three_not_worker_leadership(self):
        good = {'Config': {'Image': self.config()['image']},
                'State': {'Running': True, 'Health': {'Status': 'healthy'}}}
        with patch.object(m, 'inspect', side_effect=[good, good, None]):
            self.assertFalse(m.healthy(self.config()))
        with patch.object(m, 'inspect', return_value=good):
            self.assertTrue(m.healthy(self.config()))

    def test_atomic_state(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(m, 'STATE', Path(directory)):
            state = {'current': self.config(), 'previous': None}
            m.save(state)
            self.assertEqual(m.load(), state)
            self.assertEqual((Path(directory) / 'state.json').stat().st_mode & 0o777, 0o600)

    def test_firewall_and_autoboot_contract(self):
        directory = SCRIPT.parent
        firewall = (directory / 'ec2-roles-imds.nft').read_text()
        self.assertIn('iifname "sub2api0" ip daddr 169.254.169.254 reject', firewall)
        self.assertIn('ip6 daddr fd00:ec2::254 reject', firewall)
        self.assertNotIn('flush ruleset', firewall)
        unit = (directory / 'ec2-roles.service').read_text()
        self.assertIn('Requires=docker.service sub2api-roles-imds.service', unit)
        self.assertIn('WantedBy=multi-user.target', unit)
        self.assertIn('TimeoutStopSec=420s', unit)
        self.assertIn('Restart=on-failure', unit)
        self.assertIn('KillMode=process', unit)


if __name__ == '__main__':
    unittest.main()
