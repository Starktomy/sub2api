-- Only explicit JSON booleans from a successful identity probe authorize supply.
-- Legacy usage snapshots remain intact but fail closed until re-probed. Do not
-- backfill identity flags or persist upstream phone numbers/emails here.
CREATE OR REPLACE FUNCTION supplier_identity_verified(usage JSONB) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(usage->'phone_verified'='true'::jsonb
  AND usage->'email_matched'='true'::jsonb,FALSE)
$$;
