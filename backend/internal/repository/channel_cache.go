package repository

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const channelCachePubSubKey = "channel_cache_updated"

type channelCache struct {
	rdb *redis.Client
}

// NewChannelCache creates the Redis-backed channel cache invalidation bus.
func NewChannelCache(rdb *redis.Client) service.ChannelCachePubSub {
	return &channelCache{rdb: rdb}
}

// NotifyUpdate notifies all instances to invalidate their local channel cache.
func (c *channelCache) NotifyUpdate(ctx context.Context) error {
	return c.rdb.Publish(ctx, channelCachePubSubKey, "refresh").Err()
}

// SubscribeUpdates subscribes to channel cache invalidation notifications.
func (c *channelCache) SubscribeUpdates(ctx context.Context, handler func()) {
	if _, err := c.SubscribeUpdatesManaged(ctx, handler); err != nil {
		slog.Warn("cache update subscription failed", "error", err)
	}
}

func (c *channelCache) SubscribeUpdatesManaged(ctx context.Context, handler func()) (func(), error) {
	return subscribeCacheUpdates(ctx, c.rdb, channelCachePubSubKey, func() {
		handler()
	})
}
