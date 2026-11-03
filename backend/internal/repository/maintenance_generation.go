package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

const maintenanceGenerationLockSQL = `SELECT EXISTS (
 SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
 AND pid = pg_backend_pid() AND classid = $1::oid AND objid = $2::oid
 AND objsubid = 2 AND granted AND mode = 'ExclusiveLock'
 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
 )`

// allocateMaintenanceGeneration requires a dedicated conn already holding the
// session advisory lock. It neither acquires the lock nor publishes an active
// scheduler generation. Any error returns no usable capability, including an
// ambiguous COMMIT: callers must abandon that leadership attempt, not retry or
// read back the row to claim recovery. This is not a provider-call replay fence.
func allocateMaintenanceGeneration(ctx context.Context, conn *sql.Conn) (generation int64, owner string, err error) {
	if conn == nil {
		return 0, "", errors.New("maintenance generation requires a dedicated PostgreSQL connection")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return 0, "", fmt.Errorf("generate maintenance owner: %w", err)
	}
	owner = hex.EncodeToString(token[:])
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("begin maintenance generation allocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkMaintenanceGenerationLock(ctx, tx); err != nil {
		return 0, "", err
	}
	err = tx.QueryRowContext(ctx, `UPDATE maintenance_generation
 SET generation = generation + 1, owner = $1
 WHERE singleton_id = 1
 RETURNING generation`, owner).Scan(&generation)
	if err != nil {
		return 0, "", fmt.Errorf("allocate maintenance generation: %w", err)
	}
	if generation <= 0 {
		return 0, "", errors.New("invalid allocated maintenance generation")
	}
	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("maintenance generation allocation commit outcome unknown; abandon leadership attempt: %w", err)
	}
	return generation, owner, nil
}

// withMaintenanceGeneration runs only short database work on the dedicated
// leadership session. The callback must use tx, must not commit/rollback it,
// release the advisory lock, or make provider/network calls. The row lock orders
// guarded SQL against allocation; it is not cross-store or external-call fencing.
// A commit error is not permission to replay the callback.
func withMaintenanceGeneration(ctx context.Context, conn *sql.Conn, generation int64, owner string, callback func(*sql.Tx) error) error {
	if conn == nil || generation <= 0 || len(owner) != 64 || callback == nil {
		return errors.New("invalid maintenance generation transaction capability")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin maintenance generation guard: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkMaintenanceGenerationLock(ctx, tx); err != nil {
		return err
	}
	var currentGeneration int64
	var currentOwner sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT generation, owner FROM maintenance_generation
 WHERE singleton_id = 1 FOR UPDATE`).Scan(&currentGeneration, &currentOwner); err != nil {
		return fmt.Errorf("read maintenance generation guard: %w", err)
	}
	if currentGeneration != generation || !currentOwner.Valid || currentOwner.String != owner {
		return errors.New("maintenance generation or owner no longer current")
	}
	if err := callback(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("maintenance generation guarded commit outcome unknown; do not replay: %w", err)
	}
	return nil
}

func checkMaintenanceGenerationLock(ctx context.Context, tx *sql.Tx) error {
	var held bool
	if err := tx.QueryRowContext(ctx, maintenanceGenerationLockSQL, maintenanceLockNamespace, maintenanceLockID).Scan(&held); err != nil {
		return fmt.Errorf("check maintenance generation session lock: %w", err)
	}
	if !held {
		return errors.New("PostgreSQL session no longer holds maintenance advisory lock")
	}
	return nil
}
