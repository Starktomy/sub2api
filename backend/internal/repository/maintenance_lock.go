package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Stable application-specific two-int PostgreSQL advisory lock namespace.
const maintenanceLockNamespace = 1937072690
const maintenanceLockID = 1

type MaintenanceLockRepository struct{ db *sql.DB }

func NewMaintenanceLockRepository(db *sql.DB) *MaintenanceLockRepository {
	return &MaintenanceLockRepository{db: db}
}

func (r *MaintenanceLockRepository) TryAcquire(ctx context.Context) (service.MaintenanceSession, bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("maintenance requires a PostgreSQL database")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&acquired)
	if err != nil {
		discardMaintenanceConnection(conn)
		return nil, false, err
	}
	if !acquired {
		return nil, false, conn.Close()
	}
	return &maintenanceSession{conn: conn}, true, nil
}

type maintenanceSession struct{ conn *sql.Conn }

func (s *maintenanceSession) Check(ctx context.Context) error {
	// SELECT rather than Ping: check ownership on exactly the dedicated session.
	// driver replacement is not permitted: sql.Conn remains tied to this session.
	var held bool
	err := s.conn.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
 AND pid = pg_backend_pid() AND classid = $1::oid AND objid = $2::oid
 AND objsubid = 2 AND granted
 )`, maintenanceLockNamespace, maintenanceLockID).Scan(&held)
	if err != nil {
		return err
	}
	if !held {
		return errors.New("PostgreSQL session no longer holds maintenance advisory lock")
	}
	return nil
}

func (s *maintenanceSession) Release(ctx context.Context) error {
	var unlocked bool
	err := s.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, $2)", maintenanceLockNamespace, maintenanceLockID).Scan(&unlocked)
	// A connection with uncertain unlock outcome MUST NOT return to the pool.
	if err != nil || !unlocked {
		discardMaintenanceConnection(s.conn)
		if err != nil {
			return err
		}
		return fmt.Errorf("maintenance advisory lock was not held on release")
	}
	return s.conn.Close()
}

func discardMaintenanceConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
