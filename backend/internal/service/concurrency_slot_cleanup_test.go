package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type slotCleanupCache struct {
	ConcurrencyCache
	calls atomic.Int64
}

func (c *slotCleanupCache) CleanupExpiredAccountSlotKeys(context.Context) error {
	c.calls.Add(1)
	return nil
}

func TestStartSlotCleanupWorker_UsesCacheWideCleanupWithoutAccountRepo(t *testing.T) {
	cache := &slotCleanupCache{}
	svc := NewConcurrencyService(cache)
	t.Cleanup(svc.StopSlotCleanupWorker)

	svc.StartSlotCleanupWorker(nil, time.Hour)

	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cache.calls.Load() > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("cleanup worker did not call cache-wide account slot cleanup")
		case <-ticker.C:
		}
	}
}

type blockingSlotCleanupCache struct {
	ConcurrencyCache
	entered chan struct{}
	exited  chan struct{}
}

func (c *blockingSlotCleanupCache) CleanupExpiredAccountSlotKeys(ctx context.Context) error {
	close(c.entered)
	<-ctx.Done()
	close(c.exited)
	return ctx.Err()
}

func TestConcurrencySlotCleanupStopCancelsAndJoins(t *testing.T) {
	cache := &blockingSlotCleanupCache{entered: make(chan struct{}), exited: make(chan struct{})}
	svc := NewConcurrencyService(cache)
	t.Cleanup(svc.StopSlotCleanupWorker)
	svc.StartSlotCleanupWorker(nil, time.Hour)
	select {
	case <-cache.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup never started")
	}
	done := make(chan struct{})
	go func() { svc.StopSlotCleanupWorker(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not stop after cancellation")
	}
	select {
	case <-cache.exited:
	default:
		t.Fatal("Stop returned before in-flight cleanup exited")
	}
	if reason := MaintenanceTaskPolicy("ConcurrencySlotCleanup"); reason != "" {
		t.Fatalf("audited cleanup must be admitted: %s", reason)
	}
}
