package service

import (
	"encoding/json"
	"math"
	"time"
)

// Presence-aware supplier-only wire types. Shared legacy quota DTOs stay unchanged.
type supplierQuotaWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
	ResetAt            *int64   `json:"reset_at"`
	ResetAfterSeconds  int64    `json:"reset_after_seconds"`
}
type supplierQuotaPayload struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Allowed         *bool           `json:"allowed"`
		LimitReached    *bool           `json:"limit_reached"`
		PrimaryWindow   json.RawMessage `json:"primary_window"`
		SecondaryWindow json.RawMessage `json:"secondary_window"`
	} `json:"rate_limit"`
}

func supplierQuotaWindowProjection(raw json.RawMessage) (*OpenAIRateLimitWindow, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var w *supplierQuotaWindow
	if json.Unmarshal(raw, &w) != nil {
		return nil, false
	}
	if w == nil {
		return nil, true
	} // An explicitly-null unused window, not missing data.
	if w.UsedPercent == nil || w.LimitWindowSeconds == nil || w.ResetAt == nil {
		return nil, false
	}
	return &OpenAIRateLimitWindow{UsedPercent: *w.UsedPercent, LimitWindowSeconds: *w.LimitWindowSeconds, ResetAt: *w.ResetAt, ResetAfterSeconds: w.ResetAfterSeconds}, true
}
func parseSupplierQuota(raw []byte, now time.Time) (*SupplierResourceUsage, error) {
	var p supplierQuotaPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &SupplierProbeFailure{Code: "quota_unknown"}
	}
	switch p.PlanType {
	case "free", "plus", "pro", "team", "business", "enterprise", "edu":
	default:
		p.PlanType = ""
	}
	u := &SupplierResourceUsage{PlanType: p.PlanType, FetchedAt: now.Unix()}
	if p.RateLimit != nil && p.RateLimit.Allowed != nil && p.RateLimit.LimitReached != nil {
		u.RateLimit = &OpenAIRateLimit{Allowed: *p.RateLimit.Allowed, LimitReached: *p.RateLimit.LimitReached}
		u.RateLimit.PrimaryWindow, u.PrimaryWindowKnown = supplierQuotaWindowProjection(p.RateLimit.PrimaryWindow)
		u.RateLimit.SecondaryWindow, u.SecondaryWindowKnown = supplierQuotaWindowProjection(p.RateLimit.SecondaryWindow)
	}
	return u, nil
}

// SupplierQuotaAvailability returns a public reason, never upstream text. There is
// no reserved quota: any finite known 0<=used<100 is available until window expiry.
func SupplierQuotaAvailability(u *SupplierResourceUsage, now time.Time) string {
	if u == nil || u.RateLimit == nil {
		return "quota_unknown"
	}
	q := u.RateLimit
	if !q.Allowed || q.LimitReached {
		return "quota_exhausted"
	}
	if q.PrimaryWindow == nil && q.SecondaryWindow == nil {
		return "quota_unknown"
	}
	for i, w := range []*OpenAIRateLimitWindow{q.PrimaryWindow, q.SecondaryWindow} {
		if w == nil {
			if (i == 0 && u.PrimaryWindowKnown) || (i == 1 && u.SecondaryWindowKnown) {
				continue
			}
			return "quota_unknown"
		}
		if math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) || w.UsedPercent < 0 || w.LimitWindowSeconds <= 0 || w.ResetAt <= 0 {
			return "quota_unknown"
		}
		if w.UsedPercent >= 100 {
			return "quota_exhausted"
		}
		if w.ResetAt <= now.Unix() {
			return "quota_stale"
		}
		if float64(w.ResetAt) > float64(now.Unix())+float64(w.LimitWindowSeconds) {
			return "quota_unknown"
		}
	}
	return ""
}

// SupplierQuotaSchedulerExtra projects known quota into the existing Codex
// scheduler fields. The authoritative eligibility gate remains PostgreSQL.
func SupplierQuotaSchedulerExtra(u *SupplierResourceUsage, now time.Time) map[string]any {
	out := map[string]any{}
	if SupplierQuotaAvailability(u, now) != "" {
		return out
	}
	a, b := u.RateLimit.PrimaryWindow, u.RateLimit.SecondaryWindow
	if a == nil || (b != nil && a.LimitWindowSeconds > b.LimitWindowSeconds) {
		a, b = b, a
	}
	for name, w := range map[string]*OpenAIRateLimitWindow{"5h": a, "7d": b} {
		if w == nil {
			// Clear an older applicable window without inventing zero usage.
			for _, field := range []string{"used_percent", "window_minutes", "reset_at", "reset_after_seconds"} {
				out["codex_"+name+"_"+field] = nil
			}
			continue
		}
		out["codex_"+name+"_used_percent"] = w.UsedPercent
		out["codex_"+name+"_window_minutes"] = w.LimitWindowSeconds / 60
		out["codex_"+name+"_reset_at"] = time.Unix(w.ResetAt, 0).UTC().Format(time.RFC3339)
		out["codex_"+name+"_reset_after_seconds"] = w.ResetAt - u.FetchedAt
	}
	out["codex_usage_updated_at"] = time.Unix(u.FetchedAt, 0).UTC().Format(time.RFC3339)
	return out
}
