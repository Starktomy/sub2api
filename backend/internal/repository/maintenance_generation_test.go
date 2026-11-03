package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type maintenanceOwnerArgument struct{}

func (maintenanceOwnerArgument) Match(value driver.Value) bool {
	owner, ok := value.(string)
	if !ok || len(owner) != 64 {
		return false
	}
	_, err := hex.DecodeString(owner)
	return err == nil
}

func maintenanceGenerationMock(t *testing.T) (*sql.Conn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, conn.Close())
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return conn, mock
}

func expectMaintenanceGenerationLock(mock sqlmock.Sqlmock, held bool) {
	mock.ExpectQuery("SELECT EXISTS").WithArgs(maintenanceLockNamespace, maintenanceLockID).
		WillReturnRows(sqlmock.NewRows([]string{"held"}).AddRow(held))
}

func TestAllocateMaintenanceGeneration(t *testing.T) {
	conn, mock := maintenanceGenerationMock(t)
	mock.ExpectBegin()
	expectMaintenanceGenerationLock(mock, true)
	mock.ExpectQuery("UPDATE maintenance_generation").WithArgs(maintenanceOwnerArgument{}).
		WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(int64(42)))
	mock.ExpectCommit()
	generation, owner, err := allocateMaintenanceGeneration(context.Background(), conn)
	require.NoError(t, err)
	require.Equal(t, int64(42), generation)
	require.Len(t, owner, 64)
	_, err = hex.DecodeString(owner)
	require.NoError(t, err)
}

func TestAllocateMaintenanceGenerationFailsClosed(t *testing.T) {
	for _, failure := range []string{"lock lost", "lock query", "missing schema", "missing singleton", "overflow", "commit ambiguous"} {
		t.Run(failure, func(t *testing.T) {
			conn, mock := maintenanceGenerationMock(t)
			mock.ExpectBegin()
			if failure == "lock query" {
				mock.ExpectQuery("SELECT EXISTS").WillReturnError(errors.New("session unavailable"))
			} else {
				expectMaintenanceGenerationLock(mock, failure != "lock lost")
			}
			switch failure {
			case "missing schema":
				mock.ExpectQuery("UPDATE maintenance_generation").WillReturnError(errors.New("relation does not exist"))
			case "missing singleton":
				mock.ExpectQuery("UPDATE maintenance_generation").WillReturnRows(sqlmock.NewRows([]string{"generation"}))
			case "overflow":
				mock.ExpectQuery("UPDATE maintenance_generation").WillReturnError(errors.New("bigint out of range"))
			case "commit ambiguous":
				mock.ExpectQuery("UPDATE maintenance_generation").WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(9))
				mock.ExpectCommit().WillReturnError(errors.New("connection lost"))
			}
			if failure != "commit ambiguous" {
				mock.ExpectRollback()
			}
			generation, owner, err := allocateMaintenanceGeneration(context.Background(), conn)
			require.Error(t, err)
			require.Zero(t, generation)
			require.Empty(t, owner, "no capability may escape an uncertain allocation")
		})
	}
}

func TestWithMaintenanceGenerationTx(t *testing.T) {
	owner := strings.Repeat("a", 64)
	for _, scenario := range []string{"success", "stale generation", "forged owner", "lock lost", "missing schema", "missing singleton", "callback error", "commit ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			conn, mock := maintenanceGenerationMock(t)
			mock.ExpectBegin()
			expectMaintenanceGenerationLock(mock, scenario != "lock lost")
			if scenario != "lock lost" {
				query := mock.ExpectQuery("SELECT generation, owner FROM maintenance_generation.*FOR UPDATE")
				switch scenario {
				case "missing schema":
					query.WillReturnError(errors.New("relation does not exist"))
				case "missing singleton":
					query.WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}))
				case "stale generation":
					query.WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}).AddRow(8, owner))
				case "forged owner":
					query.WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}).AddRow(7, strings.Repeat("b", 64)))
				default:
					query.WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}).AddRow(7, owner))
				}
			}
			runs := scenario == "success" || scenario == "callback error" || scenario == "commit ambiguous"
			if runs {
				mock.ExpectExec("SELECT 1").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			switch scenario {
			case "success":
				mock.ExpectCommit()
			case "commit ambiguous":
				mock.ExpectCommit().WillReturnError(errors.New("connection lost"))
			default:
				mock.ExpectRollback()
			}
			called := false
			err := withMaintenanceGeneration(context.Background(), conn, 7, owner, func(tx *sql.Tx) error {
				called = true
				_, err := tx.ExecContext(context.Background(), "SELECT 1")
				require.NoError(t, err)
				if scenario == "callback error" {
					return errors.New("callback rejected")
				}
				return nil
			})
			require.Equal(t, runs, called)
			if scenario == "success" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestMaintenanceGenerationInvalidArguments(t *testing.T) {
	generation, owner, err := allocateMaintenanceGeneration(context.Background(), nil)
	require.Error(t, err)
	require.Zero(t, generation)
	require.Empty(t, owner)
	conn, _ := maintenanceGenerationMock(t)
	callback := func(*sql.Tx) error { t.Fatal("invalid capability ran callback"); return nil }
	for _, call := range []func() error{
		func() error {
			return withMaintenanceGeneration(context.Background(), nil, 1, strings.Repeat("a", 64), callback)
		},
		func() error {
			return withMaintenanceGeneration(context.Background(), conn, 0, strings.Repeat("a", 64), callback)
		},
		func() error { return withMaintenanceGeneration(context.Background(), conn, 1, "", callback) },
		func() error {
			return withMaintenanceGeneration(context.Background(), conn, 1, strings.Repeat("a", 64), nil)
		},
	} {
		require.Error(t, call())
	}
}
