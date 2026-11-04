-- Explicit administrative observations only. No background upstream polling.
ALTER TABLE kiro_credentials ADD COLUMN models_snapshot JSONB NOT NULL DEFAULT '{"state":"unknown","source":"not_observed"}';
ALTER TABLE kiro_credentials ADD COLUMN quota_snapshot JSONB NOT NULL DEFAULT '{"state":"unknown","source":"not_observed"}';

-- All legacy/bulk account writers share this safety boundary too.
CREATE FUNCTION guard_kiro_account_runtime() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.platform='kiro' THEN
  IF NEW.platform IS DISTINCT FROM OLD.platform OR NEW.type IS DISTINCT FROM OLD.type OR NEW.credentials IS DISTINCT FROM OLD.credentials THEN
   RAISE EXCEPTION 'Kiro identity changes require dedicated lifecycle API';
  END IF;
 END IF;
 IF NEW.platform='kiro' THEN
  IF NEW.type<>'oauth' OR jsonb_typeof(NEW.credentials) IS DISTINCT FROM 'object' OR NOT (NEW.credentials ? 'kiro_credential_ref') OR (NEW.credentials - 'kiro_credential_ref') <> '{}'::jsonb OR jsonb_typeof(NEW.credentials->'kiro_credential_ref') IS DISTINCT FROM 'string' THEN
   RAISE EXCEPTION 'Kiro accounts require reference-only credentials';
  END IF;
  NEW.status := 'disabled'; NEW.schedulable := false;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER kiro_account_runtime_guard BEFORE INSERT OR UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION guard_kiro_account_runtime();
