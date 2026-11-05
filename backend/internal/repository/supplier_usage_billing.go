package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// recordSupplierUsageBilling shares the customer transaction. Mutable bindings
// are used ONLY to reject new unjournaled supplier charges, never for attribution
// or pricing. Historical committed duplicate commands are handled before here.
func recordSupplierUsageBilling(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, dispatch *supplierDispatchRow) error {
	if dispatch == nil {
		var resourceID string
		var supplierID, credentialVersion int64
		err := tx.QueryRowContext(ctx, `SELECT resource_id, supplier_id, credential_version FROM supplier_account_bindings WHERE account_id=$1 FOR SHARE`, cmd.AccountID).Scan(&resourceID, &supplierID, &credentialVersion)
		if errors.Is(err, sql.ErrNoRows) {
			if cmd.SupplierCostProvided || cmd.StandardCost != 0 || cmd.CustomerCharge != 0 || supplierCommandJournaled(cmd) {
				return service.ErrSupplierDispatchUnavailable
			}
			// A missing binding cannot make a still-marked supplier account legacy.
			var marked bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND (credentials ? 'supplier_resource_id' OR extra ? 'supplier_resource_id' OR extra ? 'supplier_id'))`, cmd.AccountID).Scan(&marked)
			if err != nil {
				return err
			}
			if marked {
				return service.ErrSupplierDispatchUnavailable
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("supplier billing binding: %w", err)
		}
		return service.ErrSupplierDispatchUnavailable
	}
	s := dispatch.snapshot
	fee, err := s.FeePolicy.Calculate(service.CostBreakdown{TotalCost: cmd.StandardCost, ActualCost: cmd.CustomerCharge})
	if err != nil {
		return err
	}
	// Unexpected duplicates roll back ALL customer effects. Acceptance identity and
	// fee policy are immutable even after deletion, rotation or consent revocation.
	_, err = tx.ExecContext(ctx, `INSERT INTO supplier_usage (
 request_id,api_key_id,request_fingerprint,user_id,account_id,resource_id,supplier_id,credential_version,
 model,service_tier,reasoning_effort,billing_type,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,
 image_count,media_type,standard_cost,customer_charge,fee_mode,fee_ratio_bps,policy_version,fee_base_amount,fee_amount,
 supplier_attempt_id,supplier_snapshot_digest,authorization_version,consent_version,group_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
		cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, cmd.UserID, cmd.AccountID, s.ResourceID, s.SupplierID, s.CredentialVersion,
		cmd.Model, cmd.ServiceTier, cmd.ReasoningEffort, cmd.BillingType, cmd.InputTokens, cmd.OutputTokens, cmd.CacheCreationTokens, cmd.CacheReadTokens,
		cmd.ImageCount, cmd.MediaType, cmd.StandardCost, cmd.CustomerCharge, fee.Mode, fee.RatioBPS, fee.Version, fee.BaseAmount, fee.FeeAmount,
		s.AttemptID, s.Digest, s.AuthorizationVersion, s.ConsentVersion, s.GroupID)
	if err != nil {
		return fmt.Errorf("supplier billing insert: %w", err)
	}
	return nil
}
