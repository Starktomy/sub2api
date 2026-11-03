package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// This opt-in suite writes ONLY disposable fixture rows/keys in the named AWS
// integration DB/cache. It never applies migrations, changes schema or flushes
// an entire cache. Real generation/payment endpoints are never called.
func TestManagedDataLiveContracts(t *testing.T) {
	if os.Getenv("MANAGED_DATA_LIVE") != "1" {
		t.Skip("set MANAGED_DATA_LIVE=1 with the dedicated integration runtime settings")
	}
	cfg, err := config.LoadForBootstrap()
	require.NoError(t, err)
	require.Equal(t, "sub2api_integration_test", cfg.Database.DBName)
	require.True(t, strings.HasSuffix(cfg.Database.Host, ".us-west-2.rds.amazonaws.com"))
	require.Equal(t, "verify-full", cfg.Database.SSLMode)
	require.NotEmpty(t, cfg.Database.SSLRootCert)
	require.True(t, cfg.Redis.EnableTLS)
	require.True(t, strings.HasSuffix(cfg.Redis.Host, ".cache.amazonaws.com"))
	require.Equal(t, "sub2api_it_runtime", cfg.Database.User)
	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezone("UTC"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var encrypted bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&encrypted))
	require.True(t, encrypted)
	require.NoError(t, CheckMigrations(ctx, db))
	client := InitRedis(cfg)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(ctx).Err())
	other := InitRedis(cfg)
	t.Cleanup(func() { _ = other.Close() })
	var random [8]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	runID := hex.EncodeToString(random[:])
	id, err := strconv.ParseInt(runID[:12], 16, 64)
	require.NoError(t, err)
	id += 1000000

	t.Run("shared_lua_slots_and_wait_counters", func(t *testing.T) {
		a := NewConcurrencyCache(client, 15, 900)
		b := NewConcurrencyCache(other, 15, 900)
		defer func() { _ = a.ReleaseAccountSlot(context.Background(), id, runID) }()
		first, err := a.AcquireAccountSlot(ctx, id, 1, runID)
		require.NoError(t, err)
		require.True(t, first)
		second, err := b.AcquireAccountSlot(ctx, id, 1, runID+"-blocked")
		require.NoError(t, err)
		require.False(t, second, "a separate connection must observe account capacity")
		count, err := b.GetAccountConcurrency(ctx, id)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		require.NoError(t, a.ReleaseAccountSlot(ctx, id, runID))
		second, err = b.AcquireAccountSlot(ctx, id, 1, runID+"-next")
		require.NoError(t, err)
		require.True(t, second)
		require.NoError(t, b.ReleaseAccountSlot(ctx, id, runID+"-next"))
		waiting, err := a.IncrementAccountWaitCount(ctx, id, 1)
		require.NoError(t, err)
		require.True(t, waiting)
		waiting, err = b.IncrementAccountWaitCount(ctx, id, 1)
		require.NoError(t, err)
		require.False(t, waiting)
		require.NoError(t, a.DecrementAccountWaitCount(ctx, id))
	})

	t.Run("billing_cache_lua_and_managed_pubsub", func(t *testing.T) {
		a := NewBillingCache(client)
		b := NewBillingCache(other)
		defer func() { _ = client.Del(context.Background(), billingBalanceKey(id)).Err() }()
		require.NoError(t, a.SetUserBalance(ctx, id, 10))
		require.NoError(t, b.DeductUserBalance(ctx, id, 1.25))
		balance, err := a.GetUserBalance(ctx, id)
		require.NoError(t, err)
		require.InDelta(t, 8.75, balance, 0.00000001)
		messages := make(chan string, 2)
		managed := a.(interface {
			SubscribeSubscriptionCacheInvalidationManaged(context.Context, func(string)) (func(), error)
		})
		stop, err := managed.SubscribeSubscriptionCacheInvalidationManaged(ctx, func(message string) { messages <- message })
		require.NoError(t, err)
		defer stop()
		require.NoError(t, b.(interface {
			PublishSubscriptionCacheInvalidation(context.Context, string) error
		}).PublishSubscriptionCacheInvalidation(ctx, "managed-live-"+runID))
		select {
		case message := <-messages:
			require.Equal(t, "managed-live-"+runID, message)
		case <-ctx.Done():
			t.Fatal("cross-connection invalidation did not arrive")
		}
		stop()
	})

	t.Run("runtime_sql_privileges_and_billing_idempotency", func(t *testing.T) {
		var userID, keyID int64
		key := "sk-managed-live-" + runID
		err := db.QueryRowContext(ctx, "INSERT INTO users (email,password_hash,balance) VALUES ($1,$2,10) RETURNING id", runID+"@integration.invalid", "not-a-usable-password-hash").Scan(&userID)
		require.NoError(t, err)
		defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM users WHERE id=$1", userID) }()
		err = db.QueryRowContext(ctx, "INSERT INTO api_keys (user_id,key,name) VALUES ($1,$2,$3) RETURNING id", userID, key, "managed-live-fixture").Scan(&keyID)
		require.NoError(t, err)
		defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM api_keys WHERE id=$1", keyID) }()
		requestID := "managed-live-" + runID
		defer func() {
			_, _ = db.ExecContext(context.Background(), "DELETE FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", requestID, keyID)
		}()
		repo := NewUsageBillingRepository(nil, db)
		cmd := &service.UsageBillingCommand{RequestID: requestID, APIKeyID: keyID, UserID: userID, BalanceCost: 1.25}
		first, err := repo.Apply(ctx, cmd)
		require.NoError(t, err)
		require.True(t, first.Applied)
		second, err := repo.Apply(ctx, cmd)
		require.NoError(t, err)
		require.False(t, second.Applied)
		var balance float64
		require.NoError(t, db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", userID).Scan(&balance))
		require.InDelta(t, 8.75, balance, 0.00000001)
		_, err = db.ExecContext(ctx, "CREATE TABLE managed_live_must_not_create(id bigint)")
		require.Error(t, err, "runtime may not execute DDL")
	})

	t.Run("authenticated_shared_key_across_two_gateways", func(t *testing.T) {
		var userID, keyID, groupID int64
		key := "sk-managed-http-" + runID
		require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO groups (name,platform) VALUES ($1,'anthropic') RETURNING id", "managed-http-"+runID).Scan(&groupID))
		defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM groups WHERE id=$1", groupID) }()
		require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO users (email,password_hash,balance) VALUES ($1,$2,10) RETURNING id", runID+"-http@integration.invalid", "not-a-usable-password-hash").Scan(&userID))
		defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM users WHERE id=$1", userID) }()
		require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO api_keys (user_id,key,name,group_id) VALUES ($1,$2,$3,$4) RETURNING id", userID, key, "managed-live-http", groupID).Scan(&keyID))
		defer func() {
			_, _ = db.ExecContext(context.Background(), "DELETE FROM api_keys WHERE id=$1", keyID)
			// Test cleanup must revoke already-warmed Gateway L1/L2 snapshots as
			// well as remove fixture rows; never leave a deleted test key usable.
			sum := sha256.Sum256([]byte(key))
			cacheKey := hex.EncodeToString(sum[:])
			cache := NewAPIKeyCache(client)
			_ = cache.DeleteAuthCache(context.Background(), cacheKey)
			_ = cache.PublishAuthCacheInvalidation(context.Background(), cacheKey)
		}()
		for _, port := range []int{18081, 18082} {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1/usage", port), nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, "gateway must authenticate a shared DB/Valkey-backed key")
		}
	})

	// Wrong passwords must fail; do not accidentally leave anonymous/default auth.
	t.Run("cache_rejects_invalid_credentials", func(t *testing.T) {
		opts := buildRedisOptions(cfg)
		opts.Password = "deliberately-invalid-" + runID
		bad := redis.NewClient(opts)
		defer func() { _ = bad.Close() }()
		err := bad.Ping(ctx).Err()
		require.Error(t, err)
		require.False(t, errors.Is(err, context.DeadlineExceeded), "authentication rejection should not be a network timeout")
	})
}
