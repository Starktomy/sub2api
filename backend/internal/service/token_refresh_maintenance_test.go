//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTokenRefreshMaintenanceSkipsUnfencedPostActions(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	s := &TokenRefreshService{accountRepo: repo, maintenanceSafe: true}
	until := time.Now().Add(time.Hour)
	a := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: "missing_project_id: old", TempUnschedulableUntil: &until, Extra: map[string]any{antigravityForceTokenRefreshExtraKey: true}}
	s.postRefreshActions(context.Background(), a)
	require.Zero(t, repo.updateExtraCalls)
	require.Zero(t, repo.clearTempCalls)
	require.True(t, accountNeedsAntigravityForceTokenRefresh(a))
}

func TestTokenRefreshMaintenanceRejectsLegacyExecutor(t *testing.T) {
	s := &TokenRefreshService{maintenanceSafe: true, cfg: &config.TokenRefreshConfig{MaxRetries: 1}}
	err := s.refreshWithRetry(context.Background(), &Account{ID: 1}, nil, nil, time.Minute)
	require.Error(t, err)
	var configErr *providerConfigurationRefreshError
	require.ErrorAs(t, err, &configErr)
}

func TestTokenRefreshMaintenanceDurableAllPlatformsNoReplay(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			a := durableTestAccount()
			a.Platform = platform
			repo := &durableRefreshTestRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: a}, completeFailures: 10}
			api := NewOAuthRefreshAPI(repo, nil)
			api.SetDurableRequired(true)
			svc := &TokenRefreshService{accountRepo: repo, refreshAPI: api, maintenanceSafe: true, cfg: &config.TokenRefreshConfig{MaxRetries: 3}, refreshPolicy: DefaultBackgroundRefreshPolicy()}
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "new"}}
			err := svc.refreshWithRetry(context.Background(), a, executor, executor, time.Minute)
			require.Error(t, err)
			require.True(t, repo.ambiguous)
			require.Equal(t, 1, executor.refreshCalls)
			err = svc.refreshWithRetry(context.Background(), a, executor, executor, time.Minute)
			require.Error(t, err)
			require.Equal(t, 1, executor.refreshCalls)
		})
	}
}
