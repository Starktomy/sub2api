package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageAttemptRepository struct {
	db         *sql.DB
	maxPending int
}

// A nil DB or invalid bound fails closed. Capacity includes blocked work, since
// blocked facts must never disappear merely to admit more upstream generations.
func NewUsageAttemptRepository(db *sql.DB, maxPending int) service.UsageAttemptJournal {
	return &usageAttemptRepository{db: db, maxPending: maxPending}
}
func (r *usageAttemptRepository) available() bool {
	return r != nil && r.db != nil && r.maxPending > 0 && r.maxPending <= 100000
}
func journalError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{service.ErrUsageJournalInvalid, service.ErrUsageJournalConflict, service.ErrUsageJournalFence, service.ErrUsageJournalFull, service.ErrUsageJournalAmbiguous} {
		if errors.Is(err, e) {
			return e
		}
	}
	return service.ErrUsageJournalUnavailable
}
func (r *usageAttemptRepository) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if !r.available() {
		return service.ErrUsageJournalUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.ErrUsageJournalUnavailable
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return journalError(err)
	}
	if err = tx.Commit(); err != nil {
		return service.ErrUsageJournalAmbiguous
	}
	return nil
}
func (r *usageAttemptRepository) Create(ctx context.Context, a service.UsageAttemptInput) error {
	if err := a.Validate(); err != nil {
		return err
	}
	return r.transaction(ctx, func(tx *sql.Tx) error {
		// Serializes only short admissions, never external generation or observations.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(256,1)`); err != nil {
			return err
		}
		var old service.UsageAttemptInput
		err := tx.QueryRowContext(ctx, `SELECT id,operation_id,account_id,credential_version,request_digest FROM usage_attempts WHERE id=$1 OR operation_id=$2`, a.ID, a.OperationID).Scan(&old.ID, &old.OperationID, &old.AccountID, &old.CredentialVersion, &old.RequestDigest)
		if err == nil {
			if old != a {
				return service.ErrUsageJournalConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_reconciliation`).Scan(&n); err != nil {
			return err
		}
		if n >= r.maxPending {
			return service.ErrUsageJournalFull
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_attempts(id,operation_id,account_id,credential_version,request_digest) VALUES($1,$2,$3,$4,$5)`, a.ID, a.OperationID, a.AccountID, a.CredentialVersion, a.RequestDigest); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_reconciliation(attempt_id) VALUES($1)`, a.ID)
		return err
	})
}
func (r *usageAttemptRepository) Claim(ctx context.Context, id, owner string, lease time.Duration) (*service.UsageAttemptFence, error) {
	if !service.ValidUsageDigest(id) || !service.ValidUsageDigest(owner) || lease < time.Second || lease > 10*time.Minute {
		return nil, service.ErrUsageJournalInvalid
	}
	f := &service.UsageAttemptFence{AttemptID: id, Owner: owner}
	err := r.transaction(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE usage_attempts SET owner=$2,generation=generation+1,lease_until=clock_timestamp()+($3 * interval '1 millisecond') WHERE id=$1 AND lease_until<=clock_timestamp() RETURNING generation`, id, owner, lease.Milliseconds()).Scan(&f.Generation)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrUsageJournalFence
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}
func (r *usageAttemptRepository) MarkSendIntent(ctx context.Context, f service.UsageAttemptFence) error {
	if err := f.Validate(); err != nil {
		return err
	}
	return r.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE usage_attempts SET send_intent=true,status='send_intent' WHERE id=$1 AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() AND NOT send_intent`, f.AttemptID, f.Owner, f.Generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return service.ErrUsageJournalFence
		}
		return nil
	})
}
func (r *usageAttemptRepository) Lookup(ctx context.Context, id string) (*service.UsageAttempt, error) {
	if !r.available() {
		return nil, service.ErrUsageJournalUnavailable
	}
	if !service.ValidUsageDigest(id) {
		return nil, service.ErrUsageJournalInvalid
	}
	a := &service.UsageAttempt{}
	// Infinity is not representable in time.Time: normalize the unclaimed sentinel.
	err := r.db.QueryRowContext(ctx, `SELECT id,operation_id,account_id,credential_version,request_digest,owner,generation,CASE WHEN lease_until='-infinity'::timestamptz THEN 'epoch'::timestamptz ELSE lease_until END,send_intent,status FROM usage_attempts WHERE id=$1`, id).Scan(&a.ID, &a.OperationID, &a.AccountID, &a.CredentialVersion, &a.RequestDigest, &a.Owner, &a.Generation, &a.LeaseUntil, &a.SendIntent, &a.Status)
	if err != nil {
		return nil, journalError(err)
	}
	return a, nil
}
func (r *usageAttemptRepository) Pending(ctx context.Context, limit int) ([]service.UsagePending, error) {
	if !r.available() {
		return nil, service.ErrUsageJournalUnavailable
	}
	if limit < 1 || limit > 1000 {
		return nil, service.ErrUsageJournalInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT attempt_id,state,reason,revision FROM usage_reconciliation ORDER BY updated_at,attempt_id LIMIT $1`, limit)
	if err != nil {
		return nil, journalError(err)
	}
	defer rows.Close()
	out := []service.UsagePending{}
	for rows.Next() {
		var p service.UsagePending
		if err = rows.Scan(&p.AttemptID, &p.State, &p.Reason, &p.Revision); err != nil {
			return nil, journalError(err)
		}
		out = append(out, p)
	}
	return out, journalError(rows.Err())
}
func scanUsageFact(row interface{ Scan(...any) error }) (*service.UsageFactRevision, error) {
	var b []byte
	f := &service.UsageFactRevision{}
	if err := row.Scan(&f.AttemptID, &f.Revision, &f.ContentDigest, &b); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &f.UsageFactInput); err != nil {
		return nil, service.ErrUsageJournalUnavailable
	}
	return f, nil
}
func (r *usageAttemptRepository) LookupObservation(ctx context.Context, id, observation string) (*service.UsageFactRevision, error) {
	if !r.available() {
		return nil, service.ErrUsageJournalUnavailable
	}
	if !service.ValidUsageDigest(id) || !service.ValidUsageDigest(observation) {
		return nil, service.ErrUsageJournalInvalid
	}
	f, err := scanUsageFact(r.db.QueryRowContext(ctx, `SELECT attempt_id,revision,content_digest,fact FROM usage_fact_revisions WHERE attempt_id=$1 AND observation_id=$2`, id, observation))
	if err != nil {
		return nil, journalError(err)
	}
	return f, nil
}
