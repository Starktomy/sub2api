package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Observe is local-only and append-only. Repeated metering observations are never
// summed: upstream cumulative/incremental semantics are not established.
func (r *usageAttemptRepository) Observe(ctx context.Context, fence service.UsageAttemptFence, input service.UsageFactInput) (*service.UsageFactRevision, error) {
	if err := fence.Validate(); err != nil {
		return nil, err
	}
	hash, err := input.ContentHash()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > 16384 {
		return nil, service.ErrUsageJournalInvalid
	}
	var result *service.UsageFactRevision
	err = r.transaction(ctx, func(tx *sql.Tx) error {
		var owner string
		var generation int64
		var active, sent bool
		if err := tx.QueryRowContext(ctx, `SELECT owner,generation,lease_until>clock_timestamp(),send_intent FROM usage_attempts WHERE id=$1 FOR UPDATE`, fence.AttemptID).Scan(&owner, &generation, &active, &sent); err != nil {
			return err
		}
		old, err := scanUsageFact(tx.QueryRowContext(ctx, `SELECT attempt_id,revision,content_digest,fact FROM usage_fact_revisions WHERE attempt_id=$1 AND observation_id=$2`, fence.AttemptID, input.ObservationID))
		if err == nil {
			if old.ContentDigest != hash {
				return service.ErrUsageJournalConflict
			}
			result = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if owner != fence.Owner || generation != fence.Generation || !active || !sent {
			return service.ErrUsageJournalFence
		}
		var revision int64
		if err = tx.QueryRowContext(ctx, `SELECT revision FROM usage_reconciliation WHERE attempt_id=$1 FOR UPDATE`, fence.AttemptID).Scan(&revision); err != nil {
			return err
		}
		if revision >= 256 {
			return service.ErrUsageJournalFull
		}
		if input.Supersedes > revision {
			return service.ErrUsageJournalInvalid
		}
		revision++
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_fact_revisions(attempt_id,revision,observation_id,content_digest,fact) VALUES($1,$2,$3,$4,$5::jsonb)`, fence.AttemptID, revision, input.ObservationID, hash, string(body)); err != nil {
			return err
		}
		// Only complete non-null provider metrics qualify as observed, never priced.
		status, state, reason := "usage_unknown", "pending", "usage_unknown"
		known := false
		for _, m := range input.Metrics {
			if m.Value != nil {
				known = true
			}
		}
		if input.Source == "provider_reported" && input.Quality == "reported" && !input.Partial && input.Outcome == "observed" && known {
			status, state, reason = "observed", "blocked", "pricing_missing"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE usage_attempts SET status=$2 WHERE id=$1`, fence.AttemptID, status); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE usage_reconciliation SET state=$2,reason=$3,revision=$4,updated_at=clock_timestamp() WHERE attempt_id=$1`, fence.AttemptID, state, reason, revision); err != nil {
			return err
		}
		result = &service.UsageFactRevision{UsageFactInput: input, AttemptID: fence.AttemptID, Revision: revision, ContentDigest: hash}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
