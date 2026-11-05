package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func supplierRecoveryLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// RecoverSupplierBilling only replays persisted commands. It has no provider,
// decryptor or pricing dependency. Each claim statement and Apply use short,
// separate transactions; no lock is held while waiting on another operation.
func (r *usageBillingRepository) RecoverSupplierBilling(ctx context.Context, limit int) (service.SupplierRecoveryResult, error) {
	result := service.SupplierRecoveryResult{}
	if r == nil || r.db == nil {
		return result, service.ErrSupplierDispatchUnavailable
	}
	limit = supplierRecoveryLimit(limit)
	// Two days protects long active generations. An unstaged observation cannot
	// be priced after restart; retain its evidence and require explicit review.
	stale, err := r.db.ExecContext(ctx, `WITH candidates AS (
 SELECT attempt_id FROM supplier_dispatches WHERE state IN ('accepted','dispatched','observed') AND updated_at<CURRENT_TIMESTAMP-INTERVAL '48 hours'
 ORDER BY updated_at,attempt_id LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE supplier_dispatches d SET state='review',review_reason='stale_unstaged',updated_at=CURRENT_TIMESTAMP FROM candidates c WHERE d.attempt_id=c.attempt_id`, limit)
	if err != nil {
		return result, err
	}
	n, err := stale.RowsAffected()
	if err != nil {
		return result, err
	}
	result.Review = int(n)
	lease := uuid.NewString()
	rows, err := r.db.QueryContext(ctx, `WITH candidates AS (
 SELECT attempt_id FROM supplier_dispatches WHERE state='pending' AND next_retry_at<=CURRENT_TIMESTAMP AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP)
 ORDER BY next_retry_at,attempt_id LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE supplier_dispatches d SET lease_token=$2,lease_until=CURRENT_TIMESTAMP+INTERVAL '2 minutes',retry_count=LEAST(retry_count+1,1000000)
 FROM candidates c WHERE d.attempt_id=c.attempt_id RETURNING d.attempt_id,d.command_payload,d.command_hash,d.retry_count`, limit, lease)
	if err != nil {
		return result, err
	}
	type work struct {
		attempt, payload, hash string
		tries                  int
	}
	var jobs []work
	for rows.Next() {
		var w work
		if err = rows.Scan(&w.attempt, &w.payload, &w.hash, &w.tries); err != nil {
			rows.Close()
			return result, err
		}
		jobs = append(jobs, w)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	for _, w := range jobs {
		if ctx.Err() != nil {
			result.Pending++
			continue
		} // lease expiration resumes this exact command
		var cmd service.UsageBillingCommand
		invalid := supplierHash([]byte(w.payload)) != w.hash || json.Unmarshal([]byte(w.payload), &cmd) != nil || cmd.SupplierAttemptID != w.attempt
		if invalid {
			_, err = r.db.ExecContext(ctx, `UPDATE supplier_dispatches SET state='review',review_reason='invalid_command',lease_token=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND lease_token=$2 AND state='pending'`, w.attempt, lease)
			if err != nil {
				return result, err
			}
			result.Review++
			continue
		}
		_, applyErr := r.Apply(ctx, &cmd)
		if applyErr == nil {
			result.Replayed++
			continue
		}
		if errors.Is(applyErr, service.ErrSupplierDispatchConflict) || errors.Is(applyErr, service.ErrUsageBillingRequestConflict) {
			_, err = r.db.ExecContext(ctx, `UPDATE supplier_dispatches SET state='review',review_reason='billing_conflict',lease_token=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND lease_token=$2 AND state='pending'`, w.attempt, lease)
			if err != nil {
				return result, err
			}
			result.Review++
			continue
		}
		// Never persist arbitrary SQL/error strings; they may contain caller data.
		shift := w.tries
		if shift > 10 {
			shift = 10
		}
		delay := time.Duration(1<<uint(shift)) * time.Second
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		_, err = r.db.ExecContext(ctx, `UPDATE supplier_dispatches SET next_retry_at=CURRENT_TIMESTAMP+($3*INTERVAL '1 second'),lease_token=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND lease_token=$2 AND state='pending'`, w.attempt, lease, int64(delay/time.Second))
		if err != nil {
			return result, err
		}
		result.Pending++
	}
	// Counts describe only this bounded batch, not an invented global backlog.
	return result, ctx.Err()
}

func (r *usageBillingRepository) ListSupplierBillingRepairs(ctx context.Context, limit int) ([]service.SupplierBillingRepair, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrSupplierDispatchUnavailable
	}
	rows, err := r.db.QueryContext(ctx, `SELECT attempt_id,user_id,api_key_id,group_id,account_id FROM supplier_dispatches WHERE state='committed' AND repair_pending AND repair_available_at<=CURRENT_TIMESTAMP ORDER BY repair_available_at,attempt_id LIMIT $1`, supplierRecoveryLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.SupplierBillingRepair, 0)
	for rows.Next() {
		var repair service.SupplierBillingRepair
		if err = rows.Scan(&repair.AttemptID, &repair.UserID, &repair.APIKeyID, &repair.GroupID, &repair.AccountID); err != nil {
			return nil, err
		}
		result = append(result, repair)
	}
	return result, rows.Err()
}

func (r *usageBillingRepository) CompleteSupplierBillingRepair(ctx context.Context, attempt string) error {
	if r == nil || r.db == nil {
		return service.ErrSupplierDispatchUnavailable
	}
	if !supplierUUID(attempt) {
		return service.ErrSupplierDispatchConflict
	}
	// A concurrent first-pass ACK cannot collapse the safety pass: after the
	// first ACK repair_available_at lies in the future and excludes every other
	// first-pass worker. The delayed second invalidation drains stale DB loaders.
	_, err := r.db.ExecContext(ctx, `UPDATE supplier_dispatches SET repair_pending=CASE WHEN repair_stage=0 THEN TRUE ELSE FALSE END,repair_available_at=CASE WHEN repair_stage=0 THEN CURRENT_TIMESTAMP+INTERVAL '30 seconds' ELSE repair_available_at END,repair_stage=1 WHERE attempt_id=$1 AND state='committed' AND repair_pending AND repair_available_at<=CURRENT_TIMESTAMP`, attempt)
	return err
}
