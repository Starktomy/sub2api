package admin

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIManualRefresher interface {
	RefreshOpenAIManually(context.Context, *service.Account, *service.OpenAIOAuthService) (*service.OAuthRefreshResult, error)
}

// SetOAuthRefreshAPI preserves constructor compatibility while sharing the same
// refresh coordinator used by gateway and background refresh paths.
func (h *AccountHandler) SetOAuthRefreshAPI(api *service.OAuthRefreshAPI) {
	if api != nil {
		h.oauthRefreshAPI = api
	} else {
		h.oauthRefreshAPI = nil
	}
}

func (h *OpenAIOAuthHandler) SetOAuthRefreshAPI(api *service.OAuthRefreshAPI) {
	if api != nil {
		h.oauthRefreshAPI = api
	} else {
		h.oauthRefreshAPI = nil
	}
}

func validateOpenAIManualRefresh(ctx context.Context, api openAIManualRefresher) error {
	if dbent.TxFromContext(ctx) != nil {
		return infraerrors.ServiceUnavailable("OPENAI_SCOPED_REFRESH_UNAVAILABLE", "OpenAI refresh requires transaction-free authorized credential persistence")
	}
	if api == nil {
		return infraerrors.ServiceUnavailable("OPENAI_REFRESH_NOT_CONFIGURED", "OpenAI refresh coordinator is not configured")
	}
	if adminaccess.FromContext(ctx) != nil {
		if _, ok := api.(openAIAuthorizedManualRefresher); !ok {
			return infraerrors.ServiceUnavailable("OPENAI_SCOPED_REFRESH_UNAVAILABLE", "Authorized OpenAI refresh persistence is not configured")
		}
	}
	return nil
}

type openAIAuthorizedManualRefresher interface {
	RefreshOpenAIManuallyAuthorized(context.Context, *service.Account, *service.OpenAIOAuthService) (*service.OAuthRefreshResult, error)
}

func refreshOpenAIManually(ctx context.Context, api openAIManualRefresher, account *service.Account, oauth *service.OpenAIOAuthService) (*service.OAuthRefreshResult, error) {
	if err := validateOpenAIManualRefresh(ctx, api); err != nil {
		return nil, err
	}
	if authorized, ok := api.(openAIAuthorizedManualRefresher); ok {
		return authorized.RefreshOpenAIManuallyAuthorized(ctx, account, oauth)
	}
	return api.RefreshOpenAIManually(ctx, account, oauth)
}

func openAIManualRefreshAccount(result *service.OAuthRefreshResult) (*service.Account, error) {
	if result != nil && result.LockHeld {
		return nil, infraerrors.Conflict("OAUTH_REFRESH_IN_PROGRESS", "Account credentials are already being refreshed")
	}
	if result == nil || result.Account == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_REFRESH_RESULT_UNAVAILABLE", "OpenAI refresh did not return a durable account")
	}
	return result.Account, nil
}
