package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestKiroNativePostgresEncryptedCAS(t *testing.T) {
	dsn := os.Getenv("KIRO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KIRO_TEST_PG_DSN is required; disposable local PostgreSQL only")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	schema := "kiro_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	_, err = db.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE accounts(id BIGSERIAL PRIMARY KEY,name TEXT,platform TEXT,type TEXT,credentials JSONB,extra JSONB,concurrency INT,priority INT,status TEXT,schedulable BOOL,deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/257_kiro_credentials.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	runtimeMigration, err := os.ReadFile("../../migrations/258_kiro_runtime.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(runtimeMigration))
	require.NoError(t, err)
	repo := NewKiroNativeRepository(db)
	ref := uuid.NewString()
	record, err := repo.Create(ctx, service.KiroNativeImport{Name: "fake", Region: "us-east-1", AuthMode: service.KiroCredentialIdC, UserAgent: "Sub2API-Test/1", AmzUserAgent: "Sub2API-Test/1"}, ref, func(service.KiroCredentialBinding) ([]byte, error) { return []byte("encrypted-fake-envelope"), nil })
	require.NoError(t, err)
	var credentials string
	var schedulable bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credentials::text,schedulable FROM accounts WHERE id=$1`, record.Binding.AccountID).Scan(&credentials, &schedulable))
	require.False(t, schedulable)
	require.NotContains(t, credentials, "encrypted-fake-envelope")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(credentials), &parsed))
	require.Len(t, parsed, 1)
	require.Equal(t, ref, parsed["kiro_credential_ref"])
	owner := uuid.NewString()
	require.NoError(t, repo.BeginRefresh(ctx, record, owner))
	require.ErrorIs(t, repo.BeginRefresh(ctx, record, uuid.NewString()), service.ErrKiroRefreshBarrier)
	require.ErrorIs(t, repo.CompleteRefresh(ctx, record, uuid.NewString(), []byte("wrong")), service.ErrKiroRefreshBarrier)
	require.NoError(t, repo.CompleteRefresh(ctx, record, owner, []byte("encrypted-rotated-envelope")))
	require.ErrorIs(t, repo.CompleteRefresh(ctx, record, owner, []byte("stale")), service.ErrKiroRefreshBarrier)
	next, err := repo.Get(ctx, record.Binding.AccountID)
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Binding.Version)
	require.Equal(t, "encrypted-rotated-envelope", string(next.Envelope))
	owner = uuid.NewString()
	require.NoError(t, repo.BeginRefresh(ctx, next, owner))
	require.NoError(t, repo.AmbiguousRefresh(ctx, next, owner))
	// An arbitrarily old claim never grants permission to reuse its refresh token.
	_, err = db.ExecContext(ctx, `UPDATE kiro_credentials SET refresh_started_at=NOW()-INTERVAL '1 year'`)
	require.NoError(t, err)
	require.ErrorIs(t, repo.BeginRefresh(ctx, next, uuid.NewString()), service.ErrKiroRefreshBarrier)
	reopened := NewKiroNativeRepository(db)
	persisted, err := reopened.Get(ctx, next.Binding.AccountID)
	require.NoError(t, err)
	require.Equal(t, "ambiguous", persisted.RefreshState)
	require.Equal(t, int64(2), persisted.Binding.Version)
	require.Equal(t, owner, persisted.RefreshOwner)
	// Barrier recovery requires both current version and owner fencing.
	patch := service.KiroRuntimePatch{Operation: "reimport", ExpectedVersion: 2, ExpectedRefreshOwner: owner}
	wrong := *persisted
	wrong.RefreshOwner = uuid.NewString()
	require.ErrorIs(t, repo.PatchRuntime(ctx, &wrong, patch, []byte("wrong")), service.ErrKiroRefreshBarrier)
	require.NoError(t, repo.PatchRuntime(ctx, persisted, patch, []byte("new-authorization-envelope")))
	require.ErrorIs(t, repo.CompleteRefresh(ctx, persisted, owner, []byte("late-old-refresh")), service.ErrKiroRefreshBarrier)
	recovered, err := repo.Get(ctx, persisted.Binding.AccountID)
	require.NoError(t, err)
	require.Equal(t, int64(3), recovered.Binding.Version)
	require.Equal(t, "idle", recovered.RefreshState)
	require.NoError(t, repo.SaveSnapshot(ctx, recovered, "quota", json.RawMessage(`{"state":"known","source":"fake","data":{"usageLimit":5}}`)))
	reopenedStatus, err := reopened.Get(ctx, recovered.Binding.AccountID)
	require.NoError(t, err)
	require.Contains(t, string(reopenedStatus.QuotaSnapshot), `"known"`)
	_, err = db.ExecContext(ctx, `UPDATE accounts SET status='active',schedulable=true WHERE id=$1`, recovered.Binding.AccountID)
	require.NoError(t, err)
	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,schedulable FROM accounts WHERE id=$1`, recovered.Binding.AccountID).Scan(&status, &schedulable))
	require.Equal(t, "disabled", status)
	require.False(t, schedulable)
	_, err = db.ExecContext(ctx, `UPDATE accounts SET credentials='{"accessToken":"canary"}' WHERE id=$1`, recovered.Binding.AccountID)
	require.Error(t, err)
}
