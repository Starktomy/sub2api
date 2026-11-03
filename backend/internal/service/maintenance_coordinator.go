package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// MaintenanceLocker must use one dedicated PostgreSQL session, never a TTL
// lease. A session is retained until every task has joined on orderly shutdown.
type MaintenanceLocker interface {
	TryAcquire(context.Context) (MaintenanceSession, bool, error)
}

type MaintenanceSession interface {
	Check(context.Context) error
	Release(context.Context) error
}

// MaintenanceCoordinator is single-use, like the services it owns. After loss of
// a session it cancels and joins tasks but never restarts those service instances.
// A different standby may take over. Session loss is NOT an external-action fence:
// only tasks admitted by MaintenanceTaskPolicy may run here.
type MaintenanceCoordinator struct {
	locker   MaintenanceLocker
	interval time.Duration
	timeout  time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
	started  bool
	// Keep a strong reference on failed joins; never let an abandoned session
	// be reclaimed while quiescence is unknown.
	retained MaintenanceSession
}

func NewMaintenanceCoordinator(locker MaintenanceLocker) *MaintenanceCoordinator {
	return &MaintenanceCoordinator{locker: locker, interval: time.Second, timeout: 5 * time.Second}
}

func (c *MaintenanceCoordinator) Start(ctx context.Context, tasks []RuntimeTask) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return errors.New("maintenance coordinator is single-use")
	}
	if c.locker == nil {
		return errors.New("multi-replica maintenance requires PostgreSQL session coordination")
	}
	for _, task := range tasks {
		if reason := MaintenanceTaskPolicy(task.Name); reason != "" {
			return fmt.Errorf("unsafe maintenance task %s: %s", task.Name, reason)
		}
	}
	c.started = true
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel, c.done = cancel, make(chan struct{})
	go c.run(runCtx, append([]RuntimeTask(nil), tasks...))
	return nil
}

func (c *MaintenanceCoordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	case <-ctx.Done():
		// The run goroutine retains the session until task joins finish. Do not
		// unlock merely because our caller's shutdown budget elapsed.
		return ctx.Err()
	}
}

func (c *MaintenanceCoordinator) run(ctx context.Context, tasks []RuntimeTask) {
	defer close(c.done)
	var session MaintenanceSession
	for session == nil {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, c.timeout)
		acquired, ok, err := c.locker.TryAcquire(attempt)
		cancel()
		if err == nil && ok {
			session = acquired
			break
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("maintenance leadership acquisition failed; staying standby", "error", err)
		}
		timer := time.NewTimer(c.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	slog.Info("maintenance PostgreSQL session leadership acquired")
	workCtx, cancelWork := context.WithCancel(ctx)
	if fenced, ok := session.(FencedMaintenanceSession); ok && fenced.Fence().Valid() {
		workCtx = ContextWithMaintenanceFence(workCtx, fenced.Fence())
	}
	var started []RuntimeTask
	var result error
	for _, task := range tasks {
		if err := workCtx.Err(); err != nil {
			result = err
			break
		}
		// Recheck immediately before each Start; no future task is started after a
		// detected disconnect. SQL task operations still need their own predicates.
		checkCtx, cancel := context.WithTimeout(workCtx, c.timeout)
		err := session.Check(checkCtx)
		cancel()
		if err != nil {
			result = fmt.Errorf("maintenance session lost before start: %w", err)
			break
		}
		if err := task.Start(workCtx); err != nil {
			result = fmt.Errorf("start maintenance task %s: %w", task.Name, err)
			break
		}
		started = append(started, task)
	}
	if result == nil {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
	monitor:
		for {
			select {
			case <-ctx.Done():
				break monitor
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
				err := session.Check(checkCtx)
				cancel()
				if err != nil {
					result = fmt.Errorf("maintenance session lost: %w", err)
					break monitor
				}
			}
		}
	}
	cancelWork()
	// Join callbacks run in this goroutine, not detached timeout wrappers. Stop's
	// caller has a bounded wait, but the session cannot be voluntarily released
	// while a callback or its tasks might still be executing.
	for phase := RuntimeStopProducers; phase <= RuntimeStopStores; phase++ {
		for i := len(started) - 1; i >= 0; i-- {
			if started[i].StopPhase != phase {
				continue
			}
			if err := started[i].Stop(context.Background()); err != nil {
				result = errors.Join(result, fmt.Errorf("join maintenance task %s: %w", started[i].Name, err))
				// No proof of quiescence: fail closed, retain session indefinitely until
				// process exit. Automatic release on a server disconnect is unavoidable.
				c.mu.Lock()
				c.err = result
				c.retained = session
				c.mu.Unlock()
				slog.Error("maintenance join failed; retaining PostgreSQL session until process exit", "task", started[i].Name, "error", err)
				return
			}
		}
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), c.timeout)
	result = errors.Join(result, session.Release(releaseCtx))
	releaseCancel()
	c.mu.Lock()
	c.err = result
	c.mu.Unlock()
	if result != nil {
		slog.Error("maintenance leadership ended; process will not reacquire", "error", result)
	}
}

// MaintenanceTaskPolicy is an explicit fail-closed inventory. Empty means an
// audited conditional/idempotent SQL operation or atomic Redis cleanup, with
// cancellation and join. Redis cleanup is safe even if old/new leaders overlap.
// Unknown/new worker tasks are disabled rather than silently becoming HA-safe.
func MaintenanceTaskPolicy(name string) string {
	switch name {
	case "AccountExpiryService", "IdempotencyCleanupService", "SubscriptionExpiryService", "UserMessageQueueCleanup", "AuthCacheInvalidationWorker", "ConcurrencySlotCleanup":
		return ""
	case "TokenRefreshService":
		return "provider refresh and reconciliation require durable fencing for every provider"
	case "ProxyExpiryService":
		return "fallback target snapshot is not fenced against concurrent changes"

	case "PaymentOrderExpiryService":
		return "payment reconciliation calls external providers; no durable per-attempt claim"
	case "BatchImageCleanupService":
		return "external object deletion has no durable operation claim"
	case "CNProviderBalanceCheckService":
		return "external balance probes and account reconciliation are not fenced"
	case "OpenAICodexVersionSyncService", "ClaudeCodeVersionSyncService":
		return "external release lookup followed by unfenced settings write"
	case "DashboardAggregationService":
		return "scheduled callbacks and startup backfills do not join"
	case "UsageCleanupService":
		return "cleanup task progress and recovery are not generation fenced"
	case "SchedulerSnapshotService":
		return "shared snapshot writes/outbox acknowledgements are not generation fenced"
	case "OpsAggregationService", "ChannelMonitorV2Aggregator":
		return "aggregation/backfill writes have no owner generation fence"
	case "OpsAlertEvaluatorService", "OpsScheduledReportService":
		return "external notifications lack durable delivery claims"
	case "OpsCleanupService":
		return "cron callbacks and reload paths lack cancellation and full join"
	case "GeminiCachedContentService":
		return "mutation recovery may issue external actions without durable operation fence"
	case "ScheduledTestRunnerService", "ChannelMonitorRunner":
		return "external model/proxy tests lack durable per-run claims"
	case "BackupService":
		return "external backup/archive work lacks durable per-run claims"
	case "UserPlatformQuotaUsageFlusher":
		return "shared quota flush recovery needs atomic claim and join audit"
	default:
		return "task has not been audited for multi-replica maintenance"
	}
}
