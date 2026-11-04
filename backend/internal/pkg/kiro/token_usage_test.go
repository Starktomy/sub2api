package kiro

import "testing"

func TestProviderTokenUsagePreservesUnknownAndZero(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"tokenUsage":{"uncachedInputTokens":0,"outputTokens":0,"totalTokens":0}}`, true},
		{`{"stopReason":"END_TURN"}`, false},
		{`{"tokenUsage":{"outputTokens":3,"totalTokens":3}}`, false},
		{`{"tokenUsage":{"uncachedInputTokens":-1,"outputTokens":1,"totalTokens":0}}`, false},
	} {
		e := Event{messageType: "event", eventType: "metadataEvent", raw: []byte(tc.raw)}
		m, ok := e.Metadata()
		if !ok || m.TokenUsage.ValidComplete() != tc.valid {
			t.Fatalf("unexpected token eligibility: %s", tc.raw)
		}
	}
}
