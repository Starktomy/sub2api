//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Uses the shared Docker PostgreSQL harness and forward migrations. No provider
// calls, namespace publication, or outbox admission is exercised here.
func TestMaintenanceGenerationDedicatedSessionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	leader, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer leader.Close()
	successor, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer successor.Close()
	// Always release any surviving session lock before returning to the pool,
	// including on a failed assertion (defers run before conn.Close).
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = leader.ExecContext(cleanup, "SELECT pg_advisory_unlock_all()")
		_, _ = successor.ExecContext(cleanup, "SELECT pg_advisory_unlock_all()")
	}()
	acquire := func(conn *sql.Conn) {
		t.Helper()
		var held bool
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&held))
		require.True(t, held)
	}
	unlock := func(conn *sql.Conn) {
		t.Helper()
		var unlocked bool
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&unlocked))
		require.True(t, unlocked)
	}
	acquire(leader)
	var before int64
	var activeBefore sql.NullInt64
	require.NoError(t, leader.QueryRowContext(ctx, "SELECT generation, active_scheduler_generation FROM maintenance_generation WHERE singleton_id = 1").Scan(&before, &activeBefore))
	first, firstOwner, err := allocateMaintenanceGeneration(ctx, leader)
	require.NoError(t, err)
	require.Equal(t, before+1, first)
	require.Len(t, firstOwner, 64)
	called := false
	callback := func(tx *sql.Tx) error {
		called = true
		var pid int
		return tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)
	}
	require.NoError(t, withMaintenanceGeneration(ctx, leader, first, firstOwner, callback))
	require.True(t, called)
	called = false
	forged := strings.Repeat("0", 64)
	if forged == firstOwner {
		forged = strings.Repeat("1", 64)
	}
	require.Error(t, withMaintenanceGeneration(ctx, leader, first, forged, callback))
	require.False(t, called, "forged owner must not run database work")
	// Possession of the persisted capability on another session is insufficient.
	require.Error(t, withMaintenanceGeneration(ctx, successor, first, firstOwner, callback))
	require.False(t, called)
	unlock(leader)
	require.Error(t, withMaintenanceGeneration(ctx, leader, first, firstOwner, callback))
	require.False(t, called, "lock loss rejects even the current row capability")
	failedGeneration, failedOwner, err := allocateMaintenanceGeneration(ctx, leader)
	require.Error(t, err)
	require.Zero(t, failedGeneration)
	require.Empty(t, failedOwner)

	acquire(successor)
	second, secondOwner, err := allocateMaintenanceGeneration(ctx, successor)
	require.NoError(t, err)
	require.Equal(t, first+1, second)
	require.NotEqual(t, firstOwner, secondOwner)
	require.Error(t, withMaintenanceGeneration(ctx, successor, first, firstOwner, callback))
	require.False(t, called, "stale generation must not run callback even on current lock session")
	require.NoError(t, withMaintenanceGeneration(ctx, successor, second, secondOwner, callback))
	require.True(t, called)
	unlock(successor)

	// The former session can reacquire the advisory lock, but its old durable
	// capability must still be rejected after a successor allocated a generation.
	acquire(leader)
	called = false
	require.Error(t, withMaintenanceGeneration(ctx, leader, first, firstOwner, callback))
	require.False(t, called)
	var persisted int64
	var persistedOwner string
	var activeAfter sql.NullInt64
	require.NoError(t, leader.QueryRowContext(ctx, "SELECT generation, owner, active_scheduler_generation FROM maintenance_generation WHERE singleton_id = 1").Scan(&persisted, &persistedOwner, &activeAfter))
	require.Equal(t, second, persisted)
	require.Equal(t, secondOwner, persistedOwner)
	require.Equal(t, activeBefore, activeAfter, "allocation must never auto-publish scheduler authority")
	unlock(leader)
}
