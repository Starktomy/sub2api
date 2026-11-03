package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// AccountExpiryService periodically pauses expired accounts when auto-pause is enabled.
type AccountExpiryService struct {
	accountRepo  AccountRepository
	interval     time.Duration
	stopCh       chan struct{}
	workerCtx    context.Context
	workerCancel context.CancelFunc
	lifecycleMu  sync.Mutex
	stopOnce     sync.Once
	wg           sync.WaitGroup
}

func NewAccountExpiryService(accountRepo AccountRepository, interval time.Duration) *AccountExpiryService {
	return &AccountExpiryService{
		accountRepo: accountRepo,
		interval:    interval,
		stopCh:      make(chan struct{}),
	}
}

func (s *AccountExpiryService) Start() { s.StartContext(context.Background()) }

func (s *AccountExpiryService) StartContext(parent context.Context) {
	if s == nil || s.accountRepo == nil || s.interval <= 0 {
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
	s.wg.Add(1)
	s.lifecycleMu.Unlock()
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.workerCtx.Done():
				return
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *AccountExpiryService) Stop() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.workerCancel != nil {
		s.workerCancel()
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.lifecycleMu.Unlock()
	s.wg.Wait()
}

func (s *AccountExpiryService) runOnce() {
	ctx, cancel := context.WithTimeout(s.maintenanceContext(), 5*time.Second)
	defer cancel()

	updated, err := s.accountRepo.AutoPauseExpiredAccounts(ctx, time.Now())
	if err != nil {
		log.Printf("[AccountExpiry] Auto pause expired accounts failed: %v", err)
		return
	}
	if updated > 0 {
		log.Printf("[AccountExpiry] Auto paused %d expired accounts", updated)
	}
}

func (s *AccountExpiryService) maintenanceContext() context.Context {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.workerCtx != nil {
		return s.workerCtx
	}
	return context.Background()
}
