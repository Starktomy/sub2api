//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubMonitorSvc 实现 monitorRunnerSvc，用于隔离 runner 与真实 service/repo。
type stubMonitorSvc struct {
	enabled    []*ChannelMonitor
	runCount   atomic.Int64
	runCalled  chan int64 // 每次 RunCheck 触发时 push 一次（缓冲足够大避免阻塞）
	runErr     error
	listErr    error
	list       func(context.Context) ([]*ChannelMonitor, error)
	run        func(context.Context, int64) ([]*CheckResult, error)
	runHoldFor time.Duration // RunCheck 内额外阻塞的时长，用来测试 Stop 等待行为
}

func (s *stubMonitorSvc) ListEnabledMonitors(ctx context.Context) ([]*ChannelMonitor, error) {
	if s.list != nil {
		return s.list(ctx)
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.enabled, nil
}

func (s *stubMonitorSvc) RunCheck(ctx context.Context, id int64) ([]*CheckResult, error) {
	s.runCount.Add(1)
	if s.runCalled != nil {
		select {
		case s.runCalled <- id:
		default:
		}
	}
	if s.run != nil {
		return s.run(ctx, id)
	}
	if s.runHoldFor > 0 {
		select {
		case <-time.After(s.runHoldFor):
		case <-ctx.Done():
		}
	}
	return nil, s.runErr
}

func newRunnerForTest(svc monitorRunnerSvc) *ChannelMonitorRunner {
	r := newChannelMonitorRunner(svc, nil)
	// 测试通过手动 ReconcileOnce 或显式 tick 驱动，不等待生产 30 秒轮询。
	r.reconcileTicks = make(chan time.Time)
	return r
}

// 等待 condition 在 timeout 内变 true，否则 t.Fatalf。轮询 5ms 一次。
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("waitFor timed out: %s", msg)
	}
}

func runnerTaskCount(r *ChannelMonitorRunner) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

func runnerTaskPtr(r *ChannelMonitorRunner, id int64) *scheduledMonitor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[id]
}

// TestSchedule_AddsTaskAndFiresOnce 验证 Schedule 后立即触发一次首检测，并把任务记入 tasks 表。
func TestSchedule_AddsTaskAndFiresOnce(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start() // svc.enabled 为空，Start 立即完成

	r.Schedule(&ChannelMonitor{ID: 1, Name: "m1", Enabled: true, IntervalSeconds: 60})

	if got := runnerTaskCount(r); got != 1 {
		t.Fatalf("expected 1 scheduled task, got %d", got)
	}

	select {
	case id := <-svc.runCalled:
		if id != 1 {
			t.Fatalf("expected first fire for id=1, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected immediate first fire within 2s")
	}

	r.Stop()
}

// TestSchedule_ReplaceCancelsOldTask 验证对同一 id 二次 Schedule 会替换旧 task 实例。
// （旧 goroutine 通过 ctx 取消退出；这里以 task 指针不同 + Stop 不超时作为证据。）
func TestSchedule_ReplaceCancelsOldTask(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 8)}
	r := newRunnerForTest(svc)
	r.Start()

	m := &ChannelMonitor{ID: 7, Name: "m7", Enabled: true, IntervalSeconds: 60}
	r.Schedule(m)
	first := runnerTaskPtr(r, 7)
	if first == nil {
		t.Fatal("first schedule did not register task")
	}

	r.Schedule(m)
	second := runnerTaskPtr(r, 7)
	if second == nil {
		t.Fatal("second schedule did not register task")
	}
	if first == second {
		t.Fatal("re-Schedule should create a new scheduledMonitor instance")
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestUnschedule_RemovesTask 验证 Unschedule 删除 task 并使对应 goroutine 退出。
func TestUnschedule_RemovesTask(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 3, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Unschedule(3)
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after Unschedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestSchedule_DisabledRedirectsToUnschedule 验证 Enabled=false 等同于 Unschedule。
func TestSchedule_DisabledRedirectsToUnschedule(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 9, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Schedule(&ChannelMonitor{ID: 9, Enabled: false, IntervalSeconds: 60})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after disabled re-Schedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestSchedule_DecryptFailedRedirectsToUnschedule(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 10, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Schedule(&ChannelMonitor{ID: 10, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after decrypt-failed re-Schedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestSchedule_RepairedAPIKeyCanBeScheduled(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 1)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 11, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for decrypt-failed monitor, got %d", got)
	}

	r.Schedule(&ChannelMonitor{ID: 11, Enabled: true, IntervalSeconds: 60, APIKey: "replacement-key"})
	if got := runnerTaskCount(r); got != 1 {
		t.Fatalf("expected repaired monitor to be scheduled, got %d tasks", got)
	}
	select {
	case id := <-svc.runCalled:
		if id != 11 {
			t.Fatalf("expected repaired monitor id=11 to fire, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected repaired monitor to fire immediately")
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestRunOne_DecryptFailureUnschedulesTask(t *testing.T) {
	svc := &stubMonitorSvc{
		runCalled: make(chan int64, 1),
		runErr:    ErrChannelMonitorAPIKeyDecryptFailed,
	}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 12, Enabled: true, IntervalSeconds: 60})
	select {
	case id := <-svc.runCalled:
		if id != 12 {
			t.Fatalf("expected failing monitor id=12 to fire, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected failing monitor to fire immediately")
	}
	waitFor(t, time.Second, "decrypt-failed task unscheduled", func() bool { return runnerTaskCount(r) == 0 })

	stoppedWithin(t, r, 3*time.Second)
}

// TestSchedule_InvalidIntervalSkipped 验证 IntervalSeconds<=0 不会注册任务（防御性检查）。
func TestSchedule_InvalidIntervalSkipped(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 0})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for invalid interval, got %d", got)
	}
	r.Stop()
}

// TestSchedule_BeforeStartIsNoOp 验证 Start 之前调用 Schedule 不会注册任务。
func TestSchedule_BeforeStartIsNoOp(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	// 故意不调用 Start

	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task before Start, got %d", got)
	}
	r.Stop()
}

// TestStart_LoadsAllEnabledMonitors 验证 Start 会为 ListEnabledMonitors 返回的每条记录建立任务。
func TestStart_LoadsAllEnabledMonitors(t *testing.T) {
	svc := &stubMonitorSvc{
		enabled: []*ChannelMonitor{
			{ID: 1, Enabled: true, IntervalSeconds: 60},
			{ID: 2, Enabled: true, IntervalSeconds: 60},
			{ID: 3, Enabled: true, IntervalSeconds: 60},
		},
	}
	r := newRunnerForTest(svc)
	r.Start()
	waitFor(t, 2*time.Second, "all 3 tasks scheduled", func() bool { return runnerTaskCount(r) == 3 })

	stoppedWithin(t, r, 3*time.Second)
}

func TestStart_SkipsDecryptFailedMonitor(t *testing.T) {
	svc := &stubMonitorSvc{
		enabled: []*ChannelMonitor{
			{ID: 4, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true},
		},
	}
	r := newRunnerForTest(svc)
	r.Start()

	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for decrypt-failed startup monitor, got %d", got)
	}
	if got := svc.runCount.Load(); got != 0 {
		t.Fatalf("expected decrypt-failed startup monitor not to run, got %d calls", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestStop_DrainsAllGoroutines 验证 Stop 会等待所有调度 goroutine 退出（无游离）。
func TestStop_DrainsAllGoroutines(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	r.Start()

	for id := int64(1); id <= 5; id++ {
		r.Schedule(&ChannelMonitor{ID: id, Enabled: true, IntervalSeconds: 60})
	}
	waitFor(t, 2*time.Second, "5 tasks scheduled", func() bool { return runnerTaskCount(r) == 5 })

	stoppedWithin(t, r, 3*time.Second)
}

// TestStop_WaitsForInFlightCheck 验证取消传播，且所有并发 Stop 都等在飞任务退出。
func TestStop_WaitsForInFlightCheck(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCheck := func() { releaseOnce.Do(func() { close(release) }) }
	svc := &stubMonitorSvc{run: func(ctx context.Context, _ int64) ([]*CheckResult, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	}}
	r := newRunnerForTest(svc)
	t.Cleanup(r.Stop)
	t.Cleanup(releaseCheck)
	r.Start()
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})
	awaitMonitorSignal(t, entered, "check entered")

	firstDone := make(chan struct{})
	go func() { r.Stop(); close(firstDone) }()
	awaitMonitorSignal(t, cancelled, "check received cancellation")
	secondDone := make(chan struct{})
	go func() { r.Stop(); close(secondDone) }()
	for _, done := range []<-chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
			t.Fatal("Stop returned before in-flight check exited")
		default:
		}
	}
	releaseCheck()
	awaitMonitorSignal(t, firstDone, "first Stop drained")
	awaitMonitorSignal(t, secondDone, "second Stop drained")
}

// TestInFlight_PoolFullReleasesSlot 直接驱动 fire 路径，模拟 pool.TrySubmit 失败时 inFlight 必须释放。
// 用一个小型 stub pool 替换 r.pool 不便（pond.Pool 是接口但 mock 麻烦），
// 改为：占满 inFlight 后直接 fire，验证不会在 inFlight 空槽时永久卡住。
func TestInFlight_AcquireReleaseSymmetric(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)

	if !r.tryAcquireInFlight(42) {
		t.Fatal("first acquire should succeed")
	}
	if r.tryAcquireInFlight(42) {
		t.Fatal("second acquire (no release) must fail")
	}
	r.releaseInFlight(42)
	if !r.tryAcquireInFlight(42) {
		t.Fatal("acquire after release should succeed")
	}
	r.releaseInFlight(42)
	r.Stop()
}

// monitorPersistentState 模拟两个独立实例共用的 PG 持久化状态，不持有 scheduler/callback。
// 每次 list/get 都返回新快照，避免靠共享 ChannelMonitor 指针实现“传播”。
type monitorPersistentState struct {
	mu       sync.Mutex
	monitors map[int64]ChannelMonitor
	listErr  error
}

func (s *monitorPersistentState) put(m ChannelMonitor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.monitors == nil {
		s.monitors = make(map[int64]ChannelMonitor)
	}
	s.monitors[m.ID] = m
}

func (s *monitorPersistentState) remove(id int64) {
	s.mu.Lock()
	delete(s.monitors, id)
	s.mu.Unlock()
}

func (s *monitorPersistentState) snapshot(ctx context.Context) ([]*ChannelMonitor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var result []*ChannelMonitor
	for _, m := range s.monitors {
		if m.Enabled {
			copy := m
			result = append(result, &copy)
		}
	}
	return result, nil
}

func (s *monitorPersistentState) get(ctx context.Context, id int64) (*ChannelMonitor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.monitors[id]
	if !ok {
		return nil, errors.New("monitor deleted")
	}
	return &m, nil
}

func awaitMonitorSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: %s", message)
	}
}

func reconcileMonitorRunner(t *testing.T, r *ChannelMonitorRunner) {
	t.Helper()
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
}

func TestReconcileOnce_IndependentRunnersReadPersistentState(t *testing.T) {
	state := &monitorPersistentState{}
	// 这两个 service/runner 不共用回调，只有持久化状态共享。
	firstSvc := &stubMonitorSvc{list: state.snapshot}
	secondSvc := &stubMonitorSvc{list: state.snapshot}
	first := newRunnerForTest(firstSvc)
	second := newRunnerForTest(secondSvc)
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)
	first.Start()
	second.Start()

	m := ChannelMonitor{ID: 1, Name: "one", Enabled: true, IntervalSeconds: 60}
	state.put(m)
	reconcileMonitorRunner(t, first)
	if runnerTaskCount(second) != 0 {
		t.Fatal("independent runner changed without reading persistence")
	}
	reconcileMonitorRunner(t, second)
	for _, r := range []*ChannelMonitorRunner{first, second} {
		if task := runnerTaskPtr(r, 1); task == nil || task.interval != time.Minute {
			t.Fatal("persisted create did not schedule monitor")
		}
	}
	original := runnerTaskPtr(second, 1)
	reconcileMonitorRunner(t, second)
	if runnerTaskPtr(second, 1) != original {
		t.Fatal("unchanged snapshot restarted task")
	}

	m.IntervalSeconds = 120
	state.put(m)
	reconcileMonitorRunner(t, second)
	intervalTask := runnerTaskPtr(second, 1)
	if intervalTask == original || intervalTask.interval != 2*time.Minute {
		t.Fatal("interval update did not replace task")
	}
	if runnerTaskPtr(first, 1).interval != time.Minute {
		t.Fatal("first runner changed through a shared callback or pointer")
	}
	m.JitterSeconds = 10
	state.put(m)
	reconcileMonitorRunner(t, second)
	jitterTask := runnerTaskPtr(second, 1)
	if jitterTask == intervalTask || jitterTask.jitter != 10*time.Second {
		t.Fatal("jitter update did not replace task")
	}
	m.Name = "renamed"
	state.put(m)
	reconcileMonitorRunner(t, second)
	if task := runnerTaskPtr(second, 1); task == jitterTask || task.name != "renamed" {
		t.Fatal("name update did not converge")
	}

	m.Enabled = false
	state.put(m)
	reconcileMonitorRunner(t, first)
	reconcileMonitorRunner(t, second)
	if runnerTaskCount(first) != 0 || runnerTaskCount(second) != 0 {
		t.Fatal("disable did not remove tasks")
	}
	m.Enabled = true
	state.put(m)
	reconcileMonitorRunner(t, first)
	reconcileMonitorRunner(t, second)
	if runnerTaskCount(first) != 1 || runnerTaskCount(second) != 1 {
		t.Fatal("re-enable did not restore tasks")
	}
	state.remove(1)
	reconcileMonitorRunner(t, first)
	reconcileMonitorRunner(t, second)
	if runnerTaskCount(first) != 0 || runnerTaskCount(second) != 0 {
		t.Fatal("delete did not remove tasks")
	}
}

func TestReconcileOnce_ErrorPreservesTasksAndSuccessfulEmptyRemoves(t *testing.T) {
	state := &monitorPersistentState{}
	state.put(ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})
	r := newRunnerForTest(&stubMonitorSvc{list: state.snapshot})
	t.Cleanup(r.Stop)
	r.Start()
	original := runnerTaskPtr(r, 1)
	state.remove(1)
	snapshotErr := errors.New("PG unavailable")
	state.mu.Lock()
	state.listErr = snapshotErr
	state.mu.Unlock()
	if err := r.ReconcileOnce(context.Background()); !errors.Is(err, snapshotErr) {
		t.Fatalf("expected snapshot error, got %v", err)
	}
	if original == nil || runnerTaskPtr(r, 1) != original {
		t.Fatal("failed snapshot deleted or replaced existing task")
	}
	state.mu.Lock()
	state.listErr = nil
	state.mu.Unlock()
	reconcileMonitorRunner(t, r)
	if runnerTaskCount(r) != 0 {
		t.Fatal("successful empty snapshot did not remove task")
	}
}

func TestReconcileOnce_FailedAndRepairedKey(t *testing.T) {
	state := &monitorPersistentState{}
	m := ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60}
	state.put(m)
	r := newRunnerForTest(&stubMonitorSvc{list: state.snapshot})
	t.Cleanup(r.Stop)
	r.Start()
	m.APIKeyDecryptFailed = true
	state.put(m)
	reconcileMonitorRunner(t, r)
	if runnerTaskCount(r) != 0 {
		t.Fatal("decrypt-failed monitor remained scheduled")
	}
	m.APIKeyDecryptFailed = false
	m.APIKey = "replacement-key"
	state.put(m)
	reconcileMonitorRunner(t, r)
	if runnerTaskPtr(r, 1) == nil {
		t.Fatal("repaired key did not restore task")
	}
}

func TestReconcileOnce_UnchangedAndProbeUpdatesDoNotFire(t *testing.T) {
	state := &monitorPersistentState{}
	m := ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 3600, Endpoint: "https://before.invalid"}
	state.put(m)
	observed := make(chan string, 4)
	svc := &stubMonitorSvc{list: state.snapshot, run: func(ctx context.Context, id int64) ([]*CheckResult, error) {
		current, err := state.get(ctx, id)
		if err == nil {
			observed <- current.Endpoint
		}
		return nil, err
	}}
	r := newRunnerForTest(svc)
	t.Cleanup(r.Stop)
	r.Start()
	select {
	case endpoint := <-observed:
		if endpoint != m.Endpoint {
			t.Fatalf("unexpected initial endpoint %q", endpoint)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial check did not run")
	}
	original := runnerTaskPtr(r, 1)
	reconcileMonitorRunner(t, r)
	m.Endpoint = "https://after.invalid"
	m.UpdatedAt = time.Now()
	m.LastCheckedAt = &m.UpdatedAt
	state.put(m)
	reconcileMonitorRunner(t, r)
	if runnerTaskPtr(r, 1) != original {
		t.Fatal("probe configuration/timestamps restarted unchanged schedule")
	}
	// 等待调度 goroutine 和 pool 完全退出后计数，避免基于 sleep 的负断言。
	stoppedWithin(t, r, 2*time.Second)
	if original == nil || svc.runCount.Load() != 1 {
		t.Fatalf("unchanged/probe-only updates caused extra checks: %d", svc.runCount.Load())
	}
	// 非调度配置不需要重建：每次 RunCheck 都按 ID 获取最新持久化值。
	if _, err := svc.RunCheck(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if endpoint := <-observed; endpoint != m.Endpoint {
		t.Fatalf("RunCheck did not see updated endpoint: %q", endpoint)
	}
}

func TestStart_PollRetriesFailedInitialSnapshot(t *testing.T) {
	var calls atomic.Int64
	listed := make(chan struct{}, 1)
	svc := &stubMonitorSvc{
		list: func(ctx context.Context) ([]*ChannelMonitor, error) {
			if calls.Add(1) == 1 {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("initial snapshot has no deadline")
				}
				return nil, errors.New("transient PG error")
			}
			listed <- struct{}{}
			return []*ChannelMonitor{{ID: 1, Enabled: true, IntervalSeconds: 60}}, nil
		},
		runCalled: make(chan int64, 1),
	}
	r := newRunnerForTest(svc)
	t.Cleanup(r.Stop)
	ticks := make(chan time.Time, 1)
	r.reconcileTicks = ticks
	r.Start()
	r.Start() // 幂等，不做额外初次读取。
	if calls.Load() != 1 {
		t.Fatal("Start performed duplicate initial reads")
	}
	ticks <- time.Now()
	awaitMonitorSignal(t, listed, "poll retried snapshot")
	select {
	case <-svc.runCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("successful retry did not schedule check")
	}
	stoppedWithin(t, r, 2*time.Second)
	ticks <- time.Now()
	reconcileMonitorRunner(t, r) // Stop 后不再读取。
	if calls.Load() != 2 || runnerTaskCount(r) != 0 {
		t.Fatal("stopped runner continued polling or retained tasks")
	}
}

func TestStop_CancelsInitialAndPollingSnapshots(t *testing.T) {
	for _, phase := range []string{"initial", "poll"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			var calls atomic.Int64
			svc := &stubMonitorSvc{list: func(ctx context.Context) ([]*ChannelMonitor, error) {
				if calls.Add(1) == 1 && phase == "poll" {
					return nil, nil
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > monitorStartupLoadTimeout {
					t.Error("snapshot must have bounded deadline")
				}
				close(entered)
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			}}
			r := newRunnerForTest(svc)
			t.Cleanup(r.Stop)
			ticks := make(chan time.Time, 1)
			r.reconcileTicks = ticks
			startDone := make(chan struct{})
			go func() { r.Start(); close(startDone) }()
			if phase == "poll" {
				awaitMonitorSignal(t, startDone, "Start completed")
				ticks <- time.Now()
			}
			awaitMonitorSignal(t, entered, "snapshot started")
			stoppedWithin(t, r, 2*time.Second)
			awaitMonitorSignal(t, cancelled, "snapshot cancelled")
			awaitMonitorSignal(t, startDone, "Start exited")
		})
	}
}

func TestStop_BeforeStartAndRepeated(t *testing.T) {
	svc := &stubMonitorSvc{list: func(context.Context) ([]*ChannelMonitor, error) {
		t.Error("stopped runner read persistence")
		return nil, nil
	}}
	r := newRunnerForTest(svc)
	stoppedWithin(t, r, 2*time.Second)
	stoppedWithin(t, r, 2*time.Second)
	r.Start()
	reconcileMonitorRunner(t, r)
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})
	if runnerTaskCount(r) != 0 {
		t.Fatal("Stop-before-Start allowed tasks to start")
	}
}

func TestReconcileOnce_CancelledSnapshotKeepsTask(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	svc := &stubMonitorSvc{list: func(ctx context.Context) ([]*ChannelMonitor, error) {
		if calls.Add(1) == 1 {
			return []*ChannelMonitor{{ID: 1, Enabled: true, IntervalSeconds: 60}}, nil
		}
		close(entered)
		<-ctx.Done()
		// 即使底层错误地在取消后返回“成功空快照”，也不能删除现有任务。
		return nil, nil
	}}
	r := newRunnerForTest(svc)
	t.Cleanup(r.Stop)
	r.Start()
	original := runnerTaskPtr(r, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- r.ReconcileOnce(ctx) }()
	awaitMonitorSignal(t, entered, "snapshot entered")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled reconcile did not exit")
	}
	if original == nil || runnerTaskPtr(r, 1) != original {
		t.Fatal("cancelled snapshot removed task")
	}
}

func TestReconcileOnce_LocalCallbackWinsOverInProgressSnapshot(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	svc := &stubMonitorSvc{list: func(ctx context.Context) ([]*ChannelMonitor, error) {
		if calls.Add(1) == 1 {
			return nil, nil
		}
		close(entered)
		select {
		case <-release:
			return []*ChannelMonitor{{ID: 1, Enabled: true, IntervalSeconds: 60}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	r := newRunnerForTest(svc)
	t.Cleanup(r.Stop)
	r.Start()
	result := make(chan error, 1)
	go func() { result <- r.ReconcileOnce(context.Background()) }()
	awaitMonitorSignal(t, entered, "snapshot entered")
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 120})
	updated := runnerTaskPtr(r, 1)
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile did not complete")
	}
	if updated == nil || runnerTaskPtr(r, 1) != updated || updated.interval != 2*time.Minute {
		t.Fatal("in-progress snapshot overwrote newer CRUD callback")
	}
}

// stoppedWithin 在 timeout 内并行调用 Stop，超时则 Fatal。验证 Stop 不会阻塞。
func stoppedWithin(t *testing.T, r *ChannelMonitorRunner, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	var once sync.Once
	go func() {
		r.Stop()
		once.Do(func() { close(done) })
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("Stop did not return within %s — leaked goroutine?", timeout)
	}
}
