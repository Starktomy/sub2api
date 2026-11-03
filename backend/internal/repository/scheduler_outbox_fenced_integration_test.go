//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Uses the shared Docker PostgreSQL harness. No runtime admission, scheduler
// namespace, provider calls or cloud infrastructure is involved.
func TestFencedSchedulerOutboxSessionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = integrationDB.ExecContext(cleanup, "TRUNCATE scheduler_outbox")
	}()
	locker := NewFencedMaintenanceLockRepository(integrationDB)
	acquire := func() *maintenanceSession {
		t.Helper()
		session, acquired, err := locker.TryAcquire(ctx)
		require.NoError(t, err)
		require.True(t, acquired)
		s := session.(*maintenanceSession)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = s.Release(cleanup)
		})
		return s
	}
	bind := func(s *maintenanceSession) service.SchedulerOutboxRepository {
		t.Helper()
		r, err := BindFencedSchedulerOutbox(integrationDB, s)
		require.NoError(t, err)
		return r
	}
	insert := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}, key string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, q.QueryRowContext(ctx, "INSERT INTO scheduler_outbox (event_type, dedup_key) VALUES ('full_rebuild', $1) RETURNING id", key).Scan(&id))
		return id
	}
	leader := acquire()
	repo := bind(leader)
	protected := insert(integrationDB, "fenced-protected")
	assertRejected := func(r service.SchedulerOutboxRepository) {
		t.Helper()
		events, err := r.ListPendingAndReleaseDedup(ctx, 100)
		require.Error(t, err)
		require.Nil(t, events)
		require.Error(t, r.AcknowledgeProcessed(ctx, []int64{protected}))
		var key string
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT dedup_key FROM scheduler_outbox WHERE id = $1", protected).Scan(&key))
		require.Equal(t, "fenced-protected", key, "failed guard must neither release dedup nor delete")
	}

	// Even on the real lock-holding connection, a forged owner cannot mutate.
	forged := &maintenanceSession{conn: leader.conn, fence: service.MaintenanceFence{Generation: leader.fence.Generation, Owner: strings.Repeat("0", 64)}}
	if forged.fence.Owner == leader.fence.Owner {
		forged.fence.Owner = strings.Repeat("1", 64)
	}
	assertRejected(bind(forged))

	var unlocked bool
	require.NoError(t, leader.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&unlocked))
	require.True(t, unlocked)
	assertRejected(repo) // current durable identity, but session lock was lost

	successor := acquire()
	require.Greater(t, successor.fence.Generation, leader.fence.Generation)
	require.NoError(t, successor.Release(ctx))
	// Former leader regains the lock but never regains its expired generation.
	var held bool
	require.NoError(t, leader.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&held))
	require.True(t, held)
	assertRejected(repo)
	require.NoError(t, leader.Release(ctx))
	assertRejected(repo) // released adapter cannot be reused

	current := acquire()
	currentRepo := bind(current)
	// Reserve a lower sequence ID in an uncommitted producer transaction.
	producer, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer producer.Rollback()
	late := insert(producer, "fenced-late")
	higher := insert(integrationDB, "fenced-higher")
	require.Less(t, late, higher)
	events, err := currentRepo.ListPendingAndReleaseDedup(ctx, 100)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, protected, events[0].ID)
	require.Equal(t, higher, events[1].ID)
	var key sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT dedup_key FROM scheduler_outbox WHERE id = $1", protected).Scan(&key))
	require.False(t, key.Valid)
	// Releasing dedup permits another event for the same logical key.
	newer := insert(integrationDB, "fenced-protected")
	require.NoError(t, producer.Commit())
	require.NoError(t, currentRepo.AcknowledgeProcessed(ctx, []int64{protected, higher}))
	for id, expectedKey := range map[int64]string{late: "fenced-late", newer: "fenced-protected"} {
		var got string
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT dedup_key FROM scheduler_outbox WHERE id = $1", id).Scan(&got))
		require.Equal(t, expectedKey, got, "exact ack preserves late and newly enqueued events")
	}
	events, err = currentRepo.ListPendingAndReleaseDedup(ctx, 100)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, late, events[0].ID)
	require.Equal(t, newer, events[1].ID)
	require.NoError(t, currentRepo.AcknowledgeProcessed(ctx, []int64{late, newer}))
	require.NoError(t, current.Release(ctx))
}
