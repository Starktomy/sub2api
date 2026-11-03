package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fencedTestMaintenanceSession struct {
	MaintenanceSession
	identity MaintenanceFence
}

func (s *fencedTestMaintenanceSession) Fence() MaintenanceFence { return s.identity }

type fencedTestMaintenanceLocker struct {
	base     *testMaintenanceLocker
	identity MaintenanceFence
}

func (l *fencedTestMaintenanceLocker) TryAcquire(ctx context.Context) (MaintenanceSession, bool, error) {
	s, ok, err := l.base.TryAcquire(ctx)
	if err != nil || !ok {
		return s, ok, err
	}
	return &fencedTestMaintenanceSession{MaintenanceSession: s, identity: l.identity}, true, nil
}
func TestMaintenanceCoordinatorPassesImmutableFence(t *testing.T) {
	identity := MaintenanceFence{Generation: 42, Owner: "fixture-owner"}
	c := fastMaintenance(&fencedTestMaintenanceLocker{base: &testMaintenanceLocker{}, identity: identity})
	seen := make(chan MaintenanceFence, 1)
	task := RuntimeTask{Name: "AccountExpiryService", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true,
		Start: func(ctx context.Context) error {
			f, ok := MaintenanceFenceFromContext(ctx)
			if !ok {
				seen <- MaintenanceFence{}
			} else {
				seen <- f
			}
			return nil
		},
		Stop: func(context.Context) error { return nil }}
	require.NoError(t, c.Start(context.Background(), []RuntimeTask{task}))
	defer func() { require.NoError(t, c.Stop(context.Background())) }()
	select {
	case got := <-seen:
		require.Equal(t, identity, got)
	case <-time.After(time.Second):
		t.Fatal("fenced task not started")
	}
}

func TestMaintenanceCoordinatorRejectsRequiredFenceOnLegacySession(t *testing.T) {
	c := fastMaintenance(&testMaintenanceLocker{})
	started := make(chan struct{}, 1)
	task := RuntimeTask{Name: "AccountExpiryService", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true, Start: func(context.Context) error { started <- struct{}{}; return nil }, Stop: func(context.Context) error { return nil }}
	require.NoError(t, c.Start(ContextWithMaintenanceFence(context.Background(), MaintenanceFence{Generation: 99, Owner: "inherited-owner"}), []RuntimeTask{task}))
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("legacy session was not rejected")
	}
	require.ErrorContains(t, c.Stop(context.Background()), "requires a durable generation")
	require.Empty(t, started)
}

func TestRuntimeLifecycleRejectsFenceTaskWithoutCoordinator(t *testing.T) {
	l := NewRuntimeLifecycle(nil)
	l.Register(RuntimeTask{Name: "fenced", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true, Start: func(context.Context) error { t.Fatal("unsafe start"); return nil }, Stop: func(context.Context) error { return nil }})
	require.ErrorContains(t, l.Start(context.Background()), "requires fenced maintenance coordination")
	require.NoError(t, l.Stop(context.Background()))
}
