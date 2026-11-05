-- P0 durable dispatch accounting. No credentials, request bodies, output text or
-- headers belong here. Historical supplier_usage rows are not backfilled.
CREATE TABLE IF NOT EXISTS supplier_dispatches (
 attempt_id UUID PRIMARY KEY,
 request_id UUID NOT NULL,
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 snapshot_digest VARCHAR(64) NOT NULL CHECK(snapshot_digest ~ '^[0-9a-f]{64}$'),
 user_id BIGINT NOT NULL CHECK(user_id>0),
 api_key_id BIGINT NOT NULL CHECK(api_key_id>0),
 account_id BIGINT NOT NULL CHECK(account_id>0),
 group_id BIGINT NOT NULL CHECK(group_id>0),
 state TEXT NOT NULL DEFAULT 'accepted' CHECK(state IN ('accepted','dispatched','observed','pending','committed','review')),
 observation JSONB,
 observation_hash VARCHAR(64),
 command_payload TEXT,
 command_hash VARCHAR(64),
 review_reason TEXT NOT NULL DEFAULT '',
 retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count>=0),
 next_retry_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 lease_token UUID,
 lease_until TIMESTAMPTZ,
 repair_pending BOOLEAN NOT NULL DEFAULT FALSE,
 repair_stage SMALLINT NOT NULL DEFAULT 0 CHECK(repair_stage IN (0,1)),
 repair_available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 dispatched_at TIMESTAMPTZ,
 observed_at TIMESTAMPTZ,
 staged_at TIMESTAMPTZ,
 committed_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 CHECK((command_payload IS NULL)=(command_hash IS NULL)),
 CHECK(command_payload IS NULL OR (octet_length(command_payload)<=16384 AND command_hash ~ '^[0-9a-f]{64}$')),
 CHECK((observation IS NULL)=(observation_hash IS NULL)),
 CHECK(observation IS NULL OR (octet_length(observation::text)<=8192 AND observation_hash ~ '^[0-9a-f]{64}$')),
 CHECK(state IN ('accepted','review') OR dispatched_at IS NOT NULL),
 CHECK(state NOT IN ('pending','committed') OR command_payload IS NOT NULL),
 CHECK(state<>'committed' OR committed_at IS NOT NULL),
 CHECK(NOT repair_pending OR state='committed')
);
CREATE INDEX IF NOT EXISTS supplier_dispatch_pending_idx ON supplier_dispatches(next_retry_at,attempt_id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS supplier_dispatch_stale_idx ON supplier_dispatches(updated_at,attempt_id) WHERE state IN ('accepted','dispatched','observed');
CREATE INDEX IF NOT EXISTS supplier_dispatch_repair_idx ON supplier_dispatches(committed_at,attempt_id) WHERE repair_pending;
CREATE INDEX IF NOT EXISTS supplier_dispatch_request_idx ON supplier_dispatches(request_id);

-- Every distinct typed observation survives even if a later observation conflicts
-- with it. Reconciliation must never manufacture zero consumption from absence.
CREATE TABLE IF NOT EXISTS supplier_dispatch_observations (
 attempt_id UUID NOT NULL REFERENCES supplier_dispatches(attempt_id),
 observation_hash VARCHAR(64) NOT NULL CHECK(observation_hash ~ '^[0-9a-f]{64}$'),
 observation JSONB NOT NULL CHECK(jsonb_typeof(observation)='object' AND octet_length(observation::text)<=8192),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(attempt_id,observation_hash)
);
CREATE OR REPLACE FUNCTION supplier_dispatch_observation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'supplier dispatch observations are immutable';
END $$;
DROP TRIGGER IF EXISTS supplier_dispatch_observation_immutable ON supplier_dispatch_observations;
CREATE TRIGGER supplier_dispatch_observation_immutable BEFORE UPDATE OR DELETE ON supplier_dispatch_observations FOR EACH ROW EXECUTE FUNCTION supplier_dispatch_observation_immutable();

CREATE OR REPLACE FUNCTION supplier_dispatch_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'supplier dispatch history is immutable'; END IF;
 IF ROW(NEW.attempt_id,NEW.request_id,NEW.snapshot,NEW.snapshot_digest,NEW.user_id,NEW.api_key_id,NEW.account_id,NEW.group_id,NEW.accepted_at)
    IS DISTINCT FROM ROW(OLD.attempt_id,OLD.request_id,OLD.snapshot,OLD.snapshot_digest,OLD.user_id,OLD.api_key_id,OLD.account_id,OLD.group_id,OLD.accepted_at) THEN
  RAISE EXCEPTION 'supplier dispatch snapshot is immutable';
 END IF;
 IF OLD.command_payload IS NOT NULL AND ROW(NEW.command_payload,NEW.command_hash,NEW.staged_at) IS DISTINCT FROM ROW(OLD.command_payload,OLD.command_hash,OLD.staged_at) THEN
  RAISE EXCEPTION 'supplier dispatch command is immutable';
 END IF;
 IF OLD.command_payload IS NOT NULL AND ROW(NEW.observation,NEW.observation_hash) IS DISTINCT FROM ROW(OLD.observation,OLD.observation_hash) THEN
  RAISE EXCEPTION 'staged supplier observation is immutable';
 END IF;
 IF OLD.dispatched_at IS NOT NULL AND NEW.dispatched_at IS DISTINCT FROM OLD.dispatched_at THEN
  RAISE EXCEPTION 'supplier dispatch send marker is immutable';
 END IF;
 IF OLD.state='committed' AND (NEW.state<>'committed' OR NEW.committed_at IS DISTINCT FROM OLD.committed_at) THEN
  RAISE EXCEPTION 'supplier dispatch commit is immutable';
 END IF;
 IF NEW.state='accepted' AND OLD.state<>'accepted' THEN RAISE EXCEPTION 'supplier dispatch cannot be resent'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS supplier_dispatch_guard ON supplier_dispatches;
CREATE TRIGGER supplier_dispatch_guard BEFORE UPDATE OR DELETE ON supplier_dispatches FOR EACH ROW EXECUTE FUNCTION supplier_dispatch_guard();

ALTER TABLE supplier_usage
 ADD COLUMN IF NOT EXISTS supplier_attempt_id UUID,
 ADD COLUMN IF NOT EXISTS supplier_snapshot_digest VARCHAR(64),
 ADD COLUMN IF NOT EXISTS authorization_version BIGINT,
 ADD COLUMN IF NOT EXISTS consent_version TEXT,
 ADD COLUMN IF NOT EXISTS group_id BIGINT;
CREATE UNIQUE INDEX IF NOT EXISTS supplier_usage_attempt_idx ON supplier_usage(supplier_attempt_id) WHERE supplier_attempt_id IS NOT NULL;
-- The existing immutable supplier_usage trigger remains in force.
