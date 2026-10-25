package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	openAIQuotaRecoveryScanInterval        = time.Minute
	openAIQuotaRecoveryInitialScanDelay    = 3 * time.Second
	openAIQuotaRecoveryScanLead            = 30 * time.Second
	openAIQuotaRecoveryBatchSize           = 64
	openAIQuotaRecoveryWorkerCount         = 4
	openAIQuotaRecoveryQueryTimeout        = 25 * time.Second
	openAIQuotaRecoveryQueryFailureBackoff = time.Minute
	openAIQuotaRecoveryLeaderLockKey       = "jobs:openai-quota-window-recovery"
	// A full batch may take 16 sequential query slots at the configured worker
	// width. Keep the lease beyond that upper bound so another instance does
	// not start a duplicate reconciliation pass while the current owner is
	// still querying upstream.
	openAIQuotaRecoveryLeaderLockTTL    = (openAIQuotaRecoveryBatchSize/openAIQuotaRecoveryWorkerCount)*openAIQuotaRecoveryQueryTimeout + 2*time.Minute
	openAIQuotaRecoveryExhaustedPercent = 100.0
)

// openAIQuotaRecoveryQuota is deliberately narrower than OpenAIQuotaService so
// the recovery worker cannot consume reset credits.
type openAIQuotaRecoveryQuota interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
}

// openAIQuotaRecoveryCandidateLister is implemented by the concrete account
// repository. Keeping it optional avoids broadening AccountRepository solely
// for a periodic maintenance job.
type openAIQuotaRecoveryCandidateLister interface {
	ListOpenAIQuotaRecoveryCandidates(ctx context.Context, resetBefore time.Time, limit int) ([]Account, error)
}

type openAIQuotaRecoveryClearer interface {
	ClearRateLimitIfObserved(ctx context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error)
}

type openAIQuotaRecoverySetter interface {
	SetRateLimitedIfUnchanged(ctx context.Context, id int64, expectedUpdatedAt time.Time, expectedLimitedAt, expectedResetAt *time.Time, newResetAt time.Time) (bool, error)
}

type openAIQuotaRecoveryAction uint8

const (
	openAIQuotaRecoveryActionUncertain openAIQuotaRecoveryAction = iota
	openAIQuotaRecoveryActionClear
	openAIQuotaRecoveryActionRearm
)

type openAIQuotaRecoveryDecision struct {
	action  openAIQuotaRecoveryAction
	resetAt time.Time
}

// OpenAIQuotaRecoveryService reconciles an account-level local 429 cooldown
// with the official /wham/usage response around a known quota-window boundary.
// It does not consume paid reset credits. The database compare-and-set writes
// make stale probes harmless when a request receives a newer 429 concurrently.
type OpenAIQuotaRecoveryService struct {
	accountRepo    AccountRepository
	quota          openAIQuotaRecoveryQuota
	runtimeBlocker AccountRuntimeBlocker
	leaderLock     LeaderLockCache

	ctx    context.Context
	cancel context.CancelFunc
	owner  string
	start  sync.Once
	stop   sync.Once
	wg     sync.WaitGroup

	retryMu    sync.Mutex
	retryAfter map[int64]time.Time
}

func NewOpenAIQuotaRecoveryService(
	accountRepo AccountRepository,
	quota openAIQuotaRecoveryQuota,
	runtimeBlocker AccountRuntimeBlocker,
	leaderLock LeaderLockCache,
) *OpenAIQuotaRecoveryService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenAIQuotaRecoveryService{
		accountRepo:    accountRepo,
		quota:          quota,
		runtimeBlocker: runtimeBlocker,
		leaderLock:     leaderLock,
		ctx:            ctx,
		cancel:         cancel,
		owner:          uuid.NewString(),
		retryAfter:     make(map[int64]time.Time),
	}
}

func (s *OpenAIQuotaRecoveryService) Start() {
	if s == nil || s.accountRepo == nil || s.quota == nil {
		return
	}
	s.start.Do(func() {
		s.wg.Add(1)
		go s.runScanner()
	})
}

func (s *OpenAIQuotaRecoveryService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		s.cancel()
		s.wg.Wait()
	})
}

func (s *OpenAIQuotaRecoveryService) runScanner() {
	defer s.wg.Done()

	timer := time.NewTimer(openAIQuotaRecoveryInitialScanDelay)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-timer.C:
		s.scan(s.ctx)
	}

	ticker := time.NewTicker(openAIQuotaRecoveryScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scan(s.ctx)
		}
	}
}

func (s *OpenAIQuotaRecoveryService) scan(ctx context.Context) {
	if s == nil || s.accountRepo == nil || s.quota == nil {
		return
	}
	release, acquired := tryAcquireSingletonLeaderLock(
		ctx,
		s.leaderLock,
		nil,
		openAIQuotaRecoveryLeaderLockKey,
		s.owner,
		openAIQuotaRecoveryLeaderLockTTL,
	)
	if !acquired {
		return
	}
	defer release()

	now := time.Now()
	candidates, err := s.listCandidates(ctx, now.Add(openAIQuotaRecoveryScanLead))
	if err != nil {
		slog.Warn("openai_quota_recovery_scan_failed", "error", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	workerCount := min(openAIQuotaRecoveryWorkerCount, len(candidates))
	jobs := make(chan *Account)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for account := range jobs {
				s.recoverCandidate(ctx, account)
			}
		}()
	}
	for i := range candidates {
		account := candidates[i]
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		case jobs <- &account:
		}
	}
	close(jobs)
	workers.Wait()
}

func (s *OpenAIQuotaRecoveryService) listCandidates(ctx context.Context, resetBefore time.Time) ([]Account, error) {
	if lister, ok := s.accountRepo.(openAIQuotaRecoveryCandidateLister); ok {
		return lister.ListOpenAIQuotaRecoveryCandidates(ctx, resetBefore, openAIQuotaRecoveryBatchSize)
	}

	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, "", "", 0, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	result := make([]Account, 0, min(len(accounts), openAIQuotaRecoveryBatchSize))
	for i := range accounts {
		if !isOpenAIQuotaRecoveryCandidate(&accounts[i], now, resetBefore) {
			continue
		}
		result = append(result, accounts[i])
		if len(result) >= openAIQuotaRecoveryBatchSize {
			break
		}
	}
	return result, nil
}

func (s *OpenAIQuotaRecoveryService) recoverCandidate(parent context.Context, observed *Account) {
	if s == nil || observed == nil || s.accountRepo == nil || s.quota == nil {
		return
	}
	now := time.Now()
	if !isOpenAIQuotaRecoveryCandidate(observed, now, now.Add(openAIQuotaRecoveryScanLead)) ||
		s.queryFailureBackoffActive(observed.ID, now) {
		return
	}
	if observed.RateLimitedAt == nil || observed.RateLimitResetAt == nil {
		return
	}

	ctx, cancel := context.WithTimeout(parent, openAIQuotaRecoveryQueryTimeout)
	defer cancel()
	usage, err := s.quota.QueryUsage(ctx, observed.ID)
	if err != nil || usage == nil {
		s.deferRetry(observed.ID)
		if err != nil {
			slog.Warn("openai_quota_recovery_query_failed", "account_id", observed.ID, "error", err)
		} else {
			slog.Warn("openai_quota_recovery_query_empty", "account_id", observed.ID)
		}
		return
	}

	updates := buildOpenAIAutoResetUsageUpdates(usage, time.Now())
	if len(updates) > 0 {
		if err := s.accountRepo.UpdateExtra(ctx, observed.ID, updates); err != nil {
			s.deferRetry(observed.ID)
			slog.Warn("openai_quota_recovery_snapshot_write_failed", "account_id", observed.ID, "error", err)
			return
		}
	}

	// UpdateExtra intentionally bumps UpdatedAt. Re-read before the cooldown CAS,
	// then require the rate-limit generation originally probed to still be current.
	current, err := s.accountRepo.GetByID(ctx, observed.ID)
	if err != nil || current == nil {
		s.deferRetry(observed.ID)
		if err != nil {
			slog.Warn("openai_quota_recovery_account_load_failed", "account_id", observed.ID, "error", err)
		}
		return
	}
	now = time.Now()
	if !isOpenAIQuotaRecoveryCandidate(current, now, now.Add(openAIQuotaRecoveryScanLead)) ||
		!sameOpenAIQuotaRecoveryGeneration(observed, current) {
		s.clearRetry(observed.ID)
		return
	}

	decision := decideOpenAIQuotaRecovery(usage, now)
	switch decision.action {
	case openAIQuotaRecoveryActionClear:
		clearer, ok := s.accountRepo.(openAIQuotaRecoveryClearer)
		if !ok {
			s.deferRetry(observed.ID)
			slog.Warn("openai_quota_recovery_clear_unsupported", "account_id", observed.ID)
			return
		}
		cleared, err := clearer.ClearRateLimitIfObserved(ctx, current.ID, *current.RateLimitedAt, *current.RateLimitResetAt)
		if err != nil {
			s.deferRetry(observed.ID)
			slog.Warn("openai_quota_recovery_clear_failed", "account_id", observed.ID, "error", err)
			return
		}
		if cleared && s.runtimeBlocker != nil {
			s.runtimeBlocker.ClearAccountSchedulingBlock(current.ID)
		}
		s.clearRetry(observed.ID)
		return

	case openAIQuotaRecoveryActionRearm:
		setter, ok := s.accountRepo.(openAIQuotaRecoverySetter)
		if !ok {
			s.deferRetry(observed.ID)
			slog.Warn("openai_quota_recovery_rearm_unsupported", "account_id", observed.ID)
			return
		}
		rearmed, err := setter.SetRateLimitedIfUnchanged(
			ctx,
			current.ID,
			current.UpdatedAt,
			current.RateLimitedAt,
			current.RateLimitResetAt,
			decision.resetAt,
		)
		if err != nil {
			s.deferRetry(observed.ID)
			slog.Warn("openai_quota_recovery_rearm_failed", "account_id", observed.ID, "error", err)
			return
		}
		if rearmed && s.runtimeBlocker != nil {
			s.runtimeBlocker.BlockAccountScheduling(current, decision.resetAt, "openai_quota_window_recovery")
		}
		s.clearRetry(observed.ID)
		return

	default:
		// The upstream says it is unavailable but did not supply a complete,
		// future reset boundary. Preserve the current cooldown and retry later.
		s.deferRetry(observed.ID)
		slog.Warn("openai_quota_recovery_snapshot_uncertain", "account_id", observed.ID)
	}
}

func isOpenAIQuotaRecoveryCandidate(account *Account, now, resetBefore time.Time) bool {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		account.IsShadow() || !account.IsActive() || !account.Schedulable ||
		account.RateLimitedAt == nil || account.RateLimitResetAt == nil ||
		account.RateLimitResetAt.After(resetBefore) {
		return false
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return false
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		return false
	}
	return account.TempUnschedulableUntil == nil || !now.Before(*account.TempUnschedulableUntil)
}

func sameOpenAIQuotaRecoveryGeneration(observed, current *Account) bool {
	if observed == nil || current == nil || observed.RateLimitedAt == nil || observed.RateLimitResetAt == nil ||
		current.RateLimitedAt == nil || current.RateLimitResetAt == nil {
		return false
	}
	return observed.RateLimitedAt.Equal(*current.RateLimitedAt) &&
		observed.RateLimitResetAt.Equal(*current.RateLimitResetAt)
}

func decideOpenAIQuotaRecovery(usage *OpenAIQuotaUsage, now time.Time) openAIQuotaRecoveryDecision {
	if usage == nil || usage.RateLimit == nil {
		return openAIQuotaRecoveryDecision{action: openAIQuotaRecoveryActionUncertain}
	}
	rateLimit := usage.RateLimit
	if rateLimit.Allowed {
		return openAIQuotaRecoveryDecision{action: openAIQuotaRecoveryActionClear}
	}

	knownWindows := 0
	allKnownBelowLimit := true
	latestReset := time.Time{}
	for _, window := range []*OpenAIRateLimitWindow{rateLimit.PrimaryWindow, rateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		knownWindows++
		if window.UsedPercent < openAIQuotaRecoveryExhaustedPercent {
			continue
		}
		allKnownBelowLimit = false
		resetAt, ok := openAIQuotaRecoveryResetAt(usage, window, now)
		if !ok {
			continue
		}
		if resetAt.After(latestReset) {
			latestReset = resetAt
		}
	}
	if !latestReset.IsZero() {
		return openAIQuotaRecoveryDecision{action: openAIQuotaRecoveryActionRearm, resetAt: latestReset}
	}
	if knownWindows > 0 && allKnownBelowLimit {
		return openAIQuotaRecoveryDecision{action: openAIQuotaRecoveryActionClear}
	}
	return openAIQuotaRecoveryDecision{action: openAIQuotaRecoveryActionUncertain}
}

func openAIQuotaRecoveryResetAt(usage *OpenAIQuotaUsage, window *OpenAIRateLimitWindow, now time.Time) (time.Time, bool) {
	if window == nil {
		return time.Time{}, false
	}
	if window.ResetAt > 0 {
		resetAt := time.Unix(window.ResetAt, 0)
		if resetAt.After(now) {
			return resetAt, true
		}
	}
	if window.ResetAfterSeconds <= 0 {
		return time.Time{}, false
	}
	base := now
	if usage != nil && usage.FetchedAt > 0 {
		base = time.Unix(usage.FetchedAt, 0)
	}
	resetAt := base.Add(time.Duration(window.ResetAfterSeconds) * time.Second)
	return resetAt, resetAt.After(now)
}

func (s *OpenAIQuotaRecoveryService) queryFailureBackoffActive(accountID int64, now time.Time) bool {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	until, ok := s.retryAfter[accountID]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(s.retryAfter, accountID)
	return false
}

func (s *OpenAIQuotaRecoveryService) deferRetry(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	if s.retryAfter == nil {
		s.retryAfter = make(map[int64]time.Time)
	}
	s.retryAfter[accountID] = time.Now().Add(openAIQuotaRecoveryQueryFailureBackoff)
}

func (s *OpenAIQuotaRecoveryService) clearRetry(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	delete(s.retryAfter, accountID)
}
