package repository

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// subscribeCacheUpdates joins the actual Redis callback loop on shutdown. The
// returned stop must be invoked outside the callback itself.
func subscribeCacheUpdates(ctx context.Context, client *redis.Client, channel string, handler func()) (func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	pubsub := client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe cache updates: %w", err)
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		defer func() { _ = pubsub.Close() }()
		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				if msg != nil && ctx.Err() == nil {
					handler()
				}
			}
		}
	}()
	return func() {
		once.Do(func() { cancel(); _ = pubsub.Close() })
		<-done
	}, nil
}
