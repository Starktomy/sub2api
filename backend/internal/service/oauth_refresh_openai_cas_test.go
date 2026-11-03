//go:build unit

package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIRefreshCASRepo struct {
	refreshAPIAccountRepo
	beforeCAS func()
	applied   bool
}

func (r *openAIRefreshCASRepo) UpdateOAuthCredentialsIfUnchanged(_ context.Context, id int64, expected map[string]any, proxy *int64, next map[string]any) (bool, error) {
	if r.beforeCAS != nil {
		r.beforeCAS()
	}
	if r.updateErr != nil {
		return false, r.updateErr
	}
	if r.account.ID != id || !reflect.DeepEqual(r.account.Credentials, expected) || !reflect.DeepEqual(r.account.ProxyID, proxy) {
		return false, nil
	}
	r.account.Credentials = shallowCopyMap(next)
	r.applied = true
	return true, nil
}

func TestOAuthRefreshOpenAIRejectsMissingCASBeforeUpstream(t *testing.T) {
	r := &refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}}
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	_, err := NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.Zero(t, executor.refreshCalls)
}

func TestOAuthRefreshOpenAICASRejectsStaleSuccess(t *testing.T) {
	r := &openAIRefreshCASRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"refresh_token": "old"}}}}
	r.beforeCAS = func() { r.account.Credentials = map[string]any{"refresh_token": "reauthorized"} }
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "stale-result"}}
	result, err := NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), snapshotOAuthRefreshAccount(r.account), executor, time.Minute)
	require.NoError(t, err)
	require.False(t, result.Refreshed)
	require.False(t, r.applied)
	require.Equal(t, "reauthorized", result.Account.GetCredential("refresh_token"))
}

func TestOAuthRefreshOpenAICASSuccessAndAmbiguousPersist(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "persist-error"}[fail], func(t *testing.T) {
			r := &openAIRefreshCASRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"refresh_token": "old"}}}}
			if fail {
				r.updateErr = errors.New("database unavailable")
			}
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "new"}}
			result, err := NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), snapshotOAuthRefreshAccount(r.account), executor, time.Minute)
			if fail {
				require.Error(t, err)
				var contained *providerCycleContainmentRefreshError
				require.ErrorAs(t, err, &contained)
				return
			}
			require.NoError(t, err)
			require.True(t, result.Refreshed)
			require.True(t, r.applied)
			require.Equal(t, "new", result.Account.GetCredential("refresh_token"))
		})
	}
}

type ownedRefreshTestCache struct {
	refreshAPICacheStub
	acquireErr    error
	acquired      bool
	releasedOwner string
	seenOwner     string
}

func (c *ownedRefreshTestCache) AcquireRefreshLease(_ context.Context, _ string, owner string, _ time.Duration) (bool, error) {
	c.seenOwner = owner
	return c.acquired, c.acquireErr
}
func (c *ownedRefreshTestCache) ReleaseRefreshLease(_ context.Context, _ string, owner string) error {
	c.releasedOwner = owner
	return nil
}

func TestOAuthRefreshOwnedLeaseReleaseAndBusy(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		r := &refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeOAuth}}
		cache := &ownedRefreshTestCache{acquired: acquired}
		executor := &refreshAPIExecutorStub{needsRefresh: false}
		result, err := NewOAuthRefreshAPI(r, cache).RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
		require.NoError(t, err)
		require.Len(t, cache.seenOwner, 48)
		if acquired {
			require.Equal(t, cache.seenOwner, cache.releasedOwner)
			require.False(t, result.LockHeld)
		} else {
			require.Empty(t, cache.releasedOwner)
			require.True(t, result.LockHeld)
			require.Zero(t, r.getByIDCalls)
		}
	}
}

func TestOAuthManualRefreshSkipsChangedCredentials(t *testing.T) {
	r := &refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "newer"}}}
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	ctx := context.WithValue(context.Background(), oauthManualRefreshKey{}, true)
	result, err := NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(ctx, &Account{ID: 1, Credentials: map[string]any{"refresh_token": "older"}}, executor, time.Minute)
	require.NoError(t, err)
	require.False(t, result.Refreshed)
	require.Zero(t, executor.refreshCalls)
}

func TestOAuthRefreshOwnedLeaseFailsClosed(t *testing.T) {
	r := &refreshAPIAccountRepo{account: &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeOAuth}}
	cache := &ownedRefreshTestCache{acquireErr: errors.New("redis unavailable")}
	executor := &refreshAPIExecutorStub{needsRefresh: true}
	_, err := NewOAuthRefreshAPI(r, cache).RefreshIfNeeded(context.Background(), r.account, executor, time.Minute)
	require.Error(t, err)
	require.Zero(t, executor.refreshCalls)
	require.Zero(t, r.getByIDCalls)
}
