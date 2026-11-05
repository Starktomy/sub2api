package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// SupplierBillingRepair contains no client credentials. Authentication cache
// invalidations use the existing transactional auth invalidation outbox.
type SupplierBillingRepair struct {
	AttemptID      string
	UserID         int64
	APIKeyID       int64
	GroupID        int64
	AccountID      int64
	SubscriptionID *int64
}

type SupplierBillingRepairRepository interface {
	ListSupplierBillingRepairs(context.Context, int) ([]SupplierBillingRepair, error)
	CompleteSupplierBillingRepair(context.Context, string) error
}

type supplierBillingCacheInvalidator interface {
	InvalidateUserBalance(context.Context, int64) error
	InvalidateSubscription(context.Context, int64, int64) error
	InvalidateAPIKeyRateLimit(context.Context, int64) error
}

// SupplierBillingRecoveryWorker never possesses a vault, provider client or
// generation callback. PostgreSQL commands are replayed through existing billing
// deduplication; cache repair deletes, rather than re-increments, cached values.
type SupplierBillingRecoveryWorker struct {
	repo     SupplierDispatchRepository
	repairs  SupplierBillingRepairRepository
	cache    supplierBillingCacheInvalidator
	deferred *DeferredService
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewSupplierBillingRecoveryWorker(repo UsageBillingRepository, cache *BillingCacheService, deferred *DeferredService) *SupplierBillingRecoveryWorker {
	dispatch, _ := repo.(SupplierDispatchRepository)
	repairs, _ := repo.(SupplierBillingRepairRepository)
	w := &SupplierBillingRecoveryWorker{repo: dispatch, repairs: repairs, deferred: deferred, interval: 5 * time.Second}
	if cache != nil {
		w.cache = cache
	}
	return w
}

func (w *SupplierBillingRecoveryWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	if w.repo == nil || w.repairs == nil || w.cache == nil {
		return ErrSupplierDispatchUnavailable
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			w.tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (w *SupplierBillingRecoveryWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *SupplierBillingRecoveryWorker) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	result, err := w.repo.RecoverSupplierBilling(call, 20)
	cancel()
	if err != nil && ctx.Err() == nil {
		// Never log raw SQL/provider/command error strings: aggregate failure is
		// actionable without exposing customer IDs, model payloads or credentials.
		slog.Warn("supplier billing recovery failed", "operation", "replay")
	} else if result.Replayed > 0 || result.Review > 0 {
		slog.Info("supplier billing recovery processed", "replayed", result.Replayed, "review", result.Review)
	}
	if ctx.Err() != nil {
		return
	}
	call, cancel = context.WithTimeout(ctx, 5*time.Second)
	repairs, err := w.repairs.ListSupplierBillingRepairs(call, 20)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("supplier billing recovery failed", "operation", "list_cache_repairs")
		}
		return
	}
	for _, repair := range repairs {
		if ctx.Err() != nil {
			return
		}
		call, cancel = context.WithTimeout(ctx, 5*time.Second)
		err = w.repair(call, repair)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("supplier billing recovery failed", "operation", "cache_repair")
		}
	}
}

func (w *SupplierBillingRecoveryWorker) repair(ctx context.Context, r SupplierBillingRepair) error {
	if r.AttemptID == "" || r.UserID <= 0 || r.APIKeyID <= 0 || r.AccountID <= 0 || r.GroupID <= 0 {
		return ErrSupplierDispatchConflict
	}
	// Retry is safe even if a previous delete or completion acknowledgement was
	// ambiguous. Financial effects are never replayed here.
	err := errors.Join(w.cache.InvalidateUserBalance(ctx, r.UserID), w.cache.InvalidateSubscription(ctx, r.UserID, r.GroupID), w.cache.InvalidateAPIKeyRateLimit(ctx, r.APIKeyID))
	if err != nil {
		return err
	}
	if w.deferred != nil {
		w.deferred.ScheduleLastUsedUpdate(r.AccountID)
	}
	return w.repairs.CompleteSupplierBillingRepair(ctx, r.AttemptID)
}
