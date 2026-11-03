-- Encrypted supplier-only vault. No Account credentials or scheduler link.
CREATE TABLE IF NOT EXISTS supplier_resources (
 id UUID PRIMARY KEY,
 supplier_id BIGINT NOT NULL REFERENCES supplier_profiles(id),
 display_name VARCHAR(100) NOT NULL,
 identity_fingerprint VARCHAR(64) NOT NULL,
 account_hint VARCHAR(256) NOT NULL,
 token_fingerprint VARCHAR(64) NOT NULL,
 key_id VARCHAR(128) NOT NULL,
 envelope BYTEA,
 masked_email VARCHAR(320) NOT NULL,
 plan_type VARCHAR(64) NOT NULL DEFAULT '',
 token_expires_at TIMESTAMPTZ NOT NULL,
 status VARCHAR(16) NOT NULL CHECK(status IN ('paused','disconnected')),
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 usage JSONB,
 last_usage_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 CHECK((status='disconnected' AND envelope IS NULL) OR (status='paused' AND envelope IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS supplier_resources_owner ON supplier_resources(supplier_id,created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS supplier_resources_live_identity ON supplier_resources(identity_fingerprint) WHERE status='paused';
