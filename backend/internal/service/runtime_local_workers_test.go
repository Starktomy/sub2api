package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func waitRuntimeLocalWorker(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("local worker did not reach the expected lifecycle boundary")
	}
}

type runtimeLocalBillingCache struct {
	BillingCache
	deductions atomic.Int32
	usage      atomic.Int32
}

func (c *runtimeLocalBillingCache) DeductUserBalance(context.Context, int64, float64) error {
	c.deductions.Add(1)
	return nil
}

func (c *runtimeLocalBillingCache) UpdateSubscriptionUsage(context.Context, int64, int64, float64) error {
	c.usage.Add(1)
	return nil
}

func TestRuntimeLocalWorkersBillingManaged(t *testing.T) {
	cache := &runtimeLocalBillingCache{}
	svc := NewManagedBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	require.Nil(t, svc.cacheWriteChan, "managed construction must not allocate/start the write pool")
	require.False(t, svc.enqueueCacheWrite(cacheWriteTask{kind: cacheWriteDeductBalance}))

	// Critical request writes must retain synchronous fallback before Start.
	svc.QueueDeductBalance(1, 1)
	svc.QueueUpdateSubscriptionUsage(1, 2, 1)
	require.EqualValues(t, 1, cache.deductions.Load())
	require.EqualValues(t, 1, cache.usage.Load())

	svc.Start()
	queue := svc.cacheWriteChan
	require.NotNil(t, queue)
	svc.Start()
	require.Equal(t, queue, svc.cacheWriteChan, "Start must not replace a running pool")
	require.True(t, svc.enqueueCacheWrite(cacheWriteTask{kind: cacheWriteDeductBalance}))
	require.True(t, svc.enqueueCacheWrite(cacheWriteTask{kind: cacheWriteUpdateSubscriptionUsage}))

	stopped := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); svc.Stop() }()
		go func() { defer wg.Done(); svc.Stop() }()
		wg.Wait()
		close(stopped)
	}()
	waitRuntimeLocalWorker(t, stopped)
	require.Nil(t, svc.cacheWriteChan)
	require.EqualValues(t, 2, cache.deductions.Load(), "Stop must drain accepted local writes")
	require.EqualValues(t, 2, cache.usage.Load())
	svc.Start()
	require.Nil(t, svc.cacheWriteChan, "a stopped service cannot restart")
	require.False(t, svc.enqueueCacheWrite(cacheWriteTask{}))

	// Stop does not disable existing synchronous fallback/request contracts.
	svc.QueueDeductBalance(1, 1)
	require.EqualValues(t, 3, cache.deductions.Load())
}

func TestRuntimeLocalWorkersBillingStopBeforeStart(t *testing.T) {
	svc := NewManagedBillingCacheService(nil, nil, nil, nil, nil, nil, nil, nil)
	svc.Stop()
	svc.Stop()
	svc.Start()
	require.Nil(t, svc.cacheWriteChan)
	require.True(t, svc.stopped.Load())
}

func runtimeLocalSubscriptionConfig() *config.Config {
	cfg := &config.Config{}
	cfg.SubscriptionCache.L1Size = 1024
	cfg.SubscriptionCache.L1TTLSeconds = 60
	cfg.SubscriptionMaintenance.WorkerCount = 1
	cfg.SubscriptionMaintenance.QueueSize = 2
	return cfg
}

type runtimeLocalSubscriptionPubSub struct {
	BillingCache
	calls        atomic.Int32
	stopCalls    atomic.Int32
	entered      chan struct{}
	setupRelease <-chan struct{}
	cancelled    chan struct{}
	exitRelease  <-chan struct{}
	exited       chan struct{}
	handler      func(string)
}

func (c *runtimeLocalSubscriptionPubSub) SubscribeSubscriptionCacheInvalidationManaged(ctx context.Context, handler func(string)) (func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	c.calls.Add(1)
	c.handler = handler
	go func() {
		<-ctx.Done()
		close(c.cancelled)
		if c.exitRelease != nil {
			<-c.exitRelease
		}
		close(c.exited)
	}()
	close(c.entered)
	if c.setupRelease != nil {
		<-c.setupRelease
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.stopCalls.Add(1)
			cancel()
			<-c.exited
		})
	}, nil
}

func TestRuntimeLocalWorkersSubscriptionManagedAndSubscriberJoin(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	cache := &runtimeLocalSubscriptionPubSub{
		entered: make(chan struct{}), cancelled: make(chan struct{}),
		exited: make(chan struct{}), exitRelease: release,
	}
	billing := NewManagedBillingCacheService(cache, nil, nil, nil, nil, nil, nil, nil)
	svc := NewManagedSubscriptionService(nil, nil, billing, nil, runtimeLocalSubscriptionConfig())
	t.Cleanup(func() { unblock(); svc.Stop(); billing.Stop() })
	require.Nil(t, svc.maintenanceQueue)
	require.Zero(t, cache.calls.Load(), "managed construction must not subscribe to Redis")
	require.NotNil(t, svc.subCacheL1, "Ristretto is an explicitly owned local resource")

	svc.Start()
	svc.Start()
	waitRuntimeLocalWorker(t, cache.entered)
	require.EqualValues(t, 1, cache.calls.Load())
	require.NotNil(t, svc.maintenanceQueue)
	key := subCacheKey(1, 2)
	require.True(t, svc.subCacheL1.Set(key, &UserSubscription{ID: 3}, 1))
	svc.subCacheL1.Wait()
	cache.handler(key)
	_, found := svc.subCacheL1.Get(key)
	require.False(t, found, "invalidation callback must synchronously flush the local cache")

	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	waitRuntimeLocalWorker(t, cache.cancelled)
	select {
	case <-stopped:
		t.Fatal("Stop returned before the subscriber receive loop exited")
	default:
	}
	unblock()
	waitRuntimeLocalWorker(t, stopped)
	waitRuntimeLocalWorker(t, cache.exited)
	require.EqualValues(t, 1, cache.stopCalls.Load())
	require.Nil(t, svc.subCacheL1)
	svc.Stop()
	svc.Start()
	require.EqualValues(t, 1, cache.calls.Load())
	cache.handler(key) // late legacy/transport callbacks may not touch the closed cache
	require.Error(t, svc.maintenanceQueue.TryEnqueue(func() {}))
}

func TestRuntimeLocalWorkersSubscriptionStopDuringSubscriberSetup(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	cache := &runtimeLocalSubscriptionPubSub{
		entered: make(chan struct{}), cancelled: make(chan struct{}),
		exited: make(chan struct{}), setupRelease: release,
	}
	billing := NewManagedBillingCacheService(cache, nil, nil, nil, nil, nil, nil, nil)
	svc := NewManagedSubscriptionService(nil, nil, billing, nil, runtimeLocalSubscriptionConfig())
	t.Cleanup(func() { unblock(); svc.Stop(); billing.Stop() })
	started := make(chan struct{})
	go func() { svc.Start(); close(started) }()
	waitRuntimeLocalWorker(t, cache.entered)

	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	waitRuntimeLocalWorker(t, cache.cancelled)
	select {
	case <-stopped:
		t.Fatal("Stop returned while subscription setup was still in progress")
	default:
	}
	unblock()
	waitRuntimeLocalWorker(t, started)
	waitRuntimeLocalWorker(t, stopped)
	require.EqualValues(t, 1, cache.stopCalls.Load())
	require.Nil(t, svc.subCacheL1)
}

func TestRuntimeLocalWorkersSubscriptionStopBeforeStart(t *testing.T) {
	svc := NewManagedSubscriptionService(nil, nil, nil, nil, runtimeLocalSubscriptionConfig())
	require.NotNil(t, svc.subCacheL1)
	svc.Stop()
	svc.Stop()
	svc.Start()
	require.Nil(t, svc.subCacheL1, "unstarted Ristretto resources must be closed")
	require.Nil(t, svc.maintenanceQueue)
}

type runtimeLocalMaintenanceRepo struct {
	UserSubscriptionRepository
	calls   chan int64
	release <-chan struct{}
}

func (r *runtimeLocalMaintenanceRepo) ActivateWindows(_ context.Context, id int64, _, _ time.Time) error {
	r.calls <- id
	if id == 2 {
		<-r.release
	}
	return nil
}

func TestRuntimeLocalWorkersSubscriptionMaintenanceRemainsLocal(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	repo := &runtimeLocalMaintenanceRepo{calls: make(chan int64, 3), release: release}
	svc := NewManagedSubscriptionService(nil, repo, nil, nil, runtimeLocalSubscriptionConfig())
	t.Cleanup(func() { unblock(); svc.Stop() })

	// Without a running queue, the pre-existing synchronous maintenance still runs.
	svc.DoWindowMaintenance(&UserSubscription{ID: 1})
	require.EqualValues(t, 1, <-repo.calls)
	require.Nil(t, svc.maintenanceQueue)
	svc.Start()
	svc.DoWindowMaintenance(&UserSubscription{ID: 2})
	select {
	case id := <-repo.calls:
		require.EqualValues(t, 2, id)
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance worker did not start the accepted local task")
	}
	svc.DoWindowMaintenance(&UserSubscription{ID: 3})
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned before accepted maintenance completed")
	default:
	}
	unblock()
	waitRuntimeLocalWorker(t, stopped)
	require.EqualValues(t, 3, <-repo.calls, "Stop must drain accepted maintenance before closing caches")
	require.Error(t, svc.maintenanceQueue.TryEnqueue(func() {}))
}

type runtimeLocalEmailCache struct {
	EmailCache
	calls        atomic.Int32
	entered      chan struct{}
	cancelled    chan struct{}
	cancelReturn <-chan struct{}
}

func (c *runtimeLocalEmailCache) GetVerificationCode(ctx context.Context, _ string) (*VerificationCodeData, error) {
	c.calls.Add(1)
	if c.entered != nil {
		close(c.entered)
		<-ctx.Done()
		close(c.cancelled)
		if c.cancelReturn != nil {
			<-c.cancelReturn
		}
	}
	// A cooldown response avoids SMTP/network access in lifecycle tests.
	return &VerificationCodeData{CreatedAt: time.Now()}, nil
}

func (c *runtimeLocalEmailCache) IsPasswordResetEmailInCooldown(context.Context, string) bool {
	c.calls.Add(1)
	return true
}

func TestRuntimeLocalWorkersEmailBufferAndDrain(t *testing.T) {
	cache := &runtimeLocalEmailCache{}
	svc := NewManagedEmailQueueService(NewEmailService(nil, cache), 1)
	t.Cleanup(svc.Stop)
	// Small deterministic capacity tests the existing local-buffer contract.
	svc.taskChan = make(chan EmailTask, 2)
	require.NoError(t, svc.EnqueueVerifyCode("verify@example.com", "test", "zh-CN"))
	require.NoError(t, svc.EnqueuePasswordReset("reset@example.com", "test", "https://example.com/reset", "en-US"))
	require.ErrorContains(t, svc.EnqueueVerifyCode("full@example.com", "test"), "full")
	require.False(t, svc.started)
	require.Nil(t, svc.workerCtx)
	require.Zero(t, cache.calls.Load(), "managed construction/admission must not start delivery")
	require.Len(t, svc.taskChan, 2)

	svc.Start()
	ctx := svc.workerCtx
	svc.Start()
	require.Equal(t, ctx, svc.workerCtx, "Start must not replace the running workers")
	svc.Stop()
	svc.Stop()
	require.EqualValues(t, 2, cache.calls.Load())
	require.Empty(t, svc.taskChan, "Stop should drain quick accepted work, not randomly discard it")
	require.ErrorContains(t, svc.EnqueueVerifyCode("stopped@example.com", "test"), "stopped")
	require.ErrorContains(t, svc.EnqueuePasswordReset("stopped@example.com", "test", "url"), "stopped")
	svc.Start()
	require.Error(t, svc.workerCtx.Err())
}

func TestRuntimeLocalWorkersEmailStopBeforeStart(t *testing.T) {
	svc := NewManagedEmailQueueService(nil, 0)
	require.Equal(t, 3, svc.workers)
	require.NoError(t, svc.EnqueueVerifyCode("local@example.com", "test"))
	svc.Stop()
	svc.Stop()
	svc.Start()
	require.False(t, svc.started, "Stop must not implicitly start mail delivery")
	require.Len(t, svc.taskChan, 1, "unsent local tasks must not be represented as delivered")
	require.ErrorContains(t, svc.EnqueueVerifyCode("stopped@example.com", "test"), "stopped")
}

func TestRuntimeLocalWorkersEmailCancellationStillJoinsWorker(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	cache := &runtimeLocalEmailCache{
		entered: make(chan struct{}), cancelled: make(chan struct{}), cancelReturn: release,
	}
	svc := NewManagedEmailQueueService(NewEmailService(nil, cache), 1)
	svc.drainTimeout = 0 // expire the drain budget deterministically; no sleep
	t.Cleanup(func() { unblock(); svc.Stop() })
	require.NoError(t, svc.EnqueueVerifyCode("inflight@example.com", "test"))
	require.NoError(t, svc.EnqueuePasswordReset("pending@example.com", "test", "url"))
	svc.Start()
	waitRuntimeLocalWorker(t, cache.entered)

	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	waitRuntimeLocalWorker(t, cache.cancelled)
	require.ErrorContains(t, svc.EnqueueVerifyCode("late@example.com", "test"), "stopped")
	select {
	case <-stopped:
		t.Fatal("Stop returned while a worker could still access service dependencies")
	default:
	}
	unblock()
	waitRuntimeLocalWorker(t, stopped)
	require.EqualValues(t, 1, cache.calls.Load(), "cancelled drain may not start the next pending task")
	require.Len(t, svc.taskChan, 1)
	svc.Stop()
}

func TestRuntimeLocalWorkersLegacyConstructorsStillStart(t *testing.T) {
	billing := NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	require.NotNil(t, billing.cacheWriteChan)
	subscription := NewSubscriptionService(nil, nil, nil, nil, runtimeLocalSubscriptionConfig())
	t.Cleanup(subscription.Stop)
	require.NotNil(t, subscription.maintenanceQueue)
	email := NewEmailQueueService(nil, 1)
	t.Cleanup(email.Stop)
	require.True(t, email.started)
	require.NotNil(t, email.workerCtx)
}
