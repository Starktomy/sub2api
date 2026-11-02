package service

import (
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

func ProvideOAuthService(lifecycle *RuntimeLifecycle, proxies ProxyRepository, client ClaudeOAuthClient) *OAuthService {
	svc := NewManagedOAuthService(proxies, client)
	registerRuntimeService(lifecycle, "OAuthService", RuntimeScopeControl, svc)
	return svc
}

func ProvideGeminiOAuthService(lifecycle *RuntimeLifecycle, proxies ProxyRepository, client GeminiOAuthClient, codeAssist GeminiCliCodeAssistClient, drive geminicli.DriveClient, cfg *config.Config) *GeminiOAuthService {
	svc := NewManagedGeminiOAuthService(proxies, client, codeAssist, drive, cfg)
	registerRuntimeService(lifecycle, "GeminiOAuthService", RuntimeScopeControl, svc)
	return svc
}

func ProvideAntigravityOAuthService(lifecycle *RuntimeLifecycle, proxies ProxyRepository) *AntigravityOAuthService {
	svc := NewManagedAntigravityOAuthService(proxies)
	registerRuntimeService(lifecycle, "AntigravityOAuthService", RuntimeScopeControl, svc)
	return svc
}

func ProvideSubscriptionService(lifecycle *RuntimeLifecycle, groups GroupRepository, subscriptions UserSubscriptionRepository, billing *BillingCacheService, client *ent.Client, cfg *config.Config) *SubscriptionService {
	svc := NewManagedSubscriptionService(groups, subscriptions, billing, client, cfg)
	// Each process owns its local maintenance closures and L1 invalidation
	// subscription. A worker cannot drain another process's in-memory queue.
	registerRuntimeService(lifecycle, "SubscriptionService", RuntimeScopeProcess, svc, RuntimeStopStores)
	return svc
}

func ProvideUsageRecordWorkerPool(lifecycle *RuntimeLifecycle, cfg *config.Config) *UsageRecordWorkerPool {
	svc := NewManagedUsageRecordWorkerPool(cfg)
	registerRuntimeService(lifecycle, "UsageRecordWorkerPool", RuntimeScopeProcess, svc, RuntimeStopConsumers)
	return svc
}

func ProvideChannelService(lifecycle *RuntimeLifecycle, repo ChannelRepository, groups GroupRepository, invalidator APIKeyAuthCacheInvalidator, pricing *PricingService, bus ChannelCachePubSub) *ChannelService {
	svc := NewManagedChannelService(repo, groups, invalidator, pricing, bus)
	registerRuntimeService(lifecycle, "ChannelService", RuntimeScopeProcess, svc)
	return svc
}

func ProvideErrorPassthroughService(lifecycle *RuntimeLifecycle, repo ErrorPassthroughRepository, cache ErrorPassthroughCache) *ErrorPassthroughService {
	svc := NewManagedErrorPassthroughService(repo, cache)
	registerRuntimeService(lifecycle, "ErrorPassthroughService", RuntimeScopeProcess, svc)
	return svc
}

func ProvideTLSFingerprintProfileService(lifecycle *RuntimeLifecycle, repo TLSFingerprintProfileRepository, cache TLSFingerprintProfileCache) *TLSFingerprintProfileService {
	svc := NewManagedTLSFingerprintProfileService(repo, cache)
	registerRuntimeService(lifecycle, "TLSFingerprintProfileService", RuntimeScopeProcess, svc)
	return svc
}

func ProvideContentModerationService(lifecycle *RuntimeLifecycle, settings SettingRepository, repo ContentModerationRepository, cache ContentModerationHashCache, groups GroupRepository, users UserRepository, proxies ProxyRepository, invalidator APIKeyAuthCacheInvalidator, email *EmailService) *ContentModerationService {
	svc := NewManagedContentModerationService(settings, repo, cache, groups, users, proxies, invalidator, email)
	// Moderation can enqueue from inference and from administrator test actions;
	// request-local work remains in the process that accepted it.
	registerRuntimeService(lifecycle, "ContentModerationService", RuntimeScopeRequest, svc)
	return svc
}
