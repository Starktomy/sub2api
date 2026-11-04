//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestModularProviderGatewayRoute(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, tc := range []struct {
			name, mode string
			observed   any
			want       string
		}{
			{"unknown", "auto", nil, "/v1/responses"},
			{"observed yes", "auto", true, "/v1/responses"},
			{"observed no", "auto", false, "/v1/chat/completions"},
			{"force responses beats observed no", "force_responses", false, "/v1/responses"},
			{"force chat beats observed yes", "force_chat_completions", true, "/v1/chat/completions"},
			{"invalid observation", "invalid", "false", "/v1/responses"},
		} {
			t.Run(tc.name+map[bool]string{false: " legacy", true: " explicit"}[explicit], func(t *testing.T) {
				account := rawChatCompletionsTestAccount()
				account.Extra = map[string]any{openai_compat.ExtraKeyResponsesMode: tc.mode, openai_compat.ExtraKeyResponsesSupported: tc.observed}
				if explicit {
					account.Extra[ExtraKeyUpstreamProvider] = openai_compat.ProviderID
				}
				response := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
				contentType := "text/event-stream"
				if tc.want == "/v1/chat/completions" {
					response = `{"id":"chatcmpl_test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
					contentType = "application/json"
				}
				recorder := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: recorder}
				body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
				_, err := svc.ForwardAsChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), account, body, "", "")
				require.NoError(t, err)
				require.Equal(t, tc.want, recorder.lastReq.URL.Path)
			})
		}
	}
}

func TestModularProviderRejectsBeforeForward(t *testing.T) {
	for _, binding := range []any{"unknown", "", 42, nil} {
		account := rawChatCompletionsTestAccount()
		account.Extra = map[string]any{ExtraKeyUpstreamProvider: binding}
		svc := &OpenAIGatewayService{} // any attempted transport would panic
		body := []byte(`{"model":"gpt-5.4","input":"hi"}`)
		ctx := adaptiveProtocolTestContext("/v1/responses", body)
		_, err := svc.Forward(context.Background(), ctx, account, body)
		require.Error(t, err)
		_, err = svc.ForwardAsChatCompletions(context.Background(), ctx, account, body, "", "")
		require.Error(t, err)
		_, err = svc.ForwardAsAnthropic(context.Background(), ctx, account, body, "", "")
		require.Error(t, err)
		connectionTest := &AccountTestService{}
		require.Error(t, connectionTest.testOpenAIAccountConnection(ctx, account, "gpt-5.4", "hi", ""))
		// The binding guard must fire before touching the socket or preparing auth.
		err = svc.ProxyResponsesWebSocketFromClient(context.Background(), ctx, new(coderws.Conn), account, "", body, nil)
		require.ErrorContains(t, err, "upstream")
	}
	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{Platform: PlatformKimi, Type: AccountTypeOAuth},
	} {
		require.NoError(t, validateUpstreamProviderBinding(account))
		account.Extra = map[string]any{ExtraKeyUpstreamProvider: openai_compat.ProviderID}
		require.Error(t, validateUpstreamProviderBinding(account))
	}
}

func TestModularProviderCatalogAndNativeCredentials(t *testing.T) {
	catalog := UpstreamProviderCatalog()
	require.Len(t, catalog, 23)
	previous := ""
	for _, d := range catalog {
		require.Greater(t, d.ID, previous)
		previous = d.ID
		require.NotEmpty(t, d.ExecutionFamily)
		for _, typ := range d.AccountTypes {
			account := &Account{Platform: d.Platform, Type: typ, Extra: map[string]any{ExtraKeyUpstreamProvider: d.ID}}
			require.NoError(t, validateUpstreamProviderBinding(account), d.ID)
			got, err := moduleCredentialType(account)
			require.NoError(t, err)
			require.Equal(t, typ, got, d.ID)
			account.Type = AccountTypeOAuth
			require.Error(t, validateUpstreamProviderBinding(account), d.ID)
		}
	}
	catalog[0].Capabilities["streaming"] = "secret"
	catalog[0].AccountTypes[0] = "oauth"
	require.NotEqual(t, catalog, UpstreamProviderCatalog())
	for platform := range providerProfiles {
		account := routingTestAccount(platform, AccountTypeAPIKey, nil, nil)
		legacy := resolveLegacyUpstreamProtocol(account, APIProtocolResponses, "gpt-5.4", nil)
		account.Extra = map[string]any{ExtraKeyUpstreamProvider: platform, openai_compat.ExtraKeyResponsesMode: "force_chat_completions"}
		require.Equal(t, legacy, resolveUpstreamProtocol(account, APIProtocolResponses, "gpt-5.4", nil), platform)
	}
}

func TestModularProviderNonTextFailClosed(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{ExtraKeyUpstreamProvider: "unknown"}}
	s := &OpenAIGatewayService{}
	c := adaptiveProtocolTestContext("/v1/images/generations", nil)
	_, err := s.ForwardImages(context.Background(), c, a, nil, nil, "")
	require.ErrorContains(t, err, "upstream")
	_, err = s.ForwardSeedance(context.Background(), c, a, "", "", nil)
	require.ErrorContains(t, err, "upstream")
	_, err = s.ForwardGrokVoice(context.Background(), c, a, "", nil, "")
	require.ErrorContains(t, err, "upstream")
	_, err = s.OpenGrokRealtime(context.Background(), a, "", "")
	require.ErrorContains(t, err, "upstream")
	g := &GatewayService{}
	_, err = g.ForwardSystemOne(context.Background(), c, a, nil)
	require.ErrorContains(t, err, "upstream")
	gemini := &GeminiMessagesCompatService{}
	_, err = gemini.ForwardNative(context.Background(), c, a, "", "", false, nil)
	require.ErrorContains(t, err, "upstream")
	_, err = gemini.Forward(context.Background(), c, a, nil)
	require.ErrorContains(t, err, "upstream")
	gp := &GeminiAPIBatchImageProvider{}
	_, err = gp.Submit(context.Background(), nil, a, BatchImageInput{})
	require.ErrorContains(t, err, "upstream")
	vp := &VertexBatchImageProvider{}
	_, err = vp.Submit(context.Background(), nil, a, BatchImageInput{})
	require.ErrorContains(t, err, "upstream")
}

func TestModularProviderBindingUpdates(t *testing.T) {
	a := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	require.NoError(t, validateUpstreamProviderUpdates(a, map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}))
	require.Error(t, validateUpstreamProviderUpdates(a, map[string]any{ExtraKeyUpstreamProvider: "anthropic-apikey"}))
	require.NoError(t, validateUpstreamProviderUpdates(a, map[string]any{ExtraKeyUpstreamProvider: nil}))
	require.Nil(t, a.Extra)
}

func TestModularProviderVertexExpressExecution(t *testing.T) {
	svc, upstream := newGeminiVertexAPIKeyTestService()
	c, _ := newGeminiNativeTestContext(t)
	account := geminiVertexAPIKeyTestAccount("")
	account.Extra = map[string]any{ExtraKeyUpstreamProvider: "gemini-vertex"}
	result, err := svc.ForwardNative(context.Background(), c, account, "gemini-3-flash-preview", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	requireVertexAPIKeyAuth(t, upstream.lastReq)
	require.Equal(t, "aiplatform.googleapis.com", upstream.lastReq.URL.Host)
	require.Contains(t, upstream.lastReq.URL.Path, "publishers/google/models")
}

func TestModularProviderTypeSafeExecution(t *testing.T) {
	svc, repo, requests, c, _ := newTypeSafeAccountTestFixture(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{"connection_test":{"type":"noul"}},"usage":{"input_tokens":9}}`)
	repo.account.Extra = map[string]any{ExtraKeyUpstreamProvider: "typesafe"}
	require.NoError(t, svc.TestAccountConnection(c, 31, "", "", AccountTestModeDefault))
	require.Len(t, *requests, 1)
	require.Equal(t, "/v1/systemone", (*requests)[0].URL.Path)
	require.Equal(t, "Bearer ts-secret", (*requests)[0].Header.Get("Authorization"))
}

func TestModularProviderCreateAndAdminWriteValidation(t *testing.T) {
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAccountRepo{}}
	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Name: "invalid", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}, SkipDefaultGroupBind: true, Extra: map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}})
	require.ErrorContains(t, err, "upstream provider")
	for _, d := range UpstreamProviderCatalog() {
		account, err := buildAccountForCreate(&CreateAccountInput{Platform: d.Platform, Type: d.AccountTypes[0]}, map[string]any{ExtraKeyUpstreamProvider: d.ID})
		require.NoError(t, err, d.ID)
		require.Equal(t, d.ID, defaultUpstreamProviderID(account))
	}
}

func TestModularProviderBatchExecution(t *testing.T) {
	client := &fakeGeminiBatchClient{uploaded: &GeminiUploadedFile{Name: "files/input-jsonl"}, created: &GeminiBatchJob{Name: "batches/module-123", State: "JOB_STATE_PENDING"}}
	provider := NewGeminiAPIBatchImageProvider(client)
	account := geminiAPIKeyAccount("module-key")
	account.Extra = map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}
	result, err := provider.Submit(context.Background(), &BatchImageJob{BatchID: "module-batch", Model: "gemini-3.1-flash-image"}, account, validGeminiBatchInput())
	require.NoError(t, err)
	require.Equal(t, "batches/module-123", result.ProviderJobName)
	require.Equal(t, []string{"upload", "create"}, client.calls)
	account.Extra[ExtraKeyUpstreamProvider] = "gemini-vertex"
	require.False(t, provider.SupportsAccount(account))
}

func TestModularProviderAnthropicVertexExecution(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeServiceAccount, Credentials: map[string]any{"project_id": "vertex-proj", "location": "us-east5"}, Extra: map[string]any{ExtraKeyUpstreamProvider: "anthropic-vertex"}}
	c := adaptiveProtocolTestContext("/v1/messages", nil)
	s := &GatewayService{}
	req, _, err := s.buildUpstreamRequest(context.Background(), c, account, []byte(`{"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`), "vertex-token", "service_account", "claude-sonnet-4-5@20250929", false, false)
	require.NoError(t, err)
	require.Contains(t, req.URL.Path, "publishers/anthropic/models")
	require.Equal(t, "Bearer vertex-token", getHeaderRaw(req.Header, "authorization"))
	require.Empty(t, getHeaderRaw(req.Header, "x-api-key"))
}

func TestModularProviderAdminUpdateAndBulkRejectBeforeWrite(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}}}
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{upstreamBillingProbeAccountRepo: repo}}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}})
	require.ErrorContains(t, err, "upstream provider")
	require.ErrorContains(t, svc.UpdateAccountExtra(context.Background(), 1, map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}), "upstream provider")
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{ExtraKeyUpstreamProvider: "gemini-apikey"}})
	require.ErrorContains(t, err, "upstream provider")
}
