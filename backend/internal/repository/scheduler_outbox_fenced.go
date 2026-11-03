package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// fencedSchedulerOutboxRepository is permanently bound to one dedicated
// maintenance session. It cannot rebind to a successor or authorize work from
// a fence supplied through a context or a different session implementation.
type fencedSchedulerOutboxRepository struct {
	*schedulerOutboxRepository // read-only observations may use the pool
	session                    *maintenanceSession
}

// BindFencedSchedulerOutbox selects opt-in, session-bound outbox mutations.
// Only a repository-issued session with a durable fence is accepted. Binding
// does not perform a pooled precheck: each mutation validates lock ownership and
// generation inside its transaction on the dedicated leadership connection.
func BindFencedSchedulerOutbox(db *sql.DB, session service.MaintenanceSession) (service.SchedulerOutboxRepository, error) {
	s, ok := session.(*maintenanceSession)
	if db == nil || !ok || s == nil {
		return nil, errors.New("fenced scheduler outbox requires a database and repository maintenance session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.released || !s.fence.Valid() || len(s.fence.Owner) != 64 {
		return nil, errors.New("fenced scheduler outbox requires an available durable maintenance session")
	}
	return &fencedSchedulerOutboxRepository{
		schedulerOutboxRepository: &schedulerOutboxRepository{db: db},
		session:                   s,
	}, nil
}

func (r *fencedSchedulerOutboxRepository) ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]service.SchedulerOutboxEvent, error) {
	var events []service.SchedulerOutboxEvent
	err := r.session.withGeneration(ctx, func(tx *sql.Tx) error {
		var err error
		events, err = listPendingSchedulerOutboxAndReleaseDedup(ctx, tx, limit)
		return err
	})
	if err != nil {
		// Even a successful scan conveys no usable events if COMMIT failed.
		return nil, err
	}
	return events, nil
}

func (r *fencedSchedulerOutboxRepository) AcknowledgeProcessed(ctx context.Context, ids []int64) error {
	return r.session.withGeneration(ctx, func(tx *sql.Tx) error {
		if len(ids) == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE id = ANY($1)", pq.Array(ids))
		return err
	})
}
