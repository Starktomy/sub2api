package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *supplierResourceRepository) GetExecutionResource(ctx context.Context, accountID int64, resourceID string) (*service.SupplierResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	resource, err := GetSupplierExecutionResourceTx(ctx, tx, accountID, resourceID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return resource, nil
}

// GetSupplierExecutionResourceTx authorizes and returns the exact vault snapshot
// for execution/dispatch journaling. Caller owns a REPEATABLE READ transaction,
// freezes fee policy in that same transaction and commits before any upstream I/O.
// Decrypt this returned resource, never re-fetch a potentially rotated credential.
// No writes, scheduler-cache trust or financial transitions occur here.
func GetSupplierExecutionResourceTx(ctx context.Context, tx *sql.Tx, accountID int64, resourceID string) (*service.SupplierResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if tx == nil {
		return nil, service.ErrSupplierResource
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='supplier_operating_policy_v1'`).Scan(&raw); err != nil {
		return nil, service.ErrSupplierResource
	}
	var policy service.SupplierOperatingPolicy
	if json.Unmarshal([]byte(raw), &policy) != nil || policy.Validate() != nil || policy.Version < 1 || !policy.AutoAdmission {
		return nil, service.ErrSupplierResource
	}
	if group, ok := ctx.Value(ctxkey.Group).(*service.Group); ok && group != nil {
		allowed := false
		for _, id := range policy.GroupIDs {
			if group.ID == id {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, service.ErrSupplierResource
		}
	}
	var owner int64
	err := tx.QueryRowContext(ctx, `SELECT r.supplier_id FROM supplier_account_bindings b JOIN supplier_resources r ON r.id=b.resource_id JOIN supplier_profiles p ON p.id=r.supplier_id JOIN users u ON u.id=p.user_id JOIN accounts a ON a.id=b.account_id WHERE b.account_id=$1 AND b.resource_id=$2 AND b.supplier_id=r.supplier_id AND b.credential_version=r.credential_version AND (`+supplierAdmissionEligibleSQL+`) AND a.deleted_at IS NULL AND a.status='active' AND a.schedulable AND a.platform='openai' AND a.type='oauth' AND a.credentials->>'supplier_resource_id'=r.id::text AND a.credentials->>'chatgpt_account_id'=r.account_hint AND a.extra->>'supplier_resource_id'=r.id::text AND a.extra->>'supplier_id'=r.supplier_id::text`, accountID, resourceID).Scan(&owner)
	if err != nil {
		return nil, service.ErrSupplierResource
	}
	// Every configured group must still be eligible and assigned to this account.
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM groups g JOIN account_groups ag ON ag.group_id=g.id WHERE g.id=ANY($1::bigint[]) AND ag.account_id=$2 AND g.deleted_at IS NULL AND g.status='active' AND g.platform IN ('openai','composite')`, pq.Array(policy.GroupIDs), accountID).Scan(&count)
	if err != nil || count != len(policy.GroupIDs) {
		return nil, service.ErrSupplierResource
	}
	resource, err := scanResource(tx.QueryRowContext(ctx, `SELECT `+resourceCols+` FROM supplier_resources WHERE id=$1 AND supplier_id=$2`, resourceID, owner))
	if err != nil {
		return nil, err
	}
	return resource, nil
}
