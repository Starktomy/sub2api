package service

import (
	"context"
	"errors"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type compositeSourceBucketsKey struct{}

// selectCompositeCandidates expands explicit sources before acquiring any slot.
// Existing selectors remain authoritative for fresh eligibility and concurrency.
func (s *OpenAIGatewayService) selectCompositeCandidates(ctx context.Context, d CompositeRouteDecision, groupID *int64, previous, session, model string, excluded map[int64]struct{}, transport OpenAIUpstreamTransport, capability OpenAIEndpointCapability, image OpenAIImagesCapability, compact, movable, tokenCost bool) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	if groupID == nil || *groupID != d.GroupID {
		return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
	}
	// Multi-source Responses must not inherit a weaker capability inferred
	// from the primary provider. Native Responses support is required on every
	// source; do not downgrade encrypted state/compaction to a chat bridge.
	if d.Endpoint == CompositeRouteEndpointResponses {
		capability = OpenAIEndpointCapabilityResponses
	}
	type candidate struct {
		source  CompositeRouteCandidate
		account *Account
		cost    *float64
		order   int
	}
	pool := []candidate{}
	// Cache each provider bucket for this expansion, in addition to the existing
	// scheduler snapshot cache. No network probes or slot acquisition here.
	buckets := map[string][]Account{}
	checker := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	costFirst := tokenCost && s.costFirstEnabled(ctx)
	sticky, _ := s.getStickySessionAccountID(ctx, groupID, session)
	hardID := s.resolveOpenAIGuardianParentAccountID(ctx, groupID)
	hardPrevious := previous != "" && !movable
	if hardPrevious {
		owner, err := s.getOpenAIWSStateStore().GetResponseAccount(ctx, *groupID, previous)
		if err != nil || owner <= 0 {
			return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
		}
		if hardID > 0 && hardID != owner {
			return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
		}
		hardID = owner
	}
	var channel *Channel
	if costFirst && s.channelService != nil {
		channel, _ = s.channelService.GetChannelForGroup(ctx, *groupID)
	}
	modes := map[BillingMode]bool{}
	for index, source := range d.Route.Candidates {
		if !source.Enabled || !domain.UsesOpenAIGateway(source.TargetPlatform) || source.TargetPlatform == PlatformGrok {
			continue
		}
		if hardPrevious && source.TargetPlatform != PlatformOpenAI {
			continue
		}
		accounts, loaded := buckets[source.TargetPlatform]
		if !loaded {
			var err error
			accounts, err = s.listSchedulableAccounts(ctx, groupID, source.TargetPlatform)
			if err != nil {
				return nil, OpenAIAccountScheduleDecision{}, err
			}
			buckets[source.TargetPlatform] = accounts
		}
		sourceCtx := compositeSourceContext(ctx, d, source)
		sourceCtx = s.withOpenAIQuotaAutoPauseContext(sourceCtx)
		sourceCtx = s.withOpenAIGroupPrivacyRequirement(sourceCtx, groupID)
		sourceCtx = s.withOpenAIProfitControlGate(sourceCtx, groupID)
		req := OpenAIAccountScheduleRequest{GroupID: groupID, Platform: source.TargetPlatform, RequestedModel: source.UpstreamModel, RequiredTransport: transport, RequiredCapability: capability, RequiredImageCapability: image, RequireCompact: compact, RequirePrivacySet: s.openAIGroupRequiresPrivacySet(sourceCtx, groupID)}
		for i := range accounts {
			a := &accounts[i]
			if _, skip := excluded[a.ID]; skip {
				continue
			}
			if hardID > 0 && a.ID != hardID {
				continue
			}
			if a.Platform != source.TargetPlatform || !a.IsSchedulable() || !s.openAIAccountMatchesSchedulingGroup(a, groupID) || !checker.isAccountTransportCompatible(a, transport) || !checker.isAccountRequestCompatible(sourceCtx, a, req) {
				continue
			}
			entry := candidate{source: source, account: a, order: index}
			if costFirst && channel != nil {
				upstream := purchaseUpstreamModel(sourceCtx, a, source.UpstreamModel, compact)
				pricing := explicitPurchasePricing(channel, a.ID, *groupID, a.Platform, upstream)
				if pricing != nil {
					mode := pricing.BillingMode
					if mode == "" {
						mode = BillingModeToken
					}
					modes[mode] = true
					if mode == BillingModeToken {
						entry.cost = explicitPurchaseBasketCost(channel, a.ID, *groupID, a.Platform, upstream)
					}
					if mode == BillingModePerRequest && len(pricing.Intervals) == 0 && validPurchasePrice(pricing.PerRequestPrice) {
						v := *pricing.PerRequestPrice
						entry.cost = &v
					}
				}
			}
			pool = append(pool, entry)
		}
	}
	if len(modes) > 1 {
		for i := range pool {
			pool[i].cost = nil
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if costFirst {
			if a.cost != nil && b.cost == nil {
				return true
			}
			if a.cost == nil && b.cost != nil {
				return false
			}
			if a.cost != nil && b.cost != nil && *a.cost != *b.cost {
				return *a.cost < *b.cost
			}
			if (a.account.ID == sticky) != (b.account.ID == sticky) {
				return a.account.ID == sticky
			}
		}
		return a.order < b.order
	})
	ctx = context.WithValue(ctx, compositeSourceBucketsKey{}, buckets)
	var waiting *AccountSelectionResult
	var waitDecision OpenAIAccountScheduleDecision
	// A source is allowed to use only accounts in the already expanded eligible
	// pool. The selector rechecks ownership, health, model and endpoint again.
	seenSources := map[int]bool{}
	for _, c := range pool {
		if !costFirst && seenSources[c.order] {
			continue
		}
		seenSources[c.order] = true
		sourceCtx := compositeSourceContext(ctx, d, c.source)
		blocked := make(map[int64]struct{}, len(pool)+len(excluded))
		for id := range excluded {
			blocked[id] = struct{}{}
		}
		for _, accounts := range buckets {
			for _, a := range accounts {
				if (costFirst && a.ID != c.account.ID) || a.Platform != c.source.TargetPlatform {
					blocked[a.ID] = struct{}{}
				}
			}
		}
		result, decision, err := s.selectAccountWithScheduler(sourceCtx, groupID, previous, session, c.source.UpstreamModel, blocked, transport, capability, image, compact, c.source.TargetPlatform, movable, tokenCost)
		if err != nil {
			if errors.Is(err, ErrNoAvailableAccounts) || errors.Is(err, ErrNoAvailableCompactAccounts) {
				continue
			}
			return nil, decision, err
		}
		if result == nil {
			continue
		}
		source := c.source
		result.CompositeSource = &source
		if result.Acquired {
			return result, decision, nil
		}
		if waiting == nil {
			waiting, waitDecision = result, decision
		}
	}
	if waiting != nil {
		return waiting, waitDecision, nil
	}
	return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
}

func compositeSourceContext(ctx context.Context, d CompositeRouteDecision, source CompositeRouteCandidate) context.Context {
	ctx = context.WithValue(ctx, compositeCandidatesKey{}, nil)
	d.Route = nil
	d.TargetPlatform = source.TargetPlatform
	d.UpstreamModel = source.UpstreamModel
	ctx = WithCompositeRouteDecision(ctx, d)
	return WithOpenAIForwardModel(ctx, source.UpstreamModel, false)
}

// ApplyCompositeSelection updates the request-local projection only after the
// scheduler has selected a single source. Public billing identity stays intact.
func ApplyCompositeSelection(ctx context.Context, r *AccountSelectionResult) context.Context {
	if r == nil || r.CompositeSource == nil {
		return ctx
	}
	public, _ := RequestedPublicModelFromContext(ctx)
	d, _ := ctx.Value(compositeCandidatesKey{}).(CompositeRouteDecision)
	selected := compositeSourceContext(ctx, CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, PublicModel: public}, *r.CompositeSource)
	return context.WithValue(selected, compositeCandidatesKey{}, d)
}
