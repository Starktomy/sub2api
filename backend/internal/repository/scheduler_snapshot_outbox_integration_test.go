//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotOutboxReplay(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	client := testEntClient(t)

	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")

	accountRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	outboxRepo := NewSchedulerOutboxRepository(integrationDB)
	cache := NewSchedulerCache(rdb)

	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxPollIntervalSeconds:  1,
				FullRebuildIntervalSeconds: 0,
				DbFallbackEnabled:          true,
			},
		},
	}

	account := &service.Account{
		Name:        "outbox-replay-" + time.Now().Format("150405.000000"),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    1,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
	require.NoError(t, accountRepo.Create(ctx, account))
	require.NoError(t, cache.SetAccount(ctx, account))

	svc := service.NewSchedulerSnapshotService(cache, outboxRepo, accountRepo, nil, cfg)
	svc.Start()
	t.Cleanup(svc.Stop)

	require.NoError(t, accountRepo.UpdateLastUsed(ctx, account.ID))
	updated, err := accountRepo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.LastUsedAt)
	expectedUnix := updated.LastUsedAt.Unix()

	require.Eventually(t, func() bool {
		cached, err := cache.GetAccount(ctx, account.ID)
		if err != nil || cached == nil || cached.LastUsedAt == nil {
			return false
		}
		return cached.LastUsedAt.Unix() == expectedUnix
	}, 5*time.Second, 100*time.Millisecond)
}

// PostgreSQL sequence allocation order is not commit order. No age/TTL
// assumption is needed: the lower row remains pending after higher-ID ack.
func TestSchedulerOutboxLateLowerIDRemainsPending(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox") })
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var lower, higher int64
	require.NoError(t, tx.QueryRowContext(ctx, "INSERT INTO scheduler_outbox (event_type) VALUES ('full_rebuild') RETURNING id").Scan(&lower))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "INSERT INTO scheduler_outbox (event_type) VALUES ('full_rebuild') RETURNING id").Scan(&higher))
	require.Greater(t, higher, lower)
	repo := NewSchedulerOutboxRepository(integrationDB)
	events, err := repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, higher, events[0].ID)
	require.NoError(t, repo.AcknowledgeProcessed(ctx, []int64{higher}))
	require.NoError(t, tx.Commit())
	events, err = repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, lower, events[0].ID)
	require.NoError(t, repo.AcknowledgeProcessed(ctx, []int64{higher})) // stale duplicate ack
	events, err = repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, lower, events[0].ID)
}
