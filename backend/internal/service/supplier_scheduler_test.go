package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierSchedulersRejectUnsupportedRequestBeforeSlots(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"supplier_resource_id": "resource"}}
	scheduler := &defaultOpenAIAccountScheduler{}
	for _, ctx := range []context.Context{context.Background(), WithSupplierResponsesEligibility(context.Background(), false)} {
		reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, PlatformOpenAI, "", false, "")
		require.Equal(t, "supplier_request_unsupported", reason)
		ok, reason := scheduler.isAccountRequestCompatibleReason(ctx, account, OpenAIAccountScheduleRequest{})
		require.False(t, ok)
		require.Equal(t, "supplier_request_unsupported", reason)
	}
	// Native eligibility only admits the candidate to ordinary checks; it is not
	// an authorization or concurrency permit and does not skip model eligibility.
	ctx := WithSupplierResponsesEligibility(context.Background(), true)
	account.Credentials["model_mapping"] = map[string]any{"allowed-model": "allowed-model"}
	require.Equal(t, "model_not_supported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, PlatformOpenAI, "other-model", false, ""))
	ok, reason := scheduler.isAccountRequestCompatibleReason(ctx, account, OpenAIAccountScheduleRequest{RequestedModel: "other-model"})
	require.False(t, ok)
	require.Equal(t, "model_not_supported", reason)
}

func TestSupplierSchedulerConsumesAllRemainingQuotaWithoutLegacyReserve(t *testing.T) {
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.9, DefaultThreshold7d: 0.9})
	extra := map[string]any{
		"codex_5h_used_percent":  99.999,
		"codex_5h_reset_at":      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
	paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	require.True(t, paused, "ordinary account retains its configured reserve")
	account.Credentials = map[string]any{"supplier_resource_id": "resource"}
	paused, _ = shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	require.False(t, paused, "supplier authorizes all remaining allowance")
	extra["codex_5h_used_percent"] = 100.0
	paused, decision := shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	require.True(t, paused)
	require.Equal(t, 1.0, decision.threshold)
}

func TestSupplierSchedulerFilterPreservesOrdinaryCandidates(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	require.NotEqual(t, "supplier_request_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(context.Background(), account, PlatformOpenAI, "", false, ""))
}
