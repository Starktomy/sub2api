package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Reuse the normal repositories/filters/conversion, but never the pooled SQL
// executor. Both repository clients share the same read-only MVCC snapshot.
func (r *accountRepository) WithSchedulerSnapshot(ctx context.Context, load func(service.AccountRepository, service.GroupRepository) error) error {
	tx, err := r.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err := load(newAccountRepositoryWithSQL(client, nil, nil), newGroupRepositoryWithSQL(client, nil)); err != nil {
		return err
	}
	return tx.Commit()
}
