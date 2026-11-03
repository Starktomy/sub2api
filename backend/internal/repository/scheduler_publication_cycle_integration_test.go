//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Executes the actual service rebuild against real account/group repositories,
// not just the staging primitive. Redis loss occurs after the outbox is empty.
func TestSchedulerPublicationActualCycleRollbackRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{Name: "publication-cycle", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	rdb := testRedis(t)
	cache := NewPublicationSchedulerCache(integrationDB, rdb).(*publicationSchedulerCache)
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox, scheduler_publication, scheduler_publication_lifecycle")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("TRUNCATE scheduler_outbox, scheduler_publication, scheduler_publication_lifecycle")
	})
	session, ok, err := NewFencedMaintenanceLockRepository(integrationDB).TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	t.Cleanup(func() { _ = session.Release(context.Background()) })
	publisher, err := cache.BindPublisher(session)
	require.NoError(t, err)
	repo := NewAccountRepository(client, integrationDB, cache)
	svc := service.NewSchedulerSnapshotService(cache, nil, repo, NewGroupRepository(client, integrationDB), nil)
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.RunPublicationCycle(ctx, publisher))
	before, manifest, err := cache.manifest(ctx)
	require.NoError(t, err)
	// No active key may expire merely because events/workers are absent.
	for key := range manifest.Payloads {
		ttl, err := rdb.PTTL(ctx, publicationPrefix(before)+key).Result()
		require.NoError(t, err)
		require.Equal(t, time.Duration(-1), ttl)
	}
	ttl, err := rdb.PTTL(ctx, publicationPrefix(before)+"manifest").Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl)
	got, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ID, got.ID)
	events, err := publisher.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Empty(t, events)

	// Healthy empty-outbox cycles must leave the namespace and PG ID unchanged.
	keysBefore, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)
	require.NoError(t, svc.RunPublicationCycle(ctx, publisher))
	unchanged, _, err := cache.manifest(ctx)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	keysAfter, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)
	require.ElementsMatch(t, keysBefore, keysAfter)
	require.NoError(t, rdb.Del(ctx, publicationPrefix(before)+"manifest").Err())
	got, err = cache.GetAccount(ctx, account.ID)
	require.Error(t, err)
	require.Nil(t, got)
	// Gateway DB fallback cannot publish or mutate any Redis keys.
	legacyBefore, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)
	got, err = svc.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ID, got.ID)
	require.NoError(t, svc.UpdateAccountInCache(ctx, got))
	accounts, _, err := svc.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
	require.NoError(t, err)
	require.NotEmpty(t, accounts)
	legacyAfter, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)
	require.ElementsMatch(t, legacyBefore, legacyAfter)
	require.NoError(t, svc.RunPublicationCycle(ctx, publisher))
	after, _, err := cache.manifest(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Generation, after.Generation)
	require.NotEqual(t, before.ID, after.ID)

	// A valid manifest alone is insufficient: missing any full payload triggers
	// health repair even when the bucket/slim payloads remain intact.
	var missing string
	for key := range manifest.Payloads {
		if len(key) > 5 && key[:5] == "full:" {
			missing = key
			break
		}
	}
	require.NotEmpty(t, missing)
	require.NoError(t, rdb.Del(ctx, publicationPrefix(after)+missing).Err())
	healthy, err := publisher.Healthy(ctx)
	require.NoError(t, err)
	require.False(t, healthy)
	require.NoError(t, svc.RunPublicationCycle(ctx, publisher))
	repaired, _, err := cache.manifest(ctx)
	require.NoError(t, err)
	require.NotEqual(t, after.ID, repaired.ID)
	got, err = cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ID, got.ID)
}

func TestSchedulerPublicationLoaderRepeatableRead(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{Name: "snapshot-before", Platform: service.PlatformOpenAI})
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	require.NoError(t, repo.WithSchedulerSnapshot(ctx, func(accounts service.AccountRepository, groups service.GroupRepository) error {
		before, err := accounts.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, "snapshot-before", before.Name)
		// Concurrent commit after the first snapshot read cannot alter later loads.
		_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET name='snapshot-after',schedulable=false WHERE id=$1", account.ID)
		require.NoError(t, err)
		after, err := accounts.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, before.Name, after.Name)
		require.True(t, after.Schedulable)
		members, err := accounts.ListSchedulableUngroupedByPlatform(ctx, service.PlatformOpenAI)
		require.NoError(t, err)
		found := false
		for _, member := range members {
			if member.ID == account.ID {
				found = true
			}
		}
		require.True(t, found)
		return nil
	}))
}
