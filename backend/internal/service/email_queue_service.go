package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Task type constants
const (
	TaskTypeVerifyCode    = "verify_code"
	TaskTypePasswordReset = "password_reset"

	emailQueueDrainTimeout = 30 * time.Second
)

// EmailTask 邮件发送任务
type EmailTask struct {
	Email    string
	SiteName string
	TaskType string // "verify_code" or "password_reset"
	ResetURL string // Only used for password_reset task type
	Locale   string // Optional Accept-Language locale hint
}

// EmailQueueService 异步邮件队列服务。Tasks are local, bounded and non-durable.
type EmailQueueService struct {
	emailService *EmailService
	taskChan     chan EmailTask
	wg           sync.WaitGroup
	workers      int

	mu           sync.RWMutex // serializes admission/Start with closing taskChan
	started      bool
	stopped      bool
	stopOnce     sync.Once
	workerCtx    context.Context
	workerCancel context.CancelFunc
	drainTimeout time.Duration
}

// NewEmailQueueService 创建邮件队列服务，保留自动启动行为。
func NewEmailQueueService(emailService *EmailService, workers int) *EmailQueueService {
	service := NewManagedEmailQueueService(emailService, workers)
	service.Start()
	return service
}

// NewManagedEmailQueueService creates only the local queue, with no workers.
// Enqueue retains its bounded-buffer contract before Start; the runtime must
// Start this service in the process receiving email requests, not in a remote
// worker that cannot see these local tasks.
func NewManagedEmailQueueService(emailService *EmailService, workers int) *EmailQueueService {
	if workers <= 0 {
		workers = 3 // 默认3个工作协程
	}
	return &EmailQueueService{
		emailService: emailService,
		taskChan:     make(chan EmailTask, 100), // 缓冲100个任务
		workers:      workers,
		drainTimeout: emailQueueDrainTimeout,
	}
}

// Start starts workers once. Start after Stop is a no-op.
func (s *EmailQueueService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.workerCtx, s.workerCancel = context.WithCancel(context.Background())
	s.wg.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go s.worker(i)
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Started %d workers", s.workers)
}

// worker drains the closed queue unless the shared shutdown budget expires.
func (s *EmailQueueService) worker(id int) {
	defer s.wg.Done()
	for {
		if s.workerCtx.Err() != nil {
			return
		}
		select {
		case task, ok := <-s.taskChan:
			if !ok || s.workerCtx.Err() != nil {
				return
			}
			s.processTask(id, task)
		case <-s.workerCtx.Done():
			return
		}
	}
}

// processTask 处理任务
func (s *EmailQueueService) processTask(workerID int, task EmailTask) {
	ctx, cancel := context.WithTimeout(s.workerCtx, 30*time.Second)
	defer cancel()

	switch task.TaskType {
	case TaskTypeVerifyCode:
		if err := s.emailService.SendVerifyCode(ctx, task.Email, task.SiteName, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send verify code to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent verify code to %s", workerID, task.Email)
		}
	case TaskTypePasswordReset:
		if err := s.emailService.SendPasswordResetEmailWithCooldown(ctx, task.Email, task.SiteName, task.ResetURL, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send password reset to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent password reset to %s", workerID, task.Email)
		}
	default:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d unknown task type: %s", workerID, task.TaskType)
	}
}

func (s *EmailQueueService) enqueue(task EmailTask) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stopped {
		return fmt.Errorf("email queue is stopped")
	}
	select {
	case s.taskChan <- task:
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// EnqueueVerifyCode 将验证码发送任务加入队列
func (s *EmailQueueService) EnqueueVerifyCode(email, siteName string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypeVerifyCode,
		Locale:   firstEmailLocale(locale),
	}
	if err := s.enqueue(task); err != nil {
		return err
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued verify code task for %s", email)
	return nil
}

// EnqueuePasswordReset 将密码重置邮件任务加入队列
func (s *EmailQueueService) EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypePasswordReset,
		ResetURL: resetURL,
		Locale:   firstEmailLocale(locale),
	}
	if err := s.enqueue(task); err != nil {
		return err
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued password reset task for %s", email)
	return nil
}

// Stop closes admission and tries to drain accepted work for 30 seconds before
// cancelling in-flight tasks. It always joins workers before returning so they
// cannot access dependencies after cleanup. Legacy SMTP ignores context but has
// its own dial/IO deadlines, so an in-flight SMTP call may exceed this budget.
// Stop before Start never starts mail delivery. Remaining tasks are not durable.
func (s *EmailQueueService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		if s.taskChan != nil {
			close(s.taskChan)
		}
		started, cancel := s.started, s.workerCancel
		s.mu.Unlock()

		if started {
			timer := time.AfterFunc(s.drainTimeout, cancel)
			s.wg.Wait()
			timer.Stop()
			cancel()
		}
		if remaining := len(s.taskChan); remaining > 0 {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Stopped with %d unsent local tasks (queue is not durable)", remaining)
		}
		logger.LegacyPrintf("service.email_queue", "%s", "[EmailQueue] All workers stopped")
	})
}
