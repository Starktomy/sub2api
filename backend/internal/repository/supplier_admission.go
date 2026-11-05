package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Freshness is deliberately independent of the next scheduled probe. A stalled
// worker must not leave supply eligible indefinitely. Execution rechecks this
// predicate against PostgreSQL, not a scheduler snapshot.
const supplierAdmissionEligibleSQL = `r.status='paused' AND r.auto_sync_enabled
 AND r.envelope IS NOT NULL AND r.token_expires_at>CURRENT_TIMESTAMP
 AND r.probe_state='fresh' AND r.probe_error_code IS NULL
 AND r.verified_credential_version=r.credential_version AND btrim(r.account_hint)<>''
 AND r.last_probe_at BETWEEN CURRENT_TIMESTAMP-INTERVAL '15 minutes' AND CURRENT_TIMESTAMP
 AND r.last_usage_at BETWEEN CURRENT_TIMESTAMP-INTERVAL '15 minutes' AND CURRENT_TIMESTAMP
 AND p.status='active' AND u.status='active' AND u.deleted_at IS NULL`

// reconcileSupplierAdmission processes at most 200 resources: 100 existing
// bindings in oldest-checked order and 100 new candidates. Resource row locks
// serialize admission with disconnect, rotation and probe completion; no global
// accounts lock or destructive binding cleanup is used.
func reconcileSupplierAdmission(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='supplier_operating_policy_v1' FOR SHARE`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var policy service.SupplierOperatingPolicy
	enabled := err == nil && json.Unmarshal([]byte(raw), &policy) == nil && policy.Validate() == nil && policy.Version >= 1 && policy.AutoAdmission
	if enabled {
		rows, e := tx.QueryContext(ctx, `SELECT id FROM groups WHERE id=ANY($1) AND status='active' AND deleted_at IS NULL AND platform IN ('openai','composite') ORDER BY id FOR SHARE`, pq.Array(policy.GroupIDs))
		if e != nil {
			return e
		}
		count := 0
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		enabled = count == len(policy.GroupIDs)
	}
	rows, err := tx.QueryContext(ctx, `SELECT resource_id::text FROM (SELECT resource_id FROM supplier_account_bindings ORDER BY checked_at,resource_id LIMIT 100) existing
 UNION ALL SELECT id::text FROM (SELECT r.id FROM supplier_resources r JOIN supplier_profiles p ON p.id=r.supplier_id JOIN users u ON u.id=p.user_id WHERE NOT EXISTS (SELECT 1 FROM supplier_account_bindings b WHERE b.resource_id=r.id) AND (`+supplierAdmissionEligibleSQL+`) ORDER BY r.id LIMIT 100) candidates`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Consistent resource lock order across concurrent reconciliation calls.
	sort.Strings(ids)
	for _, id := range ids {
		if err = syncSupplierAdmission(ctx, tx, id, enabled, policy.GroupIDs); err != nil {
			return fmt.Errorf("supplier admission: %w", err)
		}
	}
	return tx.Commit()
}

func syncSupplierAdmission(ctx context.Context, tx *sql.Tx, id string, enabled bool, groupIDs []int64) error {
	var supplierID, version int64
	var hint string
	var expires sql.NullTime
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT r.supplier_id,r.credential_version,r.account_hint,r.token_expires_at,COALESCE((`+supplierAdmissionEligibleSQL+`),FALSE) FROM supplier_resources r JOIN supplier_profiles p ON p.id=r.supplier_id JOIN users u ON u.id=p.user_id WHERE r.id=$1 FOR UPDATE OF r FOR SHARE OF p,u`, id).Scan(&supplierID, &version, &hint, &expires, &eligible)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	eligible = eligible && enabled
	var accountID int64
	var boundVersion, boundSupplier int64
	err = tx.QueryRowContext(ctx, `SELECT account_id,credential_version,supplier_id FROM supplier_account_bindings WHERE resource_id=$1 FOR UPDATE`, id).Scan(&accountID, &boundVersion, &boundSupplier)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	created := errors.Is(err, sql.ErrNoRows)
	if !created && boundSupplier != supplierID {
		eligible = false
	}
	if created && !eligible {
		return nil
	}
	credentials, _ := json.Marshal(map[string]any{"supplier_resource_id": id, "chatgpt_account_id": hint})
	extra, _ := json.Marshal(map[string]any{"supplier_resource_id": id, "supplier_id": supplierID})
	if created {
		err = tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra,status,schedulable,expires_at,auto_pause_on_expired) VALUES($1,'openai','oauth',$2::jsonb,$3::jsonb,'active',TRUE,$4,TRUE) RETURNING id`, "Supplier "+id, string(credentials), string(extra), expires).Scan(&accountID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO supplier_account_bindings(resource_id,account_id,supplier_id,credential_version) VALUES($1,$2,$3,$4)`, id, accountID, supplierID, version)
		if err != nil {
			return err
		}
	}
	// A rotated credential cannot reuse an old verification. Advance the binding
	// version only after a fresh successful probe, preserving the same Account.
	if eligible && version != boundVersion && !created {
		if _, err = tx.ExecContext(ctx, `UPDATE supplier_account_bindings SET credential_version=$2 WHERE resource_id=$1`, id, version); err != nil {
			return err
		}
	}
	var deleted bool
	if err = tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&deleted); err != nil {
		return err
	}
	eligible = eligible && !deleted
	desired := []int64{}
	if eligible {
		desired = append(desired, groupIDs...)
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i] < desired[j] })
	rows, err := tx.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id=$1 ORDER BY group_id`, accountID)
	if err != nil {
		return err
	}
	var old []int64
	for rows.Next() {
		var g int64
		if err = rows.Scan(&g); err != nil {
			rows.Close()
			return err
		}
		old = append(old, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	status := "disabled"
	if eligible {
		status = "active"
	}
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET platform='openai',type='oauth',credentials=$2::jsonb,extra=extra||$3::jsonb,status=$4,schedulable=$5,expires_at=$6,auto_pause_on_expired=TRUE,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND (platform<>'openai' OR type<>'oauth' OR credentials IS DISTINCT FROM $2::jsonb OR NOT extra @> $3::jsonb OR status<>$4 OR schedulable<>$5 OR expires_at IS DISTINCT FROM $6 OR NOT auto_pause_on_expired)`, accountID, string(credentials), string(extra), status, eligible, expires)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	groupsChanged := len(old) != len(desired)
	if !groupsChanged {
		for i := range old {
			if old[i] != desired[i] {
				groupsChanged = true
				break
			}
		}
	}
	if groupsChanged {
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND NOT(group_id=ANY($2))`, accountID, pq.Array(desired)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) SELECT $1,unnest($2::bigint[]) ON CONFLICT DO NOTHING`, accountID, pq.Array(desired)); err != nil {
			return err
		}
	}
	if created || changed > 0 || groupsChanged {
		affected := append(old, desired...)
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, buildSchedulerGroupPayload(affected)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE supplier_account_bindings SET checked_at=CURRENT_TIMESTAMP WHERE resource_id=$1`, id)
	return err
}
