package service

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type purchaseCostOrder struct {
	costs     map[int64]*float64
	stickyIDs []int64
}

func (o purchaseCostOrder) compare(a, b *Account) int {
	x, y := o.costs[a.ID], o.costs[b.ID]
	if x == nil && y == nil {
		return o.compareSticky(a, b)
	}
	if x == nil {
		return 1
	}
	if y == nil {
		return -1
	}
	if *x < *y {
		return -1
	}
	if *x > *y {
		return 1
	}
	return o.compareSticky(a, b)
}
func (o purchaseCostOrder) compareSticky(a, b *Account) int {
	x, y := purchaseStickyRank(o.stickyIDs, a.ID), purchaseStickyRank(o.stickyIDs, b.ID)
	if x < y {
		return -1
	}
	if x > y {
		return 1
	}
	return 0
}
func (o purchaseCostOrder) sort(accounts []*Account) {
	sort.SliceStable(accounts, func(i, j int) bool { return o.compare(accounts[i], accounts[j]) < 0 })
}
func (o purchaseCostOrder) filter(pool []accountWithLoad) []accountWithLoad {
	if len(pool) == 0 {
		return pool
	}
	best := pool[0].account
	for _, c := range pool {
		if o.compare(c.account, best) < 0 {
			best = c.account
		}
	}
	out := make([]accountWithLoad, 0, len(pool))
	for _, c := range pool {
		if o.compare(c.account, best) == 0 {
			out = append(out, c)
		}
	}
	return out
}
func (s *GatewayService) costFirstEnabled(ctx context.Context) bool {
	proxy := &OpenAIGatewayService{rateLimitService: &RateLimitService{settingService: s.settingService}}
	return proxy.openAIAdvancedSchedulerRuntimeSettings(ctx).costFirstEnabled
}
func (s *OpenAIGatewayService) costFirstEnabled(ctx context.Context) bool {
	return s.openAIAdvancedSchedulerRuntimeSettings(ctx).costFirstEnabled
}

type purchaseCostDisabledKey struct{}

func purchaseCostAllowed(ctx context.Context) bool {
	disabled, _ := ctx.Value(purchaseCostDisabledKey{}).(bool)
	return !disabled
}

// newPurchaseCostOrder resolves explicit procurement rules once per candidate pool.
// Incomparable billing modes remain unknown rather than mixing cost units.
func newPurchaseCostOrder(ctx context.Context, cs *ChannelService, group *int64, model string, accounts []*Account, compact bool) purchaseCostOrder {
	ids, _ := ctx.Value(purchaseStickyKey{}).([]int64)
	order := purchaseCostOrder{costs: map[int64]*float64{}, stickyIDs: ids}
	if cs == nil || group == nil || !purchaseCostAllowed(ctx) {
		return order
	}
	channel, err := cs.GetChannelForGroup(ctx, *group)
	if err != nil || channel == nil {
		return order
	}
	// A pool with incompatible billing units cannot be ranked faithfully.
	modes := map[BillingMode]bool{}
	for _, a := range accounts {
		upstream := purchaseUpstreamModel(ctx, a, model, compact)
		pricing := explicitPurchasePricing(channel, a.ID, *group, a.Platform, upstream)
		if pricing == nil {
			continue
		}
		mode := pricing.BillingMode
		if mode == "" {
			mode = BillingModeToken
		}
		modes[mode] = true
		if mode != BillingModeToken && mode != BillingModePerRequest {
			continue
		}
		if mode == BillingModePerRequest {
			if len(pricing.Intervals) > 0 {
				continue
			} // no known request tier/dimensions
			if validPurchasePrice(pricing.PerRequestPrice) {
				v := *pricing.PerRequestPrice
				order.costs[a.ID] = &v
			}
		} else {
			order.costs[a.ID] = explicitPurchaseBasketCost(channel, a.ID, *group, a.Platform, upstream)
		}
	}
	if len(modes) > 1 {
		order.costs = map[int64]*float64{}
	}
	return order
}
func validPurchasePrice(p *float64) bool {
	return p != nil && *p >= 0 && !math.IsNaN(*p) && !math.IsInf(*p, 0)
}
func explicitPurchasePricing(ch *Channel, account, group int64, platform, model string) *ChannelModelPricing {
	for _, rule := range ch.AccountStatsPricingRules {
		if matchAccountStatsRule(&rule, account, group) {
			if p := findPricingForModel(rule.Pricing, platform, strings.ToLower(model)); p != nil {
				return p
			}
		}
	}
	return nil
}
func purchaseUpstreamModel(ctx context.Context, a *Account, model string, compact bool) string {
	if resolved, ok := ResolvedUpstreamModelFromContext(ctx); ok {
		model = resolved
	}
	if a.IsBedrock() {
		m, ok := ResolveBedrockModelID(a, model)
		if !ok {
			return ""
		}
		return m
	}
	if a.Platform == PlatformAntigravity {
		if m, ok := resolveGeminiThinkingVariantForLevel(a, model, ""); ok {
			return m
		}
		return mapAntigravityModel(a, model)
	}
	if domain.UsesOpenAIGateway(a.Platform) {
		if forward, ok := openAIForwardModelFromContext(ctx); ok {
			model, compact = forward.model, forward.useCompactModelMapping
		}
		return resolveOpenAIAccountUpstreamModelForRequest(a, model, compact)
	}
	return a.GetMappedModel(strings.TrimPrefix(model, "models/"))
}

func purchaseAccountPointers(accounts []Account) []*Account {
	out := make([]*Account, 0, len(accounts))
	for i := range accounts {
		out = append(out, &accounts[i])
	}
	return out
}

// Soft affinity is a strict tie-break only, never a procurement discount.
type purchaseStickyKey struct{}

func withPurchaseSticky(ctx context.Context, ids ...int64) context.Context {
	return context.WithValue(ctx, purchaseStickyKey{}, ids)
}
func purchaseStickyRank(ids []int64, id int64) int {
	for i, candidate := range ids {
		if candidate > 0 && candidate == id {
			return i
		}
	}
	return len(ids)
}
func (s *GatewayService) withPurchaseSession(ctx context.Context, group *int64, session string) context.Context {
	if s.cache != nil && session != "" {
		if id, err := s.cache.GetSessionAccountID(ctx, derefGroupID(group), session); err == nil {
			return withPurchaseSticky(ctx, id)
		}
	}
	return ctx
}
func (s *OpenAIGatewayService) withPurchaseSession(ctx context.Context, group *int64, session string) context.Context {
	if s.cache != nil && session != "" {
		if id, err := s.getStickySessionAccountID(ctx, group, session); err == nil {
			return withPurchaseSticky(ctx, id)
		}
	}
	return ctx
}
