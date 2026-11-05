//go:build unit

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const supplierTestResourceID = "018f0000-0000-7000-8000-000000000001"

func supplierTestCommand() *service.UsageBillingCommand {
	return &service.UsageBillingCommand{RequestID: "supplier-request", APIKeyID: 3, UserID: 4, AccountID: 5,
		Model: "test-model", InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 7, CacheReadTokens: 8,
		SupplierCostProvided: true, StandardCost: 0.000078125, CustomerCharge: 0.2, BalanceCost: 0.2}
}

func expectSupplierClaim(mock sqlmock.Sqlmock, cmd *service.UsageBillingCommand) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT request_fingerprint.*usage_billing_dedup_archive").WithArgs(cmd.RequestID, cmd.APIKeyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(conditionalBalanceDeductSQL).WithArgs(cmd.BalanceCost, cmd.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(9.8))
}

func expectSupplierBinding(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT resource_id, supplier_id, credential_version").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "supplier_id", "credential_version"}).AddRow(supplierTestResourceID, 6, 2))
}

func TestSupplierUsageBillingAtomicSnapshot(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		base, fee float64
	}{
		{"standard_cost", 0.00007813, 0.00005469}, {"customer_charge", 0.2, 0.14},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			cmd := supplierTestCommand()
			cmd.Normalize()
			expectSupplierClaim(mock, cmd)
			expectSupplierBinding(mock)
			mock.ExpectQuery("SELECT value FROM settings").WithArgs("supplier_operating_policy_v1").
				WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(`{"fee_mode":"` + tc.mode + `","fee_ratio_bps":7000,"version":9}`))
			mock.ExpectExec("INSERT INTO supplier_usage").WithArgs(
				cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, cmd.UserID, cmd.AccountID, supplierTestResourceID, int64(6), int64(2),
				cmd.Model, cmd.ServiceTier, cmd.ReasoningEffort, cmd.BillingType, cmd.InputTokens, cmd.OutputTokens, cmd.CacheCreationTokens, cmd.CacheReadTokens,
				cmd.ImageCount, cmd.MediaType, cmd.StandardCost, cmd.CustomerCharge, tc.mode, 7000, int64(9), tc.base, tc.fee).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSupplierUsageBillingFailuresRollbackCustomerEffects(t *testing.T) {
	for _, tc := range []struct {
		name, policy                                              string
		missingCost, missingBinding, missingPolicy, insertFailure bool
	}{
		{name: "missing cost", missingCost: true}, {name: "missing binding", missingBinding: true},
		{name: "missing policy", missingPolicy: true},
		{name: "malformed policy", policy: `{`},
		{name: "missing ratio", policy: `{"fee_mode":"standard_cost","version":1}`},
		{name: "missing version", policy: `{"fee_mode":"standard_cost","fee_ratio_bps":7000}`},
		{name: "invalid mode", policy: `{"fee_mode":"other","fee_ratio_bps":7000,"version":1}`},
		{name: "invalid ratio", policy: `{"fee_mode":"standard_cost","fee_ratio_bps":10001,"version":1}`},
		{name: "insert error", policy: `{"fee_mode":"standard_cost","fee_ratio_bps":7000,"version":1}`, insertFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			cmd := supplierTestCommand()
			cmd.SupplierCostProvided = !tc.missingCost
			cmd.Normalize()
			expectSupplierClaim(mock, cmd)
			if tc.missingBinding {
				mock.ExpectQuery("SELECT resource_id, supplier_id, credential_version").WithArgs(cmd.AccountID).WillReturnError(sql.ErrNoRows)
			} else {
				expectSupplierBinding(mock)
			}
			if !tc.missingCost && !tc.missingBinding {
				q := mock.ExpectQuery("SELECT value FROM settings").WithArgs("supplier_operating_policy_v1")
				if tc.missingPolicy {
					q.WillReturnError(sql.ErrNoRows)
				} else {
					q.WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(tc.policy))
				}
			}
			if tc.insertFailure {
				mock.ExpectExec("INSERT INTO supplier_usage").WillReturnError(errors.New("write failed"))
			}
			mock.ExpectRollback()
			result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
			require.Error(t, err)
			require.Nil(t, result)
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
	result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierUsageBillingLegacyUnbound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cmd := supplierTestCommand()
	cmd.StandardCost = 0
	cmd.CustomerCharge = 0
	cmd.SupplierCostProvided = false
	cmd.Normalize()
	expectSupplierClaim(mock, cmd)
	mock.ExpectQuery("SELECT resource_id, supplier_id, credential_version").WithArgs(cmd.AccountID).WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()
	result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierUsageBillingZeroAndInvalidCosts(t *testing.T) {
	for _, amount := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		t.Run(fmtCostTestName(amount), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			cmd := supplierTestCommand()
			cmd.StandardCost = amount
			cmd.CustomerCharge = 0
			expectSupplierBinding(mock)
			mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(`{"fee_mode":"standard_cost","fee_ratio_bps":0,"version":1}`))
			if amount == 0 {
				mock.ExpectExec("INSERT INTO supplier_usage").WillReturnResult(sqlmock.NewResult(1, 1))
			}
			err = recordSupplierUsageBilling(context.Background(), tx, cmd)
			if amount == 0 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func fmtCostTestName(amount float64) string {
	if math.IsNaN(amount) {
		return "nan"
	}
	if math.IsInf(amount, 0) {
		return "infinite"
	}
	if amount < 0 {
		return "negative"
	}
	return "explicit zero"
}

func TestSupplierUsageBillingMissingRepositoryDBFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo *usageBillingRepository
	}{
		{name: "nil repository"},
		{name: "nil database", repo: &usageBillingRepository{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.repo.Apply(context.Background(), supplierTestCommand())
			require.ErrorContains(t, err, "db is nil")
			require.Nil(t, result)
		})
	}
}

func TestSupplierUsageBillingLegacyFingerprintUnchanged(t *testing.T) {
	cmd := &service.UsageBillingCommand{}
	cmd.Normalize()
	raw := "0|0|0|||||0|0|0|0|0|0||0|0.0000000000|0.0000000000|0.0000000000|0.0000000000|0.0000000000"
	hash := sha256.Sum256([]byte(raw))
	require.Equal(t, hex.EncodeToString(hash[:]), cmd.RequestFingerprint)
	extended := &service.UsageBillingCommand{SupplierCostProvided: true}
	extended.Normalize()
	require.NotEqual(t, cmd.RequestFingerprint, extended.RequestFingerprint)
	changed := &service.UsageBillingCommand{StandardCost: 1}
	changed.Normalize()
	require.NotEqual(t, cmd.RequestFingerprint, changed.RequestFingerprint)
}
