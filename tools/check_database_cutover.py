#!/usr/bin/env python3
"""Offline cutover evidence gate. No credentials, DB calls or AWS operations.
Exit 0 means the supplied evidence is internally consistent, NOT a live safety verdict.
"""
import argparse
import json
import sys
from pathlib import Path

REQUIRED = {
    'source_writes_stopped', 'old_workers_joined', 'inflight_drained',
    'target_manifest_verified', 'target_permissions_verified',
    'target_data_reconciled', 'sequences_verified', 'extensions_verified',
    'writer_identity_verified', 'tls_hostname_verified',
    'single_writer_enforced', 'replication_caught_up',
}


def validate(evidence):
    if not isinstance(evidence, dict):
        raise ValueError('evidence must be an object')
    if set(evidence) != REQUIRED | {'method', 'target_endpoint_kind', 'rollback_phase'}:
        raise ValueError('unknown or missing evidence fields')
    if evidence['method'] not in ('snapshot', 'replica_promotion', 'logical_replication'):
        raise ValueError('unsupported migration method')
    if evidence['target_endpoint_kind'] != 'writer':
        raise ValueError('writer endpoint required for all authoritative operations')
    if evidence['rollback_phase'] != 'before_target_writes':
        raise ValueError('after target writes, endpoint-only rollback is forbidden')
    if any(evidence[key] is not True for key in REQUIRED):
        raise ValueError('all cutover evidence must be explicitly true')
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('evidence', type=Path, help='Nonsecret reviewed JSON evidence; never an env/state/secret file')
    args = parser.parse_args()
    try:
        validate(json.loads(args.evidence.read_text()))
    except (OSError, ValueError, TypeError):
        print('CUTOVER_BLOCKED: incomplete or inconsistent evidence (values omitted)', file=sys.stderr)
        return 1
    print('OFFLINE_EVIDENCE_VALID: does not execute or authorize a cutover')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
