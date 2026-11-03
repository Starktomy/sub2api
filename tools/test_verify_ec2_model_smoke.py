"""Offline readiness guard tests: no model, database, Redis, or AWS operations."""
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('smoke', Path(__file__).with_name('verify_ec2_model_smoke.py'))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class PublicationReadinessTests(unittest.TestCase):
    def fixture(self, status='active', members=None):
        account = {'ID': 2, 'Status': status, 'Schedulable': True,
                   'Platform': 'openai', 'GroupIDs': [42]}
        payloads = {key: json.dumps(account).encode() for key in ('slim:2', 'full:2')}
        manifest = {'Generation': 5, 'ID': 'a'*64,
                    'Buckets': {'42:openai:single': [2] if members is None else members},
                    'Payloads': {k: hashlib.sha256(v).hexdigest() for k, v in payloads.items()}}
        raw = json.dumps(manifest).encode()
        publication = {'generation': 5, 'publication_id': 'a'*64,
                       'manifest_digest': hashlib.sha256(raw).hexdigest()}
        return publication, raw, payloads

    def test_ready(self):
        self.assertTrue(smoke.publication_ready(*self.fixture(), 2, 42))

    def test_stale_disabled_account(self):
        self.assertFalse(smoke.publication_ready(*self.fixture(status='disabled'), 2, 42))

    def test_empty_or_wrong_pool(self):
        for members in ([], [1], [1, 2]):
            self.assertFalse(smoke.publication_ready(*self.fixture(members=members), 2, 42))

    def test_manifest_digest_mismatch(self):
        p, raw, payloads = self.fixture()
        self.assertFalse(smoke.publication_ready(p, raw+b' ', payloads, 2, 42))

    def test_full_and_slim_digest_mismatch(self):
        for key in ('full:2', 'slim:2'):
            p, raw, payloads = self.fixture()
            payloads[key] += b' '
            self.assertFalse(smoke.publication_ready(p, raw, payloads, 2, 42))

    def test_identity_mismatch(self):
        p, raw, payloads = self.fixture()
        p['generation'] = 6
        self.assertFalse(smoke.publication_ready(p, raw, payloads, 2, 42))

    def test_timeout_sends_no_http_generation(self):
        stack = object.__new__(smoke.Stack)
        stack.healthy = lambda: self.fail('zero deadline must not perform network operations')
        with self.assertRaisesRegex(RuntimeError, 'no model call sent'):
            stack.wait_ready(2, 42, timeout=0)


if __name__ == '__main__':
    unittest.main()
