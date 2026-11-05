package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type multiSourceHTTPAccounts struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *multiSourceHTTPAccounts) ListSchedulableByPlatform(_ context.Context, p string) ([]service.Account, error) {
	var out []service.Account
	for _, a := range r.accounts {
		if a.Platform == p {
			out = append(out, a)
		}
	}
	return out, nil
}
func (r *multiSourceHTTPAccounts) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, p string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, p)
}
func (r *multiSourceHTTPAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

type multiSourceHTTPChannels struct {
	service.ChannelRepository
	channel service.Channel
}

func (r *multiSourceHTTPChannels) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}
func (r *multiSourceHTTPChannels) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{10: service.PlatformComposite}, nil
}

type multiSourceHTTPUsage struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *multiSourceHTTPUsage) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, log)
	return true, nil
}

type multiSourceHTTPOutbound struct {
	ids       []int64
	models    []string
	failFirst bool
}

func (u *multiSourceHTTPOutbound) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.ids = append(u.ids, id)
	u.models = append(u.models, gjson.GetBytes(body, "model").String())
	status := http.StatusOK
	payload := `{"id":"chatcmpl_test","model":"` + u.models[len(u.models)-1] + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	if u.failFirst && len(u.ids) == 1 {
		status = http.StatusBadGateway
		payload = `{"error":{"message":"temporary","type":"upstream_error"}}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
}
func (u *multiSourceHTTPOutbound) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

func TestCompositeCandidatesHTTPProductionHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failover := range []bool{false, true} {
		t.Run(map[bool]string{false: "cheapest-once", true: "failover-next-source"}[failover], func(t *testing.T) {
			groupID := int64(10)
			ptr := func(v float64) *float64 { return &v }
			oauth := service.Account{ID: 1, Name: "oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_mapping": map[string]any{"oauth-alias": "oauth-upstream"}}}
			code := service.Account{ID: 2, Name: "code", Platform: service.PlatformOpenCodeGo, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "test-only", "base_url": "https://upstream.example", "model_mapping": map[string]any{"code-alias": "code-upstream"}, "api_protocol": "chat_completions"}}
			routerAccount := code
			routerAccount.ID = 3
			routerAccount.Platform = service.PlatformOpenRouter
			routerAccount.Credentials = map[string]any{"api_key": "test-only", "base_url": "https://router.example", "model_mapping": map[string]any{"router-alias": "router-upstream"}, "api_protocol": "chat_completions"}
			accounts := &multiSourceHTTPAccounts{accounts: []service.Account{oauth, code, routerAccount}}
			channel := service.Channel{ID: 1, Status: service.StatusActive, GroupIDs: []int64{groupID}, AccountStatsPricingRules: []service.AccountStatsPricingRule{
				{AccountIDs: []int64{1}, Pricing: []service.ChannelModelPricing{{Models: []string{"oauth-upstream"}, InputPrice: ptr(.00001), OutputPrice: ptr(.00001)}}},
				{AccountIDs: []int64{2}, Pricing: []service.ChannelModelPricing{{Models: []string{"code-upstream"}, InputPrice: ptr(.000001), OutputPrice: ptr(.000001)}}},
				{AccountIDs: []int64{3}, Pricing: []service.ChannelModelPricing{{Models: []string{"router-upstream"}, InputPrice: ptr(.000002), OutputPrice: ptr(.000002)}}},
			}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			cfg.Gateway.MaxAccountSwitches = 2
			settings := service.NewSettingService(&channelMonitorRouteSettingRepoStub{values: map[string]string{service.SettingKeyCostFirstRoutingEnabled: "true"}}, cfg)
			rate := service.NewRateLimitService(accounts, nil, cfg, nil, nil)
			rate.SetSettingService(settings)
			channels := service.NewManagedChannelService(&multiSourceHTTPChannels{channel: channel}, nil, nil, nil, nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			outbound := &multiSourceHTTPOutbound{failFirst: failover}
			usage := &multiSourceHTTPUsage{}
			gateway := service.NewOpenAIGatewayService(accounts, usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), rate, billing, outbound, &service.DeferredService{}, nil, nil, nil, channels, nil, settings, nil)
			h := handler.NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			route := service.CompositeModelRoute{GroupID: groupID, PublicModel: "gpt6luna", MatchType: "exact", Endpoint: "chat_completions", Enabled: true, Candidates: []service.CompositeRouteCandidate{{TargetPlatform: service.PlatformOpenAI, UpstreamModel: "oauth-alias", Enabled: true}, {TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "code-alias", Enabled: true}, {TargetPlatform: service.PlatformOpenRouter, UpstreamModel: "router-alias", Enabled: true}}}
			resolver := service.NewCompositeRouteResolver(compositeRouteRepoStub{routes: []service.CompositeModelRoute{route}})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, GroupID: &groupID, User: &service.User{ID: 20, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformComposite, Status: service.StatusActive}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 20, Concurrency: 0})
				c.Next()
			})
			router.Use(compositeTargetPlatformMiddleware(resolver))
			router.POST("/v1/chat/completions", func(c *gin.Context) {
				require.Equal(t, service.PlatformOpenAI, getGroupPlatform(c))
				h.ChatCompletions(c)
				public, _ := service.RequestedPublicModelFromContext(c.Request.Context())
				require.Equal(t, "gpt6luna", public)
				selected, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
				want := service.PlatformOpenCodeGo
				if failover {
					want = service.PlatformOpenRouter
				}
				require.Equal(t, want, selected)
			})
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt6luna","messages":[{"role":"user","content":"test"}],"stream":false}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, request)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, usage.logs, 1)
			require.Equal(t, "gpt6luna", usage.logs[0].RequestedModel, "public model must survive handler billing handoff")
			if failover {
				require.Equal(t, []int64{2, 3}, outbound.ids)
				require.Equal(t, []string{"code-upstream", "router-upstream"}, outbound.models)
			} else {
				require.Equal(t, []int64{2}, outbound.ids)
				require.Equal(t, []string{"code-upstream"}, outbound.models)
			}
		})
	}
}
