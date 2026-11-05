package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type supplierResourceRepository struct{ db *sql.DB }

func NewSupplierResourceRepository(db *sql.DB) service.SupplierResourceRepository {
	return &supplierResourceRepository{db}
}

const resourceCols = `id,supplier_id,display_name,status,version,masked_email,plan_type,token_expires_at,last_usage_at,created_at,updated_at,usage,envelope,key_id,account_hint,identity_fingerprint,token_fingerprint,auto_sync_enabled,credential_version,probe_state,last_probe_at,next_probe_at,probe_error_code,supply_enabled,supply_enable_pending,admin_disabled,consent_version,authorization_version,authorization_expires_at,authorization_revoked_at,last_verified_at,verified_credential_version,email`

func scanResource(row supplierScanner) (*service.SupplierResource, error) {
	r := new(service.SupplierResource)
	var usage []byte
	e := row.Scan(&r.ID, &r.SupplierID, &r.DisplayName, &r.Status, &r.Version, &r.MaskedEmail, &r.PlanType, &r.TokenExpiresAt, &r.LastUsageAt, &r.CreatedAt, &r.UpdatedAt, &usage, &r.Envelope, &r.KeyID, &r.AccountHint, &r.IdentityFingerprint, &r.TokenFingerprint, &r.AutoSyncEnabled, &r.CredentialVersion, &r.ProbeState, &r.LastProbeAt, &r.NextProbeAt, &r.ProbeErrorCode, &r.SupplyEnabled, &r.SupplyEnablePending, &r.AdminDisabled, &r.ConsentVersion, &r.AuthorizationVersion, &r.AuthorizationExpiresAt, &r.AuthorizationRevokedAt, &r.LastVerifiedAt, &r.VerifiedCredentialVersion, &r.Email)
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
func (r *supplierResourceRepository) FindImportResource(ctx context.Context, owner int64, identity string) (*service.SupplierResource, error) {
	v, err := scanResource(r.db.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE supplier_id=$1 AND identity_fingerprint=$2 ORDER BY (EXISTS(SELECT 1 FROM supplier_account_bindings b WHERE b.resource_id=supplier_resources.id)) DESC,(status='paused') DESC,created_at,id LIMIT 1`, owner, identity))
	if errors.Is(err, service.ErrSupplierResource) {
		return nil, nil
	}
	return v, err
}

func (r *supplierResourceRepository) Create(ctx context.Context, owner int64, v *service.SupplierResource) error {
	if v.ConsentVersion != service.SupplierConsentVersion {
		return service.ErrSupplierConsent
	}
	if v.AuthorizationExpiresAt == nil || !v.AuthorizationExpiresAt.After(time.Now()) || v.AuthorizationExpiresAt.After(v.TokenExpiresAt) {
		return service.ErrSupplierAuthorizationExpiry
	}
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
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_resources WHERE identity_fingerprint=$1 AND (supplier_id<>$2 OR id<>$3))`, v.IdentityFingerprint, owner, v.ID).Scan(&duplicate)
	if e != nil {
		return e
	}
	if duplicate {
		return service.ErrSupplierResourceConflict
	}
	current, readErr := scanResource(tx.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE id=$1 FOR UPDATE`, v.ID))
	if readErr != nil && !errors.Is(readErr, service.ErrSupplierResource) {
		return readErr
	}
	rotating := readErr == nil
	if rotating {
		if current.SupplierID != owner || current.IdentityFingerprint != v.IdentityFingerprint || current.AccountHint != v.AccountHint {
			return service.ErrSupplierResourceConflict
		}
		if current.Version != v.Version || current.CredentialVersion != v.CredentialVersion {
			return service.ErrSupplierResourceVersion
		}
	}
	var exists bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a WHERE platform='openai' AND deleted_at IS NULL AND (credentials->>'chatgpt_account_id'=$1 OR credentials->>'account_id'=$1 OR credentials->>'organization_id'=$1 OR encode(sha256(convert_to(COALESCE(credentials->>'access_token',''),'UTF8')),'hex')=$2) AND NOT EXISTS(SELECT 1 FROM supplier_account_bindings b JOIN supplier_resources r ON r.id=b.resource_id WHERE b.account_id=a.id AND b.resource_id=$3 AND b.supplier_id=$4 AND r.supplier_id=$4 AND r.account_hint=$1 AND a.credentials->>'supplier_resource_id'=r.id::text))`, v.AccountHint, v.TokenFingerprint, v.ID, owner).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return service.ErrSupplierResourceConflict
	}
	if rotating {
		updated, err := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET display_name=$4,email=$14,masked_email=$5,plan_type=$6,token_expires_at=$7,envelope=$8,key_id=$9,token_fingerprint=$10,status='paused',auto_sync_enabled=TRUE,version=version+1,credential_version=credential_version+1,verified_credential_version=NULL,last_verified_at=NULL,supply_enabled=FALSE,supply_enable_pending=TRUE,consent_version=$12,authorization_version=authorization_version+1,authorization_expires_at=$13,authorization_revoked_at=NULL,usage=NULL,last_usage_at=NULL,last_probe_at=NULL,probe_error_code=NULL,probe_state='pending',next_probe_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND supplier_id=$2 AND version=$3 AND credential_version=$11 RETURNING `+resourceCols, v.ID, owner, v.Version, v.DisplayName, v.MaskedEmail, v.PlanType, v.TokenExpiresAt, v.Envelope, v.KeyID, v.TokenFingerprint, v.CredentialVersion, v.ConsentVersion, v.AuthorizationExpiresAt, v.Email))
		if errors.Is(err, service.ErrSupplierResource) {
			return service.ErrSupplierResourceVersion
		}
		if err != nil {
			return err
		}
		// Reset even if auto-sync was already enabled: migration255's trigger
		// intentionally only enqueues the disabled-to-enabled transition.
		_, err = tx.ExecContext(ctx, `INSERT INTO supplier_probe_jobs(resource_id,credential_version) VALUES($1,$2) ON CONFLICT(resource_id) DO UPDATE SET credential_version=EXCLUDED.credential_version,state='pending',next_run_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,resource_version=NULL,lease_generation=supplier_probe_jobs.lease_generation+1,attempts=0`, updated.ID, updated.CredentialVersion)
		if err != nil {
			return err
		}
		if err = disableSupplierBinding(ctx, tx, updated.ID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		*v = *updated
		return nil
	}
	e = tx.QueryRowContext(ctx, `INSERT INTO supplier_resources(id,supplier_id,display_name,status,masked_email,plan_type,token_expires_at,envelope,key_id,account_hint,identity_fingerprint,token_fingerprint,auto_sync_enabled,probe_state,next_probe_at,consent_version,authorization_version,authorization_expires_at,supply_enable_pending,email) VALUES($1,$2,$3,'paused',$4,$5,$6,$7,$8,$9,$10,$11,TRUE,'pending',CURRENT_TIMESTAMP,$12,1,$13,TRUE,$14) RETURNING created_at,updated_at,next_probe_at,version,credential_version,authorization_version`, v.ID, owner, v.DisplayName, v.MaskedEmail, v.PlanType, v.TokenExpiresAt, v.Envelope, v.KeyID, v.AccountHint, v.IdentityFingerprint, v.TokenFingerprint, v.ConsentVersion, v.AuthorizationExpiresAt, v.Email).Scan(&v.CreatedAt, &v.UpdatedAt, &v.NextProbeAt, &v.Version, &v.CredentialVersion, &v.AuthorizationVersion)
	if e != nil {
		var pg *pq.Error
		if errors.As(e, &pg) && pg.Code == "23505" {
			return service.ErrSupplierResourceConflict
		}
		return service.ErrServiceUnavailable
	}
	return tx.Commit()
}

// Mutations and admission share resource-first lock order. Disabling a binding
// publishes the existing scheduler outbox event; execution still rechecks DB.
func (r *supplierResourceRepository) Transition(ctx context.Context, owner int64, id string, version int64, disconnect bool) (*service.SupplierResource, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = supplierControlTimeout(ctx, tx); e != nil {
		return nil, e
	}
	v, e := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET status=CASE WHEN $4 THEN 'disconnected' ELSE 'paused' END,envelope=CASE WHEN $4 THEN NULL ELSE envelope END,auto_sync_enabled=CASE WHEN $4 THEN FALSE ELSE auto_sync_enabled END,supply_enabled=FALSE,supply_enable_pending=FALSE,authorization_revoked_at=CASE WHEN $4 AND authorization_version>0 THEN COALESCE(authorization_revoked_at,CURRENT_TIMESTAMP) ELSE authorization_revoked_at END,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND version=$3 AND status='paused' RETURNING `+resourceCols, owner, id, version, disconnect))
	if errors.Is(e, service.ErrSupplierResource) {
		return nil, supplierMutationError(ctx, tx, owner, id)
	}
	if e != nil {
		return nil, e
	}
	if e = disableSupplierBinding(ctx, tx, id); e != nil {
		return nil, e
	}
	return v, tx.Commit()
}
func supplierControlTimeout(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'; SET LOCAL statement_timeout='2s'`)
	return err
}
func supplierMutationError(ctx context.Context, tx *sql.Tx, owner int64, id string) error {
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_resources WHERE id=$1 AND supplier_id=$2)`, id, owner).Scan(&found); err != nil {
		return err
	}
	if !found {
		return service.ErrSupplierResource
	}
	return service.ErrSupplierResourceVersion
}
func (r *supplierResourceRepository) StartSupply(ctx context.Context, owner int64, id string, version int64) (*service.SupplierResource, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = supplierControlTimeout(ctx, tx); err != nil {
		return nil, err
	}
	current, err := scanResource(tx.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE supplier_id=$1 AND id=$2 FOR UPDATE`, owner, id))
	if err != nil {
		return nil, err
	}
	if current.Version != version {
		return nil, service.ErrSupplierResourceVersion
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((`+supplierAuthorizationValidSQL+` AND `+supplierCredentialVerifiedSQL+`),FALSE) FROM supplier_resources r JOIN supplier_profiles p ON p.id=r.supplier_id JOIN users u ON u.id=p.user_id WHERE r.id=$1`, id).Scan(&eligible)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, service.ErrSupplierSupplyUnavailable
	}
	// Supplier choice can be restored under an admin prohibition, but never clears it.
	v, err := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET supply_enabled=TRUE,supply_enable_pending=FALSE,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND version=$3 RETURNING `+resourceCols, owner, id, version))
	if err != nil {
		return nil, err
	}
	return v, tx.Commit()
}
func (r *supplierResourceRepository) AdminSupplyControl(ctx context.Context, id string, version int64, disabled bool) (*service.SupplierResource, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = supplierControlTimeout(ctx, tx); err != nil {
		return nil, err
	}
	v, err := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET admin_disabled=$3,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$2 RETURNING `+resourceCols, id, version, disabled))
	if errors.Is(err, service.ErrSupplierResource) {
		return nil, service.ErrSupplierResourceVersion
	}
	if err != nil {
		return nil, err
	}
	if disabled {
		if err = disableSupplierBinding(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	v.Envelope = nil
	v.KeyID = ""
	v.AccountHint = ""
	v.IdentityFingerprint = ""
	v.TokenFingerprint = ""
	return v, tx.Commit()
}
func (r *supplierResourceRepository) SaveUsage(ctx context.Context, owner int64, id string, version int64, u *service.SupplierResourceUsage) (*service.SupplierResource, error) {
	b, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = supplierControlTimeout(ctx, tx); err != nil {
		return nil, err
	}
	v, err := scanResource(tx.QueryRowContext(ctx, `UPDATE supplier_resources SET usage=$4::jsonb,last_usage_at=CURRENT_TIMESTAMP,last_probe_at=CURRENT_TIMESTAMP,probe_state='fresh',probe_error_code=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND version=$3 AND status='paused' AND token_expires_at>CURRENT_TIMESTAMP AND probe_state<>'reauthorization_required' RETURNING `+resourceCols, owner, id, version, string(b)))
	if errors.Is(err, service.ErrSupplierResource) {
		return nil, supplierMutationError(ctx, tx, owner, id)
	}
	if err != nil {
		return nil, err
	}
	if service.SupplierQuotaAvailability(u, time.Now()) != "" {
		if err = disableSupplierBinding(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return v, tx.Commit()
}

// Manual read-only probes have the same fail-closed semantics as worker probes.
// Credential CAS allows a pause during the GET but never poisons a rotated token.
func (r *supplierResourceRepository) RecordProbeFailure(ctx context.Context, owner int64, id string, credentialVersion int64, code string, terminal bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = supplierControlTimeout(ctx, tx); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE supplier_resources SET last_probe_at=CURRENT_TIMESTAMP,probe_error_code=$4,probe_state=CASE WHEN $5 THEN 'reauthorization_required' ELSE 'retrying' END,auto_sync_enabled=CASE WHEN $5 THEN FALSE ELSE auto_sync_enabled END,supply_enabled=CASE WHEN $5 THEN FALSE ELSE supply_enabled END,supply_enable_pending=CASE WHEN $5 THEN FALSE ELSE supply_enable_pending END,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE supplier_id=$1 AND id=$2 AND credential_version=$3 AND status='paused' AND (probe_state<>'reauthorization_required' OR $5)`, owner, id, credentialVersion, code, terminal)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if err = disableSupplierBinding(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
