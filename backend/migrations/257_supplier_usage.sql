-- Supplier usage facts are committed atomically with the existing customer
-- billing dedup/effects. This is not a balance, withdrawal, or settlement ledger.
-- Historical identifiers deliberately have no cascading foreign keys: deleting
-- an API key, account, or binding must not erase the accounting snapshot.
CREATE TABLE IF NOT EXISTS supplier_usage (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    request_fingerprint VARCHAR(64) NOT NULL,
    user_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    resource_id UUID NOT NULL,
    supplier_id BIGINT NOT NULL,
    credential_version BIGINT NOT NULL CHECK (credential_version >= 1),
    model TEXT NOT NULL,
    service_tier TEXT NOT NULL DEFAULT '',
    reasoning_effort TEXT NOT NULL DEFAULT '',
    billing_type SMALLINT NOT NULL,
    input_tokens BIGINT NOT NULL CHECK (input_tokens >= 0),
    output_tokens BIGINT NOT NULL CHECK (output_tokens >= 0),
    cache_creation_tokens BIGINT NOT NULL CHECK (cache_creation_tokens >= 0),
    cache_read_tokens BIGINT NOT NULL CHECK (cache_read_tokens >= 0),
    image_count BIGINT NOT NULL CHECK (image_count >= 0),
    media_type TEXT NOT NULL DEFAULT '',
    standard_cost NUMERIC(20,8) NOT NULL CHECK (standard_cost >= 0 AND standard_cost < 1000000000000),
    customer_charge NUMERIC(20,8) NOT NULL CHECK (customer_charge >= 0 AND customer_charge < 1000000000000),
    fee_mode TEXT NOT NULL CHECK (fee_mode IN ('standard_cost', 'customer_charge')),
    fee_ratio_bps INTEGER NOT NULL CHECK (fee_ratio_bps BETWEEN 0 AND 10000),
    policy_version BIGINT NOT NULL CHECK (policy_version >= 1),
    fee_base_amount NUMERIC(20,8) NOT NULL CHECK (fee_base_amount >= 0 AND fee_base_amount < 1000000000000),
    fee_amount NUMERIC(20,8) NOT NULL CHECK (fee_amount >= 0 AND fee_amount < 1000000000000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (request_id, api_key_id),
    CONSTRAINT supplier_usage_fee_base_matches_mode CHECK (
        fee_base_amount = CASE fee_mode WHEN 'standard_cost' THEN standard_cost ELSE customer_charge END
    ),
    CONSTRAINT supplier_usage_fee_matches_snapshot CHECK (
        fee_amount = ROUND(fee_base_amount * fee_ratio_bps / 10000::NUMERIC, 8)
    )
);

CREATE INDEX IF NOT EXISTS supplier_usage_supplier_created_idx ON supplier_usage (supplier_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS supplier_usage_resource_created_idx ON supplier_usage (resource_id, created_at DESC, id DESC);

-- Financial facts are append-only. Corrections must be represented separately,
-- never by repricing or erasing a previously committed request.
CREATE OR REPLACE FUNCTION supplier_usage_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'supplier usage facts are immutable';
END $$;
DROP TRIGGER IF EXISTS supplier_usage_immutable ON supplier_usage;
CREATE TRIGGER supplier_usage_immutable BEFORE UPDATE OR DELETE ON supplier_usage
FOR EACH ROW EXECUTE FUNCTION supplier_usage_reject_mutation();
