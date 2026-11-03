//go:build integration

package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Exercise real durable PG state -> real service/refresher -> concrete HTTP
// client, without importing a real account or contacting a provider.
func (s *AccountRepoSuite) TestOAuthRefreshConcreteHTTPDurableNoReplay() {
	s.useDurableRefreshClient()
	for _, outcome := range []string{"success", "disconnect"} {
		s.Run(outcome, func() {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if outcome == "disconnect" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-rotated","expires_in":3600,"token_type":"Bearer"}`))
			}))
			defer server.Close()
			a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "http-durable-" + outcome, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old", "refresh_token": "fixture-refresh", "expires_at": strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)}})
			expected, err := s.repo.GetByID(s.ctx, a.ID)
			s.Require().NoError(err)
			oauth := service.NewManagedOpenAIOAuthService(nil, &openaiOAuthService{tokenURL: server.URL})
			defer oauth.Stop()
			executor := service.NewOpenAITokenRefresher(oauth, s.repo)
			api := service.NewOAuthRefreshAPI(s.repo, nil)
			api.SetDurableRequired(true)
			ctx := refreshpolicy.WithSingleAttempt(context.Background())
			result, err := api.RefreshIfNeeded(ctx, expected, executor, time.Minute)
			if outcome == "success" {
				s.Require().NoError(err)
				s.Require().True(result.Refreshed)
			} else {
				s.Require().ErrorIs(err, service.ErrOAuthRefreshAmbiguous)
			}
			s.Require().Equal(int32(1), calls.Load())
			// Reconstruct coordinator to exclude its local mutex/history as the barrier.
			next := service.NewOAuthRefreshAPI(s.repo, nil)
			next.SetDurableRequired(true)
			_, err = next.RefreshIfNeeded(ctx, expected, executor, time.Minute)
			if outcome == "success" {
				s.Require().NoError(err)
			} else {
				s.Require().ErrorIs(err, service.ErrOAuthRefreshAmbiguous)
			}
			s.Require().Equal(int32(1), calls.Load())
			var state string
			s.Require().NoError(integrationDB.QueryRowContext(ctx, "SELECT state FROM oauth_refresh_state WHERE account_id=$1", a.ID).Scan(&state))
			want := "completed"
			if outcome == "disconnect" {
				want = "ambiguous"
			}
			s.Require().Equal(want, state)
		})
	}
}
