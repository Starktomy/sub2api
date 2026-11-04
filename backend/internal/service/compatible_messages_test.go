//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompatibleNativeMessagesExecution(t *testing.T) {
	for _, tc := range []struct{ platform, model, path, auth string }{
		{PlatformOpenRouter, "anthropic/claude-sonnet-4", "/api/v1/messages", "bearer"},
		{PlatformBailian, "qwen-plus", "/apps/anthropic/v1/messages", "key"},
		{PlatformTokenHub, "deepseek-v3.2", "/v1/messages", "key"},
		{PlatformSiliconFlow, "deepseek-ai/DeepSeek-V4-Flash", "/v1/messages", "bearer"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", tc.platform, stream), func(t *testing.T) {
				a := routingTestAccount(tc.platform, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolAnthropic}, map[string]any{ExtraKeyUpstreamProvider: tc.platform})
				response := nativeAnthropicBufferedResponse()
				if stream {
					response = nativeAnthropicStreamResponse()
				}
				upstream := &httpUpstreamRecorder{resp: response}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, tc.model, stream))
				result, err := svc.ForwardAsAnthropic(context.Background(), adaptiveProtocolTestContext("/v1/messages", body), a, body, "", "")
				if tc.platform == PlatformSiliconFlow && stream {
					require.Error(t, err)
					require.Nil(t, upstream.lastReq)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.path, upstream.lastReq.URL.Path)
				if tc.auth == "bearer" {
					require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
					require.Empty(t, upstream.lastReq.Header.Get("x-api-key"))
				} else {
					require.Equal(t, "sk-test", getHeaderRaw(upstream.lastReq.Header, "x-api-key"))
					require.Empty(t, upstream.lastReq.Header.Get("Authorization"))
				}
				require.Equal(t, "2023-06-01", getHeaderRaw(upstream.lastReq.Header, "anthropic-version"))
				require.Equal(t, 93, result.Usage.InputTokens)
				require.Equal(t, 16, result.Usage.OutputTokens)
			})
		}
	}
}

func TestCompatibleMessagesRestrictionsCannotBypass(t *testing.T) {
	a := routingTestAccount(PlatformSiliconFlow, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolAnthropic}, nil)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	body := []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	_, err := svc.ForwardAsChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), a, body, "", "")
	require.ErrorContains(t, err, "streaming") // converted path forces stream; final builder rejects
	testSvc := &AccountTestService{}
	err = testSvc.testCNProviderAnthropicConnection(adaptiveProtocolTestContext("/v1/messages", nil), a, "model")
	require.ErrorContains(t, err, "streaming")
	a = routingTestAccount(PlatformBailian, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolAnthropic}, nil)
	require.Error(t, validateNativeCompatibleMessages(a, []byte(`{}`), "unverified-model"))
	for _, platform := range []string{PlatformMistral, PlatformQianfanV2} {
		a = routingTestAccount(platform, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolAnthropic}, nil)
		require.Error(t, validateUpstreamProviderBinding(a))
	}
}
