package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
)

// MonitorScheduler 调度器接口，供 ChannelMonitorService 在 CRUD 时回调，
// 用 setter 注入避免 service ↔ runner 的 wire 依赖环。
type MonitorScheduler interface {
	// Schedule 为指定监控创建（或重置）独立定时任务。
	// 当 m.Enabled=false 时等同于 Unschedule(m.ID)。
	Schedule(m *ChannelMonitor)
	// Unschedule 取消指定监控的定时任务（若存在）。
	Unschedule(id int64)
}

const monitorReconcileInterval = 30 * time.Second

// monitorRunnerSvc 抽出 runner 实际依赖的两个 service 方法：
//   - 启动及周期性重读持久化的 enabled monitor
//   - 每次 timer 触发执行检测
//
// 用接口而非 *ChannelMonitorService 是为了让 runner 单元测试可注入轻量 stub，
// 避免依赖完整的 repo + encryptor 链路。生产实现 *ChannelMonitorService 自然满足。
type monitorRunnerSvc interface {
	ListEnabledMonitors(ctx context.Context) ([]*ChannelMonitor, error)
	RunCheck(ctx context.Context, id int64) ([]*CheckResult, error)
}

// ChannelMonitorRunner 渠道监控调度器。
//
// 设计：
//   - 每个 enabled monitor 对应一个独立 goroutine + ticker（按各自 IntervalSeconds）
//   - Start 初次加载，之后每 30 秒从 PG repository 重读收敛；失败保留现有任务
//   - Service 的本地 CRUD 回调仅加速同步，独立 worker 不依赖另一个进程的回调
//   - 周期快照中只有调度参数变化才重建任务；RunCheck 按 ID 重读其它配置
//   - 实际 HTTP 检测交给 pond 池（容量 monitorWorkerConcurrency），
//     防止突发并发拖垮上游
//
// 本 runner 没有跨实例 leader/lease：多个 worker 仍会重复探测，当前仅适用于
// 单 maintenance worker 的预生产部署，inFlight 只保证本实例内不重入。
// 历史清理与日聚合维护由 OpsCleanupService 的 cron 触发
// ChannelMonitorService.RunDailyMaintenance（复用 leader lock + heartbeat），
// 不在 runner 职责内。
type ChannelMonitorRunner struct {
	svc            monitorRunnerSvc
	settingService *SettingService

	pool         pond.Pool
	parentCtx    context.Context
	parentCancel context.CancelFunc

	mu       sync.Mutex
	tasks    map[int64]*scheduledMonitor
	wg       sync.WaitGroup // startup/poll、reconcile 和调度 goroutine；pool 单独 drain
	started  bool
	stopped  bool
	stopDone chan struct{}

	// 串行读取/应用快照；等待槽位也受 reconcile 的 context 超时约束。
	reconcileGate chan struct{}
	// 本地回调在读取快照期间发生时，丢弃该快照，避免覆盖更新的本地状态。
	scheduleRevision uint64 // guarded by mu
	// 仅供测试在 Start 前注入；生产固定使用 monitorReconcileInterval，无新增配置。
	reconcileTicks <-chan time.Time

	// inFlight 跟踪正在执行的 monitor.ID。fire 调度前会检查避免重复提交，
	// 防止单次检测耗时 > interval 时同一 monitor 被并发执行。
	inFlight   map[int64]struct{}
	inFlightMu sync.Mutex
}

// scheduledMonitor 单个监控的运行时上下文。
type scheduledMonitor struct {
	id       int64
	name     string
	interval time.Duration
	jitter   time.Duration // 每轮 ± [0, jitter] 的均匀随机偏移；0 = 固定间隔
	cancel   context.CancelFunc
}

// nextDelay 计算下一次触发的等待时长：interval ± [0, jitter] 的均匀随机偏移。
// 校验链路已保证 interval - jitter >= monitorMinIntervalSeconds，
// 这里仍 clamp 一次下限，兜底数据库中违反约束的脏数据。
func (t *scheduledMonitor) nextDelay() time.Duration {
	if t.jitter <= 0 {
		return t.interval
	}
	offset := time.Duration(rand.Int64N(int64(2*t.jitter) + 1)) // [0, 2*jitter]
	d := t.interval - t.jitter + offset
	if floor := monitorMinIntervalSeconds * time.Second; d < floor {
		d = floor
	}
	return d
}

// NewChannelMonitorRunner 构造调度器。Start 在 wire 中调用一次。
// settingService 用于在每次 fire 前读取功能开关；传 nil 时视为总是启用（兼容测试）。
//
// pool 在构造时即建好：避免 Start 在 mu 内赋值、fire/Stop 在 mu 外读取的竞态隐患，
// 且 pond.NewPool 创建本身近似零开销，提前建池不会浪费资源。
func NewChannelMonitorRunner(svc *ChannelMonitorService, settingService *SettingService) *ChannelMonitorRunner {
	return newChannelMonitorRunner(svc, settingService)
}

// newChannelMonitorRunner 内部构造，接受最小化接口，便于单元测试注入 stub。
func newChannelMonitorRunner(svc monitorRunnerSvc, settingService *SettingService) *ChannelMonitorRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChannelMonitorRunner{
		svc:            svc,
		settingService: settingService,
		pool:           pond.NewPool(monitorWorkerConcurrency),
		parentCtx:      ctx,
		parentCancel:   cancel,
		tasks:          make(map[int64]*scheduledMonitor),
		inFlight:       make(map[int64]struct{}),
		stopDone:       make(chan struct{}),
		reconcileGate:  make(chan struct{}, 1),
	}
}

// Start 同步执行初次 reconcile，然后启动固定间隔的轮询；初次失败仍会重试。
// 可重复调用；Stop 后不会再次启动。
func (r *ChannelMonitorRunner) Start() {
	if r == nil || r.svc == nil {
		return
	}
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.started = true
	// 这个槽覆盖 initial reconcile 到 poll 退出，Stop 不会漏等启动中的读取。
	r.wg.Add(1)
	r.mu.Unlock()

	if err := r.ReconcileOnce(r.parentCtx); err != nil {
		slog.Error("channel_monitor: initial reconcile failed; polling will retry", "error", err)
	}
	go r.pollReconcile()
}

// ReconcileOnce 从持久化配置收敛本实例的任务表。必须先 Start；Stop 后为 no-op。
// 单轮（包括等待其它 reconcile）最多 monitorStartupLoadTimeout，且受调用方和
// Stop 的取消约束。读取失败/取消不删除旧任务；未变的任务保留 timer，不额外探测。
// RunCheck 每次按 ID 读当前配置，因此仅需比较本地保存的调度参数，不缓存 API key、
// endpoint 等探测配置，也不能用随检测结果变化的 UpdatedAt 判定是否重建。
func (r *ChannelMonitorRunner) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.svc == nil {
		return nil
	}
	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.wg.Add(1)
	r.mu.Unlock()
	defer r.wg.Done()

	ctx, cancel := context.WithTimeout(ctx, monitorStartupLoadTimeout)
	stopCancel := context.AfterFunc(r.parentCtx, cancel)
	defer stopCancel()
	defer cancel()
	select {
	case r.reconcileGate <- struct{}{}:
		defer func() { <-r.reconcileGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	revision := r.scheduleRevision
	r.mu.Unlock()

	enabled, err := r.svc.ListEnabledMonitors(ctx)
	if err != nil {
		return fmt.Errorf("list enabled channel monitors: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.stopped || revision != r.scheduleRevision {
		// 快照读取期间发生了本地 CRUD/撤销；下一轮重读，不能让旧快照反向覆盖。
		return nil
	}
	wanted := make(map[int64]struct{}, len(enabled))
	for _, m := range enabled {
		if m == nil || !m.Enabled || m.APIKeyDecryptFailed {
			continue
		}
		if time.Duration(m.IntervalSeconds)*time.Second <= 0 {
			slog.Error("channel_monitor: skip reconcile for invalid interval",
				"monitor_id", m.ID, "interval_seconds", m.IntervalSeconds)
			continue
		}
		wanted[m.ID] = struct{}{}
		r.scheduleLocked(m, false)
	}
	for id := range r.tasks {
		if _, ok := wanted[id]; !ok {
			r.unscheduleLocked(id)
		}
	}
	return nil
}

func (r *ChannelMonitorRunner) pollReconcile() {
	defer r.wg.Done()
	ticks := r.reconcileTicks
	if ticks == nil {
		ticker := time.NewTicker(monitorReconcileInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-r.parentCtx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if err := r.ReconcileOnce(r.parentCtx); err != nil && r.parentCtx.Err() == nil {
				slog.Warn("channel_monitor: reconcile failed; keeping scheduled tasks", "error", err)
			}
		}
	}
}

// Schedule 为指定监控创建（或重置）独立定时任务。
//   - m.Enabled=false 或 APIKeyDecryptFailed=true → 等同于 Unschedule(m.ID)
//   - 已存在的任务会先被取消再重建（适用于 IntervalSeconds 变更场景）
//   - 新任务立即触发首次检测，之后按 IntervalSeconds 周期触发
func (r *ChannelMonitorRunner) Schedule(m *ChannelMonitor) {
	if r == nil || m == nil {
		return
	}
	if !m.Enabled || m.APIKeyDecryptFailed {
		r.Unschedule(m.ID)
		return
	}
	interval := time.Duration(m.IntervalSeconds) * time.Second
	if interval <= 0 {
		// Create/Update 已通过 validateInterval 校验区间，正常路径不可能到这里。
		// 真触发说明数据库中存在违反约束的数据或校验链路有 bug，记 Error 暴露问题。
		slog.Error("channel_monitor: skip schedule for invalid interval",
			"monitor_id", m.ID, "interval_seconds", m.IntervalSeconds)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if !r.started {
		// 未启动角色不接受本地回调；以后 Start 会从持久化配置读取。
		slog.Warn("channel_monitor: schedule before runner started, skip",
			"monitor_id", m.ID, "name", m.Name)
		return
	}
	r.scheduleRevision++
	r.scheduleLocked(m, true)
}

// scheduleLocked 调用方持有 mu。显式 Schedule 保留 CRUD 即时重置语义；
// reconcile 仅在 name/interval/jitter 变化时重建，其它配置由 RunCheck 重读。
func (r *ChannelMonitorRunner) scheduleLocked(m *ChannelMonitor, force bool) {
	interval := time.Duration(m.IntervalSeconds) * time.Second
	jitter := time.Duration(m.JitterSeconds) * time.Second
	if jitter < 0 {
		jitter = 0
	}
	if existing := r.tasks[m.ID]; existing != nil {
		if !force && existing.name == m.Name && existing.interval == interval && existing.jitter == jitter {
			return
		}
		existing.cancel()
	}
	ctx, cancel := context.WithCancel(r.parentCtx)
	task := &scheduledMonitor{
		id:       m.ID,
		name:     m.Name,
		interval: interval,
		jitter:   jitter,
		cancel:   cancel,
	}
	r.tasks[m.ID] = task
	r.wg.Add(1)
	go r.runScheduled(ctx, task)
}

// Unschedule 取消指定监控的定时任务（若存在）。
// 已经在执行中的检测会通过 ctx 取消信号传递。
func (r *ChannelMonitorRunner) Unschedule(id int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduleRevision++
	r.unscheduleLocked(id)
}

func (r *ChannelMonitorRunner) unscheduleLocked(id int64) {
	if task := r.tasks[id]; task != nil {
		delete(r.tasks, id)
		task.cancel()
	}
}

// Stop 取消启动读取、poll、所有定时/在飞任务，再等待退出及 pool drain。
// 未 Start 也可 Stop；重复/并发 Stop 都等待同一个完成信号。
func (r *ChannelMonitorRunner) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		<-r.stopDone
		return
	}
	r.stopped = true
	r.parentCancel()
	r.tasks = nil
	r.mu.Unlock()

	r.wg.Wait()
	r.pool.StopAndWait()
	close(r.stopDone)
}

// runScheduled 单个监控的循环：立即触发首次（满足"新建/启用即跑"），
// 之后按 interval ± jitter 周期触发；ctx 取消即退出。
// 用 timer 而非 ticker：jitter > 0 时每轮等待时长都需要重新随机化。
func (r *ChannelMonitorRunner) runScheduled(ctx context.Context, task *scheduledMonitor) {
	defer r.wg.Done()

	r.fire(ctx, task)

	timer := time.NewTimer(task.nextDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			r.fire(ctx, task)
			timer.Reset(task.nextDelay())
		}
	}
}

// fire 提交一次检测到 worker 池。功能开关关闭时跳过本次（不取消任务，
// 重新启用时立即恢复）；池满或重复在飞时也跳过。
func (r *ChannelMonitorRunner) fire(ctx context.Context, task *scheduledMonitor) {
	if ctx.Err() != nil {
		return
	}
	if r.settingService != nil {
		rt := r.settingService.GetChannelMonitorRuntime(ctx)
		if !rt.ActiveProbesAllowed() {
			return
		}
	}
	if !r.tryAcquireInFlight(task.id) {
		slog.Debug("channel_monitor: skip already in-flight",
			"monitor_id", task.id, "name", task.name)
		return
	}
	if _, ok := r.pool.TrySubmit(func() {
		r.runOne(ctx, task)
	}); !ok {
		// 池满：丢弃本次检测，但必须释放已占用的 inFlight 槽，否则该 monitor 会被永久卡住。
		r.releaseInFlight(task.id)
		slog.Warn("channel_monitor: worker pool full, skip submission",
			"monitor_id", task.id, "name", task.name)
	}
}

// tryAcquireInFlight 原子地占用 monitor 的 in-flight 槽。
// 已被占用返回 false（调用方应跳过本次提交）。
func (r *ChannelMonitorRunner) tryAcquireInFlight(id int64) bool {
	r.inFlightMu.Lock()
	defer r.inFlightMu.Unlock()
	if _, exists := r.inFlight[id]; exists {
		return false
	}
	r.inFlight[id] = struct{}{}
	return true
}

// releaseInFlight 释放 in-flight 槽。runOne 完成（含 panic recover）后必须调用。
func (r *ChannelMonitorRunner) releaseInFlight(id int64) {
	r.inFlightMu.Lock()
	delete(r.inFlight, id)
	r.inFlightMu.Unlock()
}

// runOne 执行单个监控的检测。普通错误只记日志；API key 解密失败会撤销任务。
// 任务结束时（含 panic recover）必须释放 in-flight 槽。
func (r *ChannelMonitorRunner) runOne(parent context.Context, task *scheduledMonitor) {
	ctx, cancel := context.WithTimeout(parent, monitorRequestTimeout+monitorPingTimeout+monitorRunOneBuffer)
	defer cancel()
	defer r.releaseInFlight(task.id)

	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("channel_monitor: runner panic",
				"monitor_id", task.id, "name", task.name, "panic", rec)
		}
	}()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.svc.RunCheck(ctx, task.id); err != nil {
		if errors.Is(err, ErrChannelMonitorAPIKeyDecryptFailed) {
			r.mu.Lock()
			// 旧检测失败不能撤销刚由 CRUD/reconcile 替换的新任务。
			if r.tasks[task.id] == task {
				r.scheduleRevision++
				r.unscheduleLocked(task.id)
			}
			r.mu.Unlock()
		}
		slog.Warn("channel_monitor: run check failed",
			"monitor_id", task.id, "name", task.name, "error", err)
	}
}
