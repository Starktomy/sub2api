package repository

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestManagedCacheSubscriptionJoinsCallbackLoop(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	stop, err := subscribeCacheUpdates(context.Background(), client, "cloud:test:updates", func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	require.NoError(t, err)
	_, err = client.Publish(context.Background(), "cloud:test:updates", "refresh").Result()
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("managed subscriber never dispatched its callback")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("managed subscriber did not join after callback returned")
	}
	stop()
	_, err = client.Publish(context.Background(), "cloud:test:updates", "late").Result()
	require.NoError(t, err)
	// Stop joins our callback loop. The server may observe the TCP close later,
	// so PUBLISH's remote subscriber count is not a synchronous teardown signal.
	// A late publication must not dispatch another local callback.
	stop()
	require.EqualValues(t, 1, calls.Load())
}

func TestManagedSubscriptionInvalidationStopsRepeatedly(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &billingCache{rdb: client}
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := cache.SubscribeSubscriptionCacheInvalidationManaged(ctx, func(string) {})
	require.NoError(t, err)
	cancel()
	stop()
	stop()
	_, err = cache.SubscribeSubscriptionCacheInvalidationManaged(context.Background(), nil)
	require.ErrorContains(t, err, "handler is required")
}
