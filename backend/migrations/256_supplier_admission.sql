-- Accounts contain vault references only; the supplier vault remains authoritative.
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS verified_credential_version BIGINT;
-- Only a successful quota probe can verify the current credential version.
CREATE OR REPLACE FUNCTION supplier_resource_verify_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.last_probe_at IS DISTINCT FROM OLD.last_probe_at AND NEW.probe_state='fresh'
    AND NEW.probe_error_code IS NULL AND NEW.usage IS NOT NULL
    AND NEW.last_usage_at=NEW.last_probe_at THEN
  NEW.verified_credential_version:=NEW.credential_version;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS supplier_resource_verify_probe ON supplier_resources;
CREATE TRIGGER supplier_resource_verify_probe BEFORE UPDATE OF last_probe_at ON supplier_resources FOR EACH ROW EXECUTE FUNCTION supplier_resource_verify_probe();
CREATE TABLE IF NOT EXISTS supplier_account_bindings (
 resource_id UUID PRIMARY KEY REFERENCES supplier_resources(id),
 account_id BIGINT NOT NULL UNIQUE REFERENCES accounts(id),
 supplier_id BIGINT NOT NULL REFERENCES supplier_profiles(id),
 credential_version BIGINT NOT NULL CHECK (credential_version > 0),
 checked_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS supplier_account_bindings_reconcile ON supplier_account_bindings(checked_at,resource_id);
CREATE OR REPLACE FUNCTION supplier_account_binding_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND (NEW.resource_id<>OLD.resource_id OR NEW.account_id<>OLD.account_id OR NEW.supplier_id<>OLD.supplier_id) THEN
  RAISE EXCEPTION 'supplier account binding identity is immutable';
 END IF;
 IF (TG_OP='INSERT' OR NEW.credential_version<>OLD.credential_version) AND NOT EXISTS (SELECT 1 FROM supplier_resources WHERE id=NEW.resource_id AND supplier_id=NEW.supplier_id AND credential_version=NEW.credential_version) THEN
  RAISE EXCEPTION 'supplier account binding resource/version mismatch';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS supplier_account_binding_immutable ON supplier_account_bindings;
CREATE TRIGGER supplier_account_binding_immutable BEFORE INSERT OR UPDATE ON supplier_account_bindings FOR EACH ROW EXECUTE FUNCTION supplier_account_binding_immutable();
