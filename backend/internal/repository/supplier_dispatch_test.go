//go:build unit

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierDispatchRequestValidation(t *testing.T) {
	req := service.SupplierDispatchRequest{AttemptID: supplierTestAttemptID, RequestID: supplierTestResourceID, ResourceID: supplierTestResourceID, UserID: 4, APIKeyID: 3, GroupID: 7, AccountID: 5, Model: "gpt-5.2"}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &service.Group{ID: 7})
	require.True(t, supplierRequestValid(ctx, req))
	require.False(t, supplierRequestValid(context.Background(), req))
	require.False(t, supplierRequestValid(context.WithValue(ctx, ctxkey.UserID, int64(999)), req))
	req.RequestID = "client-supplied"
	require.False(t, supplierRequestValid(ctx, req))
	req.RequestID = supplierTestResourceID
	req.Model = "prompt\nsecret"
	require.False(t, supplierRequestValid(ctx, req))
}
func TestSupplierDispatchObservationBoundedAndNoHeaders(t *testing.T) {
	o := service.SupplierUsageObservation{Model: "gpt-5.2", Source: "native_json", Terminal: "json", UsagePresent: true, Complete: true, UpstreamRequestID: "Bearer SHOULD-NOT-PERSIST", ObservedAt: time.Now()}
	got, b, h, err := normalizeSupplierObservation(o)
	require.NoError(t, err)
	require.True(t, got.Complete)
	require.NotContains(t, string(b), "SHOULD-NOT-PERSIST")
	require.True(t, got.ObservedAt.IsZero())
	require.Len(t, h, 64)
	o.ObservedAt = o.ObservedAt.Add(time.Hour)
	_, _, h2, err := normalizeSupplierObservation(o)
	require.NoError(t, err)
	require.Equal(t, h, h2)
	o.UsagePresent = false
	got, _, _, err = normalizeSupplierObservation(o)
	require.NoError(t, err)
	require.False(t, got.Complete)
	o.Source = "raw prompt secret"
	_, _, _, err = normalizeSupplierObservation(o)
	require.Error(t, err)
	o.Source = "native_json"
	o.Usage.InputTokens = -1
	_, _, _, err = normalizeSupplierObservation(o)
	require.Error(t, err)
}
func TestSupplierDispatchSnapshotDigestFreezesAllTerms(t *testing.T) {
	_, row := supplierTestRow(t)
	s := row.snapshot
	require.Equal(t, s.Digest, supplierSnapshotHash(s))
	s.FeePolicy.RatioBPS++
	require.NotEqual(t, s.Digest, supplierSnapshotHash(s))
	s = row.snapshot
	s.AuthorizationVersion++
	require.NotEqual(t, s.Digest, supplierSnapshotHash(s))
	s = row.snapshot
	s.CredentialVersion++
	require.NotEqual(t, s.Digest, supplierSnapshotHash(s))
}
func TestSupplierDispatchMarkRepeatedNeverSends(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_, row := supplierTestRow(t)
	row.state = "dispatched"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
	mock.ExpectRollback()
	err = (&usageBillingRepository{db: db}).MarkSupplierDispatched(context.Background(), row.snapshot.AttemptID, row.snapshot.Digest)
	require.ErrorIs(t, err, service.ErrSupplierDispatchConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierDispatchStageDuplicateAndConflict(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "same command", true: "changed command"}[change], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			cmd, row := supplierTestRow(t)
			if change {
				cmd.StandardCost++
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
			if change {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			err = (&usageBillingRepository{db: db}).StageSupplierBilling(context.Background(), cmd)
			if change {
				require.ErrorIs(t, err, service.ErrSupplierDispatchConflict)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
func TestSupplierDispatchObservationConflictPreservesEvidence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_, row := supplierTestRow(t)
	row.state = "observed"
	row.command = sql.NullString{}
	row.commandHash = sql.NullString{}
	var o service.SupplierUsageObservation
	require.NoError(t, json.Unmarshal(row.observation, &o))
	o.Usage.OutputTokens++
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
	mock.ExpectExec("INSERT INTO supplier_dispatch_observations").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE supplier_dispatches SET state='review'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, (&usageBillingRepository{db: db}).ObserveSupplierUsage(context.Background(), row.snapshot.AttemptID, row.snapshot.Digest, o))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierDispatchRecoveryReplaysOnlyPersistedCommand(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_, row := supplierTestRow(t)
	mock.ExpectExec("(?s)WITH candidates AS.*48 hours.*SKIP LOCKED").WithArgs(20).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery("(?s)WITH candidates AS.*next_retry_at.*SKIP LOCKED.*lease_token").WithArgs(20, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"attempt_id", "command_payload", "command_hash", "retry_count"}).AddRow(row.snapshot.AttemptID, row.command.String, row.commandHash.String, 1))
	// Simulate restart after an ambiguous commit: exact staged command finds its
	// committed journal, with no pricing/settings/resource/provider calls.
	row.state = "committed"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT snapshot,state").WillReturnRows(supplierMockRow(t, row))
	mock.ExpectRollback()
	got, err := (&usageBillingRepository{db: db}).RecoverSupplierBilling(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, got.Replayed)
	require.Equal(t, 2, got.Review)
	require.Zero(t, got.Pending)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierDispatchRecoveryBackoffAndBound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_, row := supplierTestRow(t)
	mock.ExpectExec("(?s)WITH candidates AS.*48 hours").WithArgs(100).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)WITH candidates AS.*lease_token").WithArgs(100, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"attempt_id", "command_payload", "command_hash", "retry_count"}).AddRow(row.snapshot.AttemptID, row.command.String, row.commandHash.String, 20))
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	mock.ExpectExec("UPDATE supplier_dispatches SET next_retry_at").WithArgs(row.snapshot.AttemptID, sqlmock.AnyArg(), int64(900)).WillReturnResult(sqlmock.NewResult(0, 1))
	got, err := (&usageBillingRepository{db: db}).RecoverSupplierBilling(context.Background(), 10000)
	require.NoError(t, err)
	require.Equal(t, 1, got.Pending)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierDispatchCacheRepairTwoPassSQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectExec("(?s)UPDATE supplier_dispatches SET repair_pending=.*30 seconds.*repair_available_at<=CURRENT_TIMESTAMP").WithArgs(supplierTestAttemptID).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, (&usageBillingRepository{db: db}).CompleteSupplierBillingRepair(context.Background(), supplierTestAttemptID))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSupplierDispatchCommandNoRawPayload(t *testing.T) {
	cmd, _ := supplierTestRow(t)
	for _, raw := range []string{"client:prompt", "Bearer secret", strings.Repeat("a", 65)} {
		cmd.RequestPayloadHash = raw
		_, _, err := supplierCommandPayload(cmd)
		require.Error(t, err)
	}
}
