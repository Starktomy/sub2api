package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceDedicatedSessionAcquireCheckRelease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := NewMaintenanceLockRepository(db)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(maintenanceLockNamespace, maintenanceLockID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	session, ok, err := repo.TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, db.Stats().InUse, "leader keeps a dedicated connection")
	mock.ExpectQuery("SELECT EXISTS").WithArgs(maintenanceLockNamespace, maintenanceLockID).WillReturnRows(sqlmock.NewRows([]string{"held"}).AddRow(true))
	require.NoError(t, session.Check(context.Background()))
	mock.ExpectQuery("SELECT pg_advisory_unlock").WithArgs(maintenanceLockNamespace, maintenanceLockID).WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
	require.NoError(t, session.Release(context.Background()))
	require.Zero(t, db.Stats().InUse)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMaintenanceStandbyReturnsConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	session, ok, err := NewMaintenanceLockRepository(db).TryAcquire(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, session)
	require.Zero(t, db.Stats().InUse)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMaintenanceLostSessionIsNotHealthy(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	session, ok, err := NewMaintenanceLockRepository(db).TryAcquire(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"held"}).AddRow(false))
	require.ErrorContains(t, session.Check(context.Background()), "no longer holds")
	mock.ExpectQuery("SELECT pg_advisory_unlock").WillReturnError(errors.New("connection lost"))
	mock.ExpectClose()
	require.ErrorContains(t, session.Release(context.Background()), "connection lost")
	require.NoError(t, mock.ExpectationsWereMet())
}
