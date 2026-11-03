//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type durableRefreshTestRepo struct {
	refreshAPIAccountRepo
	beginErr           error
	completeFailures   int
	completions        int
	ambiguous          bool
	active             bool
	generation         int64
	completeContextErr error
}

func (r *durableRefreshTestRepo) BeginOAuthRefresh(_ context.Context, a *Account, _ time.Duration) (*OAuthRefreshAttempt, error) {
	if r.beginErr != nil {
		return nil, r.beginErr
	}
	if r.active {
		return nil, ErrOAuthRefreshAmbiguous
	}
	r.active = true
	r.generation++
	return &OAuthRefreshAttempt{AccountID: a.ID, Generation: r.generation, Owner: "test-owner"}, nil
}
func (r *durableRefreshTestRepo) CompleteOAuthRefresh(ctx context.Context, _ *OAuthRefreshAttempt, _ *Account, next map[string]any) (bool, error) {
	r.completions++
	r.completeContextErr = ctx.Err()
	if r.completions <= r.completeFailures {
		return false, errors.New("unknown commit outcome")
	}
	r.account.Credentials = next
	return true, nil
}
func (r *durableRefreshTestRepo) MarkOAuthRefreshAmbiguous(context.Context, *OAuthRefreshAttempt) error {
	r.ambiguous = true
	return nil
}

func durableTestAccount() *Account {
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"refresh_token": "test-old"}}
}

func TestOAuthRefreshDurableRetriesOnlyPersistence(t *testing.T) {
	r := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: durableTestAccount()}, completeFailures: 1}
	api := NewOAuthRefreshAPI(r, nil)
	api.SetDurableRequired(true)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "test-new"}}
	result, err := api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, 2, r.completions)
	require.False(t, r.ambiguous)
}
func TestOAuthRefreshDurableQuarantineNeverReconsumes(t *testing.T) {
	r := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: durableTestAccount()}, completeFailures: 10}
	api := NewOAuthRefreshAPI(r, nil)
	api.SetDurableRequired(true)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "test-new"}}
	_, err := api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.True(t, r.ambiguous)
	_, err = api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.Equal(t, 1, executor.refreshCalls)
}
func TestOAuthRefreshDurableBeginBarrier(t *testing.T) {
	for _, barrier := range []error{ErrOAuthRefreshAmbiguous, ErrOAuthRefreshPending, errors.New("state storage missing")} {
		t.Run(barrier.Error(), func(t *testing.T) {
			r := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: durableTestAccount()}, beginErr: barrier}
			api := NewOAuthRefreshAPI(r, nil)
			api.SetDurableRequired(true)
			executor := &refreshAPIExecutorStub{needsRefresh: true}
			result, err := api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
			if errors.Is(barrier, ErrOAuthRefreshPending) {
				require.NoError(t, err)
				require.True(t, result.LockHeld)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, executor.refreshCalls)
		})
	}
}
func TestOAuthRefreshDurableLateResponseSalvaged(t *testing.T) {
	r := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: durableTestAccount()}}
	api := NewOAuthRefreshAPI(r, nil)
	api.SetDurableRequired(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "test-new"}, onRefresh: cancel}
	result, err := api.RefreshIfNeeded(ctx, r.account, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.NoError(t, r.completeContextErr)
}
func TestOAuthRefreshDurableProviderUnknownQuarantines(t *testing.T) {
	r := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: durableTestAccount()}}
	api := NewOAuthRefreshAPI(r, nil)
	api.SetDurableRequired(true)
	executor := &refreshAPIExecutorStub{needsRefresh: true, err: context.DeadlineExceeded}
	_, err := api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.True(t, r.ambiguous)
	require.Zero(t, r.completions)
	_, err = api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.Equal(t, 1, executor.refreshCalls)
}
func TestOAuthRefreshDurableMissingStateFailsClosed(t *testing.T) {
	r := &refreshAPIAccountRepo{account: durableTestAccount()}
	api := NewOAuthRefreshAPI(r, nil)
	api.SetDurableRequired(true)
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	_, err := api.RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.Zero(t, executor.refreshCalls)
}
