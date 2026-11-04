-- Native Kiro secrets never enter accounts.credentials or scheduler snapshots.
CREATE TABLE kiro_credentials (
 id UUID PRIMARY KEY,
 account_id BIGINT NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,
 version BIGINT NOT NULL CHECK(version > 0),
 mode TEXT NOT NULL CHECK(mode IN ('social','idc')),
 region TEXT NOT NULL CHECK(region IN ('us-east-1','us-west-2')),
 envelope BYTEA NOT NULL,
 user_agent TEXT NOT NULL,
 amz_user_agent TEXT NOT NULL,
 machine_id TEXT NOT NULL DEFAULT '',
 refresh_state TEXT NOT NULL DEFAULT 'idle' CHECK(refresh_state IN ('idle','pending','ambiguous')),
 refresh_owner UUID,
 refresh_started_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Expiration NEVER releases a refresh claim. Pending/ambiguous requires explicit
-- reauthorization; an uncertain refresh token is never replayed.
