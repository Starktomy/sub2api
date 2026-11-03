package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type forgedOutboxMaintenanceSession struct{}

func (forgedOutboxMaintenanceSession) Check(context.Context) error   { return nil }
func (forgedOutboxMaintenanceSession) Release(context.Context) error { return nil }
func (forgedOutboxMaintenanceSession) Fence() service.MaintenanceFence {
	return service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}
}

func fencedOutboxMock(t *testing.T) (*maintenanceSession, service.SchedulerOutboxRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// A pooled precheck/write would block: the sole connection belongs to s.
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	s := &maintenanceSession{conn: conn, fence: service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}}
	r, err := BindFencedSchedulerOutbox(db, s)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return s, r, mock
}

func expectOutboxFence(mock sqlmock.Sqlmock, generation int64, owner string) {
	mock.ExpectBegin()
	expectMaintenanceGenerationLock(mock, true)
	mock.ExpectQuery("SELECT generation, owner FROM maintenance_generation.*FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"generation", "owner"}).AddRow(generation, owner))
}

func TestBindFencedSchedulerOutboxRejectsInvalidSessions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	var typedNil *maintenanceSession
	for _, session := range []service.MaintenanceSession{
		nil, typedNil, forgedOutboxMaintenanceSession{},
		&maintenanceSession{conn: conn},
		&maintenanceSession{fence: service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}},
		&maintenanceSession{conn: conn, fence: service.MaintenanceFence{Generation: 0, Owner: strings.Repeat("a", 64)}},
		&maintenanceSession{conn: conn, fence: service.MaintenanceFence{Generation: 7, Owner: "short"}},
		&maintenanceSession{conn: conn, released: true, fence: service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}},
	} {
		r, err := BindFencedSchedulerOutbox(db, session)
		require.Error(t, err)
		require.Nil(t, r)
	}
	r, err := BindFencedSchedulerOutbox(nil, &maintenanceSession{conn: conn, fence: service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}})
	require.Error(t, err)
	require.Nil(t, r)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFencedSchedulerOutboxListCommitBoundary(t *testing.T) {
	for _, failure := range []string{"", "query", "scan", "payload", "rows", "commit"} {
		t.Run(failure, func(t *testing.T) {
			s, repo, mock := fencedOutboxMock(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			expectOutboxFence(mock, s.fence.Generation, s.fence.Owner)
			query := mock.ExpectQuery("WITH selected AS MATERIALIZED").WithArgs(100)
			created := time.Now().UTC()
			rows := sqlmock.NewRows([]string{"id", "event_type", "account_id", "group_id", "payload", "created_at"})
			switch failure {
			case "query":
				query.WillReturnError(errors.New("query failed"))
			case "scan":
				query.WillReturnRows(rows.AddRow("bad-id", "account_changed", nil, nil, nil, created))
			case "payload":
				query.WillReturnRows(rows.AddRow(9, "account_changed", nil, nil, []byte("{"), created))
			default:
				rows.AddRow(9, "account_changed", 42, 3, []byte(`{"changed":true}`), created)
				if failure == "rows" {
					rows.RowError(0, errors.New("stream failed"))
				}
				query.WillReturnRows(rows)
			}
			switch failure {
			case "":
				mock.ExpectCommit()
			case "commit":
				mock.ExpectCommit().WillReturnError(errors.New("commit outcome unknown"))
			default:
				mock.ExpectRollback()
			}
			events, err := repo.ListPendingAndReleaseDedup(ctx, 0)
			if failure != "" {
				require.Error(t, err)
				require.Nil(t, events, "no events escape a failed transaction")
				return
			}
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, int64(9), events[0].ID)
			require.Equal(t, int64(42), *events[0].AccountID)
			require.Equal(t, int64(3), *events[0].GroupID)
			require.Equal(t, map[string]any{"changed": true}, events[0].Payload)
			require.Equal(t, created, events[0].CreatedAt)
		})
	}
}

func TestFencedSchedulerOutboxMutationsFailClosed(t *testing.T) {
	for _, operation := range []string{"list", "ack", "empty ack"} {
		for _, failure := range []string{"released", "invalid fence", "lock lost", "stale generation", "forged owner", "lost connection"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				s, repo, mock := fencedOutboxMock(t)
				switch failure {
				case "released":
					s.released = true
				case "invalid fence":
					s.fence = service.MaintenanceFence{}
				case "lost connection":
					mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
				case "lock lost":
					mock.ExpectBegin()
					expectMaintenanceGenerationLock(mock, false)
					mock.ExpectRollback()
				case "stale generation":
					expectOutboxFence(mock, 8, s.fence.Owner)
					mock.ExpectRollback()
				case "forged owner":
					expectOutboxFence(mock, 7, strings.Repeat("b", 64))
					mock.ExpectRollback()
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if operation == "list" {
					events, err := repo.ListPendingAndReleaseDedup(ctx, 1)
					require.Error(t, err)
					require.Nil(t, events)
				} else {
					ids := []int64{9}
					if operation == "empty ack" {
						ids = nil
					}
					require.Error(t, repo.AcknowledgeProcessed(ctx, ids))
				}
			})
		}
	}
}

func TestFencedSchedulerOutboxExactAcknowledgment(t *testing.T) {
	for _, outcome := range []string{"success", "empty", "delete failure", "commit failure"} {
		t.Run(outcome, func(t *testing.T) {
			s, repo, mock := fencedOutboxMock(t)
			expectOutboxFence(mock, 7, s.fence.Owner)
			ids := []int64{3, 10000}
			if outcome == "empty" {
				ids = nil
			} else {
				exec := mock.ExpectExec(regexp.QuoteMeta("DELETE FROM scheduler_outbox WHERE id = ANY($1)")).WithArgs("{3,10000}")
				if outcome == "delete failure" {
					exec.WillReturnError(errors.New("delete failed"))
				} else {
					exec.WillReturnResult(sqlmock.NewResult(0, 2))
				}
			}
			switch outcome {
			case "delete failure":
				mock.ExpectRollback()
			case "commit failure":
				mock.ExpectCommit().WillReturnError(errors.New("commit unknown"))
			default:
				mock.ExpectCommit()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := repo.AcknowledgeProcessed(ctx, ids)
			if outcome == "delete failure" || outcome == "commit failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFencedSchedulerOutboxReadOnlyObservationsUsePool(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	s := &maintenanceSession{conn: conn, fence: service.MaintenanceFence{Generation: 7, Owner: strings.Repeat("a", 64)}}
	repo, err := BindFencedSchedulerOutbox(db, s)
	require.NoError(t, err)
	// Read-only methods do not confer mutation authority, even after release.
	s.released = true
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox")).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(12))
	max, err := repo.MaxID(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), max)
	created := time.Now().UTC()
	mock.ExpectQuery("SELECT created_at FROM scheduler_outbox").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(created))
	got, found, err := repo.FirstCreatedAtAfter(context.Background(), 9)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
