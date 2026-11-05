package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type supplierExecutionReaderStub struct {
	SupplierResourceRepository
	resource *SupplierResource
	err      error
	calls    int
}

func (r *supplierExecutionReaderStub) GetExecutionResource(context.Context, int64, string) (*SupplierResource, error) {
	r.calls++
	return r.resource, r.err
}

func TestSupplierAccountCredentialIdentity(t *testing.T) {
	s, r := supplierExecutionFixture(t)
	reader := &supplierExecutionReaderStub{resource: r}
	s.repo = reader
	a := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"supplier_resource_id": r.ID, "chatgpt_account_id": r.AccountHint}}
	got, err := s.ResolveAccountCredentials(context.Background(), a)
	require.ErrorIs(t, err, ErrSupplierDispatchUnavailable)
	require.Nil(t, got)
	dispatch := &supplierGatewayDispatch{accepted: SupplierAcceptedDispatch{
		Resource: r, Snapshot: SupplierDispatchSnapshot{AccountID: a.ID, ResourceID: r.ID},
	}}
	ctx := context.WithValue(context.Background(), supplierDispatchKey{}, dispatch)
	got, err = s.ResolveAccountCredentials(ctx, a)
	require.NoError(t, err)
	require.Equal(t, "synthetic-execution-token", got.AccessToken)
	a.Credentials["chatgpt_account_id"] = "forged-header"
	got, err = s.ResolveAccountCredentials(ctx, a)
	require.ErrorIs(t, err, ErrSupplierResource)
	require.Nil(t, got)
	a.Credentials["chatgpt_account_id"] = r.AccountHint
	// Current binding rotates/revokes after acceptance: this attempt must still
	// decrypt the exact envelope returned by acceptance, never reread current.
	reader.resource = &SupplierResource{ID: "rotated-current"}
	reader.err = ErrSupplierResource
	got, err = s.ResolveAccountCredentials(ctx, a)
	require.NoError(t, err)
	require.Equal(t, "synthetic-execution-token", got.AccessToken)
	require.Zero(t, reader.calls)
}

func TestSupplierAccountGuardsRejectLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name               string
		credentials, extra map[string]any
	}{
		{"valid reference", map[string]any{"supplier_resource_id": "resource"}, nil},
		{"empty reference", map[string]any{"supplier_resource_id": ""}, nil},
		{"malformed reference", map[string]any{"supplier_resource_id": nil}, nil},
		{"extra only", nil, map[string]any{"supplier_resource_id": "resource"}},
		{"owner marker only", nil, map[string]any{"supplier_id": int64(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: tc.credentials, Extra: tc.extra}
			if a.Credentials == nil {
				a.Credentials = map[string]any{}
			}
			a.Credentials["access_token"] = "forbidden-plaintext"
			a.Credentials["refresh_token"] = "forbidden-refresh"
			require.True(t, supplierAccountMarked(a))
			p := &OpenAITokenProvider{}
			token, err := p.GetAccessToken(context.Background(), a)
			require.ErrorIs(t, err, ErrSupplierVault)
			require.Empty(t, token)
			gateway := &OpenAIGatewayService{}
			token, _, err = gateway.GetAccessToken(context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1}), a)
			require.ErrorIs(t, err, ErrSupplierVault)
			require.Empty(t, token)
			gateway.cfg = &config.Config{RunMode: config.RunModeSimple}
			token, _, err = gateway.GetAccessToken(context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1}), a)
			require.ErrorIs(t, err, ErrSupplierResource)
			require.Empty(t, token)
			err = gateway.ProxyResponsesWebSocketFromClient(context.Background(), nil, nil, a, "forbidden-token", nil, nil)
			require.ErrorIs(t, err, ErrSupplierResource)
			headers, _, err := gateway.buildOpenAIWSHeaders(context.Background(), nil, a, "forbidden-token", OpenAIWSProtocolDecision{}, false, "", "", "", "", "")
			require.ErrorIs(t, err, ErrSupplierResource)
			require.Nil(t, headers)
			generic := &GatewayService{}
			token, _, err = generic.GetAccessToken(context.Background(), a)
			require.ErrorIs(t, err, ErrSupplierResource)
			require.Empty(t, token)
			refresh := &OpenAITokenRefresher{}
			require.False(t, refresh.CanRefresh(a))
			require.False(t, refresh.NeedsRefresh(a, time.Hour))
			credentials, err := refresh.Refresh(context.Background(), a)
			require.ErrorIs(t, err, ErrSupplierResource)
			require.Nil(t, credentials)
			oauth := &OpenAIOAuthService{}
			info, err := oauth.RefreshAccountToken(context.Background(), a)
			require.ErrorIs(t, err, ErrSupplierResource)
			require.Nil(t, info)
		})
	}
}

func TestSupplierWSProtocolUsesHTTP(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"supplier_resource_id": "resource"}}
	decision := NewOpenAIWSProtocolResolver(nil).Resolve(account)
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, decision.Transport)
	require.Equal(t, "supplier_requires_per_request_http", decision.Reason)
}

func TestSupplierGatewayRequiresAuthenticatedGroup(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"supplier_resource_id": "resource"}}
	token, _, err := (&OpenAIGatewayService{}).GetAccessToken(context.Background(), account)
	require.ErrorIs(t, err, ErrSupplierResource)
	require.Empty(t, token)
}
