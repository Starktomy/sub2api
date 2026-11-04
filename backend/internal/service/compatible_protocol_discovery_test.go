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

func TestCompatibleStatelessResponsesPreSend(t *testing.T) {
	for _, platform := range []string{PlatformOpenRouter, PlatformMistral} {
		a := routingTestAccount(platform, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolResponses}, nil)
		s := &OpenAIGatewayService{}
		for _, field := range []string{`"store":true`, `"previous_response_id":"resp_old"`, `"conversation":"conv_old"`, `"background":true`} {
			body := []byte(`{"model":"model","input":"hi",` + field + `}`)
			_, err := s.Forward(context.Background(), adaptiveProtocolTestContext("/v1/responses", body), a, body)
			require.Error(t, err) // nil transport proves rejection before sending
		}
		require.NoError(t, validateCompatibleResponsesBody(a, []byte(`{"model":"model","input":"hi","store":false}`)))
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid test model"}}`))}}
		s = &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		body := []byte(`{"model":"model","input":"hi","store":false}`)
		_, _ = s.Forward(context.Background(), adaptiveProtocolTestContext("/v1/responses", body), a, body)
		require.NotNil(t, upstream.lastReq)
		wantPath := "/v1/responses"
		if platform == PlatformOpenRouter {
			wantPath = "/api/v1/responses"
		}
		require.Equal(t, wantPath, upstream.lastReq.URL.Path)
	}
}

func TestCompatibleModelsFreshBoundedPagination(t *testing.T) {
	response := func(body string) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	}
	a := routingTestAccount(PlatformOpenRouter, AccountTypeAPIKey, nil, nil)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		response(`{"data":[{"id":"one"}],"total_count":2,"links":{"next":"?offset=1&limit=500"}}`),
		response(`{"data":[{"id":"two"}],"total_count":2,"links":{"next":null}}`),
	}}
	s := &AccountTestService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	models, _, err := s.fetchCompatibleModels(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, models)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "1", upstream.requests[1].URL.Query().Get("offset"))
	for _, link := range []string{"https://untrusted.example/v1/models?offset=1&limit=500", "?offset=0&limit=500", "?cursor=secret", "/different?offset=1&limit=500"} {
		upstream.responses = []*http.Response{response(`{"data":[{"id":"one"}],"links":{"next":"` + link + `"}}`)}
		models, _, err := s.fetchCompatibleModels(context.Background(), a)
		require.Error(t, err)
		require.Nil(t, models)
	}
	for _, platform := range []string{PlatformMistral, PlatformQianfanV2} {
		account := routingTestAccount(platform, AccountTypeAPIKey, nil, nil)
		upstream.resp = response(`{"data":[{"id":"listed-only"}]}`)
		models, _, err := s.fetchCompatibleModels(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, []string{"listed-only"}, models)
		account.Credentials["api_key"] = "rotated"
		upstream.resp = response(`{"data":[]}`)
		models, _, err = s.fetchCompatibleModels(context.Background(), account)
		require.Error(t, err)
		require.Nil(t, models)
		require.Equal(t, "Bearer rotated", upstream.lastReq.Header.Get("Authorization"))
	}
}
