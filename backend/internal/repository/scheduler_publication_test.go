package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerPublicationAmbiguousCommitConsumesID(t *testing.T) {
	s, repo, mock := fencedOutboxMock(t)
	p := service.SchedulerPublication{Generation: s.fence.Generation, ID: s.fence.Owner, Digest: s.fence.Owner}
	bound, ok := repo.(*fencedSchedulerOutboxRepository)
	require.True(t, ok)
	publisher := &schedulerPublicationPublisher{fencedSchedulerOutboxRepository: bound, staged: map[string]service.SchedulerPublication{p.ID: p}}
	expectOutboxFence(mock, s.fence.Generation, s.fence.Owner)
	mock.ExpectExec("UPDATE scheduler_publication_lifecycle SET state='active'").WithArgs(p.ID, p.Generation).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE scheduler_publication_lifecycle SET state='obsolete'").WithArgs(p.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO scheduler_publication").WithArgs(p.Generation, p.ID, p.Digest).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM scheduler_outbox WHERE id = ANY").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("lost commit response"))
	require.ErrorContains(t, publisher.Activate(context.Background(), p, []int64{8}), "commit outcome unknown")
	require.ErrorContains(t, publisher.Activate(context.Background(), p, []int64{8}), "already attempted")
	require.Error(t, publisher.Activate(context.Background(), service.SchedulerPublication{Generation: p.Generation, ID: "forged", Digest: p.Digest}, nil))
}
