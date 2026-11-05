//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGlobalCostRoutingMappedProviderPrices(t *testing.T) {
	ctx := context.Background()
	group := int64(10)
	one, two := 0.000001, 0.000002
	for _, tc := range []struct {
		name, platform, typ, model string
		credentials                map[string]any
	}{
		{"claude", PlatformAnthropic, AccountTypeAPIKey, "claude-sonnet-4-5", map[string]any{"model_mapping": map[string]any{"public": "claude-sonnet-4-5"}}},
		{"gemini", PlatformGemini, AccountTypeAPIKey, "gemini-2.5-pro", map[string]any{"model_mapping": map[string]any{"public": "gemini-2.5-pro"}}},
		{"antigravity", PlatformAntigravity, AccountTypeOAuth, "claude-sonnet-4-5", map[string]any{"model_mapping": map[string]any{"public": "claude-sonnet-4-5"}}},
		{"bedrock", PlatformAnthropic, AccountTypeBedrock, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", map[string]any{"aws_auth_mode": "credential_file", "model_mapping": map[string]any{"public": "us.anthropic.claude-sonnet-4-5-20250929-v1:0"}}},
		{"openrouter", PlatformOpenRouter, AccountTypeAPIKey, "provider/model", map[string]any{"model_mapping": map[string]any{"public": "provider/model"}}},
		{"opencode", PlatformOpenCodeGo, AccountTypeAPIKey, "provider/model", map[string]any{"model_mapping": map[string]any{"public": "provider/model"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: tc.platform, Type: tc.typ, Credentials: tc.credentials}
			require.Equal(t, tc.model, purchaseUpstreamModel(ctx, a, "public", false))
			ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{tc.model}, InputPrice: &one, OutputPrice: &two}}}}}
			order := newPurchaseCostOrder(ctx, newTestChannelServiceForStats(t, ch, group, tc.platform), &group, "public", []*Account{a}, false)
			require.NotNil(t, order.costs[1])
			require.InDelta(t, 3, *order.costs[1], 0.000001)
		})
	}
}

func TestGlobalCostRoutingComparableUnits(t *testing.T) {
	ctx := context.Background()
	group := int64(10)
	zero, one := 0.0, 1.0
	a, b := &Account{ID: 1, Platform: PlatformAnthropic}, &Account{ID: 2, Platform: PlatformAnthropic}
	ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{
		{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"model"}, BillingMode: BillingModePerRequest, PerRequestPrice: &zero}}},
		{AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"model"}, BillingMode: BillingModePerRequest, PerRequestPrice: &one}}},
	}}
	cs := newTestChannelServiceForStats(t, ch, group, PlatformAnthropic)
	order := newPurchaseCostOrder(ctx, cs, &group, "model", []*Account{a, b}, false)
	require.Equal(t, -1, order.compare(a, b))
	require.Equal(t, 0.0, *order.costs[1])
	ch.AccountStatsPricingRules[1].Pricing[0].BillingMode = BillingModeToken
	ch.AccountStatsPricingRules[1].Pricing[0].InputPrice = &one
	ch.AccountStatsPricingRules[1].Pricing[0].OutputPrice = &one
	order = newPurchaseCostOrder(ctx, cs, &group, "model", []*Account{a, b}, false)
	require.Empty(t, order.costs, "unlike billing units must not compete")
	ch.AccountStatsPricingRules[0].Pricing[0].BillingMode = BillingModeImage
	order = newPurchaseCostOrder(ctx, cs, &group, "model", []*Account{a}, false)
	require.Empty(t, order.costs, "image dimensions not available: unknown, not zero")
}

func TestGlobalCostRoutingSharedSchedulerLegacyAndLoad(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformTypeSafe} {
		for _, load := range []bool{false, true} {
			t.Run(platform+map[bool]string{false: "/legacy", true: "/load"}[load], func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
				ctx := context.Background()
				group := int64(10)
				low, high := 0.000001, 0.001
				accounts := []Account{
					{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 2, GroupIDs: []int64{group}, AccountGroups: []AccountGroup{{GroupID: group}}, Credentials: map[string]any{"model_mapping": map[string]any{"public": "mapped"}}},
					{ID: 2, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 100, Concurrency: 2, GroupIDs: []int64{group}, AccountGroups: []AccountGroup{{GroupID: group}}, Credentials: map[string]any{"model_mapping": map[string]any{"public": "mapped"}}},
				}
				repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{1: &accounts[0], 2: &accounts[1]}}
				settings := newOpenAIAdvancedSchedulerRateLimitService("false").settingService
				settings.settingRepo.(*openAIAdvancedSchedulerSettingRepoStub).values[SettingKeyCostFirstRoutingEnabled] = "true"
				ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{
					{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"mapped"}, InputPrice: &high, OutputPrice: &high}}},
					{AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"mapped"}, InputPrice: &low, OutputPrice: &low}}},
				}}
				cfg := testConfig()
				cfg.Gateway.Scheduling.LoadBatchEnabled = load
				svc := &GatewayService{accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{group: {ID: group, Platform: platform, Status: StatusActive, Hydrated: true}}}, cache: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky": 1}}, cfg: cfg, settingService: settings, channelService: newTestChannelServiceForStats(t, ch, group, platform)}
				if load {
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
				}
				result, err := svc.SelectAccountWithLoadAwareness(ctx, &group, "sticky", "public", nil, "", 0)
				require.NoError(t, err)
				require.Equal(t, int64(2), result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				// The first selection bound account 2. Give both equal costs:
				// sticky 2 must beat account 1's higher priority.
				high = low
				for _, unknown := range []bool{false, true} {
					if unknown {
						ch.AccountStatsPricingRules = nil
					}
					tied, err := svc.SelectAccountWithLoadAwareness(ctx, &group, "sticky", "public", nil, "", 0)
					require.NoError(t, err)
					require.Equal(t, int64(2), tied.Account.ID)
					if tied.ReleaseFunc != nil {
						tied.ReleaseFunc()
					}
				}
			})
		}
	}
}

func TestGlobalCostRoutingOpenAILegacy(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	group := int64(10)
	low, high := 0.000001, 0.001
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 1, GroupIDs: []int64{group}}, {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 100, Concurrency: 1, GroupIDs: []int64{group}}}
	rl := newOpenAIAdvancedSchedulerRateLimitService("false")
	rl.settingService.settingRepo.(*openAIAdvancedSchedulerSettingRepoStub).values[SettingKeyCostFirstRoutingEnabled] = "true"
	ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.2"}, InputPrice: &high, OutputPrice: &high}}}, {AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.2"}, InputPrice: &low, OutputPrice: &low}}}}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky": 1}}, cfg: testConfig(), rateLimitService: rl, channelService: newTestChannelServiceForStats(t, ch, group, PlatformOpenAI), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
	result, _, err := svc.SelectAccountWithScheduler(context.Background(), &group, "", "sticky", "gpt-5.2", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}

func TestGlobalCostRoutingSoftStickyPolicy(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, tc := range []struct {
			name                    string
			stickyPrice, otherPrice *float64
			enabled                 bool
			want                    int64
		}{
			{"same known", testPtrFloat64(1), testPtrFloat64(1), true, 2},
			{"same zero", testPtrFloat64(0), testPtrFloat64(0), true, 2},
			{"both unknown", nil, nil, true, 2},
			{"cheaper nonsticky", testPtrFloat64(2), testPtrFloat64(1), true, 1},
			{"unknown sticky", nil, testPtrFloat64(1), true, 1},
			{"disabled original sticky", testPtrFloat64(2), testPtrFloat64(1), false, 2},
		} {
			t.Run(advanced+"/"+tc.name, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
				group := int64(10)
				accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 1, GroupIDs: []int64{group}}, {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 100, Concurrency: 1, GroupIDs: []int64{group}}}
				rl := newOpenAIAdvancedSchedulerRateLimitService(advanced)
				if tc.enabled {
					rl.settingService.settingRepo.(*openAIAdvancedSchedulerSettingRepoStub).values[SettingKeyCostFirstRoutingEnabled] = "true"
				}
				ch := &Channel{Status: StatusActive, AccountStatsPricingRules: []AccountStatsPricingRule{{AccountIDs: []int64{1}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.2"}, InputPrice: tc.otherPrice, OutputPrice: tc.otherPrice}}}, {AccountIDs: []int64{2}, Pricing: []ChannelModelPricing{{Models: []string{"gpt-5.2"}, InputPrice: tc.stickyPrice, OutputPrice: tc.stickyPrice}}}}}
				cfg := testConfig()
				cfg.Gateway.OpenAIWS.LBTopK = 1
				cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 100
				svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky": 2}}, cfg: cfg, rateLimitService: rl, channelService: newTestChannelServiceForStats(t, ch, group, PlatformOpenAI), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &group, "", "sticky", "gpt-5.2", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Equal(t, tc.want, result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}
