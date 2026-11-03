package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Executes production repository SQL against real PostgreSQL, in a transaction
// that is always rolled back. No schema writes, network OAuth, or real accounts.
func TestOAuthRefreshPostgresLiveCAS(t *testing.T) {
	if os.Getenv("OAUTH_CAS_LIVE") != "1" {
		t.Skip("explicit named managed test database opt-in required")
	}
	cfg, err := config.LoadForBootstrap()
	require.NoError(t, err)
	require.Equal(t, "sub2api_integration_test", cfg.Database.DBName)
	require.Equal(t, "sub2api_it_runtime", cfg.Database.User)
	require.Equal(t, "verify-full", cfg.Database.SSLMode)
	require.True(t, strings.HasSuffix(cfg.Database.Host, ".us-west-2.rds.amazonaws.com"))
	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezone("UTC"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var encrypted bool
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&encrypted))
	require.True(t, encrypted)
	var id int64
	old := map[string]any{"access_token": "synthetic-live-old", "refresh_token": "synthetic-live-refresh", "nested": map[string]any{"version": float64(1)}}
	encoded, err := json.Marshal(old)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,status,schedulable) VALUES ('cas-rollback-only','openai','oauth',$1::jsonb,'disabled',false) RETURNING id`, string(encoded)).Scan(&id))
	repo := newAccountRepositoryWithSQL(nil, tx, nil)
	var before int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", id).Scan(&before))
	next := map[string]any{"access_token": "synthetic-live-new", "refresh_token": "synthetic-live-rotated"}
	applied, err := repo.UpdateOAuthCredentialsIfUnchanged(ctx, id, old, nil, next)
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = repo.UpdateOAuthCredentialsIfUnchanged(ctx, id, old, nil, map[string]any{"access_token": "stale"})
	require.NoError(t, err)
	require.False(t, applied)
	proxy := int64(999999999)
	applied, err = repo.UpdateOAuthCredentialsIfUnchanged(ctx, id, next, &proxy, map[string]any{"access_token": "wrong-proxy"})
	require.NoError(t, err)
	require.False(t, applied)
	var count int
	var status string
	var schedulable bool
	var token string
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", id).Scan(&count))
	require.Equal(t, before+1, count)
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT status,schedulable,credentials->>'access_token' FROM accounts WHERE id=$1", id).Scan(&status, &schedulable, &token))
	require.Equal(t, "disabled", status)
	require.False(t, schedulable)
	require.Equal(t, "synthetic-live-new", token)
	// An outbox publication error must roll back the credential update too.
	_, err = tx.ExecContext(ctx, "SAVEPOINT outbox_error")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `WITH updated AS (UPDATE accounts SET credentials='{"access_token":"must-rollback"}'::jsonb WHERE id=$1 RETURNING id) INSERT INTO scheduler_outbox(event_type,account_id) SELECT NULL,id FROM updated`, id)
	require.Error(t, err)
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT outbox_error")
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT credentials->>'access_token' FROM accounts WHERE id=$1", id).Scan(&token))
	require.Equal(t, "synthetic-live-new", token)
	require.NoError(t, tx.Rollback())
	t.Log("real RDS TLS CAS success/stale/proxy/outbox/state preservation and rollback passed; fixtures rolled back")
}
