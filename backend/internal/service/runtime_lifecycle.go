package service

import (
	"context"
	"errors"
	"fmt"
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

// RuntimeTask is registered while constructing dependencies and started only
// after the complete graph exists. Start must clean up its own partial work if
// it returns an error. Stop is called only for a successfully started task.
// Callbacks must honor context where supported; this registry cannot forcibly
// cancel legacy Stop methods that ignore it.
type RuntimeTask struct {
	Name  string
	Scope RuntimeScope
	Start func(context.Context) error
	Stop  func(context.Context) error
}

// RuntimeLifecycle makes task ownership explicit without a second scheduler.
// It is single-use: once stopped or a start fails, it cannot be restarted.
type RuntimeLifecycle struct {
	operation sync.Mutex
	mu        sync.Mutex
	role      string
	tasks     []RuntimeTask
	started   []RuntimeTask
	state     string
	configErr error
}

func NewRuntimeLifecycle(cfg *config.Config) *RuntimeLifecycle {
	runtime := config.RuntimeConfig{}
	if cfg != nil {
		runtime = cfg.Runtime
	}
	err := runtime.Validate()
	return &RuntimeLifecycle{role: runtime.Role, state: "new", configErr: err}
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
	if strings.TrimSpace(task.Name) == "" || task.Start == nil || task.Stop == nil {
		l.configErr = errors.Join(l.configErr, fmt.Errorf("runtime task %q requires a name, Start and Stop", task.Name))
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

	for _, task := range tasks {
		if !runtimeScopeEnabled(l.role, task.Scope) {
			continue
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
	for i := len(started) - 1; i >= 0; i-- {
		if err := started[i].Stop(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("stop runtime task %s: %w", started[i].Name, err))
		}
	}
	l.setState("stopped")
	return result
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
}) {
	lifecycle.Register(RuntimeTask{
		Name: name, Scope: scope,
		Start: func(context.Context) error { svc.Start(); return nil },
		Stop:  func(context.Context) error { svc.Stop(); return nil },
	})
}
