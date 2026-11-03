"""Offline contract tests; no AWS/database operations."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('cutover', Path(__file__).with_name('check_database_cutover.py'))
cutover = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cutover)


class CutoverGateTests(unittest.TestCase):
    def fixture(self):
        return dict.fromkeys(cutover.REQUIRED, True) | {
            'method': 'snapshot', 'target_endpoint_kind': 'writer',
            'rollback_phase': 'before_target_writes'}

    def test_complete_evidence(self):
        self.assertTrue(cutover.validate(self.fixture()))

    def test_each_missing_check_blocks(self):
        for field in cutover.REQUIRED:
            with self.subTest(field=field):
                evidence = self.fixture()
                evidence[field] = False
                with self.assertRaises(ValueError):
                    cutover.validate(evidence)

    def test_truthy_strings_are_not_evidence(self):
        evidence = self.fixture()
        evidence['source_writes_stopped'] = 'true'
        with self.assertRaises(ValueError):
            cutover.validate(evidence)

    def test_reader_endpoint_blocked(self):
        evidence = self.fixture()
        evidence['target_endpoint_kind'] = 'reader'
        with self.assertRaises(ValueError):
            cutover.validate(evidence)

    def test_after_write_endpoint_rollback_blocked(self):
        evidence = self.fixture()
        evidence['rollback_phase'] = 'after_target_writes'
        with self.assertRaises(ValueError):
            cutover.validate(evidence)

    def test_unknown_fields_blocked(self):
        evidence = self.fixture() | {'password': 'not-a-real-secret'}
        with self.assertRaises(ValueError):
            cutover.validate(evidence)


if __name__ == '__main__':
    unittest.main()
