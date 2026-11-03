//go:build unit

package openai

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedSessionAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	client := func() *redis.Client {
		c := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: time.Second})
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	a := NewManagedRedisSessionStore(client())
	b := NewManagedRedisSessionStore(client())
	ctx := context.Background()
	require.NoError(t, a.Save(ctx, "flow", &OAuthSession{State: "expected", CodeVerifier: "test-verifier", CreatedAt: time.Now()}))
	got, ok := b.Get("flow")
	require.True(t, ok)
	require.Equal(t, "expected", got.State)
	a.Stop()
	restarted := NewManagedRedisSessionStore(client())
	_, ok = restarted.Get("flow")
	require.True(t, ok, "process restart must not discard shared state")
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := b
			if i%2 == 0 {
				store = restarted
			}
			if store.TryConsumeSession("flow") {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	_, ok = b.Get("flow")
	require.False(t, ok, "consumption removes sensitive session data")
	require.NoError(t, b.Save(ctx, "expired", &OAuthSession{CreatedAt: time.Now()}))
	mr.FastForward(SessionTTL + time.Second)
	_, ok = restarted.Get("expired")
	require.False(t, ok)
	require.False(t, restarted.TryConsumeSession("expired"))
	require.NoError(t, b.Save(ctx, "outage", &OAuthSession{CreatedAt: time.Now()}))
	mr.Close()
	require.Error(t, b.Save(ctx, "failed", &OAuthSession{CreatedAt: time.Now()}))
	_, ok = b.Get("outage")
	require.False(t, ok)
	require.False(t, b.TryConsumeSession("outage"))
	require.Empty(t, b.sessions, "Redis backing must not cache secrets or fall back to process memory")
	missing := NewManagedRedisSessionStore(nil)
	require.Error(t, missing.Save(ctx, "missing", &OAuthSession{CreatedAt: time.Now()}))
	require.False(t, missing.TryConsumeSession("missing"))
}
