package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type publicationTestCache struct {
	SchedulerCache
	mutations int
}

func (*publicationTestCache) ReadOnlyPublicationCache() {}
func (*publicationTestCache) BindPublisher(MaintenanceSession) (SchedulerPublicationPublisher, error) {
	return nil, errors.New("unused")
}
func (*publicationTestCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, errors.New("missing immutable publication")
}
func (*publicationTestCache) GetAccount(context.Context, int64) (*Account, error) {
	return nil, errors.New("missing immutable publication")
}
func (c *publicationTestCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (SchedulerBucketLease, bool, error) {
	c.mutations++
	return SchedulerBucketLease{}, false, nil
}
func (c *publicationTestCache) SetAccount(context.Context, *Account) error { c.mutations++; return nil }

type publicationTestAccounts struct {
	AccountRepository
	loads int
}

func (r *publicationTestAccounts) GetByID(context.Context, int64) (*Account, error) {
	r.loads++
	return &Account{ID: 1, Name: "db"}, nil
}
func (r *publicationTestAccounts) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return []Account{{ID: 1, Name: "db"}}, nil
}
func (r *publicationTestAccounts) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	r.loads++
	return []Account{{ID: 1, Name: "db"}}, nil
}
func (r *publicationTestAccounts) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	r.loads++
	return []Account{{ID: 1, Name: "db"}}, nil
}

type publicationTestGroups struct{ GroupRepository }

func (*publicationTestGroups) ListActiveIDs(context.Context) ([]int64, error) { return nil, nil }

type publicationTestPublisher struct {
	SchedulerOutboxRepository
	stages      int
	activations int
	ids         []int64
	data        SchedulerPublicationData
}

func (*publicationTestPublisher) ListPendingAndReleaseDedup(context.Context, int) ([]SchedulerOutboxEvent, error) {
	return nil, nil
}
func (p *publicationTestPublisher) Stage(_ context.Context, data SchedulerPublicationData) (SchedulerPublication, error) {
	p.stages++
	p.data = data
	return SchedulerPublication{Generation: 1, ID: "fresh"}, nil
}
func (p *publicationTestPublisher) Activate(_ context.Context, _ SchedulerPublication, ids []int64) error {
	p.activations++
	p.ids = ids
	return nil
}

func TestSchedulerPublicationGatewayFallbackReadOnly(t *testing.T) {
	cache := &publicationTestCache{}
	repo := &publicationTestAccounts{}
	s := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	t.Cleanup(s.Stop)
	accounts, _, err := s.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "db", accounts[0].Name)
	account, err := s.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "db", account.Name)
	require.NoError(t, s.UpdateAccountInCache(context.Background(), account))
	require.Equal(t, 0, cache.mutations)
	require.Equal(t, 2, repo.loads)
}
func TestSchedulerPublicationEmptyOutboxCycleRebuilds(t *testing.T) {
	repo := &publicationTestAccounts{}
	s := NewSchedulerSnapshotService(&publicationTestCache{}, nil, repo, &publicationTestGroups{}, nil)
	t.Cleanup(s.Stop)
	publisher := &publicationTestPublisher{}
	require.NoError(t, s.RunPublicationCycle(context.Background(), publisher))
	require.Equal(t, 1, publisher.stages)
	require.Equal(t, 1, publisher.activations)
	require.Empty(t, publisher.ids)
	require.Len(t, publisher.data.Buckets, len(schedulerCanonicalBuckets(0)))
	require.NoError(t, s.RunPublicationCycle(context.Background(), publisher))
	require.Equal(t, 2, publisher.stages)
	require.Equal(t, 2, publisher.activations)
}
func TestSchedulerPublicationFactoryUsesAcquiredSession(t *testing.T) {
	locker := &fencedTestMaintenanceLocker{base: &testMaintenanceLocker{}, identity: MaintenanceFence{Generation: 3, Owner: "test"}}
	c := fastMaintenance(locker)
	bound := make(chan MaintenanceSession, 1)
	stopped := make(chan struct{})
	task := RuntimeTask{Name: "SchedulerPublicationService", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true,
		AcquiredFactory: func(s MaintenanceSession) (RuntimeTask, error) {
			bound <- s
			return RuntimeTask{Name: "SchedulerPublicationService", RequiresMaintenanceFence: true, Start: func(context.Context) error { return nil }, Stop: func(context.Context) error { close(stopped); return nil }}, nil
		}}
	require.NoError(t, c.Start(context.Background(), []RuntimeTask{task}))
	select {
	case session := <-bound:
		fenced, ok := session.(FencedMaintenanceSession)
		require.True(t, ok)
		require.Equal(t, locker.identity, fenced.Fence())
	case <-time.After(time.Second):
		t.Fatal("factory not invoked")
	}
	require.NoError(t, c.Stop(context.Background()))
	select {
	case <-stopped:
	default:
		t.Fatal("bound worker not joined")
	}
}

func TestSchedulerPublicationFactoryRegistration(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	lifecycle.Register(RuntimeTask{Name: "SchedulerPublicationService", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true, AcquiredFactory: func(MaintenanceSession) (RuntimeTask, error) { return RuntimeTask{}, nil }})
	require.NoError(t, lifecycle.configErr)
	require.Len(t, lifecycle.tasks, 1)
	require.ErrorContains(t, lifecycle.Start(context.Background()), "requires fenced maintenance coordination")
	require.NoError(t, lifecycle.Stop(context.Background()))
}

func TestSchedulerPublicationWireRegistersOnlyFencedFactory(t *testing.T) {
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker, EnableSplitRoles: true, MultiReplicaWorkers: true, SchedulerPublications: true}}
	lifecycle := NewRuntimeLifecycle(cfg)
	svc := ProvideSchedulerSnapshotService(lifecycle, &publicationTestCache{}, nil, &publicationTestAccounts{}, &publicationTestGroups{}, cfg)
	t.Cleanup(svc.Stop)
	require.NoError(t, lifecycle.configErr)
	require.Len(t, lifecycle.tasks, 1)
	task := lifecycle.tasks[0]
	require.Equal(t, "SchedulerPublicationService", task.Name)
	require.True(t, task.RequiresMaintenanceFence)
	require.NotNil(t, task.AcquiredFactory)
	require.Nil(t, task.Start)
	require.Empty(t, maintenanceRuntimeTaskPolicy(task))
	// A name alone must never enable the legacy worker.
	require.NotEmpty(t, MaintenanceTaskPolicy("SchedulerPublicationService"))
	require.NotEmpty(t, MaintenanceTaskPolicy("SchedulerSnapshotService"))
}

func (r *publicationTestAccounts) WithSchedulerSnapshot(ctx context.Context, load func(AccountRepository, GroupRepository) error) error {
	return load(r, &publicationTestGroups{})
}
func (*publicationTestPublisher) Healthy(context.Context) (bool, error) { return false, nil }
func (*publicationTestPublisher) Collect(context.Context) error         { return nil }
