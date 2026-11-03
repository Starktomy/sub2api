package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthRefreshFingerprintBindsEntireSnapshot(t *testing.T) {
	proxy := int64(9)
	original := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ProxyID: &proxy, Credentials: map[string]any{"refresh_token": "test-token", "nested": map[string]any{"k": "v"}}}
	baseline, err := oauthRefreshFingerprint(original, original.Credentials)
	require.NoError(t, err)
	require.Len(t, baseline, 64)
	for _, change := range []func(*service.Account){
		func(a *service.Account) { a.Platform = service.PlatformGemini },
		func(a *service.Account) { a.Type = service.AccountTypeAPIKey },
		func(a *service.Account) { a.ProxyID = nil },
		func(a *service.Account) { a.Credentials = map[string]any{"refresh_token": "test-other"} },
	} {
		copy := *original
		change(&copy)
		fingerprint, err := oauthRefreshFingerprint(&copy, copy.Credentials)
		require.NoError(t, err)
		require.NotEqual(t, baseline, fingerprint)
	}
	require.NotContains(t, baseline, "test-token")
}
func TestOAuthRefreshRecoveryRejectsUnsafeAccountBeforeSQL(t *testing.T) {
	repo := &accountRepository{}
	applied, err := repo.ResolveOAuthRefreshAmbiguous(context.Background(), nil, 1)
	require.Error(t, err)
	require.False(t, applied)
	applied, err = repo.ResolveOAuthRefreshAmbiguous(context.Background(), &service.Account{ID: 1, Status: service.StatusActive, Schedulable: true}, 1)
	require.Error(t, err)
	require.False(t, applied)
}

func TestOAuthRefreshQuarantineFencesGenerationAndOwner(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	err := repo.MarkOAuthRefreshAmbiguous(context.Background(), &service.OAuthRefreshAttempt{AccountID: 1, Generation: 4, Owner: "old-owner"})
	require.NoError(t, err)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "generation=$2 AND owner=$3 AND state='pending'")
	require.NotContains(t, exec.execQueries[0], "credentials")
}
