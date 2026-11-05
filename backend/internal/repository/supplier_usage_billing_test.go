//go:build unit

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const supplierTestResourceID = "018f0000-0000-7000-8000-000000000001"
const supplierTestAttemptID = "018f0000-0000-7000-8000-000000000002"

func supplierTestCommand() *service.UsageBillingCommand {
	return &service.UsageBillingCommand{RequestID: "supplier:" + supplierTestAttemptID, APIKeyID: 3, UserID: 4, AccountID: 5, AccountType: service.AccountTypeOAuth, Model: "gpt-5.2", InputTokens: 85, OutputTokens: 20, CacheCreationTokens: 7, CacheReadTokens: 8, SupplierCostProvided: true, StandardCost: 0.000078125, CustomerCharge: 0.2, BalanceCost: 0.2}
}
func supplierTestRow(t *testing.T) (*service.UsageBillingCommand, *supplierDispatchRow) {
	t.Helper()
	s := service.SupplierDispatchSnapshot{AttemptID: supplierTestAttemptID, RequestID: "018f0000-0000-7000-8000-000000000003", ResourceID: supplierTestResourceID, SupplierID: 6, AccountID: 5, CredentialVersion: 2, AuthorizationVersion: 3, ConsentVersion: service.SupplierConsentVersion, UserID: 4, APIKeyID: 3, GroupID: 7, Model: "gpt-5.2", FeePolicy: service.SupplierFeePolicy{Mode: service.SupplierFeeModeStandardCost, RatioBPS: 7000, Version: 9}, AcceptedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s.Digest = supplierSnapshotHash(s)
	_, o, h, err := normalizeSupplierObservation(service.SupplierUsageObservation{Usage: service.OpenAIUsage{InputTokens: 100, OutputTokens: 20, CacheCreationInputTokens: 7, CacheReadInputTokens: 8}, UsagePresent: true, Complete: true, Source: "native_json", Terminal: "json", Model: s.Model})
	require.NoError(t, err)
	cmd := supplierTestCommand()
	cmd.SupplierAttemptID = s.AttemptID
	cmd.SupplierSnapshotDigest = s.Digest
	p, ch, err := supplierCommandPayload(cmd)
	require.NoError(t, err)
	return cmd, &supplierDispatchRow{snapshot: s, state: "pending", observation: o, observationHash: sql.NullString{String: h, Valid: true}, command: sql.NullString{String: string(p), Valid: true}, commandHash: sql.NullString{String: ch, Valid: true}}
}
func supplierMockRow(t *testing.T, row *supplierDispatchRow) *sqlmock.Rows {
	t.Helper()
	b, err := json.Marshal(row.snapshot)
	require.NoError(t, err)
	return sqlmock.NewRows([]string{"snapshot", "state", "observation", "observation_hash", "command_payload", "command_hash"}).AddRow(b, row.state, row.observation, row.observationHash, row.command, row.commandHash)
}
func expectSupplierClaim(mock sqlmock.Sqlmock, cmd *service.UsageBillingCommand) {
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT request_fingerprint.*usage_billing_dedup_archive").WithArgs(cmd.RequestID, cmd.APIKeyID).WillReturnError(sql.ErrNoRows)
}

func TestSupplierUsageBillingAtomicSnapshot(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "supplier insert rolls back"}[failure], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			cmd, row := supplierTestRow(t)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
			expectSupplierClaim(mock, cmd)
			mock.ExpectQuery(conditionalBalanceDeductSQL).WithArgs(cmd.BalanceCost, cmd.UserID).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(9.8))
			insert := mock.ExpectExec("INSERT INTO supplier_usage").WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, cmd.UserID, cmd.AccountID, supplierTestResourceID, int64(6), int64(2), cmd.Model, cmd.ServiceTier, cmd.ReasoningEffort, cmd.BillingType, cmd.InputTokens, cmd.OutputTokens, cmd.CacheCreationTokens, cmd.CacheReadTokens, cmd.ImageCount, cmd.MediaType, cmd.StandardCost, cmd.CustomerCharge, string(service.SupplierFeeModeStandardCost), 7000, int64(9), 0.00007813, 0.00005469, supplierTestAttemptID, row.snapshot.Digest, int64(3), service.SupplierConsentVersion, int64(7))
			if failure {
				insert.WillReturnError(errors.New("write failed"))
				mock.ExpectRollback()
			} else {
				insert.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec("SELECT enqueue_auth_cache_invalidation").WithArgs(cmd.APIKeyID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE supplier_dispatches SET state='committed'").WithArgs(cmd.SupplierAttemptID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			got, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
			if failure {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.True(t, got.Applied)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
func TestSupplierUsageBillingDedupDoesNotReadOrReprice(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cmd := supplierTestCommand()
	cmd.Normalize()
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("(?s)SELECT request_fingerprint.*usage_billing_dedup").WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}).AddRow(cmd.RequestFingerprint))
	mock.ExpectRollback()
	got, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, got.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierUsageBillingCommittedJournalDoesNotReprice(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cmd, row := supplierTestRow(t)
	row.state = "committed"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
	mock.ExpectRollback()
	got, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, got.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierUsageBillingNewUnjournaledRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cmd := supplierTestCommand()
	cmd.Normalize()
	mock.ExpectBegin()
	expectSupplierClaim(mock, cmd)
	mock.ExpectQuery(conditionalBalanceDeductSQL).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(9.8))
	mock.ExpectQuery("SELECT resource_id, supplier_id, credential_version").WillReturnRows(sqlmock.NewRows([]string{"resource_id", "supplier_id", "credential_version"}).AddRow(supplierTestResourceID, 6, 2))
	mock.ExpectRollback()
	got, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.ErrorIs(t, err, service.ErrSupplierDispatchUnavailable)
	require.Nil(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierUsageBillingLegacyUnbound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cmd := supplierTestCommand()
	cmd.SupplierCostProvided = false
	cmd.StandardCost = 0
	cmd.CustomerCharge = 0
	cmd.Normalize()
	mock.ExpectBegin()
	expectSupplierClaim(mock, cmd)
	mock.ExpectQuery(conditionalBalanceDeductSQL).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(9.8))
	mock.ExpectQuery("SELECT resource_id, supplier_id, credential_version").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT EXISTS.*FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectCommit()
	got, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, got.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierUsageBillingCommandValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*service.UsageBillingCommand)
	}{
		{"changed identity", func(c *service.UsageBillingCommand) { c.UserID++ }}, {"changed tokens", func(c *service.UsageBillingCommand) { c.InputTokens++ }}, {"missing explicit cost", func(c *service.UsageBillingCommand) { c.SupplierCostProvided = false }}, {"changed model", func(c *service.UsageBillingCommand) { c.Model = "gpt-5" }}, {"inconsistent charge", func(c *service.UsageBillingCommand) { c.BalanceCost++ }}, {"media", func(c *service.UsageBillingCommand) { c.ImageCount = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, row := supplierTestRow(t)
			tc.change(cmd)
			require.Error(t, validateSupplierCommand(cmd, row))
		})
	}
	for _, amount := range []float64{-1, math.NaN(), math.Inf(1), 1e12} {
		cmd, _ := supplierTestRow(t)
		cmd.StandardCost = amount
		_, _, err := supplierCommandPayload(cmd)
		require.Error(t, err)
	}
	cmd, row := supplierTestRow(t)
	require.NoError(t, validateSupplierCommand(cmd, row))
	original := cmd.RequestFingerprint
	_, _, err := supplierCommandPayload(cmd)
	require.NoError(t, err)
	require.Equal(t, original, cmd.RequestFingerprint)
	cmd.RequestPayloadHash = "client:raw-request-content"
	_, _, err = supplierCommandPayload(cmd)
	require.Error(t, err)
}
func TestSupplierUsageBillingLegacyFingerprintUnchanged(t *testing.T) {
	cmd := &service.UsageBillingCommand{}
	cmd.Normalize()
	raw := "0|0|0|||||0|0|0|0|0|0||0|0.0000000000|0.0000000000|0.0000000000|0.0000000000|0.0000000000"
	h := sha256.Sum256([]byte(raw))
	require.Equal(t, hex.EncodeToString(h[:]), cmd.RequestFingerprint)
	strict := &service.UsageBillingCommand{SupplierAttemptID: supplierTestAttemptID}
	strict.Normalize()
	require.NotEqual(t, cmd.RequestFingerprint, strict.RequestFingerprint)
}
func TestSupplierUsageBillingMissingRepositoryDBFailsClosed(t *testing.T) {
	for _, r := range []*usageBillingRepository{nil, {}} {
		got, err := r.Apply(context.Background(), supplierTestCommand())
		require.ErrorContains(t, err, "db is nil")
		require.Nil(t, got)
	}
}
