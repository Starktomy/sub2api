//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type manualRefreshStub struct {
	calls  int
	result *service.OAuthRefreshResult
	err    error
}

func (s *manualRefreshStub) RefreshOpenAIManually(context.Context, *service.Account, *service.OpenAIOAuthService) (*service.OAuthRefreshResult, error) {
	s.calls++
	return s.result, s.err
}

func TestOpenAIManualRefreshHandlersUseCoordinator(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		for _, outcome := range []string{"success", "lock", "error", "nil_result", "nil_api", "scoped"} {
			t.Run(map[bool]string{true: "dedicated", false: "generic"}[dedicated]+"/"+outcome, func(t *testing.T) {
				account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
				durable := &service.Account{ID: 7, Name: "durable", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
				api := &manualRefreshStub{result: &service.OAuthRefreshResult{Account: durable, Refreshed: true}}
				if outcome == "lock" {
					api.result = &service.OAuthRefreshResult{LockHeld: true}
				}
				if outcome == "error" {
					api.err = errors.New("refresh failed")
				}
				if outcome == "nil_result" {
					api.result = nil
				}
				svc := &scopedRefreshPrivacyService{stubAdminService: newStubAdminService()}
				svc.getAccountResult = account
				cache := &scopedCommitInvalidator{}
				var refresher openAIManualRefresher = api
				if outcome == "nil_api" {
					refresher = nil
				}
				ctx := context.Background()
				if outcome == "scoped" {
					ctx = adminaccess.WithPrincipal(ctx, &adminaccess.Principal{})
				}
				wantCode := 200
				switch outcome {
				case "lock":
					wantCode = 409
				case "error":
					wantCode = 500
				case "nil_api", "scoped", "nil_result":
					wantCode = 503
				}
				if dedicated {
					h := &OpenAIOAuthHandler{adminService: svc, oauthRefreshAPI: refresher, tokenCacheInvalidator: cache}
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/", nil).WithContext(ctx)
					c.Params = gin.Params{{Key: "id", Value: "7"}}
					h.RefreshAccountToken(c)
					require.Equal(t, wantCode, w.Code)
					if outcome == "success" {
						require.Contains(t, w.Body.String(), "durable")
					}
				} else {
					h := &AccountHandler{adminService: svc, oauthRefreshAPI: refresher, tokenCacheInvalidator: cache}
					updated, _, err := h.refreshSingleAccount(ctx, account)
					if outcome == "success" {
						require.NoError(t, err)
						require.Same(t, durable, updated)
					} else {
						require.Error(t, err)
						require.Equal(t, wantCode, infraerrors.Code(err))
					}
					if outcome == "success" || outcome == "error" {
						require.Equal(t, 1, svc.privacy)
					} else {
						require.Zero(t, svc.privacy)
					}
				}
				if outcome == "nil_api" || outcome == "scoped" {
					require.Zero(t, api.calls)
				} else {
					require.Equal(t, 1, api.calls)
				}
				require.Zero(t, svc.updateAccountCalls, "CAS result must not be overwritten through UpdateAccount")
				if outcome == "success" {
					require.Equal(t, []int64{7}, cache.ids)
				} else {
					require.Empty(t, cache.ids)
				}
			})
		}
	}
}

func TestScopedOpenAIManualRefreshDoesNotEnterWriteTransaction(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "authorized_unavailable"}[authorized], func(t *testing.T) {
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{1}}
			base := newStubAdminService()
			base.getAccountResult = account
			svc := &scopedBulkAccountService{AdminService: base}
			api := &manualRefreshStub{}
			h := &AccountHandler{adminService: svc, oauthRefreshAPI: api}
			policy := adminaccess.Policy{}
			if authorized {
				policy.Rules = []adminaccess.Rule{{Permissions: []string{"accounts.refresh"}, GroupIDs: []int64{1}, AccountTypes: []string{"oauth"}}}
			}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{Policy: policy}))
			})
			r.POST("/api/v1/admin/accounts/:id/refresh", h.ScopedAdminKeyAccounts)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/admin/accounts/7/refresh", nil))
			want := 404
			if authorized {
				want = 503
			}
			require.Equal(t, want, w.Code)
			require.Empty(t, svc.calls, "must not start scoped write transaction")
			require.Zero(t, api.calls, "must not call remote refresh")
		})
	}
}

type authorizedManualRefreshStub struct {
	manualRefreshStub
	authorizedCalls int
}

func (s *authorizedManualRefreshStub) RefreshOpenAIManuallyAuthorized(ctx context.Context, account *service.Account, oauth *service.OpenAIOAuthService) (*service.OAuthRefreshResult, error) {
	s.authorizedCalls++
	return s.result, s.err
}

func TestScopedOpenAIManualRefreshUsesAuthorizedPathOutsideTransaction(t *testing.T) {
	account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{1}}
	base := newStubAdminService()
	base.getAccountResult = account
	svc := &scopedBulkAccountService{AdminService: base}
	api := &authorizedManualRefreshStub{manualRefreshStub: manualRefreshStub{result: &service.OAuthRefreshResult{Account: account, Refreshed: true}}}
	h := &AccountHandler{adminService: svc, oauthRefreshAPI: api}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.refresh"}, GroupIDs: []int64{1}, AccountTypes: []string{"oauth"}}}}}))
	})
	r.POST("/api/v1/admin/accounts/:id/refresh", h.ScopedAdminKeyAccounts)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/admin/accounts/7/refresh", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, 1, api.authorizedCalls)
	require.Zero(t, api.calls, "legacy refresh must never bypass authorized persistence")
	require.Empty(t, svc.calls, "remote refresh must not enter ScopedAdminWrite")
}
