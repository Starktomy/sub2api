package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFencedMaintenanceSessionTransactionRejectsReleasedAndLegacy(t *testing.T) {
	for _, session := range []*maintenanceSession{{}, {released: true, fence: service.MaintenanceFence{Generation: 1, Owner: strings.Repeat("a", 64)}}} {
		called := false
		err := session.withGeneration(context.Background(), func(*sql.Tx) error { called = true; return nil })
		require.Error(t, err)
		require.False(t, called)
	}
}

func TestFencedMaintenanceConstructorAndSessionGuard(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"held"}).AddRow(true))
	mock.ExpectBegin()
	expectMaintenanceGenerationLock(mock, true)
	mock.ExpectQuery("UPDATE maintenance_generation").WithArgs(maintenanceOwnerArgument{}).WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(11))
	mock.ExpectCommit()
	got, ok, err := NewFencedMaintenanceLockRepository(db).TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	session := got.(*maintenanceSession)
	fence := session.Fence()
	require.True(t, fence.Valid())
	require.Equal(t, int64(11), fence.Generation)
	mock.ExpectBegin()
	expectMaintenanceGenerationLock(mock, true)
	mock.ExpectQuery("SELECT generation, owner").WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}).AddRow(fence.Generation, fence.Owner))
	mock.ExpectExec("SELECT 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, session.withGeneration(context.Background(), func(tx *sql.Tx) error { _, e := tx.ExecContext(context.Background(), "SELECT 1"); return e }))
	mock.ExpectQuery("SELECT pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
	require.NoError(t, session.Release(context.Background()))
	require.NoError(t, session.Release(context.Background()))
	require.Error(t, session.withGeneration(context.Background(), func(*sql.Tx) error { t.Fatal("released callback ran"); return nil }))
	require.NoError(t, mock.ExpectationsWereMet())
}
