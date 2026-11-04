package service

import (
	"strings"
	"testing"
)

func TestUsageFactValidation(t *testing.T) {
	zero := 0.0
	base := UsageFactInput{ObservationID: strings.Repeat("a", 64), EvidenceDigest: strings.Repeat("b", 64), Source: "provider_reported", Quality: "reported", Outcome: "observed", Metrics: []UsageMetric{{Name: "usage", Unit: "credits", Value: &zero}}}
	h, err := base.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	null := base
	null.Metrics = []UsageMetric{{Name: "usage", Unit: "credits"}}
	n, err := null.ContentHash()
	if err != nil || h == n {
		t.Fatal("null conflated with explicit zero")
	}
	partial := base
	partial.Partial = true
	p, _ := partial.ContentHash()
	if p == h {
		t.Fatal("partial lost")
	}
	for _, mutate := range []func(*UsageFactInput){
		func(f *UsageFactInput) { f.Metrics = []UsageMetric{{Name: "prompt", Unit: "tokens"}} },
		func(f *UsageFactInput) { f.Source = "none" },
		func(f *UsageFactInput) { f.EvidenceDigest = "secret" },
		func(f *UsageFactInput) { f.Outcome = "success" },
		func(f *UsageFactInput) { f.Quality = "estimated" },
	} {
		bad := base
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("unsafe fact accepted")
		}
	}
	unknown := UsageFactInput{ObservationID: strings.Repeat("c", 64), EvidenceDigest: strings.Repeat("d", 64), Source: "none", Quality: "unknown", Outcome: "timeout", Partial: true}
	if unknown.Validate() != nil {
		t.Fatal("unknown rejected")
	}
}
