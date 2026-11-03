-- Durable provider-call barrier. Contains fencing metadata and one-way document
-- fingerprints only: never access/refresh tokens or provider error bodies.
-- Migration is deliberate opt-in: default legacy installations do not query this
-- table unless OAUTH_REFRESH_DURABLE_ENABLED=true (split cloud roles must enable).
CREATE TABLE IF NOT EXISTS oauth_refresh_state (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL CHECK (generation > 0),
    owner VARCHAR(64) NOT NULL,
    state VARCHAR(16) NOT NULL CHECK (state IN ('pending', 'completed', 'ambiguous')),
    input_fingerprint VARCHAR(64) NOT NULL,
    input_credentials_fingerprint VARCHAR(64) NOT NULL,
    input_refresh_token_fingerprint VARCHAR(64) NOT NULL,
    output_fingerprint VARCHAR(64),
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- A crashed/expired pending call is NOT safe to retry: the provider may have
-- rotated tokens. No automatic takeover or token replay is permitted. Operators
-- must stop all old holders and reauthorize before ResolveOAuthRefreshAmbiguous
-- advances the fence. Never delete rows to recover or reset their generation.
-- There is no encrypted recovery spool yet: a provider response lost before the
-- accounts+completed commit cannot be reconstructed; reauthorization is required.
