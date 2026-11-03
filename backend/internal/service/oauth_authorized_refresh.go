package service

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OAuthRefreshAuthorization pins the authenticated key generation across the
// remote call. It contains no usable key material and is never logged.
type OAuthRefreshAuthorization struct {
	KeyHash   string
	UpdatedAt time.Time
}
type oauthRefreshAuthorizationKey struct{}

func WithOAuthRefreshAuthorization(ctx context.Context, authorization OAuthRefreshAuthorization) context.Context {
	return context.WithValue(ctx, oauthRefreshAuthorizationKey{}, authorization)
}

func OAuthRefreshAuthorizationFromContext(ctx context.Context) (OAuthRefreshAuthorization, bool) {
	authorization, ok := ctx.Value(oauthRefreshAuthorizationKey{}).(OAuthRefreshAuthorization)
	return authorization, ok
}

// RefreshOpenAIManuallyAuthorized authorizes before any remote work without
// retaining a transaction. Begin and completion must revalidate this pinned
// principal under their own short durable-state transaction.
func (api *OAuthRefreshAPI) RefreshOpenAIManuallyAuthorized(ctx context.Context, account *Account, oauth *OpenAIOAuthService) (*OAuthRefreshResult, error) {
	if dbent.TxFromContext(ctx) != nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_REFRESH_TRANSACTION", "OpenAI refresh cannot run inside a database transaction")
	}
	if api == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_REFRESH_NOT_CONFIGURED", "OpenAI refresh coordinator is not configured")
	}
	if adminaccess.FromContext(ctx) != nil {
		// Never authorize a key against a legacy persistence fallback that cannot
		// revalidate the principal atomically with the durable credential commit.
		if _, ok := api.accountRepo.(OAuthRefreshStateRepository); !ok {
			return nil, infraerrors.ServiceUnavailable("OPENAI_SCOPED_REFRESH_UNAVAILABLE", "Authorized OpenAI refresh persistence is not configured")
		}
		repo, ok := api.accountRepo.(interface {
			PrepareOAuthRefreshAuthorization(context.Context, int64) (context.Context, error)
		})
		if !ok || account == nil {
			return nil, infraerrors.ServiceUnavailable("OPENAI_SCOPED_REFRESH_UNAVAILABLE", "Authorized OpenAI refresh persistence is not configured")
		}
		var err error
		ctx, err = repo.PrepareOAuthRefreshAuthorization(ctx, account.ID)
		if err != nil {
			return nil, err
		}
	}
	return api.RefreshOpenAIManually(ctx, account, oauth)
}
