package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type supplierProbeRepository struct{ db *sql.DB }

func NewSupplierProbeRepository(db *sql.DB) service.SupplierProbeRepository {
	return &supplierProbeRepository{db}
}
func (r *supplierProbeRepository) Reconcile(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, e := r.db.ExecContext(ctx, `WITH expired AS (SELECT id FROM supplier_resources WHERE auto_sync_enabled AND token_expires_at<=CURRENT_TIMESTAMP ORDER BY token_expires_at,id LIMIT 200 FOR UPDATE SKIP LOCKED)
 UPDATE supplier_resources r SET auto_sync_enabled=FALSE,supply_enabled=FALSE,supply_enable_pending=FALSE,probe_state='reauthorization_required',probe_error_code='expired',next_probe_at=NULL,version=version+1 FROM expired WHERE r.id=expired.id;
 INSERT INTO supplier_probe_jobs(resource_id,credential_version) SELECT r.id,r.credential_version FROM supplier_resources r LEFT JOIN supplier_probe_jobs j ON j.resource_id=r.id WHERE r.auto_sync_enabled AND r.status='paused' AND r.token_expires_at>CURRENT_TIMESTAMP AND (j.resource_id IS NULL OR j.credential_version<>r.credential_version) ORDER BY r.id LIMIT 200 ON CONFLICT(resource_id) DO UPDATE SET credential_version=EXCLUDED.credential_version,state='pending',next_run_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,lease_generation=supplier_probe_jobs.lease_generation+1 WHERE supplier_probe_jobs.credential_version<>EXCLUDED.credential_version`)
	if e != nil {
		return e
	}
	return reconcileSupplierAdmission(ctx, r.db)
}
func (r *supplierProbeRepository) Claim(ctx context.Context, owner string) (*service.SupplierProbeClaim, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = supplierControlTimeout(ctx, tx); e != nil {
		return nil, e
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT j.resource_id FROM supplier_probe_jobs j JOIN supplier_resources r ON r.id=j.resource_id WHERE r.auto_sync_enabled AND r.status='paused' AND r.token_expires_at>CURRENT_TIMESTAMP AND j.credential_version=r.credential_version AND ((j.state='pending' AND j.next_run_at<=CURRENT_TIMESTAMP) OR(j.state='running' AND j.lease_until<CURRENT_TIMESTAMP)) ORDER BY j.next_run_at LIMIT 1 FOR UPDATE OF j,r SKIP LOCKED`).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	resource, e := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET probe_state='running',version=version+1 WHERE id=$1 RETURNING `+resourceCols, id))
	if e != nil {
		return nil, e
	}
	c := &service.SupplierProbeClaim{Resource: resource, Owner: owner}
	e = tx.QueryRowContext(ctx, `UPDATE supplier_probe_jobs SET state='running',lease_owner=$2,lease_generation=lease_generation+1,lease_until=CURRENT_TIMESTAMP+INTERVAL '60 seconds',resource_version=$3 WHERE resource_id=$1 RETURNING lease_generation,attempts`, id, owner, resource.Version).Scan(&c.Generation, &c.Attempts)
	if e != nil {
		return nil, e
	}
	return c, tx.Commit()
}
func (r *supplierProbeRepository) Finish(ctx context.Context, c *service.SupplierProbeClaim, u *service.SupplierResourceUsage, code string, delay time.Duration, terminal bool) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = supplierControlTimeout(ctx, tx); e != nil {
		return e
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT j.resource_id FROM supplier_probe_jobs j JOIN supplier_resources r ON r.id=j.resource_id WHERE j.resource_id=$1 AND j.state='running' AND j.lease_owner=$2 AND j.lease_generation=$3 AND j.lease_until>CURRENT_TIMESTAMP AND j.credential_version=$4 AND r.credential_version=$4 AND r.auto_sync_enabled AND r.status='paused' AND r.probe_state<>'reauthorization_required' AND r.token_expires_at>CURRENT_TIMESTAMP FOR UPDATE OF j,r`, c.Resource.ID, c.Owner, c.Generation, c.Resource.CredentialVersion).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if u == nil && code == "" {
		code = "quota_unknown"
	}
	var b any
	if u != nil && code == "" {
		raw, err := json.Marshal(u)
		if err != nil {
			return err
		}
		b = string(raw)
	}
	state := "fresh"
	if code != "" {
		state = "retrying"
	}
	if terminal {
		state = "reauthorization_required"
	}
	_, e = tx.ExecContext(ctx, `UPDATE supplier_resources SET usage=COALESCE($2::jsonb,usage),last_usage_at=CASE WHEN $2::jsonb IS NOT NULL THEN CURRENT_TIMESTAMP ELSE last_usage_at END,last_probe_at=CURRENT_TIMESTAMP,next_probe_at=CASE WHEN $5 THEN NULL ELSE CURRENT_TIMESTAMP+($4*INTERVAL '1 second') END,probe_error_code=NULLIF($3,''),probe_state=$6,auto_sync_enabled=NOT $5,supply_enabled=CASE WHEN $5 THEN FALSE ELSE supply_enabled END,supply_enable_pending=CASE WHEN $5 THEN FALSE ELSE supply_enable_pending END,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id, b, code, int64(delay/time.Second), terminal, state)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE supplier_probe_jobs SET state=CASE WHEN $4 THEN 'cancelled' ELSE 'pending' END,lease_owner=NULL,lease_until=NULL,next_run_at=CURRENT_TIMESTAMP+($2*INTERVAL '1 second'),attempts=CASE WHEN $3='' THEN 0 ELSE LEAST(attempts+1,20) END WHERE resource_id=$1`, id, int64(delay/time.Second), code, terminal)
	if e != nil {
		return e
	}
	if terminal || code != "" || service.SupplierQuotaAvailability(u, time.Now()) != "" {
		if e = disableSupplierBinding(ctx, tx, id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *supplierProbeRepository) Resume(ctx context.Context, owner int64, id string, version int64) (*service.SupplierResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, e := scanResource(r.db.QueryRowContext(ctx, `UPDATE supplier_resources SET auto_sync_enabled=TRUE,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND supplier_id=$2 AND version=$3 AND status='paused' AND token_expires_at>CURRENT_TIMESTAMP AND probe_state<>'reauthorization_required' RETURNING `+resourceCols, id, owner, version))
	if errors.Is(e, service.ErrSupplierResource) {
		return nil, service.ErrSupplierResourceVersion
	}
	return v, e
}
