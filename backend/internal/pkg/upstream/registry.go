// Package upstream defines credential-free upstream routing contracts. It does
// not execute requests, discover plugins, or select accounts.
package upstream

import (
	"fmt"
	"sort"
)

type Support uint8

const (
	Unknown Support = iota
	Supported
	Unsupported
)

type Protocol string

const (
	Responses       Protocol = "responses"
	ChatCompletions Protocol = "chat_completions"
	Anthropic       Protocol = "anthropic"
	Gemini          Protocol = "gemini"
	Bedrock         Protocol = "bedrock"
	SystemOne       Protocol = "systemone"
)

// Capabilities describes evidence, not the gateway's ability to convert protocols.
// Unknown must not be interpreted as a positive capability advertisement.
type Capabilities struct {
	Responses       Support
	ChatCompletions Support
	Streaming       Support
	ModelDiscovery  Support
}

// RouteHints contains only normalized routing evidence, never account data or secrets.
type RouteHints struct {
	ResponsesOverride Protocol
	ObservedResponses Support
	// NativeProtocol is resolved by the host's existing profile/rules/catalog.
	// Selectors must not replace those account-specific authorities.
	NativeProtocol Protocol
}

// ProtocolSelector is deliberately independent of transport, authentication,
// model catalogs, scheduling, and billing. Implementations must be immutable and
// safe for concurrent use.
type ProtocolSelector interface {
	SelectProtocol(RouteHints) Protocol
}

type Provider struct {
	ID           string
	Capabilities Capabilities
	Selector     ProtocolSelector
	// ExecutionFamily and Authentication name existing host adapters; they do
	// not grant operations, endpoints, or credential access by themselves.
	ExecutionFamily string
	Authentication  string
}

// NativeSelector delegates account-specific protocol authority to the host.
// It is used by native adapters whose policy is already resolved by profiles.
type NativeSelector struct{ Default Protocol }

func (s NativeSelector) SelectProtocol(h RouteHints) Protocol {
	if h.NativeProtocol != "" {
		return h.NativeProtocol
	}
	return s.Default
}

// Registry is a construction-time snapshot. There is no runtime registration API.
// Provider metadata is copied; selectors must satisfy the immutable contract above.
type Registry struct {
	providers map[string]Provider
	ids       []string
}

func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if p.ID == "" || p.Selector == nil {
			return nil, fmt.Errorf("upstream provider requires an ID and protocol selector")
		}
		if _, exists := r.providers[p.ID]; exists {
			return nil, fmt.Errorf("duplicate upstream provider %q", p.ID)
		}
		r.providers[p.ID] = p
		r.ids = append(r.ids, p.ID)
	}
	sort.Strings(r.ids)
	return r, nil
}

func (r *Registry) IDs() []string { return append([]string(nil), r.ids...) }

// Resolve uses the legacy default only when no explicit provider was supplied.
// An unsupported explicit ID never silently falls back.
func (r *Registry) Resolve(explicit, legacyDefault string) (Provider, error) {
	id := explicit
	if id == "" {
		id = legacyDefault
	}
	p, ok := r.providers[id]
	if !ok {
		return Provider{}, fmt.Errorf("unsupported upstream provider %q", id)
	}
	return p, nil
}
