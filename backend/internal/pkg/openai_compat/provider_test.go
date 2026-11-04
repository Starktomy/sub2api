package openai_compat

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstream"
)

func TestProviderPreservesLegacyPolicy(t *testing.T) {
	p := Provider()
	if p.Capabilities != (upstream.Capabilities{}) {
		t.Fatal("unobserved server capabilities must remain unknown")
	}
	for _, mode := range []any{nil, "auto", "invalid", 42, "force_responses", "force_chat_completions"} {
		for _, observed := range []any{nil, true, false, "false", 42} {
			extra := map[string]any{ExtraKeyResponsesMode: mode, ExtraKeyResponsesSupported: observed}
			want := upstream.ChatCompletions
			if ShouldUseResponsesAPI(extra) {
				want = upstream.Responses
			}
			if got := p.Selector.SelectProtocol(RoutingHints(extra)); got != want {
				t.Fatalf("mode=%v observed=%v got=%s want=%s", mode, observed, got, want)
			}
		}
	}
	if p.Selector.SelectProtocol(RoutingHints(nil)) != upstream.Responses {
		t.Fatal("unknown must keep legacy route, not advertise support")
	}
}
