package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// IdempotencyCleanupService 定期清理已过期的幂等记录，避免表无限增长。
type IdempotencyCleanupService struct {
	repo     IdempotencyRepository
	interval time.Duration
	batch    int

	wg           sync.WaitGroup
	startOnce    sync.Once
	stopOnce     sync.Once
	stopCh       chan struct{}
	workerCtx    context.Context
	workerCancel context.CancelFunc
	lifecycleMu  sync.Mutex
}

func NewIdempotencyCleanupService(repo IdempotencyRepository, cfg *config.Config) *IdempotencyCleanupService {
	interval := 60 * time.Second
	batch := 500
	if cfg != nil {
		if cfg.Idempotency.CleanupIntervalSeconds > 0 {
			interval = time.Duration(cfg.Idempotency.CleanupIntervalSeconds) * time.Second
		}
		if cfg.Idempotency.CleanupBatchSize > 0 {
			batch = cfg.Idempotency.CleanupBatchSize
		}
	}
	return &IdempotencyCleanupService{
		repo:     repo,
		interval: interval,
		batch:    batch,
		stopCh:   make(chan struct{}),
	}
}

func (s *IdempotencyCleanupService) Start() { s.StartContext(context.Background()) }

func (s *IdempotencyCleanupService) StartContext(parent context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.workerCancel != nil {
		s.lifecycleMu.Unlock()
		return
	}
	select {
	case <-s.stopCh:
		s.lifecycleMu.Unlock()
		return
	default:
	}
	s.workerCtx, s.workerCancel = context.WithCancel(parent)
	s.startOnce.Do(func() {
		logger.LegacyPrintf("service.idempotency_cleanup", "[IdempotencyCleanup] started interval=%s batch=%d", s.interval, s.batch)
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.runLoop() }()
	})
	s.lifecycleMu.Unlock()
}

func (s *IdempotencyCleanupService) Stop() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.workerCancel != nil {
		s.workerCancel()
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		logger.LegacyPrintf("service.idempotency_cleanup", "[IdempotencyCleanup] stopped")
	})
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

func (s *IdempotencyCleanupService) runLoop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 启动后先清理一轮，防止重启后积压。
	s.cleanupOnce()

	for {
		select {
		case <-ticker.C:
			s.cleanupOnce()
		case <-s.workerCtx.Done():
			return
		case <-s.stopCh:
			return
		}
	}
}

func (s *IdempotencyCleanupService) cleanupOnce() {
	ctx, cancel := context.WithTimeout(s.maintenanceContext(), 10*time.Second)
	defer cancel()

	deleted, err := s.repo.DeleteExpired(ctx, time.Now(), s.batch)
	if err != nil {
		logger.LegacyPrintf("service.idempotency_cleanup", "[IdempotencyCleanup] cleanup failed err=%v", err)
		return
	}
	if deleted > 0 {
		logger.LegacyPrintf("service.idempotency_cleanup", "[IdempotencyCleanup] cleaned expired records count=%d", deleted)
	}
}

func (s *IdempotencyCleanupService) maintenanceContext() context.Context {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.workerCtx != nil {
		return s.workerCtx
	}
	return context.Background()
}
