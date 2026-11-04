-- Durable inference barrier and append-only unpriced usage evidence. No customer ledger.
CREATE TABLE usage_attempts (
 id varchar(64) PRIMARY KEY,
 operation_id varchar(64) NOT NULL UNIQUE,
 account_id bigint NOT NULL CHECK (account_id > 0),
 credential_version bigint NOT NULL CHECK (credential_version > 0),
 request_digest varchar(64) NOT NULL,
 owner varchar(64) NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 send_intent boolean NOT NULL DEFAULT false,
 status varchar(32) NOT NULL DEFAULT 'prepared' CHECK (status IN ('prepared','send_intent','usage_unknown','observed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE usage_fact_revisions (
 attempt_id varchar(64) NOT NULL REFERENCES usage_attempts(id),
 revision bigint NOT NULL CHECK (revision > 0),
 observation_id varchar(64) NOT NULL,
 content_digest varchar(64) NOT NULL,
 fact jsonb NOT NULL CHECK (octet_length(fact::text) <= 16384),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (attempt_id,revision),
 UNIQUE (attempt_id,observation_id)
);
CREATE TABLE usage_reconciliation (
 attempt_id varchar(64) PRIMARY KEY REFERENCES usage_attempts(id),
 state varchar(16) NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','blocked')),
 reason varchar(32) NOT NULL DEFAULT 'usage_unknown' CHECK (reason IN ('usage_unknown','pricing_missing')),
 revision bigint NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX usage_reconciliation_pending_idx ON usage_reconciliation(updated_at,attempt_id);
-- Past facts cannot be rewritten or removed, including through application mistakes.
CREATE FUNCTION reject_usage_fact_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'usage facts are append only'; END;
$$;
CREATE TRIGGER usage_fact_immutable BEFORE UPDATE OR DELETE ON usage_fact_revisions
FOR EACH ROW EXECUTE FUNCTION reject_usage_fact_mutation();
