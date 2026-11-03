ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS auto_sync_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS credential_version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS probe_state VARCHAR(32) NOT NULL DEFAULT 'paused';
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS last_probe_at TIMESTAMPTZ;
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS next_probe_at TIMESTAMPTZ;
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS probe_error_code VARCHAR(64);
CREATE TABLE IF NOT EXISTS supplier_probe_jobs (
 resource_id UUID PRIMARY KEY REFERENCES supplier_resources(id) ON DELETE CASCADE,
 credential_version BIGINT NOT NULL,
 state VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','cancelled')),
 next_run_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 lease_owner UUID,
 lease_generation BIGINT NOT NULL DEFAULT 0,
 lease_until TIMESTAMPTZ,
 resource_version BIGINT,
 attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS supplier_probe_jobs_due ON supplier_probe_jobs(next_run_at) WHERE state IN ('pending','running');
CREATE OR REPLACE FUNCTION supplier_resource_probe_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.auto_sync_enabled AND NEW.status='paused' AND NEW.token_expires_at>CURRENT_TIMESTAMP THEN
  IF TG_OP='INSERT' OR NOT OLD.auto_sync_enabled THEN
   NEW.probe_state:='pending'; NEW.next_probe_at:=CURRENT_TIMESTAMP; NEW.probe_error_code:=NULL;
   INSERT INTO supplier_probe_jobs(resource_id,credential_version) VALUES(NEW.id,NEW.credential_version)
   ON CONFLICT(resource_id) DO UPDATE SET state='pending',credential_version=EXCLUDED.credential_version,next_run_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,lease_generation=supplier_probe_jobs.lease_generation+1,attempts=0;
  END IF;
 ELSE
  NEW.auto_sync_enabled:=FALSE; NEW.next_probe_at:=NULL;
  IF NEW.status='disconnected' THEN NEW.probe_state:='disconnected';
  ELSIF NEW.token_expires_at<=CURRENT_TIMESTAMP THEN NEW.probe_state:='reauthorization_required'; NEW.probe_error_code:='expired';
  ELSIF NEW.probe_state<>'reauthorization_required' THEN NEW.probe_state:='paused'; END IF;
  UPDATE supplier_probe_jobs SET state='cancelled',lease_owner=NULL,lease_until=NULL,lease_generation=lease_generation+1 WHERE resource_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
-- AFTER INSERT is necessary for the queue foreign key; state fields are initialized by the insert.
CREATE OR REPLACE FUNCTION supplier_resource_probe_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.auto_sync_enabled THEN INSERT INTO supplier_probe_jobs(resource_id,credential_version) VALUES(NEW.id,NEW.credential_version) ON CONFLICT DO NOTHING; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS supplier_resource_probe_insert ON supplier_resources;
CREATE TRIGGER supplier_resource_probe_insert AFTER INSERT ON supplier_resources FOR EACH ROW EXECUTE FUNCTION supplier_resource_probe_insert();
DROP TRIGGER IF EXISTS supplier_resource_probe_update ON supplier_resources;
CREATE TRIGGER supplier_resource_probe_update BEFORE UPDATE OF auto_sync_enabled,status ON supplier_resources FOR EACH ROW EXECUTE FUNCTION supplier_resource_probe_job();
