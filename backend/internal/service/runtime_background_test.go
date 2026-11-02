package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/zeromicro/go-zero/core/collection"
)

func TestManagedTimingWheelDefersCreationAndReportsFailure(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })
	var creates int
	newTimingWheel = func(time.Duration, int, collection.Execute) (*collection.TimingWheel, error) {
		creates++
		return nil, errors.New("unavailable wheel")
	}
	lifecycle := NewRuntimeLifecycle(nil)
	svc, err := ProvideTimingWheelService(lifecycle)
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.Zero(t, creates, "provider construction must not spawn the wheel")
	require.ErrorContains(t, lifecycle.Start(context.Background()), "unavailable wheel")
	require.Equal(t, 1, creates)
	require.NoError(t, lifecycle.Stop(context.Background()))
	svc.Stop()
}

func TestManagedTimingWheelStopBeforeStart(t *testing.T) {
	svc := NewManagedTimingWheelService()
	svc.Stop()
	svc.Stop()
	require.ErrorContains(t, svc.StartManaged(), "stopped")
	svc.Schedule("never", time.Second, func() { t.Error("inactive wheel executed work") })
}

func TestManagedTimingWheelWaitsForDispatchedCallbacks(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })
	var execute collection.Execute
	newTimingWheel = func(interval time.Duration, slots int, callback collection.Execute) (*collection.TimingWheel, error) {
		execute = callback
		return original(interval, slots, callback)
	}
	svc := NewManagedTimingWheelService()
	require.NoError(t, svc.StartManaged())
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		execute("callback", func() { close(entered); <-release })
		close(completed)
	}()
	<-entered
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	// The callback cannot complete before it is released. After Stop returns,
	// no dispatched callback is allowed to access closed DB/cache dependencies.
	close(release)
	<-stopped
	<-completed
	svc.Stop()
}

type runtimeCleanupCache struct {
	ConcurrencyCache
	entered chan struct{}
	exited  chan struct{}
	calls   atomic.Int64
}

func (c *runtimeCleanupCache) CleanupExpiredAccountSlotKeys(ctx context.Context) error {
	if c.calls.Add(1) == 1 {
		close(c.entered)
	}
	<-ctx.Done()
	close(c.exited)
	return ctx.Err()
}

func TestConcurrencyCleanupCancelsAndJoins(t *testing.T) {
	cache := &runtimeCleanupCache{entered: make(chan struct{}), exited: make(chan struct{})}
	svc := NewConcurrencyService(cache)
	svc.StartSlotCleanupWorker(nil, time.Hour)
	<-cache.entered
	svc.StartSlotCleanupWorker(nil, time.Hour)
	svc.StopSlotCleanupWorker()
	<-cache.exited
	require.EqualValues(t, 1, cache.calls.Load(), "repeat start must not add a second cleanup loop")
	svc.StopSlotCleanupWorker()
	svc.StartSlotCleanupWorker(nil, time.Hour)
	require.EqualValues(t, 1, cache.calls.Load(), "stopped cleanup cannot restart")
}

type runtimeSharedSlotCache struct {
	ConcurrencyCache
	startupSweeps atomic.Int64
}

func (c *runtimeSharedSlotCache) CleanupStaleProcessSlots(context.Context, string) error {
	c.startupSweeps.Add(1)
	return nil
}

func TestRuntimeWorkerDoesNotSweepOtherGatewayPrefixes(t *testing.T) {
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker}}
	cache := &runtimeSharedSlotCache{}
	lifecycle := NewRuntimeLifecycle(cfg)
	ProvideConcurrencyService(lifecycle, cache, nil, cfg)
	require.NoError(t, lifecycle.Start(context.Background()))
	require.NoError(t, lifecycle.Stop(context.Background()))
	require.Zero(t, cache.startupSweeps.Load(), "worker joining a shared pool must not delete other process prefixes")
}

func TestManagedUsagePoolAutoscalerLifecycle(t *testing.T) {
	cfg := &config.Config{}
	pool := NewManagedUsageRecordWorkerPool(cfg)
	require.Nil(t, pool.autoScaleCancel, "managed construction must not launch an autoscaler")
	pool.Start()
	require.True(t, pool.started)
	pool.Start()
	pool.Stop()
	pool.Stop()
	pool.Start()
	require.True(t, pool.stopped)
}
