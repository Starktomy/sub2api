//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedulerCancellationCache struct {
	SchedulerCache
	cancel        context.CancelFunc
	tokenCaptures int
}

func (c *schedulerCancellationCache) GetSnapshot(ctx context.Context, _ SchedulerBucket) ([]*Account, bool, error) {
	c.cancel()
	return nil, false, ctx.Err()
}

func (c *schedulerCancellationCache) CaptureBucketWriteToken(ctx context.Context, _ SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.tokenCaptures++
	return SchedulerBucketWriteToken{}, ctx.Err()
}

func (c *schedulerCancellationCache) GetAccount(ctx context.Context, _ int64) (*Account, error) {
	c.cancel()
	return nil, ctx.Err()
}

type schedulerCancellationAccountRepo struct {
	AccountRepository
	listCalls    int
	getByIDCalls int
}

func (r *schedulerCancellationAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, _ string) ([]Account, error) {
	r.listCalls++
	return nil, ctx.Err()
}

func (r *schedulerCancellationAccountRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	r.getByIDCalls++
	return nil, ctx.Err()
}

func TestSchedulerSnapshotListStopsAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, accounts)
	require.False(t, useMixed)
	require.Zero(t, cache.tokenCaptures, "canceled requests must not capture a cache publish token")
	require.Zero(t, repo.listCalls, "canceled requests must not fall back to the database")
}

func TestSchedulerSnapshotGetAccountStopsAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cache := &schedulerCancellationCache{cancel: cancel}
	repo := &schedulerCancellationAccountRepo{}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	account, err := svc.GetAccount(ctx, 42)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, account)
	require.Zero(t, repo.getByIDCalls, "canceled requests must not fall back to the database")
}

// Explicit barriers prove cancellation reaches bootstrap and Stop joins it.
type schedulerBootstrapCancellationCache struct {
	SchedulerCache
	entered chan struct{}
	exited  chan struct{}
}

func (c *schedulerBootstrapCancellationCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	close(c.entered)
	<-ctx.Done()
	close(c.exited)
	return nil, ctx.Err()
}

func TestSchedulerSnapshotLifecycleCancelsAndJoinsBootstrap(t *testing.T) {
	cache := &schedulerBootstrapCancellationCache{entered: make(chan struct{}), exited: make(chan struct{})}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartContext(parent))
	<-cache.entered
	cancel()
	ctx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, svc.StopContext(ctx))
	select {
	case <-cache.exited:
	default:
		t.Fatal("Stop returned before bootstrap exited")
	}
	require.Error(t, svc.StartContext(context.Background()))
	// Neither immediate poll nor a queued rebuild may start after join.
	svc.pollOutbox()
	require.ErrorIs(t, svc.triggerFullRebuild("after-stop"), context.Canceled)
}

func TestSchedulerSnapshotStopBeforeStartIsSingleUse(t *testing.T) {
	svc := NewSchedulerSnapshotService(&schedulerBootstrapCancellationCache{}, nil, nil, nil, nil)
	svc.Stop()
	svc.Stop()
	require.Error(t, svc.StartContext(context.Background()))
}

func TestSchedulerSnapshotQueuedRebuildDoesNotRunAfterStop(t *testing.T) {
	svc := NewSchedulerSnapshotService(&schedulerCancellationCache{}, nil, nil, nil, nil)
	svc.fullRebuildRunMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- svc.coalesceFullRebuild(func() error { t.Error("queued rebuild ran after Stop"); return nil })
	}()
	svc.Stop()
	svc.fullRebuildRunMu.Unlock()
	require.ErrorIs(t, <-done, context.Canceled)
}
