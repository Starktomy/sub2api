package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRuntimeLifecycleRoleOwnership(t *testing.T) {
	for _, tc := range []struct {
		role string
		want []string
	}{
		{"", []string{"worker", "request", "gateway", "control", "process"}},
		{config.RuntimeRoleAll, []string{"worker", "request", "gateway", "control", "process"}},
		{config.RuntimeRoleGateway, []string{"request", "gateway", "process"}},
		{config.RuntimeRoleControl, []string{"request", "control", "process"}},
		{config.RuntimeRoleWorker, []string{"worker", "process"}},
	} {
		t.Run(tc.role, func(t *testing.T) {
			lifecycle := NewRuntimeLifecycle(&config.Config{Runtime: config.RuntimeConfig{Role: tc.role}})
			var starts, stops []string
			for _, task := range []struct {
				name  string
				scope RuntimeScope
			}{
				{"worker", RuntimeScopeWorker}, {"request", RuntimeScopeRequest},
				{"gateway", RuntimeScopeGateway}, {"control", RuntimeScopeControl},
				{"process", RuntimeScopeProcess},
			} {
				lifecycle.Register(RuntimeTask{
					Name: task.name, Scope: task.scope,
					Start: func(context.Context) error { starts = append(starts, task.name); return nil },
					Stop:  func(context.Context) error { stops = append(stops, task.name); return nil },
				})
			}
			require.Empty(t, starts, "constructing dependencies must not start their tasks")
			require.Empty(t, lifecycle.StartedTasks())
			require.True(t, lifecycle.ManagesTask("worker"))
			require.False(t, lifecycle.ManagesTask("unknown"))
			require.NoError(t, lifecycle.Start(context.Background()))
			require.Equal(t, tc.want, starts)
			require.Equal(t, tc.want, lifecycle.StartedTasks())
			require.NoError(t, lifecycle.Start(context.Background()), "start is idempotent while running")
			require.Equal(t, tc.want, starts)
			require.NoError(t, lifecycle.Stop(context.Background()))
			wantStops := make([]string, 0, len(tc.want))
			for i := len(tc.want) - 1; i >= 0; i-- {
				wantStops = append(wantStops, tc.want[i])
			}
			require.Equal(t, wantStops, stops)
			require.NoError(t, lifecycle.Stop(context.Background()))
			require.Equal(t, wantStops, stops, "stop is exactly once, even for non-idempotent legacy services")
			require.ErrorContains(t, lifecycle.Start(context.Background()), "cannot start")
		})
	}
}

func TestRuntimeLifecycleStopsProducersBeforeConsumersAndStores(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	var stopped []string
	for _, task := range []struct {
		name  string
		phase RuntimeStopPhase
	}{
		{"store", RuntimeStopStores}, {"consumer", RuntimeStopConsumers},
		{"producer", RuntimeStopProducers},
	} {
		lifecycle.Register(RuntimeTask{
			Name: task.name, Scope: RuntimeScopeProcess, StopPhase: task.phase,
			Start: func(context.Context) error { return nil },
			Stop:  func(context.Context) error { stopped = append(stopped, task.name); return nil },
		})
	}
	require.NoError(t, lifecycle.Start(context.Background()))
	require.NoError(t, lifecycle.Stop(context.Background()))
	require.Equal(t, []string{"producer", "consumer", "store"}, stopped)
}

func TestRuntimeLifecyclePartialStart(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	var calls []string
	for _, name := range []string{"first", "failure", "last"} {
		lifecycle.Register(RuntimeTask{
			Name: name, Scope: RuntimeScopeProcess,
			Start: func(context.Context) error {
				calls = append(calls, "start:"+name)
				if name == "failure" {
					return errors.New("cannot initialize")
				}
				return nil
			},
			Stop: func(context.Context) error { calls = append(calls, "stop:"+name); return nil },
		})
	}
	require.ErrorContains(t, lifecycle.Start(context.Background()), "start runtime task failure")
	require.Equal(t, []string{"first"}, lifecycle.StartedTasks())
	require.NoError(t, lifecycle.Stop(context.Background()))
	require.Equal(t, []string{"start:first", "start:failure", "stop:first"}, calls)
}

func TestRuntimeLifecycleStopContinuesAfterError(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	var stops []string
	for _, name := range []string{"first", "failure", "last"} {
		lifecycle.Register(RuntimeTask{
			Name: name, Scope: RuntimeScopeProcess,
			Start: func(context.Context) error { return nil },
			Stop: func(context.Context) error {
				stops = append(stops, name)
				if name == "failure" {
					return errors.New("flush unavailable")
				}
				return nil
			},
		})
	}
	require.NoError(t, lifecycle.Start(context.Background()))
	require.ErrorContains(t, lifecycle.Stop(context.Background()), "stop runtime task failure")
	require.Equal(t, []string{"last", "failure", "first"}, stops)
	require.NoError(t, lifecycle.Stop(context.Background()))
}

func TestRuntimeLifecycleRejectsInvalidWiringBeforeStart(t *testing.T) {
	var starts, stops int
	valid := RuntimeTask{
		Name: "valid", Scope: RuntimeScopeProcess,
		Start: func(context.Context) error { starts++; return nil },
		Stop:  func(context.Context) error { stops++; return nil },
	}
	for _, tc := range []struct {
		name string
		bad  RuntimeTask
	}{
		{"no name", RuntimeTask{Scope: RuntimeScopeProcess, Start: valid.Start, Stop: valid.Stop}},
		{"no start", RuntimeTask{Name: "no-start", Scope: RuntimeScopeProcess, Stop: valid.Stop}},
		{"no stop", RuntimeTask{Name: "no-stop", Scope: RuntimeScopeProcess, Start: valid.Start}},
		{"unknown scope", RuntimeTask{Name: "unknown", Scope: "typo", Start: valid.Start, Stop: valid.Stop}},
		{"duplicate", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle := NewRuntimeLifecycle(nil)
			lifecycle.Register(valid)
			lifecycle.Register(tc.bad)
			require.ErrorContains(t, lifecycle.Start(context.Background()), "invalid runtime lifecycle")
			require.NoError(t, lifecycle.Stop(context.Background()))
			require.Zero(t, starts, "invalid graphs must not start callbacks")
			require.Zero(t, stops, "unstarted callbacks must not stop")
		})
	}
	invalid := NewRuntimeLifecycle(&config.Config{Runtime: config.RuntimeConfig{Role: "typo"}})
	require.ErrorContains(t, invalid.Start(context.Background()), "runtime.role")
}

func TestRuntimeLifecycleCancelledStartAndStopBeforeStart(t *testing.T) {
	for _, cancelStart := range []bool{false, true} {
		lifecycle := NewRuntimeLifecycle(nil)
		lifecycle.Register(RuntimeTask{
			Name: "never", Scope: RuntimeScopeProcess,
			Start: func(context.Context) error { t.Error("must not start"); return nil },
			Stop:  func(context.Context) error { t.Error("must not stop an unstarted task"); return nil },
		})
		if cancelStart {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.ErrorIs(t, lifecycle.Start(ctx), context.Canceled)
		}
		require.NoError(t, lifecycle.Stop(context.Background()))
		require.Empty(t, lifecycle.StartedTasks())
	}
}

func TestRuntimeLifecycleRegistrationAfterStartIsNotSilentlyAccepted(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	require.NoError(t, lifecycle.Start(context.Background()))
	lifecycle.Register(RuntimeTask{
		Name: "late", Scope: RuntimeScopeProcess,
		Start: func(context.Context) error { t.Error("late registration must not start"); return nil },
		Stop:  func(context.Context) error { t.Error("late registration must not stop"); return nil },
	})
	require.ErrorContains(t, lifecycle.Start(context.Background()), "registered after startup")
	require.NoError(t, lifecycle.Stop(context.Background()))
}
