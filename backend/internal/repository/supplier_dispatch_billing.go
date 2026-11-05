package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func supplierCommandPayload(cmd *service.UsageBillingCommand) ([]byte, string, error) {
	if cmd == nil || !supplierUUID(cmd.SupplierAttemptID) || !supplierDigest.MatchString(cmd.SupplierSnapshotDigest) {
		return nil, "", service.ErrSupplierDispatchConflict
	}
	cmd.Normalize()
	if cmd.RequestID != "supplier:"+cmd.SupplierAttemptID || !supplierDigest.MatchString(cmd.RequestFingerprint) || (cmd.RequestPayloadHash != "" && !supplierDigest.MatchString(cmd.RequestPayloadHash)) {
		return nil, "", service.ErrSupplierDispatchConflict
	}
	for _, v := range []float64{cmd.StandardCost, cmd.CustomerCharge, cmd.BalanceCost, cmd.SubscriptionCost, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost, cmd.AccountQuotaCost} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= 1e12 {
			return nil, "", service.ErrSupplierDispatchConflict
		}
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, "", err
	}
	if len(b) > 16384 {
		return nil, "", service.ErrSupplierDispatchConflict
	}
	return b, supplierHash(b), nil
}

func validateSupplierCommand(cmd *service.UsageBillingCommand, row *supplierDispatchRow) error {
	s := row.snapshot
	if cmd.UserID != s.UserID || cmd.APIKeyID != s.APIKeyID || cmd.AccountID != s.AccountID || cmd.Model != s.Model || cmd.AccountType != service.AccountTypeOAuth || !cmd.SupplierCostProvided {
		return service.ErrSupplierDispatchConflict
	}
	var o service.SupplierUsageObservation
	if len(row.observation) == 0 || json.Unmarshal(row.observation, &o) != nil || !o.Complete || !o.UsagePresent || !row.observationHash.Valid {
		return service.ErrSupplierAccountingPending
	}
	_, payload, hash, err := normalizeSupplierObservation(o)
	if err != nil || len(payload) == 0 || hash != row.observationHash.String || o.Model != s.Model {
		return service.ErrSupplierDispatchConflict
	}
	u := o.Usage
	if cmd.InputTokens != u.InputTokens-u.CacheReadInputTokens-u.CacheCreationInputTokens || cmd.OutputTokens != u.OutputTokens || cmd.CacheCreationTokens != u.CacheCreationInputTokens || cmd.CacheReadTokens != u.CacheReadInputTokens || cmd.ServiceTier != o.ServiceTier || cmd.ReasoningEffort != o.ReasoningEffort {
		return service.ErrSupplierDispatchConflict
	}
	// Native P0 text responses only. Image/media accounting must be explicitly
	// modeled in the observation before it can be enabled in this journal.
	if cmd.ImageCount != 0 || cmd.MediaType != "" || u.ImageInputTokens != 0 || u.ImageCacheReadTokens != 0 || u.ImageOutputTokens != 0 {
		return service.ErrSupplierAccountingPending
	}
	if cmd.BillingType != service.BillingTypeBalance && cmd.BillingType != service.BillingTypeSubscription {
		return service.ErrSupplierDispatchConflict
	}
	if cmd.SubscriptionID != nil && *cmd.SubscriptionID <= 0 {
		return service.ErrSupplierDispatchConflict
	}
	if cmd.SubscriptionCost > 0 && (cmd.SubscriptionID == nil || cmd.BillingType != service.BillingTypeSubscription || cmd.BalanceCost != 0) {
		return service.ErrSupplierDispatchConflict
	}
	if cmd.BalanceCost > 0 && cmd.SubscriptionCost > 0 {
		return service.ErrSupplierDispatchConflict
	}
	// A zero debit is valid in simple mode; positive customer effects must equal
	// the frozen charge, never an independently supplied second monetary amount.
	for _, v := range []float64{cmd.BalanceCost, cmd.SubscriptionCost, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost} {
		if v != 0 && v != cmd.CustomerCharge {
			return service.ErrSupplierDispatchConflict
		}
	}
	_, err = s.FeePolicy.Calculate(service.CostBreakdown{TotalCost: cmd.StandardCost, ActualCost: cmd.CustomerCharge})
	return err
}

func (r *usageBillingRepository) StageSupplierBilling(ctx context.Context, cmd *service.UsageBillingCommand) error {
	if r == nil || r.db == nil {
		return service.ErrSupplierDispatchUnavailable
	}
	payload, hash, err := supplierCommandPayload(cmd)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := lockSupplierDispatch(ctx, tx, cmd.SupplierAttemptID, cmd.SupplierSnapshotDigest)
	if err != nil {
		return err
	}
	if row.command.Valid {
		if row.command.String != string(payload) || row.commandHash.String != hash {
			return service.ErrSupplierDispatchConflict
		}
		if row.state != "pending" && row.state != "committed" {
			return service.ErrSupplierAccountingPending
		}
		return tx.Commit()
	}
	if row.state != "observed" {
		return service.ErrSupplierAccountingPending
	}
	if err = validateSupplierCommand(cmd, row); err != nil {
		return err
	}
	if cmd.SubscriptionID != nil {
		var found int
		if err = tx.QueryRowContext(ctx, `SELECT 1 FROM user_subscriptions WHERE id=$1 AND user_id=$2 AND group_id=$3`, *cmd.SubscriptionID, cmd.UserID, row.snapshot.GroupID).Scan(&found); err != nil {
			return service.ErrSupplierDispatchConflict
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state='pending',command_payload=$2,command_hash=$3,staged_at=CURRENT_TIMESTAMP,next_retry_at=CURRENT_TIMESTAMP,review_reason='',updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1`, cmd.SupplierAttemptID, string(payload), hash)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func lockStagedSupplierCommand(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (*supplierDispatchRow, error) {
	payload, hash, err := supplierCommandPayload(cmd)
	if err != nil {
		return nil, err
	}
	row, err := lockSupplierDispatch(ctx, tx, cmd.SupplierAttemptID, cmd.SupplierSnapshotDigest)
	if err != nil {
		return nil, err
	}
	if !row.command.Valid || row.command.String != string(payload) || !row.commandHash.Valid || row.commandHash.String != hash {
		return nil, service.ErrSupplierDispatchConflict
	}
	if row.state != "pending" && row.state != "committed" {
		return nil, service.ErrSupplierAccountingPending
	}
	if err = validateSupplierCommand(cmd, row); err != nil {
		return nil, err
	}
	return row, nil
}

func commitSupplierDispatch(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	// Existing outbox hashes the API credential entirely inside PostgreSQL. Neither
	// the journal nor the repair queue contains raw credentials.
	if _, err := tx.ExecContext(ctx, `SELECT enqueue_auth_cache_invalidation(key) FROM api_keys WHERE id=$1`, cmd.APIKeyID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE supplier_dispatches SET state='committed',committed_at=CURRENT_TIMESTAMP,repair_pending=TRUE,repair_available_at=CURRENT_TIMESTAMP,lease_token=NULL,lease_until=NULL,review_reason='',updated_at=CURRENT_TIMESTAMP WHERE attempt_id=$1 AND state='pending'`, cmd.SupplierAttemptID)
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
