package service

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierQuotaPresenceAndAllAvailable(t *testing.T) {
	now := time.Unix(1700000000, 0)
	window := map[string]any{"used_percent": 99.999, "limit_window_seconds": 18000, "reset_at": now.Unix() + 1000}
	valid := func() map[string]any {
		return map[string]any{"allowed": true, "limit_reached": false, "primary_window": window, "secondary_window": window}
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"all remaining quota", func(map[string]any) {}, ""},
		{"missing allowed", func(q map[string]any) { delete(q, "allowed") }, "quota_unknown"},
		{"missing limit reached", func(q map[string]any) { delete(q, "limit_reached") }, "quota_unknown"},
		{"allowed false", func(q map[string]any) { q["allowed"] = false }, "quota_exhausted"},
		{"limit reached", func(q map[string]any) { q["limit_reached"] = true }, "quota_exhausted"},
		{"missing primary", func(q map[string]any) { delete(q, "primary_window") }, "quota_unknown"},
		{"missing secondary", func(q map[string]any) { delete(q, "secondary_window") }, "quota_unknown"},
		{"null secondary", func(q map[string]any) { q["secondary_window"] = nil }, ""},
		{"both null", func(q map[string]any) { q["primary_window"] = nil; q["secondary_window"] = nil }, "quota_unknown"},
		{"missing used percent", func(q map[string]any) {
			q["primary_window"] = map[string]any{"limit_window_seconds": 18000, "reset_at": now.Unix() + 100}
		}, "quota_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := valid()
			tc.edit(q)
			raw, err := json.Marshal(map[string]any{"rate_limit": q})
			require.NoError(t, err)
			u, err := parseSupplierQuota(raw, now)
			require.NoError(t, err)
			require.Equal(t, tc.want, SupplierQuotaAvailability(u, now))
		})
	}
	for _, used := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 100, 101} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			w := &OpenAIRateLimitWindow{UsedPercent: used, LimitWindowSeconds: 18000, ResetAt: now.Unix() + 100}
			require.NotEmpty(t, SupplierQuotaAvailability(&SupplierResourceUsage{RateLimit: &OpenAIRateLimit{Allowed: true, PrimaryWindow: w, SecondaryWindow: w}}, now))
		})
	}
}
func TestSupplierQuotaWindowExpiry(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, reset := range []int64{0, now.Unix() - 1, now.Unix(), now.Unix() + 18001} {
		w := &OpenAIRateLimitWindow{UsedPercent: 0, LimitWindowSeconds: 18000, ResetAt: reset}
		require.NotEmpty(t, SupplierQuotaAvailability(&SupplierResourceUsage{RateLimit: &OpenAIRateLimit{Allowed: true, PrimaryWindow: w, SecondaryWindow: w}}, now))
	}
}
