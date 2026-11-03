package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// RuntimeScope describes who owns a background task. Request-local tasks may
// drain a process-local queue; moving them to a remote worker would lose work.
// Cluster maintenance belongs to workers, even when the legacy all role runs it.
type RuntimeScope string

const (
	RuntimeScopeWorker  RuntimeScope = "worker"
	RuntimeScopeRequest RuntimeScope = "request-local"
	RuntimeScopeGateway RuntimeScope = "gateway-local"
	RuntimeScopeControl RuntimeScope = "control-local"
	RuntimeScopeProcess RuntimeScope = "process-local"
)

// RuntimeStopPhase orders producers before local consumers and their stores.
type RuntimeStopPhase int

const (
	RuntimeStopProducers RuntimeStopPhase = iota
	RuntimeStopConsumers
	RuntimeStopStores
)

// RuntimeTask is registered during construction and started after the graph is
// complete. Start must clean up its own partial work on error; Stop is called
// only after a successful Start. Legacy Stop methods may ignore context.
type RuntimeTask struct {
	// AcquiredFactory binds a new task to exactly the session acquired by the coordinator.
	AcquiredFactory func(MaintenanceSession) (RuntimeTask, error)
	Name            string
	// RequiresMaintenanceFence is reserved for generation-aware tasks. It must
	// never silently execute under a legacy session or without the coordinator.
	RequiresMaintenanceFence bool
	Scope                    RuntimeScope
	StopPhase                RuntimeStopPhase
	Start                    func(context.Context) error
	Stop                     func(context.Context) error
}

// RuntimeLifecycle makes task ownership explicit without a second scheduler.
// It is single-use: once stopped or a start fails, it cannot be restarted.
type RuntimeLifecycle struct {
	operation    sync.Mutex
	mu           sync.Mutex
	role         string
	tasks        []RuntimeTask
	started      []RuntimeTask
	state        string
	configErr    error
	multiReplica bool
	coordinator  *MaintenanceCoordinator
}

func NewRuntimeLifecycle(cfg *config.Config) *RuntimeLifecycle {
	runtime := config.RuntimeConfig{}
	if cfg != nil {
		runtime = cfg.Runtime
	}
	err := runtime.Validate()
	return &RuntimeLifecycle{role: runtime.Role, state: "new", configErr: err, multiReplica: runtime.MultiReplicaWorkers}
}

// Register does not execute callbacks. Invalid/duplicate registrations are
// reported by Start, so providers do not hide wiring errors or start partial
// graphs. Registration after startup is rejected in the same way.
func (l *RuntimeLifecycle) Register(task RuntimeTask) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != "new" {
		l.configErr = errors.Join(l.configErr, fmt.Errorf("runtime task %q registered after startup", task.Name))
		return
	}
	if strings.TrimSpace(task.Name) == "" || (task.AcquiredFactory == nil && (task.Start == nil || task.Stop == nil)) || (task.AcquiredFactory != nil && (!task.RequiresMaintenanceFence || task.Start != nil || task.Stop != nil)) {
		l.configErr = errors.Join(l.configErr, fmt.Errorf("runtime task %q requires a name, Start and Stop", task.Name))
		return
	}
	if task.StopPhase < RuntimeStopProducers || task.StopPhase > RuntimeStopStores {
		l.configErr = errors.Join(l.configErr, fmt.Errorf("runtime task %q has invalid stop phase", task.Name))
		return
	}
	if !validRuntimeScope(task.Scope) {
		l.configErr = errors.Join(l.configErr, fmt.Errorf("runtime task %q has invalid scope %q", task.Name, task.Scope))
		return
	}
	for _, existing := range l.tasks {
		if existing.Name == task.Name {
			l.configErr = errors.Join(l.configErr, fmt.Errorf("duplicate runtime task %q", task.Name))
			return
		}
	}
	l.tasks = append(l.tasks, task)
}

func (l *RuntimeLifecycle) Start(ctx context.Context) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	l.mu.Lock()
	if l.configErr != nil {
		err := l.configErr
		l.mu.Unlock()
		return fmt.Errorf("invalid runtime lifecycle: %w", err)
	}
	if l.state == "running" {
		l.mu.Unlock()
		return nil
	}
	if l.state != "new" {
		state := l.state
		l.mu.Unlock()
		return fmt.Errorf("runtime lifecycle cannot start from %s", state)
	}
	l.state = "starting"
	tasks := append([]RuntimeTask(nil), l.tasks...)
	l.mu.Unlock()

	if l.multiReplica && l.OwnsScope(RuntimeScopeWorker) && l.coordinator == nil {
		l.setState("failed")
		return errors.New("multi-replica workers require PostgreSQL maintenance coordinator")
	}
	var maintenance []RuntimeTask
	for _, task := range tasks {
		if l.multiReplica && task.Scope == RuntimeScopeWorker && l.OwnsScope(RuntimeScopeWorker) {
			if reason := maintenanceRuntimeTaskPolicy(task); reason != "" {
				slog.Warn("maintenance task disabled in multi-replica mode", "task", task.Name, "reason", reason)
			} else {
				maintenance = append(maintenance, task)
				slog.Info("maintenance task admitted for PostgreSQL coordination", "task", task.Name)
			}
			continue
		}
		if !runtimeScopeEnabled(l.role, task.Scope) {
			continue
		}
		if task.RequiresMaintenanceFence {
			l.setState("failed")
			return fmt.Errorf("runtime task %s requires fenced maintenance coordination", task.Name)
		}
		if err := ctx.Err(); err != nil {
			l.setState("failed")
			return err
		}
		if err := task.Start(ctx); err != nil {
			l.setState("failed")
			return fmt.Errorf("start runtime task %s: %w", task.Name, err)
		}
		l.mu.Lock()
		l.started = append(l.started, task)
		l.mu.Unlock()
	}
	if l.multiReplica && l.OwnsScope(RuntimeScopeWorker) {
		if err := l.coordinator.Start(ctx, maintenance); err != nil {
			l.setState("failed")
			return err
		}
		l.mu.Lock()
		l.started = append(l.started, RuntimeTask{Name: "MaintenanceCoordinator", Scope: RuntimeScopeWorker, Stop: l.coordinator.Stop})
		l.mu.Unlock()
	}
	l.setState("running")
	return nil
}

// Stop unwinds successful starts in reverse dependency order. All stop callbacks
// are attempted even if one fails. Repeated Stop calls are harmless. It also
// unwinds a partial startup; callers must call it when Start returns an error.
func (l *RuntimeLifecycle) Stop(ctx context.Context) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	l.mu.Lock()
	if l.state == "stopped" {
		l.mu.Unlock()
		return nil
	}
	l.state = "stopping"
	started := append([]RuntimeTask(nil), l.started...)
	l.mu.Unlock()

	var result error
	// Stop producers/maintenance before local consumers and their stores. Within
	// each phase preserve reverse dependency order. A shared graph can register
	// later maintenance after its sinks; reverse order alone is insufficient.
	for phase := RuntimeStopProducers; phase <= RuntimeStopStores; phase++ {
		for i := len(started) - 1; i >= 0; i-- {
			if started[i].StopPhase != phase {
				continue
			}
			if err := started[i].Stop(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("stop runtime task %s: %w", started[i].Name, err))
			}
		}
	}
	l.setState("stopped")
	return result
}

// OwnsScope reports role ownership without starting anything. Providers use it
// to avoid installing local callbacks that could bypass deferred task startup.
func (l *RuntimeLifecycle) OwnsScope(scope RuntimeScope) bool {
	return runtimeScopeEnabled(l.role, scope)
}

// ManagesTask distinguishes deferred tasks from legacy eager resources during
// the transitional cleanup. Unstarted registered tasks must not be stopped by
// the old cleanup list, and successfully stopped tasks must not be stopped twice.
func (l *RuntimeLifecycle) ManagesTask(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, task := range l.tasks {
		if task.Name == name {
			return true
		}
	}
	return false
}

// StartedTasks is a non-secret snapshot for diagnostics and role contract tests.
func (l *RuntimeLifecycle) StartedTasks() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, 0, len(l.started))
	for _, task := range l.started {
		names = append(names, task.Name)
	}
	return names
}

func (l *RuntimeLifecycle) setState(state string) {
	l.mu.Lock()
	l.state = state
	l.mu.Unlock()
}

func validRuntimeScope(scope RuntimeScope) bool {
	switch scope {
	case RuntimeScopeWorker, RuntimeScopeRequest, RuntimeScopeGateway, RuntimeScopeControl, RuntimeScopeProcess:
		return true
	default:
		return false
	}
}

func runtimeScopeEnabled(role string, scope RuntimeScope) bool {
	if !validRuntimeScope(scope) {
		return false
	}
	switch role {
	case config.RuntimeRoleAll:
		return true
	case config.RuntimeRoleGateway:
		return scope == RuntimeScopeGateway || scope == RuntimeScopeRequest || scope == RuntimeScopeProcess
	case config.RuntimeRoleControl:
		return scope == RuntimeScopeControl || scope == RuntimeScopeRequest || scope == RuntimeScopeProcess
	case config.RuntimeRoleWorker:
		return scope == RuntimeScopeWorker || scope == RuntimeScopeProcess
	default:
		return false
	}
}

// registerRuntimeService preserves providers' concrete service types while
// deferring Start/Stop to the owning role. Dependencies must be registered first.
func registerRuntimeService(lifecycle *RuntimeLifecycle, name string, scope RuntimeScope, svc interface {
	Start()
	Stop()
}, stopPhase ...RuntimeStopPhase) {
	phase := RuntimeStopProducers
	if len(stopPhase) > 0 {
		phase = stopPhase[0]
	}
	lifecycle.Register(RuntimeTask{
		Name: name, Scope: scope, StopPhase: phase,
		Start: func(ctx context.Context) error {
			if contextual, ok := svc.(interface{ StartContext(context.Context) error }); ok {
				return contextual.StartContext(ctx)
			}
			if contextual, ok := svc.(interface{ StartContext(context.Context) }); ok {
				contextual.StartContext(ctx)
			} else {
				svc.Start()
			}
			return nil
		},
		Stop: func(ctx context.Context) error {
			if contextual, ok := svc.(interface{ StopContext(context.Context) error }); ok {
				return contextual.StopContext(ctx)
			}
			svc.Stop()
			return nil
		},
	})
}

// SetMaintenanceCoordinator installs the shared worker election before startup.
// Constructors and this setter never acquire locks or launch goroutines.
func (l *RuntimeLifecycle) SetMaintenanceCoordinator(coordinator *MaintenanceCoordinator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != "new" {
		l.configErr = errors.Join(l.configErr, errors.New("maintenance coordinator installed after startup"))
		return
	}
	l.coordinator = coordinator
}
