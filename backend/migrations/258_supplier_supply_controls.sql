-- No existing resource is implicitly consented. A fresh explicit import is required.
ALTER TABLE supplier_resources
 ADD COLUMN IF NOT EXISTS supply_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS supply_enable_pending BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS admin_disabled BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS consent_version TEXT NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS authorization_version BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS authorization_expires_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS authorization_revoked_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS last_verified_at TIMESTAMPTZ;

-- Append-only metadata, never credentials, upstream identities or raw session JSON.
CREATE TABLE IF NOT EXISTS supplier_authorization_events (
 resource_id UUID NOT NULL REFERENCES supplier_resources(id),
 supplier_id BIGINT NOT NULL REFERENCES supplier_profiles(id),
 authorization_version BIGINT NOT NULL CHECK(authorization_version>0),
 credential_version BIGINT NOT NULL CHECK(credential_version>0),
 consent_version TEXT NOT NULL CHECK(consent_version='all_available_continuous_v1'),
 event TEXT NOT NULL CHECK(event IN ('authorized','revoked')),
 authorization_expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(resource_id,authorization_version,event)
);
CREATE OR REPLACE FUNCTION supplier_authorization_event_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'supplier authorization events are immutable';
END $$;
DROP TRIGGER IF EXISTS supplier_authorization_event_immutable ON supplier_authorization_events;
CREATE TRIGGER supplier_authorization_event_immutable BEFORE UPDATE OR DELETE ON supplier_authorization_events FOR EACH ROW EXECUTE FUNCTION supplier_authorization_event_immutable();

CREATE OR REPLACE FUNCTION supplier_resource_authorization_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.authorization_version<0 OR (TG_OP='UPDATE' AND NEW.authorization_version IS DISTINCT FROM OLD.authorization_version AND NEW.authorization_version<>OLD.authorization_version+1) THEN
  RAISE EXCEPTION 'invalid supplier authorization version';
 END IF;
 IF NEW.authorization_version>0 AND (TG_OP='INSERT' OR NEW.authorization_version IS DISTINCT FROM OLD.authorization_version) THEN
  IF NEW.consent_version<>'all_available_continuous_v1' OR NEW.authorization_expires_at IS NULL
     OR NEW.authorization_expires_at>NEW.token_expires_at
     OR NEW.authorization_expires_at<=CURRENT_TIMESTAMP OR NEW.authorization_revoked_at IS NOT NULL THEN
   RAISE EXCEPTION 'invalid supplier authorization';
  END IF;
  IF TG_OP='UPDATE' AND NEW.authorization_version<>OLD.authorization_version+1 THEN
   RAISE EXCEPTION 'invalid supplier authorization version';
  END IF;
  INSERT INTO supplier_authorization_events(resource_id,supplier_id,authorization_version,credential_version,consent_version,event,authorization_expires_at)
  VALUES(NEW.id,NEW.supplier_id,NEW.authorization_version,NEW.credential_version,NEW.consent_version,'authorized',NEW.authorization_expires_at);
 ELSIF TG_OP='UPDATE' AND (NEW.consent_version IS DISTINCT FROM OLD.consent_version OR NEW.authorization_expires_at IS DISTINCT FROM OLD.authorization_expires_at) THEN
  RAISE EXCEPTION 'supplier authorization requires a new version';
 END IF;
 IF TG_OP='UPDATE' AND OLD.authorization_revoked_at IS NOT NULL AND NEW.authorization_revoked_at IS NULL AND NEW.authorization_version=OLD.authorization_version THEN
  RAISE EXCEPTION 'revoked supplier authorization cannot be restored';
 END IF;
 IF NEW.authorization_version>0 AND NEW.authorization_revoked_at IS NOT NULL AND (TG_OP='INSERT' OR OLD.authorization_revoked_at IS NULL) THEN
  INSERT INTO supplier_authorization_events(resource_id,supplier_id,authorization_version,credential_version,consent_version,event,authorization_expires_at)
  VALUES(NEW.id,NEW.supplier_id,NEW.authorization_version,NEW.credential_version,NEW.consent_version,'revoked',NEW.authorization_expires_at);
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS supplier_resource_authorization_event ON supplier_resources;
CREATE TRIGGER supplier_resource_authorization_event AFTER INSERT OR UPDATE ON supplier_resources FOR EACH ROW EXECUTE FUNCTION supplier_resource_authorization_event();

-- Independent success timestamp: claiming or failing a probe cannot renew it.
CREATE OR REPLACE FUNCTION supplier_resource_verify_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.last_probe_at IS DISTINCT FROM OLD.last_probe_at AND NEW.probe_state='fresh'
    AND NEW.probe_error_code IS NULL AND NEW.usage IS NOT NULL
    AND NEW.last_usage_at=NEW.last_probe_at THEN
  NEW.verified_credential_version:=NEW.credential_version;
  NEW.last_verified_at:=NEW.last_probe_at;
  IF NEW.supply_enable_pending AND NEW.consent_version='all_available_continuous_v1'
     AND NEW.authorization_version>0 AND NEW.authorization_revoked_at IS NULL
     AND NEW.authorization_expires_at>CURRENT_TIMESTAMP AND NEW.token_expires_at>CURRENT_TIMESTAMP THEN
   NEW.supply_enabled:=TRUE;
   NEW.supply_enable_pending:=FALSE;
  END IF;
 END IF;
 RETURN NEW;
END $$;

-- JSON numbers must be present; missing, null, strings, expired or malformed
-- windows never become zero usage. Numeric casts are guarded by JSON type checks.
CREATE OR REPLACE FUNCTION supplier_quota_window_available(w JSONB, at_time TIMESTAMPTZ) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN jsonb_typeof(w->'used_percent')='number'
                  AND jsonb_typeof(w->'limit_window_seconds')='number'
                  AND jsonb_typeof(w->'reset_at')='number'
 THEN (w->>'used_percent')::numeric>=0 AND (w->>'used_percent')::numeric<100
  AND (w->>'limit_window_seconds')::numeric>0
  AND (w->>'reset_at')::numeric>extract(epoch FROM at_time)
  AND (w->>'reset_at')::numeric<=extract(epoch FROM at_time)+(w->>'limit_window_seconds')::numeric
 ELSE FALSE END
$$;
CREATE OR REPLACE FUNCTION supplier_quota_available(usage JSONB, at_time TIMESTAMPTZ) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(usage->'rate_limit'->'allowed'='true'::jsonb
  AND usage->'rate_limit'->'limit_reached'='false'::jsonb
  AND (supplier_quota_window_available(usage->'rate_limit'->'primary_window',at_time)
    OR (usage->'primary_window_known'='true'::jsonb AND COALESCE(usage->'rate_limit'->'primary_window','null'::jsonb)='null'::jsonb))
  AND (supplier_quota_window_available(usage->'rate_limit'->'secondary_window',at_time)
    OR (usage->'secondary_window_known'='true'::jsonb AND COALESCE(usage->'rate_limit'->'secondary_window','null'::jsonb)='null'::jsonb))
  AND (supplier_quota_window_available(usage->'rate_limit'->'primary_window',at_time)
    OR supplier_quota_window_available(usage->'rate_limit'->'secondary_window',at_time)),FALSE)
$$;
