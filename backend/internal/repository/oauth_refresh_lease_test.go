package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOAuthRefreshLeaseExpiredOwnerCannotDeleteReplacement(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &geminiTokenCache{rdb: client}
	ctx := context.Background()
	acquired, err := cache.AcquireRefreshLease(ctx, "openai:1", "owner-a", time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	server.FastForward(2 * time.Second)
	acquired, err = cache.AcquireRefreshLease(ctx, "openai:1", "owner-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, cache.ReleaseRefreshLease(ctx, "openai:1", "owner-a"))
	value, err := server.Get(oauthRefreshLockKeyPrefix + "openai:1")
	require.NoError(t, err)
	require.Equal(t, "owner-b", value)
	require.NoError(t, cache.ReleaseRefreshLease(ctx, "openai:1", "owner-b"))
	require.False(t, server.Exists(oauthRefreshLockKeyPrefix+"openai:1"))
}

func TestOAuthRefreshLeaseRejectsEmptyOwner(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &geminiTokenCache{rdb: client}
	_, err := cache.AcquireRefreshLease(context.Background(), "a", "", time.Minute)
	require.Error(t, err)
	require.Error(t, cache.ReleaseRefreshLease(context.Background(), "a", ""))
}
