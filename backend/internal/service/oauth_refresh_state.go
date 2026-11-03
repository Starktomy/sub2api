package service

import (
	"context"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// SetDurableRequired selects fail-closed durable coordination. Call only during
// construction, before any refresh requests; legacy all-in-one defaults unchanged.
func (api *OAuthRefreshAPI) SetDurableRequired(required bool) { api.durableRequired = required }

var (
	ErrOAuthRefreshPending   = errors.New("OAuth refresh attempt is pending")
	ErrOAuthRefreshAmbiguous = errors.New("OAuth refresh outcome is ambiguous; operator reauthorization is required")
)

// OAuthRefreshAttempt is a durable fencing identity, not a provider idempotency key.
// Expiry never authorizes another call with a possibly consumed refresh token.
type OAuthRefreshAttempt struct {
	AccountID  int64
	Generation int64
	Owner      string
}

// OAuthRefreshStateRepository must commit pending before any upstream request.
// Complete atomically compares owner/generation and the entire input snapshot
// (including platform/type/proxy), publishes outbox invalidation and completes.
// It is idempotent for the same attempt, so unknown DB outcomes can be recovered
// without invoking OAuth again. No database transaction spans a provider call.
type OAuthRefreshStateRepository interface {
	BeginOAuthRefresh(context.Context, *Account, time.Duration) (*OAuthRefreshAttempt, error)
	CompleteOAuthRefresh(context.Context, *OAuthRefreshAttempt, *Account, map[string]any) (bool, error)
	MarkOAuthRefreshAmbiguous(context.Context, *OAuthRefreshAttempt) error
}

// runDurableOAuthRefresh deliberately quarantines every failed/unknown provider
// outcome, even errors that might be safe to retry. Generic provider errors do
// not prove the refresh token was not consumed. Pending survives process death.
func (api *OAuthRefreshAPI) runDurableOAuthRefresh(ctx context.Context, cacheKey string, account *Account, executor OAuthRefreshExecutor, repo OAuthRefreshStateRepository) (*OAuthRefreshResult, error) {
	if dbent.TxFromContext(ctx) != nil {
		return nil, errors.New("OAuth refresh cannot run inside a database transaction")
	}
	expected := snapshotOAuthRefreshAccount(account)
	// Provider executors may mutate maps in place. Keep the committed input
	// snapshot independent, including nested credential documents.
	var err error
	expected.Credentials, err = cloneAccountJSONMap(account.Credentials)
	if err != nil {
		return nil, errors.New("OAuth refresh credential snapshot failed")
	}
	remote := snapshotOAuthRefreshAccount(expected)
	remote.Credentials, err = cloneAccountJSONMap(expected.Credentials)
	if err != nil {
		return nil, errors.New("OAuth refresh credential snapshot failed")
	}
	attempt, err := repo.BeginOAuthRefresh(ctx, expected, api.lockTTL)
	if errors.Is(err, ErrOAuthRefreshPending) {
		return &OAuthRefreshResult{Account: expected, LockHeld: true}, nil
	}
	if err != nil {
		return nil, &providerCycleContainmentRefreshError{err: err}
	}
	credentials, refreshErr := executor.Refresh(ctx, remote)
	// Even a canceled/expired remote call may return valid rotated credentials.
	// Salvage them on a detached bounded context; never discard them just because
	// the request's deadline expired. These retries repeat only local persistence.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*defaultRefreshPostPersistCleanupTimeout)
	defer cancel()
	if refreshErr == nil && credentials != nil {
		credentials["_token_version"] = time.Now().UnixMilli()
		var applied bool
		for i := 0; i < 2; i++ {
			applied, err = repo.CompleteOAuthRefresh(cleanup, attempt, expected, credentials)
			if err == nil {
				break
			}
		}
		if err == nil && applied {
			durable, readErr := api.loadGrokDurableAccountAfterPersist(cleanup, cacheKey, account.ID)
			if readErr == nil && durable != nil {
				return &OAuthRefreshResult{Refreshed: true, NewCredentials: durable.Credentials, Account: durable}, nil
			}
			// Completion is durable; a later request can reread it without rotating.
			return nil, &providerCycleContainmentRefreshError{err: errOAuthRefreshAccountRereadFailed}
		}
	}
	// If DB is unavailable this marker may also fail: the committed pending row
	// remains a permanent retry barrier and will be classified ambiguous on expiry.
	_ = repo.MarkOAuthRefreshAmbiguous(cleanup, attempt)
	// There is no encrypted recovery spool yet. If both persistence attempts fail
	// or the process dies after provider rotation but before commit, returned tokens
	// cannot be recovered here. Operators must stop previous holders, reauthorize,
	// then explicitly call ResolveOAuthRefreshAmbiguous to advance the fence.
	// Never place credentials or raw provider/SQL errors in logs or attempt tables.
	return &OAuthRefreshResult{Account: expected}, &providerCycleContainmentRefreshError{err: ErrOAuthRefreshAmbiguous}
}
