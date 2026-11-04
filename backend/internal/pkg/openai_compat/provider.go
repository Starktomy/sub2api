package openai_compat

import "github.com/Wei-Shaw/sub2api/internal/pkg/upstream"

const ProviderID = "openai-compatible"

type protocolSelector struct{}

// Provider wraps the existing API-key routing policy, not native OAuth transport.
// Arbitrary compatible servers do not guarantee streaming or model discovery;
// even Responses support remains unknown until account-level evidence exists.
func Provider() upstream.Provider {
	return upstream.Provider{ID: ProviderID, Selector: protocolSelector{}, Capabilities: upstream.Capabilities{
		Responses:       upstream.Unknown,
		ChatCompletions: upstream.Unknown,
		Streaming:       upstream.Unknown,
		ModelDiscovery:  upstream.Unknown,
	}}
}

func (protocolSelector) SelectProtocol(h upstream.RouteHints) upstream.Protocol {
	extra := make(map[string]any, 2)
	switch h.ResponsesOverride {
	case upstream.Responses:
		extra[ExtraKeyResponsesMode] = string(ResponsesSupportModeForceResponses)
	case upstream.ChatCompletions:
		extra[ExtraKeyResponsesMode] = string(ResponsesSupportModeForceChatCompletions)
	}
	switch h.ObservedResponses {
	case upstream.Supported:
		extra[ExtraKeyResponsesSupported] = true
	case upstream.Unsupported:
		extra[ExtraKeyResponsesSupported] = false
	}
	if ShouldUseResponsesAPI(extra) {
		return upstream.Responses
	}
	return upstream.ChatCompletions
}

// RoutingHints extracts only the two legacy policy inputs. No other account
// metadata (in particular credentials or URLs) crosses the registry boundary.
func RoutingHints(extra map[string]any) upstream.RouteHints {
	var h upstream.RouteHints
	mode, _ := extra[ExtraKeyResponsesMode].(string)
	switch NormalizeResponsesSupportMode(mode) {
	case ResponsesSupportModeForceResponses:
		h.ResponsesOverride = upstream.Responses
	case ResponsesSupportModeForceChatCompletions:
		h.ResponsesOverride = upstream.ChatCompletions
	}
	if supported, ok := extra[ExtraKeyResponsesSupported].(bool); ok {
		h.ObservedResponses = upstream.Unsupported
		if supported {
			h.ObservedResponses = upstream.Supported
		}
	}
	return h
}
