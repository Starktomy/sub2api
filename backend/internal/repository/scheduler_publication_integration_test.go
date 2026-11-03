//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerPublicationFencingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox, scheduler_publication, scheduler_publication_lifecycle")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("TRUNCATE scheduler_outbox, scheduler_publication, scheduler_publication_lifecycle")
	})
	cache := NewPublicationSchedulerCache(integrationDB, integrationRedis).(*publicationSchedulerCache)
	locker := NewFencedMaintenanceLockRepository(integrationDB)
	acquire := func() *maintenanceSession {
		s, ok, err := locker.TryAcquire(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		session := s.(*maintenanceSession)
		t.Cleanup(func() { _ = session.Release(context.Background()) })
		return session
	}
	leader := acquire()
	bound, err := cache.BindPublisher(leader)
	require.NoError(t, err)
	publisher := bound.(*schedulerPublicationPublisher)
	bucket := service.SchedulerBucket{Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	data := func(name string) service.SchedulerPublicationData {
		return service.SchedulerPublicationData{
			Accounts: []service.Account{{ID: 1, Name: name, Platform: service.PlatformOpenAI, Credentials: map[string]any{"api_key": "synthetic-test"}, Extra: map[string]any{"nested": map[string]any{"x": "original"}}}},
			Buckets:  map[string][]int64{bucket.String(): {1}},
		}
	}
	insert := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}, key string) int64 {
		var id int64
		require.NoError(t, q.QueryRowContext(ctx, "INSERT INTO scheduler_outbox(event_type,dedup_key) VALUES('full_rebuild',$1) RETURNING id", key).Scan(&id))
		return id
	}
	// Sequence assignment precedes commit: a lower ID can become visible later.
	lateTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer lateTx.Rollback()
	lateID := insert(lateTx, "publication-late")
	id := insert(integrationDB, "publication-visible")
	events, err := publisher.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, id, events[0].ID)
	first, err := publisher.Stage(ctx, data("first"))
	require.NoError(t, err)
	require.NoError(t, publisher.Activate(ctx, first, []int64{id}))
	require.NoError(t, lateTx.Commit())
	var pending int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE id=$1", lateID).Scan(&pending))
	require.Equal(t, 1, pending)
	// Per-read decoding isolates full-account mutable maps and snapshot objects.
	a, err := cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	a.Credentials["api_key"] = "mutated"
	a, err = cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "synthetic-test", a.Credentials["api_key"])
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	snapshot[0].Name = "mutated"
	snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "first", snapshot[0].Name)
	require.Error(t, cache.SetAccount(ctx, a))
	// Same generation, new publication: rolling Redis back to first must not hit.
	second, err := publisher.Stage(ctx, data("second"))
	require.NoError(t, err)
	require.Equal(t, first.Generation, second.Generation)
	require.NotEqual(t, first.ID, second.ID)
	require.NoError(t, publisher.Activate(ctx, second, []int64{lateID}))
	require.Error(t, publisher.Activate(ctx, first, nil), "publication IDs are single attempt")
	keys, err := integrationRedis.Keys(ctx, publicationPrefix(second)+"*").Result()
	require.NoError(t, err)
	require.NoError(t, integrationRedis.Del(ctx, keys...).Err())
	// Old first namespace remains intact, but PG points to the missing second.
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.Error(t, err)
	require.False(t, hit)
	a, err = cache.GetAccount(ctx, 1)
	require.Error(t, err)
	require.Nil(t, a)
	events, err = publisher.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Empty(t, events)
	// The health path uses a fresh publication even when there is nothing to ack.
	recovered, err := publisher.Stage(ctx, data("recovered"))
	require.NoError(t, err)
	require.NoError(t, publisher.Activate(ctx, recovered, nil))
	a, err = cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "recovered", a.Name)
	// Corruption must fail digest validation, not return partial snapshots.
	require.NoError(t, integrationRedis.Set(ctx, publicationPrefix(recovered)+"slim:1", `{"ID":1}`, 0).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.Error(t, err)
	require.False(t, hit)
	// A stalled stage cannot activate after leadership changes.
	stale, err := publisher.Stage(ctx, data("stale"))
	require.NoError(t, err)
	require.NoError(t, leader.Release(ctx))
	successor := acquire()
	next, err := cache.BindPublisher(successor)
	require.NoError(t, err)
	current, err := next.Stage(ctx, data("successor"))
	require.NoError(t, err)
	require.NoError(t, next.Activate(ctx, current, nil))
	protected := insert(integrationDB, "publication-protected")
	require.Error(t, publisher.Activate(ctx, stale, []int64{protected}))
	a, err = cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "successor", a.Name)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE id=$1", protected).Scan(&pending))
	require.Equal(t, 1, pending)
	// Force ack SQL failure AFTER pointer update; the pointer must roll back too.
	failed, err := next.Stage(ctx, data("must-rollback"))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION publication_test_reject_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected ack failure'; END $$;
 CREATE TRIGGER publication_test_reject_delete BEFORE DELETE ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION publication_test_reject_delete()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DROP TRIGGER IF EXISTS publication_test_reject_delete ON scheduler_outbox; DROP FUNCTION IF EXISTS publication_test_reject_delete()")
	})
	require.Error(t, next.Activate(ctx, failed, []int64{protected}))
	a, err = cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "successor", a.Name)
	require.Error(t, next.Activate(ctx, failed, nil), "failed/ambiguous attempt never reused")
	// Real PG permission failure: no in-process old pointer is used.
	_, err = integrationDB.ExecContext(ctx, "ALTER TABLE scheduler_publication RENAME TO scheduler_publication_hidden")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("ALTER TABLE IF EXISTS scheduler_publication_hidden RENAME TO scheduler_publication")
	})
	a, err = cache.GetAccount(ctx, 1)
	require.Error(t, err)
	require.Nil(t, a)
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.Error(t, err)
	require.False(t, hit)
	_, err = integrationDB.ExecContext(ctx, "ALTER TABLE scheduler_publication_hidden RENAME TO scheduler_publication")
	require.NoError(t, err)
}
