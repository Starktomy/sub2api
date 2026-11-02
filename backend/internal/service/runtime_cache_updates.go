package service

import (
	"context"
	"sync"
)

type managedCacheUpdateSubscriber interface {
	SubscribeUpdatesManaged(context.Context, func()) (func(), error)
}

type cacheUpdateSubscriber interface {
	SubscribeUpdates(context.Context, func())
}

// cacheUpdateLifecycle keeps the local update listener within its service
// lifecycle. Managed repositories join their actual Redis loop; legacy test
// doubles still receive a cancellable context and a callback barrier.
type cacheUpdateLifecycle struct {
	mu         sync.Mutex
	started    bool
	stopped    bool
	cancel     context.CancelFunc
	stop       func()
	starting   sync.WaitGroup
	callbacks  sync.WaitGroup
	callbackMu sync.Mutex
	closed     bool
}

func (l *cacheUpdateLifecycle) begin() (context.Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return nil, false
	}
	l.started = true
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.starting.Add(1)
	return ctx, true
}

func (l *cacheUpdateLifecycle) subscribe(ctx context.Context, bus cacheUpdateSubscriber, handler func()) error {
	if bus == nil {
		return nil
	}
	wrapped := func() {
		l.callbackMu.Lock()
		if l.closed || ctx.Err() != nil {
			l.callbackMu.Unlock()
			return
		}
		l.callbacks.Add(1)
		l.callbackMu.Unlock()
		defer l.callbacks.Done()
		handler()
	}
	if managed, ok := bus.(managedCacheUpdateSubscriber); ok {
		stop, err := managed.SubscribeUpdatesManaged(ctx, wrapped)
		if err != nil {
			return err
		}
		l.mu.Lock()
		l.stop = stop
		l.mu.Unlock()
		return nil
	}
	bus.SubscribeUpdates(ctx, wrapped)
	return nil
}

func (l *cacheUpdateLifecycle) close() {
	l.mu.Lock()
	l.stopped = true
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.callbackMu.Lock()
	l.closed = true
	l.callbackMu.Unlock()
	l.starting.Wait()
	l.mu.Lock()
	stop := l.stop
	l.mu.Unlock()
	if stop != nil {
		stop()
	}
	l.callbacks.Wait()
}
