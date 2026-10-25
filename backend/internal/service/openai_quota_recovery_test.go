package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIQuotaRecoveryTestRepo struct {
	AccountRepository

	mu          sync.Mutex
	accounts    map[int64]*Account
	version     int64
	updateCalls int
	clearCalls  int
	setCalls    int
	updateErr   error

	beforeClear func(*Account)
}

func (r *openAIQuotaRecoveryTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return cloneOpenAIQuotaRecoveryAccount(account), nil
}

func (r *openAIQuotaRecoveryTestRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	r.updateCalls++
	r.bump(account)
	return nil
}

func (r *openAIQuotaRecoveryTestRepo) ListOpenAIQuotaRecoveryCandidates(_ context.Context, resetBefore time.Time, limit int) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	result := make([]Account, 0)
	for _, account := range r.accounts {
		if !isOpenAIQuotaRecoveryCandidate(account, now, resetBefore) {
			continue
		}
		result = append(result, *cloneOpenAIQuotaRecoveryAccount(account))
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *openAIQuotaRecoveryTestRepo) ClearRateLimitIfObserved(_ context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return false, ErrAccountNotFound
	}
	if r.beforeClear != nil {
		r.beforeClear(account)
		r.beforeClear = nil
	}
	r.clearCalls++
	if account.RateLimitedAt == nil || account.RateLimitResetAt == nil ||
		!account.RateLimitedAt.Equal(observedLimitedAt) || !account.RateLimitResetAt.Equal(observedResetAt) {
		return false, nil
	}
	account.RateLimitedAt = nil
	account.RateLimitResetAt = nil
	r.bump(account)
	return true, nil
}

func (r *openAIQuotaRecoveryTestRepo) SetRateLimitedIfUnchanged(
	_ context.Context,
	id int64,
	expectedUpdatedAt time.Time,
	expectedLimitedAt, expectedResetAt *time.Time,
	newResetAt time.Time,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return false, ErrAccountNotFound
	}
	r.setCalls++
	if !account.UpdatedAt.Equal(expectedUpdatedAt) || !equalOpenAIQuotaRecoveryTimePtr(account.RateLimitedAt, expectedLimitedAt) ||
		!equalOpenAIQuotaRecoveryTimePtr(account.RateLimitResetAt, expectedResetAt) {
		return false, nil
	}
	now := time.Now().UTC()
	account.RateLimitedAt = &now
	reset := newResetAt
	account.RateLimitResetAt = &reset
	r.bump(account)
	return true, nil
}

func (r *openAIQuotaRecoveryTestRepo) bump(account *Account) {
	r.version++
	account.UpdatedAt = time.Unix(1_700_000_000, r.version).UTC()
}

func (r *openAIQuotaRecoveryTestRepo) account(id int64) *Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneOpenAIQuotaRecoveryAccount(r.accounts[id])
}

func cloneOpenAIQuotaRecoveryAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	copy := *account
	copy.Extra = cloneOpenAIAutoResetExtra(account.Extra)
	copy.RateLimitedAt = cloneTimePtr(account.RateLimitedAt)
	copy.RateLimitResetAt = cloneTimePtr(account.RateLimitResetAt)
	copy.OverloadUntil = cloneTimePtr(account.OverloadUntil)
	copy.TempUnschedulableUntil = cloneTimePtr(account.TempUnschedulableUntil)
	copy.ExpiresAt = cloneTimePtr(account.ExpiresAt)
	return &copy
}

func equalOpenAIQuotaRecoveryTimePtr(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

type openAIQuotaRecoveryTestQuota struct {
	mu    sync.Mutex
	usage *OpenAIQuotaUsage
	err   error
	calls int
}

func (q *openAIQuotaRecoveryTestQuota) QueryUsage(context.Context, int64) (*OpenAIQuotaUsage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.err != nil {
		return nil, q.err
	}
	return q.usage, nil
}

func (q *openAIQuotaRecoveryTestQuota) callCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

type openAIQuotaRecoveryRuntimeBlocker struct {
	mu      sync.Mutex
	blocks  []openAIQuotaRecoveryBlock
	cleared []int64
}

type openAIQuotaRecoveryBlock struct {
	accountID int64
	until     time.Time
	reason    string
}

func (b *openAIQuotaRecoveryRuntimeBlocker) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if account != nil {
		b.blocks = append(b.blocks, openAIQuotaRecoveryBlock{accountID: account.ID, until: until, reason: reason})
	}
}

func (b *openAIQuotaRecoveryRuntimeBlocker) ClearAccountSchedulingBlock(accountID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleared = append(b.cleared, accountID)
}

func (b *openAIQuotaRecoveryRuntimeBlocker) snapshot() ([]openAIQuotaRecoveryBlock, []int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]openAIQuotaRecoveryBlock(nil), b.blocks...), append([]int64(nil), b.cleared...)
}

func newOpenAIQuotaRecoveryLimitedAccount(id int64) *Account {
	limitedAt := time.Now().Add(-time.Minute).UTC()
	resetAt := time.Now().Add(-10 * time.Second).UTC()
	return &Account{
		ID:               id,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeOAuth,
		Status:           StatusActive,
		Schedulable:      true,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &resetAt,
		UpdatedAt:        time.Unix(1_700_000_000, 0).UTC(),
		Extra:            map[string]any{},
	}
}

func TestOpenAIQuotaRecoveryService_ClearsCooldownWhenUpstreamAllows(t *testing.T) {
	account := newOpenAIQuotaRecoveryLimitedAccount(701)
	repo := &openAIQuotaRecoveryTestRepo{accounts: map[int64]*Account{account.ID: account}}
	now := time.Now().UTC()
	quota := &openAIQuotaRecoveryTestQuota{usage: &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			Allowed: true,
			PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent:        12,
				LimitWindowSeconds: 5 * 60 * 60,
				ResetAfterSeconds:  90 * 60,
			},
		},
	}}
	blocker := &openAIQuotaRecoveryRuntimeBlocker{}
	service := NewOpenAIQuotaRecoveryService(repo, quota, blocker, nil)

	service.recoverCandidate(context.Background(), cloneOpenAIQuotaRecoveryAccount(account))

	got := repo.account(account.ID)
	require.Nil(t, got.RateLimitedAt)
	require.Nil(t, got.RateLimitResetAt)
	require.Equal(t, 1, repo.clearCalls)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 12.0, got.Extra["codex_5h_used_percent"])
	blocks, cleared := blocker.snapshot()
	require.Empty(t, blocks)
	require.Equal(t, []int64{account.ID}, cleared)
}

func TestOpenAIQuotaRecoveryService_RearmsToLatestExhaustedWindow(t *testing.T) {
	account := newOpenAIQuotaRecoveryLimitedAccount(702)
	repo := &openAIQuotaRecoveryTestRepo{accounts: map[int64]*Account{account.ID: account}}
	now := time.Now().UTC()
	shortReset := now.Add(5 * time.Minute).Truncate(time.Second)
	longReset := now.Add(95 * time.Minute).Truncate(time.Second)
	quota := &openAIQuotaRecoveryTestQuota{usage: &OpenAIQuotaUsage{
		FetchedAt: now.Unix(),
		RateLimit: &OpenAIRateLimit{
			PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent:        100,
				LimitWindowSeconds: 5 * 60 * 60,
				ResetAt:            shortReset.Unix(),
			},
			SecondaryWindow: &OpenAIRateLimitWindow{
				UsedPercent:        100,
				LimitWindowSeconds: 30 * 24 * 60 * 60,
				ResetAt:            longReset.Unix(),
			},
		},
	}}
	blocker := &openAIQuotaRecoveryRuntimeBlocker{}
	service := NewOpenAIQuotaRecoveryService(repo, quota, blocker, nil)

	service.recoverCandidate(context.Background(), cloneOpenAIQuotaRecoveryAccount(account))

	got := repo.account(account.ID)
	require.NotNil(t, got.RateLimitResetAt)
	require.WithinDuration(t, longReset, *got.RateLimitResetAt, time.Second)
	require.Equal(t, 1, repo.setCalls)
	blocks, cleared := blocker.snapshot()
	require.Len(t, blocks, 1)
	require.Equal(t, account.ID, blocks[0].accountID)
	require.Equal(t, "openai_quota_window_recovery", blocks[0].reason)
	require.WithinDuration(t, longReset, blocks[0].until, time.Second)
	require.Empty(t, cleared)
}

func TestOpenAIQuotaRecoveryService_StaleProbeCannotClearNewer429(t *testing.T) {
	account := newOpenAIQuotaRecoveryLimitedAccount(703)
	repo := &openAIQuotaRecoveryTestRepo{accounts: map[int64]*Account{account.ID: account}}
	freshLimitedAt := time.Now().UTC()
	freshReset := time.Now().Add(2 * time.Hour).UTC()
	repo.beforeClear = func(current *Account) {
		current.RateLimitedAt = &freshLimitedAt
		current.RateLimitResetAt = &freshReset
		repo.bump(current)
	}
	quota := &openAIQuotaRecoveryTestQuota{usage: &OpenAIQuotaUsage{
		FetchedAt: time.Now().Unix(),
		RateLimit: &OpenAIRateLimit{Allowed: true},
	}}
	blocker := &openAIQuotaRecoveryRuntimeBlocker{}
	service := NewOpenAIQuotaRecoveryService(repo, quota, blocker, nil)

	service.recoverCandidate(context.Background(), cloneOpenAIQuotaRecoveryAccount(account))

	got := repo.account(account.ID)
	require.NotNil(t, got.RateLimitedAt)
	require.NotNil(t, got.RateLimitResetAt)
	require.WithinDuration(t, freshLimitedAt, *got.RateLimitedAt, time.Second)
	require.WithinDuration(t, freshReset, *got.RateLimitResetAt, time.Second)
	require.Equal(t, 1, repo.clearCalls)
	_, cleared := blocker.snapshot()
	require.Empty(t, cleared)
}

func TestOpenAIQuotaRecoveryService_BacksOffAfterQueryFailure(t *testing.T) {
	account := newOpenAIQuotaRecoveryLimitedAccount(704)
	repo := &openAIQuotaRecoveryTestRepo{accounts: map[int64]*Account{account.ID: account}}
	quota := &openAIQuotaRecoveryTestQuota{err: errors.New("upstream unavailable")}
	service := NewOpenAIQuotaRecoveryService(repo, quota, nil, nil)

	service.recoverCandidate(context.Background(), cloneOpenAIQuotaRecoveryAccount(account))
	service.recoverCandidate(context.Background(), cloneOpenAIQuotaRecoveryAccount(account))

	require.Equal(t, 1, quota.callCount())
	require.True(t, service.queryFailureBackoffActive(account.ID, time.Now()))
}

func TestOpenAIQuotaRecoveryLeaderLeaseCoversFullBatch(t *testing.T) {
	maxQuerySlots := (openAIQuotaRecoveryBatchSize + openAIQuotaRecoveryWorkerCount - 1) / openAIQuotaRecoveryWorkerCount
	require.Greater(t, openAIQuotaRecoveryLeaderLockTTL, time.Duration(maxQuerySlots)*openAIQuotaRecoveryQueryTimeout)
}

func TestDecideOpenAIQuotaRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name       string
		usage      *OpenAIQuotaUsage
		wantAction openAIQuotaRecoveryAction
		wantReset  time.Time
	}{
		{
			name: "allowed clears even when a stale window is full",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				Allowed:       true,
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, ResetAt: now.Add(time.Hour).Unix()},
			}},
			wantAction: openAIQuotaRecoveryActionClear,
		},
		{
			name: "known windows below limit clear",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 99.5, LimitWindowSeconds: 5 * 60 * 60},
				SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 37, LimitWindowSeconds: 7 * 24 * 60 * 60},
			}},
			wantAction: openAIQuotaRecoveryActionClear,
		},
		{
			name: "five hour window re-arms",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 5 * 60 * 60, ResetAt: now.Add(10 * time.Minute).Unix()},
			}},
			wantAction: openAIQuotaRecoveryActionRearm,
			wantReset:  now.Add(10 * time.Minute),
		},
		{
			name: "missing exhausted reset remains uncertain",
			usage: &OpenAIQuotaUsage{RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 100, LimitWindowSeconds: 5 * 60 * 60},
			}},
			wantAction: openAIQuotaRecoveryActionUncertain,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := decideOpenAIQuotaRecovery(test.usage, now)
			require.Equal(t, test.wantAction, decision.action)
			if test.wantReset.IsZero() {
				require.True(t, decision.resetAt.IsZero())
				return
			}
			require.WithinDuration(t, test.wantReset, decision.resetAt, time.Second)
		})
	}
}
