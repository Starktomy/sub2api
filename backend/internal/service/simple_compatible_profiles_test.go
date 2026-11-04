//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSimpleCompatibleProfilesActualRoutes(t *testing.T) {
	cases := []struct{ platform, host, path string }{
		{PlatformOpenRouter, "openrouter.ai", "/api/v1/chat/completions"},
		{PlatformSiliconFlow, "api.siliconflow.cn", "/v1/chat/completions"},
		{PlatformMistral, "api.mistral.ai", "/v1/chat/completions"},
		{PlatformBailian, "dashscope.aliyuncs.com", "/compatible-mode/v1/chat/completions"},
		{PlatformTokenHub, "tokenhub.tencentmaas.com", "/v1/chat/completions"},
		{PlatformQianfanV2, "qianfan.baidubce.com", "/v2/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			profile := LookupProviderProfile(tc.platform)
			require.NotNil(t, profile)
			require.True(t, profile.SimpleCompatible)
			require.Equal(t, tc.platform == PlatformOpenRouter || tc.platform == PlatformMistral || tc.platform == PlatformQianfanV2, *profile.ModelDiscovery)
			account := routingTestAccount(tc.platform, AccountTypeAPIKey, map[string]any{"model_mapping": map[string]any{"public": "native-model"}}, map[string]any{ExtraKeyUpstreamProvider: tc.platform})
			account.Credentials["api_key"] = "test-compatible"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chat-test","model":"native-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			body := []byte(`{"model":"public","messages":[{"role":"user","content":"hi"}],"stream":false}`)
			result, err := svc.ForwardAsChatCompletions(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), account, body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.host, upstream.lastReq.URL.Host)
			require.Equal(t, tc.path, upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer test-compatible", upstream.lastReq.Header.Get("Authorization"))
			require.Empty(t, upstream.lastReq.Header.Get("HTTP-Referer"))
			require.Empty(t, upstream.lastReq.Header.Get("X-OpenRouter-Title"))
			require.Contains(t, string(upstream.lastBody), "native-model")
			account.Credentials["api_protocol"] = "unsupported-protocol"
			require.Error(t, validateUpstreamProviderBinding(account))
			account.Credentials["api_protocol"] = APIProtocolChatCompletions
			account.Credentials["api_key"] = "ak|sk|legacy"
			require.Error(t, validateUpstreamProviderBinding(account))
		})
	}
}

func TestRerankRequestContractFailClosedSubset(t *testing.T) {
	require.NoError(t, ValidateRerankRequest([]byte(`{"model":"rerank","query":"query","documents":["document"],"top_n":1}`)))
	for _, body := range []string{`{}`, `{"model":"r","query":"q","documents":[]}`, `{"model":"r","query":"q","documents":["d"],"stream":true}`, `{"model":"r","query":"q","documents":[{"text":"d"}]}`, `{"model":"r","query":"q","documents":["d"],"top_n":2}`} {
		require.Error(t, ValidateRerankRequest([]byte(body)))
	}
	require.Error(t, ValidateRerankRequest(make([]byte, MaxRerankBodyBytes+1)))
}
