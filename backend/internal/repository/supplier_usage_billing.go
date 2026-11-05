package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// recordSupplierUsageBilling is part of the customer billing transaction, never
// the best-effort usage log path. The caller must already own the dedup claim.
func recordSupplierUsageBilling(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var resourceID string
	var supplierID, credentialVersion int64
	err := tx.QueryRowContext(ctx, `
  SELECT resource_id, supplier_id, credential_version
  FROM supplier_account_bindings WHERE account_id = $1
  FOR SHARE
 `, cmd.AccountID).Scan(&resourceID, &supplierID, &credentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		if cmd.SupplierCostProvided || cmd.StandardCost != 0 || cmd.CustomerCharge != 0 {
			return errors.New("supplier billing: cost snapshot provided but account binding missing")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("supplier billing binding: %w", err)
	}
	if !cmd.SupplierCostProvided {
		return errors.New("supplier billing: explicit cost snapshot required")
	}

	// Read authoritative settings in the same transaction. Never silently invent
	// a default ratio/version; zero BPS is valid only when explicitly configured.
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1 FOR SHARE`, "supplier_operating_policy_v1").Scan(&raw); err != nil {
		return fmt.Errorf("supplier billing policy: %w", err)
	}
	var policy struct {
		Mode     service.SupplierFeeMode `json:"fee_mode"`
		RatioBPS *int                    `json:"fee_ratio_bps"`
		Version  int64                   `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return fmt.Errorf("supplier billing policy: %w", err)
	}
	if policy.RatioBPS == nil {
		return errors.New("supplier billing policy: fee_ratio_bps required")
	}
	snapshot, err := (service.SupplierFeePolicy{Mode: policy.Mode, RatioBPS: *policy.RatioBPS, Version: policy.Version}).Calculate(service.CostBreakdown{
		TotalCost: cmd.StandardCost, ActualCost: cmd.CustomerCharge,
	})
	if err != nil {
		return err
	}

	// No ON CONFLICT: any unexpected duplicate must roll back customer effects,
	// rather than commit a customer charge without its matching supplier fact.
	_, err = tx.ExecContext(ctx, `
  INSERT INTO supplier_usage (
   request_id, api_key_id, request_fingerprint, user_id,
   account_id, resource_id, supplier_id, credential_version,
   model, service_tier, reasoning_effort, billing_type,
   input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
   image_count, media_type, standard_cost, customer_charge,
   fee_mode, fee_ratio_bps, policy_version, fee_base_amount, fee_amount
  ) VALUES (
   $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
   $17,$18,$19,$20,$21,$22,$23,$24,$25
  )`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, cmd.UserID,
		cmd.AccountID, resourceID, supplierID, credentialVersion,
		cmd.Model, cmd.ServiceTier, cmd.ReasoningEffort, cmd.BillingType,
		cmd.InputTokens, cmd.OutputTokens, cmd.CacheCreationTokens, cmd.CacheReadTokens,
		cmd.ImageCount, cmd.MediaType, cmd.StandardCost, cmd.CustomerCharge,
		snapshot.Mode, snapshot.RatioBPS, snapshot.Version, snapshot.BaseAmount, snapshot.FeeAmount)
	if err != nil {
		return fmt.Errorf("supplier billing insert: %w", err)
	}
	return nil
}
