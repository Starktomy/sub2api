package service

import (
	"context"
	"math"
	"strings"
)

// estimatedPurchaseCost uses explicit account-statistics rules only. Customer
// sales prices, billing multipliers and supplier fee BPS are not purchase prices.
// This is a comparable basket estimate, not a prediction of final request usage.
func (s *defaultOpenAIAccountScheduler) estimatedPurchaseCost(ctx context.Context, req OpenAIAccountScheduleRequest, account *Account) *float64 {
	if !req.CostFirst || !req.UseUpstreamTokenCost || s.service == nil || s.service.channelService == nil || req.GroupID == nil {
		return nil
	}
	order := newPurchaseCostOrder(ctx, s.service.channelService, req.GroupID, req.RequestedModel, []*Account{account}, req.RequireCompact)
	return order.costs[account.ID]
}

// Reuse rule precedence, platform/model matching and interval selection from
// account statistics, but require both basket prices: missing is not free.
func explicitPurchaseBasketCost(channel *Channel, accountID, groupID int64, platform, model string) *float64 {
	if channel == nil || strings.TrimSpace(model) == "" {
		return nil
	}
	const basketTokens = 1_000_000
	for _, rule := range channel.AccountStatsPricingRules {
		if !matchAccountStatsRule(&rule, accountID, groupID) {
			continue
		}
		pricing := findPricingForModel(rule.Pricing, platform, strings.ToLower(model))
		if pricing == nil {
			continue
		}
		// Per-request and image/video prices are not comparable to a token basket.
		if pricing.BillingMode != "" && pricing.BillingMode != BillingModeToken {
			return nil
		}
		input, output := pricing.InputPrice, pricing.OutputPrice
		if len(pricing.Intervals) > 0 {
			if iv := FindMatchingInterval(pricing.Intervals, 2*basketTokens); iv != nil {
				input, output = iv.InputPrice, iv.OutputPrice
			}
		}
		valid := func(p *float64) bool { return p != nil && *p >= 0 && !math.IsNaN(*p) && !math.IsInf(*p, 0) }
		if !valid(input) || !valid(output) {
			return nil
		}
		cost := float64(basketTokens) * (*input + *output)
		if math.IsInf(cost, 0) || math.IsNaN(cost) {
			return nil
		}
		return &cost
	}
	return nil
}

func (s *defaultOpenAIAccountScheduler) costAwareStickyFallback(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, error) {
	if req.CostFirst {
		return nil, nil
	}
	return s.tryFallbackToWeightedSticky(ctx, req)
}
