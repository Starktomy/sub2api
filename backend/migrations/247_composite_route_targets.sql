-- Store ordered fallback targets without changing the one-public-model-per-route
-- invariant. Existing routes keep an empty array and are interpreted as a
-- single target from target_platform/upstream_model/priority.
ALTER TABLE composite_model_routes
    ADD COLUMN IF NOT EXISTS targets JSONB NOT NULL DEFAULT '[]'::jsonb;
