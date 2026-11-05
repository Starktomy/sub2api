package service

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

type SupplierProbeClaim struct {
	Resource   *SupplierResource
	Owner      string
	Generation int64
	Attempts   int
}
type SupplierProbeRepository interface {
	Reconcile(context.Context) error
	Claim(context.Context, string) (*SupplierProbeClaim, error)
	Finish(context.Context, *SupplierProbeClaim, *SupplierResourceUsage, string, time.Duration, bool) error
	Resume(context.Context, int64, string, int64) (*SupplierResource, error)
}
type SupplierProbeFailure struct {
	Code       string
	RetryAfter time.Duration
}

func (e *SupplierProbeFailure) Error() string { return "Supplier usage query unavailable" }
func (e *SupplierProbeFailure) Unwrap() error {
	return apperrors.New(502, "SUPPLIER_USAGE_UNAVAILABLE", "Usage query unavailable; valid session may need importing again")
}

type SupplierProbeWorker struct {
	repo      SupplierProbeRepository
	resources *SupplierResourceService
	interval  time.Duration
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewSupplierProbeWorker(repo SupplierProbeRepository, resources *SupplierResourceService, cfg *config.Config) *SupplierProbeWorker {
	interval := cfg.SupplierProbe.IntervalSeconds
	if interval < 300 || interval > 900 {
		interval = 600
	}
	return &SupplierProbeWorker{repo: repo, resources: resources, interval: time.Duration(interval) * time.Second}
}
func (w *SupplierProbeWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return nil
	}
	if !w.resources.Configured() {
		return ErrSupplierVault
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(30 * time.Second)
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
func (w *SupplierProbeWorker) Stop(ctx context.Context) error {
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
func (w *SupplierProbeWorker) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := w.repo.Reconcile(call)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("supplier probe maintenance failed", "operation", "reconcile")
		}
		return
	}
	for n := 0; n < 2 && ctx.Err() == nil; n++ {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		claim, err := w.repo.Claim(call, uuid.NewString())
		cancel()
		if err != nil || claim == nil {
			if err != nil && ctx.Err() == nil {
				slog.Warn("supplier probe maintenance failed", "operation", "claim")
			}
			return
		}
		usage, err := w.resources.queryResource(ctx, claim.Resource)
		code := ""
		terminal := false
		delay := w.interval * time.Duration(80+rand.IntN(41)) / 100
		if err != nil {
			code = "upstream_unavailable"
			var f *SupplierProbeFailure
			if errors.As(err, &f) {
				code = f.Code
			}
			terminal = code == "unauthorized" || code == "forbidden"
			delay = probeBackoff(claim.Attempts) * time.Duration(100+rand.IntN(21)) / 100
			if f != nil && f.RetryAfter > delay {
				delay = f.RetryAfter
			}
			if errors.Is(err, ErrSupplierVault) {
				code = "vault_unavailable"
			}
		}
		if ctx.Err() != nil {
			return
		}
		call, cancel = context.WithTimeout(ctx, 5*time.Second)
		finishErr := w.repo.Finish(call, claim, usage, code, delay, terminal)
		cancel()
		if finishErr != nil && ctx.Err() == nil {
			slog.Warn("supplier probe maintenance failed", "operation", "finish")
		}
	}
}
func probeBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<attempt) * time.Minute
}
