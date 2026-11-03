//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerPublicationRetirementGC(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.Exec("TRUNCATE scheduler_publication,scheduler_publication_lifecycle,scheduler_outbox")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("TRUNCATE scheduler_publication,scheduler_publication_lifecycle,scheduler_outbox")
	})
	rdb := testRedis(t)
	cache, ok := NewPublicationSchedulerCache(integrationDB, rdb).(*publicationSchedulerCache)
	require.True(t, ok)
	session, acquired, err := NewFencedMaintenanceLockRepository(integrationDB).TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { _ = session.Release(ctx) })
	bound, err := cache.BindPublisher(session)
	require.NoError(t, err)
	publisher, ok := bound.(*schedulerPublicationPublisher)
	require.True(t, ok)
	data := service.SchedulerPublicationData{Accounts: []service.Account{{ID: 1, Name: "gc", Platform: service.PlatformOpenAI}}, Buckets: map[string][]int64{}}
	first, err := publisher.Stage(ctx, data)
	require.NoError(t, err)
	require.NoError(t, publisher.Activate(ctx, first, nil))
	second, err := publisher.Stage(ctx, data)
	require.NoError(t, err)
	require.NoError(t, publisher.Activate(ctx, second, nil))
	abandoned, err := publisher.Stage(ctx, data)
	require.NoError(t, err)
	// Test clock advance in PG avoids timing sleeps.
	_, err = integrationDB.Exec("UPDATE scheduler_publication_lifecycle SET changed_at=clock_timestamp()-interval '10 minutes'")
	require.NoError(t, err)
	require.NoError(t, publisher.Collect(ctx))
	active, _, err := cache.manifest(ctx)
	require.NoError(t, err)
	require.Equal(t, second, active)
	require.Equal(t, int64(0), rdb.Exists(ctx, publicationPrefix(first)+"manifest").Val())
	require.Equal(t, int64(0), rdb.Exists(ctx, publicationPrefix(abandoned)+"manifest").Val())
	// Local staged capability remains but durable retirement rejects activation.
	require.Error(t, publisher.Activate(ctx, abandoned, nil))
	// Delayed stage writes after retirement cannot resurrect payloads.
	written, err := writePublicationKey.Run(ctx, rdb, []string{publicationPrefix(abandoned) + "retired", publicationPrefix(abandoned) + "late"}, "orphan").Bool()
	require.NoError(t, err)
	require.False(t, written)
	// Redis rollback can erase the marker; a delayed stale stage might then
	// recreate data, but PG retirement still forbids activation and periodic
	// tombstone rotation sweeps the recreated payload again.
	require.NoError(t, rdb.Del(ctx, publicationPrefix(first)+"retired").Err())
	resurrected, err := writePublicationKey.Run(ctx, rdb, []string{publicationPrefix(first) + "retired", publicationPrefix(first) + "full:1"}, "resurrected").Bool()
	require.NoError(t, err)
	require.True(t, resurrected)
	// Redis rollback resurrection of an obsolete payload is swept again.
	require.NoError(t, rdb.Set(ctx, publicationPrefix(first)+"full:1", "resurrected", 0).Err())
	require.NoError(t, publisher.Collect(ctx))
	require.Equal(t, int64(0), rdb.Exists(ctx, publicationPrefix(first)+"full:1").Val())
	// Race retirement against activation. PG serializes state transitions: either
	// it becomes active and GC excludes it, or retired and activation is rejected.
	racing, err := publisher.Stage(ctx, data)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE scheduler_publication_lifecycle SET changed_at=clock_timestamp()-interval '10 minutes' WHERE publication_id=$1", racing.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- publisher.Collect(ctx) }()
	activation := publisher.Activate(ctx, racing, nil)
	require.NoError(t, <-done)
	active, _, err = cache.manifest(ctx)
	require.NoError(t, err)
	if activation == nil {
		require.Equal(t, racing, active)
	} else {
		require.Equal(t, second, active)
	}
	require.NoError(t, session.Release(ctx))
	require.Error(t, publisher.Collect(ctx), "stale leadership cannot authorize cleanup")
}
