//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompositeCandidatesValidationRoundtrip(t *testing.T) {
	input := CompositeRouteInput{PublicModel: "gpt6luna", Endpoint: CompositeRouteEndpointResponses, Enabled: true, Candidates: []CompositeRouteCandidate{{PlatformOpenAI, "oauth-alias", true}, {PlatformOpenCodeGo, "code-alias", true}}}
	route, err := compositeRouteFromInput(10, input)
	require.NoError(t, err)
	require.Equal(t, "oauth-alias", route.UpstreamModel)
	raw, err := json.Marshal(route)
	require.NoError(t, err)
	var decoded CompositeModelRoute
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, route.Candidates, decoded.Candidates)
	resolver := NewCompositeRouteResolver(&compositeRouteRepoStub{routes: []CompositeModelRoute{decoded}})
	decision, err := resolver.Resolve(context.Background(), 10, "gpt6luna", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Len(t, decision.Route.Candidates, 2)
	for _, endpoint := range []string{"any", "images", "embeddings", "count_tokens", "gemini"} {
		unsupported := input
		unsupported.Endpoint = endpoint
		_, err := compositeRouteFromInput(10, unsupported)
		require.Error(t, err)
	}
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformTypeSafe, "unknown"} {
		unsupported := input
		unsupported.Candidates = []CompositeRouteCandidate{{TargetPlatform: platform, UpstreamModel: "alias", Enabled: true}}
		_, err := compositeRouteFromInput(10, unsupported)
		require.Error(t, err)
	}
	prefix := input
	prefix.MatchType = "prefix"
	_, err = compositeRouteFromInput(10, prefix)
	require.Error(t, err)
	for _, tc := range []CompositeRouteInput{
		{PublicModel: "x", Endpoint: "images", Candidates: input.Candidates},
		{PublicModel: "x", Endpoint: "responses", Candidates: []CompositeRouteCandidate{{PlatformComposite, "x", true}}},
		{PublicModel: "x", Endpoint: "responses", Candidates: []CompositeRouteCandidate{{PlatformOpenAI, "bad\nmodel", true}}},
		{PublicModel: "x", Endpoint: "responses", Candidates: []CompositeRouteCandidate{{PlatformOpenAI, "x", false}}},
	} {
		_, err := compositeRouteFromInput(10, tc)
		require.Error(t, err)
	}
}

func TestCompositeCandidatesGpt6lunaRoutingBoundary(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cost", false: "ordered"}[enabled], func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			group := int64(10)
			oauth := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}, Credentials: map[string]any{"model_mapping": map[string]any{"oauth-alias": "oauth-upstream"}}}
			code := Account{ID: 2, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}, Credentials: map[string]any{"model_mapping": map[string]any{"code-alias": "code-upstream"}}}
			ptr := func(v float64) *float64 { return &v }
			ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{
				{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"oauth-upstream"}, InputPrice: ptr(.00001), OutputPrice: ptr(.00001)}}},
				{AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"code-upstream"}, InputPrice: ptr(.000001), OutputPrice: ptr(.000001)}}},
			}}
			svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{oauth, code}}, cache: &schedulerTestGatewayCache{}, cfg: newSchedulerTestSubscriptionPriorityConfig(), rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true", "", "true"), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), channelService: newTestChannelServiceForStats(t, ch, group, PlatformComposite)}
			settings := svc.openAIAdvancedSchedulerSettingRepo().(*openAIAdvancedSchedulerSettingRepoStub)
			settings.values[SettingKeyCostFirstRoutingEnabled] = map[bool]string{true: "true", false: "false"}[enabled]
			resolver := NewCompositeRouteResolver(&compositeRouteRepoStub{routes: []CompositeModelRoute{{GroupID: group, PublicModel: "gpt6luna", MatchType: CompositeRouteMatchExact, Endpoint: CompositeRouteEndpointChatCompletions, Enabled: true, Candidates: []CompositeRouteCandidate{{PlatformOpenAI, "oauth-alias", true}, {PlatformOpenCodeGo, "code-alias", true}}}}})
			decision, err := resolver.Resolve(context.Background(), group, "gpt6luna", CompositeRouteEndpointChatCompletions)
			require.NoError(t, err)
			require.True(t, decision.Matched)
			ctx := WithCompositeRouteDecision(context.Background(), decision)
			result, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "oauth-alias", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, result)
			if result.ReleaseFunc != nil {
				defer result.ReleaseFunc()
			}
			want := int64(1)
			wantModel := "oauth-alias"
			if enabled {
				want = 2
				wantModel = "code-alias"
			}
			require.Equal(t, want, result.Account.ID)
			forwardCtx := ApplyCompositeSelection(ctx, result)
			upstream, ok := ResolvedUpstreamModelFromContext(forwardCtx)
			require.True(t, ok)
			require.Equal(t, wantModel, upstream)
			require.JSONEq(t, `{"model":"`+wantModel+`","messages":[]}`, string(svc.ReplaceModelInBody([]byte(`{"model":"oauth-alias","messages":[]}`), result.CompositeSource.UpstreamModel)))
			if enabled {
				pick := func(callCtx context.Context, previous string) int64 {
					r, _, err := svc.SelectAccountWithSchedulerForCapability(callCtx, &group, previous, "cross-sticky", "oauth-alias", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
					require.NoError(t, err)
					if r.ReleaseFunc != nil {
						r.ReleaseFunc()
					}
					return r.Account.ID
				}
				// Cross-provider equal-cost and both-unknown soft affinity.
				svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:cross-sticky": 2}}
				ch.AccountStatsPricingRules[0].Pricing[0].InputPrice = ptr(.000001)
				ch.AccountStatsPricingRules[0].Pricing[0].OutputPrice = ptr(.000001)
				require.Equal(t, int64(2), pick(ctx, ""))
				rules := ch.AccountStatsPricingRules
				ch.AccountStatsPricingRules = nil
				require.Equal(t, int64(2), pick(ctx, ""))
				ch.AccountStatsPricingRules = rules
				ch.AccountStatsPricingRules[0].Pricing[0].InputPrice = ptr(0)
				ch.AccountStatsPricingRules[0].Pricing[0].OutputPrice = ptr(0)
				require.Equal(t, int64(1), pick(ctx, ""), "explicit zero beats cross-provider sticky")
				ch.AccountStatsPricingRules[0].Pricing[0].InputPrice = ptr(.00001)
				ch.AccountStatsPricingRules[0].Pricing[0].OutputPrice = ptr(.00001)
				svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{2: false}})
				require.Equal(t, int64(1), pick(ctx, ""), "busy cheapest source falls through before sending")
				svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
				foreign := code
				foreign.GroupIDs = []int64{999}
				svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{oauth, foreign}}
				require.Equal(t, int64(1), pick(ctx, ""), "sources cannot authorize accounts outside the group")
				svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{oauth, code}}
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, "resp_hard", 1, 3600*time.Second))
				require.Equal(t, int64(1), pick(ctx, "resp_hard"), "hard response owner prevents cheap cross-provider migration")
				_, _, missingOwnerErr := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "resp_missing", "", "oauth-alias", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
				require.Error(t, missingOwnerErr)
			}
			if enabled {
				upstreamTransport := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_test","model":"code-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}}
				svc.cfg = rawChatCompletionsTestConfig()
				svc.httpUpstream = upstreamTransport
				selected := *result.Account
				selected.Credentials = map[string]any{"api_key": "test-only", "base_url": "http://upstream.example", "model_mapping": map[string]any{"code-alias": "code-upstream"}}
				requestBody := svc.ReplaceModelInBody([]byte(`{"model":"oauth-alias","messages":[{"role":"user","content":"test"}],"stream":false}`), result.CompositeSource.UpstreamModel)
				gin.SetMode(gin.TestMode)
				gc, _ := gin.CreateTestContext(httptest.NewRecorder())
				gc.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(requestBody)).WithContext(forwardCtx)
				require.Empty(t, upstreamTransport.requests, "cost evaluation must not send a generation")
				_, err := svc.forwardAsRawChatCompletions(forwardCtx, gc, &selected, requestBody, "")
				require.NoError(t, err)
				require.Len(t, upstreamTransport.requests, 1, "selected source sends exactly once")
				require.Equal(t, "code-upstream", gjson.GetBytes(upstreamTransport.lastBody, "model").String())
			}
			public, _ := RequestedPublicModelFromContext(forwardCtx)
			require.Equal(t, "gpt6luna", public)
		})
	}
}
