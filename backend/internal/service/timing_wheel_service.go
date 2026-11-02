package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/zeromicro/go-zero/core/collection"
)

var newTimingWheel = collection.NewTimingWheel

// TimingWheelService owns the wheel and waits for already-dispatched callbacks
// before shutdown. Managed construction does not create go-zero's eager loop.
type TimingWheelService struct {
	mu       sync.Mutex
	tw       *collection.TimingWheel
	stopped  bool
	jobs     sync.WaitGroup
	stopOnce sync.Once
}

// NewTimingWheelService preserves the legacy eager-construction contract.
func NewTimingWheelService() (*TimingWheelService, error) {
	svc := NewManagedTimingWheelService()
	if err := svc.StartManaged(); err != nil {
		return nil, err
	}
	return svc, nil
}

func NewManagedTimingWheelService() *TimingWheelService { return &TimingWheelService{} }

// StartManaged reports initialization failures to the application lifecycle.
func (s *TimingWheelService) StartManaged() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fmt.Errorf("timing wheel is stopped")
	}
	if s.tw != nil {
		return nil
	}
	tw, err := newTimingWheel(time.Second, 3600, func(key, value any) {
		fn, ok := value.(func())
		if !ok {
			return
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.jobs.Add(1)
		s.mu.Unlock()
		defer s.jobs.Done()
		fn()
	})
	if err != nil {
		return fmt.Errorf("create timing wheel: %w", err)
	}
	s.tw = tw
	logger.LegacyPrintf("service.timing_wheel", "%s", "[TimingWheel] Started")
	return nil
}

// Start is retained for legacy callers; managed providers use StartManaged.
func (s *TimingWheelService) Start() {
	if err := s.StartManaged(); err != nil {
		logger.LegacyPrintf("service.timing_wheel", "[TimingWheel] Start failed: %v", err)
	}
}

func (s *TimingWheelService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		tw := s.tw
		s.tw = nil
		s.mu.Unlock()
		if tw != nil {
			tw.Stop()
		}
		s.jobs.Wait()
	})
}

func (s *TimingWheelService) Schedule(name string, delay time.Duration, fn func()) {
	s.mu.Lock()
	tw, stopped := s.tw, s.stopped
	s.mu.Unlock()
	if tw == nil || stopped {
		logger.LegacyPrintf("service.timing_wheel", "[TimingWheel] Cannot schedule %q on an inactive wheel", name)
		return
	}
	if err := tw.SetTimer(name, fn, delay); err != nil {
		logger.LegacyPrintf("service.timing_wheel", "[TimingWheel] SetTimer failed for %q: %v", name, err)
	}
}

func (s *TimingWheelService) ScheduleRecurring(name string, interval time.Duration, fn func()) {
	var schedule func()
	schedule = func() {
		fn()
		s.Schedule(name, interval, schedule)
	}
	s.Schedule(name, interval, schedule)
}

func (s *TimingWheelService) Cancel(name string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	tw := s.tw
	s.mu.Unlock()
	if tw != nil {
		_ = tw.RemoveTimer(name)
	}
}
