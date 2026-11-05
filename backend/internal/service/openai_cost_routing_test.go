//go:build unit

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExplicitPurchaseBasketCost(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name          string
		input, output *float64
		want          *float64
	}{
		{"known", ptr(0.000001), ptr(0.000002), ptr(3)},
		{"free", ptr(0), ptr(0), ptr(0)},
		{"missing input", nil, ptr(0), nil},
		{"missing output", ptr(0), nil, nil},
		{"negative", ptr(-1), ptr(1), nil},
		{"nan", ptr(math.NaN()), ptr(1), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"mapped-*"}, InputPrice: tc.input, OutputPrice: tc.output}}}}}
			require.Equal(t, tc.want, explicitPurchaseBasketCost(ch, 1, 10, PlatformOpenAI, "mapped-model"))
			require.Nil(t, explicitPurchaseBasketCost(ch, 2, 10, PlatformOpenAI, "mapped-model"))
		})
	}
}

func TestCostFirstSelectionOrderBeatsScoresAndSoftSticky(t *testing.T) {
	cheap, expensive, zero := 1.0, 100.0, 0.0
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: 100000, purchaseCost: &expensive},
		{account: &Account{ID: 2}, score: 0, purchaseCost: &cheap},
		{account: &Account{ID: 3}, score: 200000},
		{account: &Account{ID: 4}, score: -100, purchaseCost: &zero},
	}
	scheduler := &defaultOpenAIAccountScheduler{}
	req := OpenAIAccountScheduleRequest{CostFirst: true, UseUpstreamTokenCost: true, StickyWeighted: true, StickyAccountID: 1}
	plan := openAIAccountLoadPlan{candidates: candidates, topK: 1}
	order := scheduler.buildOpenAISelectionOrder(req, plan)
	require.Equal(t, []int64{4, 2, 1, 3}, []int64{order[0].account.ID, order[1].account.ID, order[2].account.ID, order[3].account.ID})
	req.UseUpstreamTokenCost = false
	order = scheduler.buildOpenAISelectionOrder(req, plan)
	require.Equal(t, int64(3), order[0].account.ID, "opt-out preserves score-based TopK")
}

func TestCostFirstOwnOAuthSupplierRelayMappedAndDynamic(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	ctx := WithSupplierResponsesEligibility(context.Background(), true)
	group := int64(10)
	own := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{group}, Credentials: map[string]any{"plan_type": "plus"}}
	supplier := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 100, GroupIDs: []int64{group}, Credentials: map[string]any{"supplier_resource_id": "test-resource", "model_mapping": map[string]any{"gpt-5.1": "gpt-5.2"}}}
	relay := supplier
	relay.ID = 3
	relay.Type = AccountTypeAPIKey
	relay.Credentials = nil
	cost := func(v float64) *float64 { return &v }
	ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{
		{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.4"}, InputPrice: cost(0.000010), OutputPrice: cost(0.000010)}}},
		{AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.2"}, InputPrice: cost(0.000001), OutputPrice: cost(0.000001)}}},
		{AccountIDs: []int64{3}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.1"}, InputPrice: cost(0.000005), OutputPrice: cost(0.000005)}}},
	}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{own, supplier, relay}}, cache: &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:cost-session": 1}}, cfg: newSchedulerTestSubscriptionPriorityConfig(), rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true", "", "true"), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), channelService: newTestChannelServiceForStats(t, ch, group, PlatformOpenAI)}
	repo := svc.openAIAdvancedSchedulerSettingRepo().(*openAIAdvancedSchedulerSettingRepoStub)
	repo.values[SettingKeyCostFirstRoutingEnabled] = "true"
	selectID := func() int64 {
		result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "cost-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.NotNil(t, result)
		if result.ReleaseFunc != nil {
			defer result.ReleaseFunc()
		}
		return result.Account.ID
	}
	scheduler := &defaultOpenAIAccountScheduler{service: svc}
	req := OpenAIAccountScheduleRequest{CostFirst: true, UseUpstreamTokenCost: true, GroupID: &group, RequestedModel: "gpt-5.1"}
	require.NotNil(t, scheduler.estimatedPurchaseCost(ctx, req, &supplier))
	require.InDelta(t, 2, *scheduler.estimatedPurchaseCost(ctx, req, &supplier), 0.000001)
	require.InDelta(t, 20, *scheduler.estimatedPurchaseCost(ctx, req, &own), 0.000001)
	require.Equal(t, int64(2), selectID(), "explicit supplier cost beats own OAuth and relay despite priority/sticky")
	// A cheaper supplier still cannot bypass request capability gates.
	blocked, _, err := svc.SelectAccountWithScheduler(context.Background(), &group, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(3), blocked.Account.ID)
	if blocked.ReleaseFunc != nil {
		blocked.ReleaseFunc()
	}
	// Image/token-cost opt-out keeps the old soft sticky path.
	optedOut, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "cost-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, "", false, false, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), optedOut.Account.ID, "prior selection bound the session; opt-out keeps it")
	if optedOut.ReleaseFunc != nil {
		optedOut.ReleaseFunc()
	}

	ch.AccountStatsPricingRules[0].Pricing[0].InputPrice = cost(0)
	ch.AccountStatsPricingRules[0].Pricing[0].OutputPrice = cost(0)
	require.Equal(t, int64(1), selectID(), "updated explicit zero purchase cost takes effect")
	repo.values[SettingKeyCostFirstRoutingEnabled] = "false"
	cached := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting)
	cached.expiresAt = time.Now().Add(-time.Second).UnixNano()
	require.False(t, svc.openAIAdvancedSchedulerRuntimeSettings(ctx).costFirstEnabled)
}
