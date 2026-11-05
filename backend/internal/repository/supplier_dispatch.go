package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

var _ service.SupplierDispatchRepository = (*usageBillingRepository)(nil)
var _ service.SupplierBillingRepairRepository = (*usageBillingRepository)(nil)

var supplierSafeModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
var supplierDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func supplierUUID(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id != uuid.Nil && id.String() == v
}
func supplierHash(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }
func supplierSnapshotHash(s service.SupplierDispatchSnapshot) string {
	s.Digest = ""
	b, _ := json.Marshal(s)
	return supplierHash(b)
}

func supplierRequestValid(ctx context.Context, req service.SupplierDispatchRequest) bool {
	if !supplierUUID(req.AttemptID) || !supplierUUID(req.RequestID) || !supplierUUID(req.ResourceID) || req.AccountID <= 0 || req.UserID <= 0 || req.APIKeyID <= 0 || req.GroupID <= 0 || !supplierSafeModel.MatchString(req.Model) {
		return false
	}
	group, ok := ctx.Value(ctxkey.Group).(*service.Group)
	if !ok || group == nil || group.ID != req.GroupID {
		return false
	}
	// Middleware owns these values; an absent user context is allowed for internal
	// callers, but an asserted identity may never disagree with the command.
	if user := ctx.Value(ctxkey.UserID); user != nil {
		id, ok := user.(int64)
		if !ok || id != req.UserID {
			return false
		}
	}
	return true
}

// Recheck customer identity and durable admission before send. User-platform
// limits are deliberately unsupported until their increment joins this ledger.
// Fail closed instead of relying on a lossy asynchronous quota increment.
func supplierCustomerEligible(ctx context.Context, tx *sql.Tx, user, key, group int64) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_keys k JOIN users u ON u.id=k.user_id JOIN groups g ON g.id=k.group_id
 WHERE k.id=$1 AND u.id=$2 AND g.id=$3 AND k.deleted_at IS NULL AND u.deleted_at IS NULL AND g.deleted_at IS NULL
 AND k.status='active' AND u.status='active' AND g.status='active'
 AND (NOT g.is_exclusive OR EXISTS(SELECT 1 FROM user_allowed_groups ug WHERE ug.user_id=u.id AND ug.group_id=g.id))
 AND (k.expires_at IS NULL OR k.expires_at>CURRENT_TIMESTAMP) AND (k.quota=0 OR k.quota_used<k.quota)
 AND (k.rate_limit_5h=0 OR k.usage_5h<k.rate_limit_5h OR k.window_5h_start+INTERVAL '5 hours'<=CURRENT_TIMESTAMP)
 AND (k.rate_limit_1d=0 OR k.usage_1d<k.rate_limit_1d OR k.window_1d_start+INTERVAL '24 hours'<=CURRENT_TIMESTAMP)
 AND (k.rate_limit_7d=0 OR k.usage_7d<k.rate_limit_7d OR k.window_7d_start+INTERVAL '7 days'<=CURRENT_TIMESTAMP)
 AND NOT EXISTS(SELECT 1 FROM user_platform_quotas q WHERE q.user_id=u.id AND q.deleted_at IS NULL
 AND (q.daily_limit_usd IS NOT NULL OR q.weekly_limit_usd IS NOT NULL OR q.monthly_limit_usd IS NOT NULL))`, key, user, group).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSupplierDispatchUnavailable
	}
	return err
}

// Model mapping is read from PostgreSQL, not a scheduler cache. Accept either
// an allowed public name or its exact mapped upstream target (the gateway freezes
// that target). Never persist the account's credential map in the journal.
func supplierAccountModelEligible(ctx context.Context, tx *sql.Tx, accountID int64, model string) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('model_mapping',credentials->'model_mapping') FROM accounts WHERE id=$1 AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP OR NOT auto_pause_on_expired)`, accountID).Scan(&raw); err != nil {
		return service.ErrSupplierDispatchUnavailable
	}
	account := service.Account{ID: accountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	if json.Unmarshal(raw, &account.Credentials) != nil {
		return service.ErrSupplierDispatchUnavailable
	}
	if account.IsModelSupported(model) {
		return nil
	}
	for public, target := range account.GetModelMapping() {
		if target == model && account.IsModelSupported(public) {
			return nil
		}
	}
	return service.ErrSupplierDispatchUnavailable
}

func (r *usageBillingRepository) AcceptSupplierDispatch(ctx context.Context, req service.SupplierDispatchRequest) (*service.SupplierAcceptedDispatch, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrSupplierDispatchUnavailable
	}
	if !supplierRequestValid(ctx, req) {
		return nil, service.ErrSupplierDispatchConflict
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = supplierCustomerEligible(ctx, tx, req.UserID, req.APIKeyID, req.GroupID); err != nil {
		return nil, err
	}
	resource, err := GetSupplierExecutionResourceTx(ctx, tx, req.AccountID, req.ResourceID)
	if err != nil {
		return nil, err
	}
	if err = supplierAccountModelEligible(ctx, tx, req.AccountID, req.Model); err != nil {
		return nil, err
	}
	if resource == nil || resource.ID != req.ResourceID || resource.SupplierID <= 0 || resource.CredentialVersion <= 0 || resource.AuthorizationVersion <= 0 || resource.ConsentVersion != service.SupplierConsentVersion {
		return nil, service.ErrSupplierDispatchConflict
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='supplier_operating_policy_v1'`).Scan(&raw); err != nil {
		return nil, err
	}
	var p struct {
		Mode    service.SupplierFeeMode `json:"fee_mode"`
		Ratio   *int                    `json:"fee_ratio_bps"`
		Version int64                   `json:"version"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil || p.Ratio == nil {
		return nil, service.ErrSupplierDispatchUnavailable
	}
	policy := service.SupplierFeePolicy{Mode: p.Mode, RatioBPS: *p.Ratio, Version: p.Version}
	if policy.Validate() != nil {
		return nil, service.ErrSupplierDispatchUnavailable
	}
	var at time.Time
	if err = tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&at); err != nil {
		return nil, err
	}
	s := service.SupplierDispatchSnapshot{AttemptID: req.AttemptID, RequestID: req.RequestID, ResourceID: req.ResourceID, SupplierID: resource.SupplierID, AccountID: req.AccountID, CredentialVersion: resource.CredentialVersion, AuthorizationVersion: resource.AuthorizationVersion, ConsentVersion: resource.ConsentVersion, UserID: req.UserID, APIKeyID: req.APIKeyID, GroupID: req.GroupID, Model: req.Model, FeePolicy: policy, AcceptedAt: at.UTC()}
	s.Digest = supplierSnapshotHash(s)
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var inserted string
	err = tx.QueryRowContext(ctx, `INSERT INTO supplier_dispatches(attempt_id,request_id,snapshot,snapshot_digest,user_id,api_key_id,account_id,group_id,accepted_at)
 VALUES($1,$2,$3::jsonb,$4,$5,$6,$7,$8,$9) ON CONFLICT(attempt_id) DO NOTHING RETURNING attempt_id`, s.AttemptID, s.RequestID, string(payload), s.Digest, s.UserID, s.APIKeyID, s.AccountID, s.GroupID, s.AcceptedAt).Scan(&inserted)
	// An ambiguous acceptance must not yield another credential-bearing send permit.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierDispatchConflict
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	resource.AccountID = req.AccountID
	return &service.SupplierAcceptedDispatch{Snapshot: s, Resource: resource}, nil
}

type supplierDispatchRow struct {
	snapshot        service.SupplierDispatchSnapshot
	state           string
	observation     []byte
	observationHash sql.NullString
	command         sql.NullString
	commandHash     sql.NullString
}

func lockSupplierDispatch(ctx context.Context, tx *sql.Tx, attempt, digest string) (*supplierDispatchRow, error) {
	if !supplierUUID(attempt) || !supplierDigest.MatchString(digest) {
		return nil, service.ErrSupplierDispatchConflict
	}
	var raw []byte
	row := &supplierDispatchRow{}
	err := tx.QueryRowContext(ctx, `SELECT snapshot,state,observation,observation_hash,command_payload,command_hash FROM supplier_dispatches WHERE attempt_id=$1 AND snapshot_digest=$2 FOR UPDATE`, attempt, digest).Scan(&raw, &row.state, &row.observation, &row.observationHash, &row.command, &row.commandHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierDispatchConflict
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &row.snapshot) != nil || row.snapshot.AttemptID != attempt || row.snapshot.Digest != digest || supplierSnapshotHash(row.snapshot) != digest {
		return nil, service.ErrSupplierDispatchConflict
	}
	return row, nil
}

// The commit is the send permit. Repeated calls, including after an uncertain
// commit, fail closed. No caller may resend generation to resolve local ambiguity.
func (r *usageBillingRepository) MarkSupplierDispatched(ctx context.Context, attempt, digest string) error {
	if r == nil || r.db == nil {
		return service.ErrSupplierDispatchUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := lockSupplierDispatch(ctx, tx, attempt, digest)
	if err != nil {
		return err
	}
	if row.state != "accepted" {
		return service.ErrSupplierDispatchConflict
	}
	s := row.snapshot
	group, ok := ctx.Value(ctxkey.Group).(*service.Group)
	if !ok || group == nil || group.ID != s.GroupID {
		return service.ErrSupplierDispatchConflict
	}
	if err = lockSupplierSendEligibility(ctx, tx, s); err != nil {
		return err
	}
	if err = supplierCustomerEligible(ctx, tx, s.UserID, s.APIKeyID, s.GroupID); err != nil {
		return err
	}
	resource, err := GetSupplierExecutionResourceTx(ctx, tx, s.AccountID, s.ResourceID)
	if err != nil {
		return err
	}
	if err = supplierAccountModelEligible(ctx, tx, s.AccountID, s.Model); err != nil {
		return err
	}
	if resource == nil || resource.SupplierID != s.SupplierID || resource.CredentialVersion != s.CredentialVersion || resource.AuthorizationVersion != s.AuthorizationVersion || resource.ConsentVersion != s.ConsentVersion {
		return service.ErrSupplierDispatchConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state='dispatched',dispatched_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND state='accepted'`, attempt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrSupplierDispatchConflict
	}
	return tx.Commit()
}

// Match lifecycle lock order and hold only through the pre-send CAS. RR turns
// a concurrent changed row into a serialization failure rather than authorizing
// an obsolete credential/consent snapshot. Lock waits are bounded and fail closed.
func lockSupplierSendEligibility(ctx context.Context, tx *sql.Tx, s service.SupplierDispatchSnapshot) error {
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'`); err != nil {
		return err
	}
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT 1 FROM settings WHERE key='supplier_operating_policy_v1' FOR SHARE`, nil},
		{`SELECT 1 FROM groups WHERE id=$1 FOR SHARE`, []any{s.GroupID}},
		{`SELECT 1 FROM supplier_resources WHERE id=$1 FOR SHARE`, []any{s.ResourceID}},
		{`SELECT 1 FROM supplier_profiles p JOIN users u ON u.id=p.user_id WHERE p.id=$1 FOR SHARE OF p,u`, []any{s.SupplierID}},
		{`SELECT 1 FROM supplier_account_bindings WHERE resource_id=$1 AND account_id=$2 FOR SHARE`, []any{s.ResourceID, s.AccountID}},
		{`SELECT 1 FROM accounts WHERE id=$1 FOR SHARE`, []any{s.AccountID}},
		{`SELECT 1 FROM account_groups WHERE account_id=$1 AND group_id=$2 FOR SHARE`, []any{s.AccountID, s.GroupID}},
		{`SELECT 1 FROM users WHERE id=$1 FOR SHARE`, []any{s.UserID}},
		{`SELECT 1 FROM api_keys WHERE id=$1 FOR SHARE`, []any{s.APIKeyID}},
	}
	for _, check := range checks {
		var one int
		if err := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&one); err != nil {
			return err
		}
	}
	return nil
}

func supplierOneOf(v string, choices ...string) bool {
	for _, c := range choices {
		if v == c {
			return true
		}
	}
	return false
}

func normalizeSupplierObservation(o service.SupplierUsageObservation) (service.SupplierUsageObservation, []byte, string, error) {
	// Header-derived identifiers and caller timestamps are not journal content.
	o.UpstreamRequestID = ""
	o.ObservedAt = time.Time{}
	if !supplierSafeModel.MatchString(o.Model) || !supplierOneOf(o.Source, "native_json", "native_sse", "buffered_sse", "responses_json", "responses_sse") || !supplierOneOf(o.Terminal, "", "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error", "json", "[DONE]", "completed", "incomplete", "failed", "unknown") {
		return o, nil, "", service.ErrSupplierDispatchConflict
	}
	if !supplierOneOf(o.ServiceTier, "", "auto", "default", "flex", "priority", "scale", "standard") || !supplierOneOf(o.ReasoningEffort, "", "none", "minimal", "low", "medium", "high", "xhigh") {
		return o, nil, "", service.ErrSupplierDispatchConflict
	}
	u := o.Usage
	for _, v := range []int{u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.ImageInputTokens, u.ImageCacheReadTokens, u.ImageOutputTokens} {
		if v < 0 || int64(v) > 1000000000 {
			return o, nil, "", service.ErrSupplierDispatchConflict
		}
	}
	if u.CacheReadInputTokens+u.CacheCreationInputTokens > u.InputTokens {
		o.Complete = false
	}
	if !o.UsagePresent || !supplierOneOf(o.Terminal, "response.completed", "response.done", "completed", "json") {
		o.Complete = false
	}
	b, err := json.Marshal(o)
	if err != nil {
		return o, nil, "", err
	}
	return o, b, supplierHash(b), nil
}

func (r *usageBillingRepository) ObserveSupplierUsage(ctx context.Context, attempt, digest string, o service.SupplierUsageObservation) error {
	if r == nil || r.db == nil {
		return service.ErrSupplierDispatchUnavailable
	}
	o, payload, hash, err := normalizeSupplierObservation(o)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := lockSupplierDispatch(ctx, tx, attempt, digest)
	if err != nil {
		return err
	}
	if o.Model != row.snapshot.Model || row.state == "accepted" {
		return service.ErrSupplierDispatchConflict
	}
	// Save the typed evidence first, including conflicting later observations.
	_, err = tx.ExecContext(ctx, `INSERT INTO supplier_dispatch_observations(attempt_id,observation_hash,observation) VALUES($1,$2,$3::jsonb) ON CONFLICT DO NOTHING`, attempt, hash, string(payload))
	if err != nil {
		return err
	}
	if row.observationHash.Valid && row.observationHash.String == hash {
		return tx.Commit()
	}
	if row.command.Valid {
		_, err = tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state=CASE WHEN state='pending' THEN 'review' ELSE state END,review_reason='observation_conflict',updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1`, attempt)
		if err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return service.ErrSupplierDispatchConflict
	}
	var previous service.SupplierUsageObservation
	if len(row.observation) > 0 && json.Unmarshal(row.observation, &previous) != nil {
		return service.ErrSupplierDispatchConflict
	}
	// A complete observation cannot be silently replaced by another result. A
	// progressive partial observation may be superseded, with both kept as evidence.
	if previous.Complete && previous.UsagePresent {
		_, err = tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state='review',review_reason='observation_conflict',updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1`, attempt)
	} else {
		state, reason := "observed", ""
		if !o.Complete || !o.UsagePresent {
			state, reason = "review", "usage_unknown"
		}
		_, err = tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state=$2,observation=$3::jsonb,observation_hash=$4,observed_at=CURRENT_TIMESTAMP,review_reason=$5,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1`, attempt, state, string(payload), hash, reason)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *usageBillingRepository) MarkSupplierReview(ctx context.Context, attempt, digest, reason string) error {
	if r == nil || r.db == nil {
		return service.ErrSupplierDispatchUnavailable
	}
	if !supplierUUID(attempt) || !supplierDigest.MatchString(digest) {
		return service.ErrSupplierDispatchConflict
	}
	// Caller error strings can contain secrets. Only repository-owned codes persist.
	if !supplierOneOf(reason, "usage_unknown", "observation_conflict", "billing_conflict", "invalid_command", "stale_unstaged", "dispatch_failed", "pricing_unavailable", "not_dispatched", "usage_unresolved", "unsupported_usage") {
		reason = "usage_unknown"
	}
	res, err := r.db.ExecContext(ctx, `UPDATE supplier_dispatches SET state=CASE WHEN state IN ('pending','committed') THEN state ELSE 'review' END,review_reason=$3,updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND snapshot_digest=$2`, attempt, digest, reason)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrSupplierDispatchConflict
	}
	return nil
}

func supplierCommandJournaled(cmd *service.UsageBillingCommand) bool {
	return cmd != nil && (strings.TrimSpace(cmd.SupplierAttemptID) != "" || strings.TrimSpace(cmd.SupplierSnapshotDigest) != "")
}
