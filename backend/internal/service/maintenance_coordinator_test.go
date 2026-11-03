package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type testMaintenanceLocker struct {
	mu       sync.Mutex
	held     bool
	lost     atomic.Bool
	releases atomic.Int32
}
type testMaintenanceSession struct{ locker *testMaintenanceLocker }

func (l *testMaintenanceLocker) TryAcquire(context.Context) (MaintenanceSession, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return nil, false, nil
	}
	l.held = true
	return &testMaintenanceSession{l}, true, nil
}
func (s *testMaintenanceSession) Check(context.Context) error {
	if s.locker.lost.Load() {
		return errors.New("session disconnected")
	}
	return nil
}
func (s *testMaintenanceSession) Release(context.Context) error {
	s.locker.mu.Lock()
	defer s.locker.mu.Unlock()
	s.locker.held = false
	s.locker.releases.Add(1)
	return nil
}
func fastMaintenance(l MaintenanceLocker) *MaintenanceCoordinator {
	c := NewMaintenanceCoordinator(l)
	c.interval = time.Millisecond
	c.timeout = time.Second
	return c
}
func maintenanceTestTask(starts *atomic.Int32, stop func(context.Context) error) RuntimeTask {
	return RuntimeTask{Name: "AccountExpiryService", Scope: RuntimeScopeWorker, Start: func(context.Context) error { starts.Add(1); return nil }, Stop: stop}
}

func TestMaintenanceStandbyTakeoverAfterJoin(t *testing.T) {
	locker := &testMaintenanceLocker{}
	first, second := fastMaintenance(locker), fastMaintenance(locker)
	var a, b atomic.Int32
	entered, joined := make(chan struct{}), make(chan struct{})
	require.NoError(t, first.Start(context.Background(), []RuntimeTask{maintenanceTestTask(&a, func(context.Context) error { close(entered); <-joined; return nil })}))
	require.Eventually(t, func() bool { return a.Load() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, second.Start(context.Background(), []RuntimeTask{maintenanceTestTask(&b, func(context.Context) error { return nil })}))
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, first.Stop(deadline), context.DeadlineExceeded)
	<-entered
	require.Zero(t, b.Load(), "standby cannot start while old callbacks have not joined")
	require.Zero(t, locker.releases.Load())
	close(joined)
	require.Eventually(t, func() bool { return b.Load() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, first.Stop(context.Background()))
	require.NoError(t, second.Stop(context.Background()))
}

func TestMaintenanceLostSessionCancelsAndDoesNotRestart(t *testing.T) {
	locker := &testMaintenanceLocker{}
	c := fastMaintenance(locker)
	var starts atomic.Int32
	cancelled := make(chan struct{})
	task := maintenanceTestTask(&starts, func(context.Context) error { return nil })
	task.Start = func(ctx context.Context) error {
		starts.Add(1)
		go func() { <-ctx.Done(); close(cancelled) }()
		return nil
	}
	require.NoError(t, c.Start(context.Background(), []RuntimeTask{task}))
	require.Eventually(t, func() bool { return starts.Load() == 1 }, time.Second, time.Millisecond)
	locker.lost.Store(true)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("leadership loss did not cancel work")
	}
	require.ErrorContains(t, c.Stop(context.Background()), "session lost")
	require.EqualValues(t, 1, starts.Load())
	require.ErrorContains(t, c.Start(context.Background(), nil), "single-use")
}

func TestMaintenanceFailedJoinRetainsSession(t *testing.T) {
	locker := &testMaintenanceLocker{}
	c := fastMaintenance(locker)
	var starts atomic.Int32
	require.NoError(t, c.Start(context.Background(), []RuntimeTask{maintenanceTestTask(&starts, func(context.Context) error { return errors.New("not joined") })}))
	require.Eventually(t, func() bool { return starts.Load() == 1 }, time.Second, time.Millisecond)
	require.ErrorContains(t, c.Stop(context.Background()), "not joined")
	require.Zero(t, locker.releases.Load(), "no voluntary takeover when Stop cannot prove quiescence")
}

func TestRuntimeMultiReplicaGate(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(&config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker, MultiReplicaWorkers: true, EnableSplitRoles: true}})
	var safe, unsafe atomic.Int32
	lifecycle.Register(maintenanceTestTask(&safe, func(context.Context) error { return nil }))
	blocked := maintenanceTestTask(&unsafe, func(context.Context) error { return nil })
	blocked.Name = "BackupService"
	lifecycle.Register(blocked)
	require.ErrorContains(t, lifecycle.Start(context.Background()), "require PostgreSQL")
	require.Zero(t, safe.Load())
	require.Zero(t, unsafe.Load())
	lifecycle = NewRuntimeLifecycle(&config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker, MultiReplicaWorkers: true, EnableSplitRoles: true}})
	lifecycle.SetMaintenanceCoordinator(fastMaintenance(&testMaintenanceLocker{}))
	lifecycle.Register(maintenanceTestTask(&safe, func(context.Context) error { return nil }))
	lifecycle.Register(blocked)
	require.NoError(t, lifecycle.Start(context.Background()))
	require.Eventually(t, func() bool { return safe.Load() == 1 }, time.Second, time.Millisecond)
	require.Zero(t, unsafe.Load())
	require.NoError(t, lifecycle.Stop(context.Background()))
}

func TestMaintenancePolicyFailsClosed(t *testing.T) {
	require.Empty(t, MaintenanceTaskPolicy("AccountExpiryService"))
	require.NotEmpty(t, MaintenanceTaskPolicy("NewUnreviewedTask"))
	require.ErrorContains(t, fastMaintenance(&testMaintenanceLocker{}).Start(context.Background(), []RuntimeTask{{Name: "BackupService"}}), "unsafe maintenance task")
}
