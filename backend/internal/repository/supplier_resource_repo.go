package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type supplierResourceRepository struct{ db *sql.DB }

func NewSupplierResourceRepository(db *sql.DB) service.SupplierResourceRepository {
	return &supplierResourceRepository{db}
}

const resourceCols = `id,supplier_id,display_name,status,version,masked_email,plan_type,token_expires_at,last_usage_at,created_at,updated_at,usage,envelope,key_id,account_hint,identity_fingerprint,token_fingerprint,auto_sync_enabled,credential_version,probe_state,last_probe_at,next_probe_at,probe_error_code`

func scanResource(row supplierScanner) (*service.SupplierResource, error) {
	r := new(service.SupplierResource)
	var usage []byte
	e := row.Scan(&r.ID, &r.SupplierID, &r.DisplayName, &r.Status, &r.Version, &r.MaskedEmail, &r.PlanType, &r.TokenExpiresAt, &r.LastUsageAt, &r.CreatedAt, &r.UpdatedAt, &usage, &r.Envelope, &r.KeyID, &r.AccountHint, &r.IdentityFingerprint, &r.TokenFingerprint, &r.AutoSyncEnabled, &r.CredentialVersion, &r.ProbeState, &r.LastProbeAt, &r.NextProbeAt, &r.ProbeErrorCode)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, service.ErrSupplierResource
	}
	if e != nil {
		return nil, e
	}
	if len(usage) > 0 {
		if e = json.Unmarshal(usage, &r.Usage); e != nil {
			return nil, e
		}
	}
	return r, nil
}
func (r *supplierResourceRepository) List(ctx context.Context, owner int64) ([]*service.SupplierResource, error) {
	rows, e := r.db.QueryContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE supplier_id=$1 ORDER BY created_at DESC LIMIT 100`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*service.SupplierResource{}
	for rows.Next() {
		v, e := scanResource(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *supplierResourceRepository) Get(ctx context.Context, owner int64, id string) (*service.SupplierResource, error) {
	return scanResource(r.db.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE supplier_id=$1 AND id=$2`, owner, id))
}
func (r *supplierResourceRepository) Create(ctx context.Context, owner int64, v *service.SupplierResource) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'; SET LOCAL statement_timeout='2s'`); e != nil {
		return service.ErrServiceUnavailable
	}
	// Serialize with legacy Account writes: deny existing platform identity without
	// ever updating Account credentials. No upstream I/O while holding this lock.
	if _, e = tx.ExecContext(ctx, `LOCK TABLE accounts IN SHARE MODE`); e != nil {
		return service.ErrServiceUnavailable
	}
	if _, e = tx.ExecContext(ctx, `LOCK TABLE supplier_resources IN SHARE ROW EXCLUSIVE MODE`); e != nil {
		return service.ErrServiceUnavailable
	}
	var duplicate bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_resources WHERE identity_fingerprint=$1 AND (supplier_id<>$2 OR status='paused'))`, v.IdentityFingerprint, owner).Scan(&duplicate)
	if e != nil {
		return e
	}
	if duplicate {
		return service.ErrSupplierResourceConflict
	}
	var exists bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE platform='openai' AND deleted_at IS NULL AND (credentials->>'chatgpt_account_id'=$1 OR credentials->>'account_id'=$1 OR credentials->>'organization_id'=$1 OR encode(sha256(convert_to(COALESCE(credentials->>'access_token',''),'UTF8')),'hex')=$2))`, v.AccountHint, v.TokenFingerprint).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return service.ErrSupplierResourceConflict
	}
	e = tx.QueryRowContext(ctx, `INSERT INTO supplier_resources(id,supplier_id,display_name,status,masked_email,plan_type,token_expires_at,envelope,key_id,account_hint,identity_fingerprint,token_fingerprint,auto_sync_enabled,probe_state,next_probe_at) VALUES($1,$2,$3,'paused',$4,$5,$6,$7,$8,$9,$10,$11,TRUE,'pending',CURRENT_TIMESTAMP) RETURNING created_at,updated_at,next_probe_at`, v.ID, owner, v.DisplayName, v.MaskedEmail, v.PlanType, v.TokenExpiresAt, v.Envelope, v.KeyID, v.AccountHint, v.IdentityFingerprint, v.TokenFingerprint).Scan(&v.CreatedAt, &v.UpdatedAt, &v.NextProbeAt)
	if e != nil {
		var pg *pq.Error
		if errors.As(e, &pg) && pg.Code == "23505" {
			return service.ErrSupplierResourceConflict
		}
		return service.ErrServiceUnavailable
	}
	return tx.Commit()
}
func (r *supplierResourceRepository) Transition(ctx context.Context, owner int64, id string, version int64, disconnect bool) (*service.SupplierResource, error) {
	v, e := scanResource(r.db.QueryRowContext(ctx, `UPDATE supplier_resources SET status=CASE WHEN $4 THEN 'disconnected' ELSE 'paused' END,envelope=CASE WHEN $4 THEN NULL ELSE envelope END,auto_sync_enabled=FALSE,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND version=$3 AND status='paused' RETURNING `+resourceCols, owner, id, version, disconnect))
	if errors.Is(e, service.ErrSupplierResource) {
		if _, read := r.Get(ctx, owner, id); read != nil {
			return nil, read
		}
		return nil, service.ErrSupplierResourceVersion
	}
	return v, e
}
func (r *supplierResourceRepository) SaveUsage(ctx context.Context, owner int64, id string, version int64, u *service.SupplierResourceUsage) (*service.SupplierResource, error) {
	b, e := json.Marshal(u)
	if e != nil {
		return nil, e
	}
	v, e := scanResource(r.db.QueryRowContext(ctx, `UPDATE supplier_resources SET usage=$4::jsonb,last_usage_at=CURRENT_TIMESTAMP,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND version=$3 AND status='paused' RETURNING `+resourceCols, owner, id, version, string(b)))
	if errors.Is(e, service.ErrSupplierResource) {
		return nil, service.ErrSupplierResourceVersion
	}
	return v, e
}
