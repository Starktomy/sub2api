package service

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstream"
)

const ExtraKeyUpstreamProvider = "upstream_provider"

// ProviderDescriptor is deliberately an allowlist of public adapter metadata.
// Operations list wired host adapter entrypoints (conditional per Conditions),
// not an authorization allowlist or guarantees about arbitrary hosts
// or every model. Credentials, endpoint URLs and account values are never exposed.
type ProviderDescriptor struct {
	ID              string            `json:"id"`
	DisplayName     string            `json:"display_name"`
	Platform        string            `json:"platform"`
	AccountTypes    []string          `json:"account_types"`
	Protocols       []string          `json:"protocols"`
	Authentication  []string          `json:"authentication"`
	ExecutionFamily string            `json:"execution_family"`
	Operations      []string          `json:"operations"`
	Capabilities    map[string]string `json:"capabilities"`
	// Conditions explain mode/account/model gates without exposing config values.
	Conditions   []string `json:"conditions"`
	EvidenceURLs []string `json:"evidence_urls,omitempty"`
}

type builtinModule struct {
	descriptor ProviderDescriptor
}

var upstreamModules, builtinUpstreamProviders = buildUpstreamModules()

func buildUpstreamModules() (map[string]builtinModule, *upstream.Registry) {
	modules := make(map[string]builtinModule)
	var providers []upstream.Provider
	add := func(id, platform, accountType, family, auth string, protocols, operations []string, selector upstream.ProtocolSelector) {
		displayName := id
		if spec, ok := domain.LookupPlatform(platform); ok {
			displayName = spec.DisplayName + " (" + id + ")"
		}
		p := upstream.Provider{ID: id, Selector: selector, ExecutionFamily: family, Authentication: auth}
		if id == openai_compat.ProviderID {
			p.Capabilities = openai_compat.Provider().Capabilities
		}
		modules[id] = builtinModule{descriptor: ProviderDescriptor{
			ID: id, DisplayName: displayName, Platform: platform, AccountTypes: []string{accountType}, Protocols: protocols,
			Authentication: []string{auth}, ExecutionFamily: family, Operations: operations,
			Capabilities: map[string]string{"responses": "unknown", "chat_completions": "unknown", "streaming": "unknown", "model_discovery": "unknown"},
		}}
		providers = append(providers, p)
	}
	textOps := []string{"inference", "connection_test"}
	add(openai_compat.ProviderID, PlatformOpenAI, AccountTypeAPIKey, "openai", "bearer_api_key", []string{APIProtocolResponses, APIProtocolChatCompletions}, []string{"inference", "connection_test", "models", "images", "embeddings", "seedance", "search"}, openai_compat.Provider().Selector)
	for platform, profile := range providerProfiles {
		var protocols []string
		for _, endpoints := range profile.Modes {
			for protocol := range endpoints.BaseURLs {
				if !slices.Contains(protocols, protocol) {
					protocols = append(protocols, protocol)
				}
			}
		}
		slices.Sort(protocols)
		add(platform, platform, AccountTypeAPIKey, "openai_profile", "profile_api_key", protocols, []string{"inference", "connection_test", "models"}, upstream.NativeSelector{Default: upstream.ChatCompletions})
	}
	add("anthropic-apikey", PlatformAnthropic, AccountTypeAPIKey, "anthropic", "anthropic_api_key", []string{"anthropic"}, []string{"inference", "connection_test", "models", "count_tokens"}, upstream.NativeSelector{Default: upstream.Anthropic})
	add("anthropic-vertex", PlatformAnthropic, AccountTypeServiceAccount, "anthropic_vertex", "vertex_service_account", []string{"anthropic"}, []string{"inference", "connection_test", "count_tokens"}, upstream.NativeSelector{Default: upstream.Anthropic})
	add("bedrock", PlatformAnthropic, AccountTypeBedrock, "bedrock", "bedrock_sigv4_or_bearer", []string{"bedrock"}, textOps, upstream.NativeSelector{Default: upstream.Bedrock})
	add("gemini-apikey", PlatformGemini, AccountTypeAPIKey, "gemini", "google_api_key", []string{"gemini"}, []string{"inference", "connection_test", "models", "count_tokens", "batch_images"}, upstream.NativeSelector{Default: upstream.Gemini})
	add("gemini-vertex", PlatformGemini, AccountTypeServiceAccount, "gemini_vertex", "vertex_service_account_or_api_key", []string{"gemini"}, []string{"inference", "connection_test", "models", "count_tokens", "batch_images"}, upstream.NativeSelector{Default: upstream.Gemini})
	add("grok-apikey", PlatformGrok, AccountTypeAPIKey, "grok", "bearer_api_key", []string{APIProtocolResponses, APIProtocolChatCompletions}, []string{"inference", "connection_test", "models", "media", "voice", "search", "realtime"}, upstream.NativeSelector{Default: upstream.Responses})
	add("antigravity-apikey", PlatformAntigravity, AccountTypeAPIKey, "antigravity_compat", "compat_api_key", []string{"anthropic", "gemini"}, []string{"inference", "connection_test", "models", "count_tokens"}, upstream.NativeSelector{Default: upstream.Anthropic})
	add("antigravity-upstream", PlatformAntigravity, AccountTypeUpstream, "antigravity_upstream", "bearer_and_api_key", []string{"anthropic"}, textOps, upstream.NativeSelector{Default: upstream.Anthropic})
	add("typesafe", PlatformTypeSafe, AccountTypeAPIKey, "typesafe", "typesafe_api_key", []string{"systemone"}, []string{"systemone", "connection_test"}, upstream.NativeSelector{Default: upstream.SystemOne})

	for id, m := range modules {
		if !slices.Contains(m.descriptor.Protocols, APIProtocolResponses) {
			m.descriptor.Capabilities["responses"] = "unsupported"
		}
		if !slices.Contains(m.descriptor.Protocols, APIProtocolChatCompletions) {
			m.descriptor.Capabilities["chat_completions"] = "unsupported"
		}
		if profile := LookupProviderProfile(m.descriptor.Platform); profile != nil && profile.ModelDiscovery != nil && !*profile.ModelDiscovery {
			m.descriptor.Capabilities["model_discovery"] = "unsupported"
			m.descriptor.Operations = []string{"inference", "connection_test"}
		}
		if id == "typesafe" {
			m.descriptor.Capabilities["streaming"] = "unsupported"
			m.descriptor.Capabilities["model_discovery"] = "unsupported"
		}

		switch id {
		case "gemini-apikey":
			m.descriptor.Conditions = []string{"batch_images: official AI Studio endpoint and existing batch policy only"}
		case "gemini-vertex":
			m.descriptor.Conditions = []string{"auth_mode=apikey: Vertex Express/project API key; otherwise service-account token client", "batch_images: service-account JSON plus existing GCS/config gates; not Express", "model discovery: service-account publisher listing only; Express catalog sync unsupported"}
		case "bedrock":
			m.descriptor.Conditions = []string{"auth_mode: existing SigV4 or Bearer", "aws_auth_mode: static or operator credential_file policy; never default credential chain"}
		case "openai-compatible":
			m.descriptor.Conditions = []string{"unknown Responses evidence retains legacy route, not positive capability", "images/embeddings/seedance: existing account endpoint and model gates apply", "custom hosts including Ollama retain existing URL/auth/reasoning policy"}
		case "typesafe":
			m.descriptor.Conditions = []string{"native SystemOne only; no protocol conversion or streaming"}
		case "antigravity-apikey":
			m.descriptor.Conditions = []string{"third-party compatibility relay only; not native CloudCode OAuth"}

		case "siliconflow":
			m.descriptor.Conditions = []string{"native Messages: non-streaming only; stream=true rejected before execution, including forced-stream converted ingress and connection tests", "Bearer API key; stream usage contract is not verified"}
		case "bailian":
			m.descriptor.Conditions = []string{"native Messages uses independent Beijing /apps/anthropic base and verified official model allowlist", "API-key auth; final message_delta usage includes cache counters; listed model support is not account entitlement"}
		case "tokenhub":
			m.descriptor.Conditions = []string{"native Messages uses x-api-key and anthropic-version=2023-06-01; model-specific features vary", "Responses provider-side conversion is not advertised as native Responses"}
		case "openrouter":
			m.descriptor.Conditions = []string{"Responses stateless only: store=true, previous_response_id, conversation and background are rejected before execution", "live listing is metadata, not credential/model/protocol invocation authorization"}
		case "mistral":
			m.descriptor.Conditions = []string{"Responses public preview; stateless only; model-specific tools/vision are not guaranteed", "live listing is metadata, not credential/model/protocol invocation authorization"}
		case "qianfan_v2":
			m.descriptor.Conditions = []string{"live GET /v2/models; listed endpoint metadata does not enable additional protocols", "Responses and Anthropic execution are not enabled in this slice"}
		default:
			m.descriptor.Conditions = []string{"existing endpoint, model, credential and security gates remain authoritative"}
		}

		switch id {
		case "openrouter":
			m.descriptor.EvidenceURLs = []string{"https://openrouter.ai/openapi.json", "https://openrouter.ai/docs/api/reference/responses/overview", "https://openrouter.ai/docs/api/api-reference/models/get-models"}
		case "mistral":
			m.descriptor.EvidenceURLs = []string{"https://docs.mistral.ai/studio/conversations/responses.md", "https://docs.mistral.ai/api/endpoint/models"}
		case "siliconflow":
			m.descriptor.EvidenceURLs = []string{"https://api-docs.siliconflow.cn/docs/api/messages-post"}
		case "bailian":
			m.descriptor.EvidenceURLs = []string{"https://help.aliyun.com/zh/model-studio/anthropic-api-messages"}
		case "tokenhub":
			m.descriptor.EvidenceURLs = []string{"https://cloud.tencent.com/document/product/1823/130079", "https://cloud.tencent.com/document/product/1823/135874"}
		case "qianfan_v2":
			m.descriptor.EvidenceURLs = []string{"https://cloud.baidu.com/doc/qianfan-api/s/Dmba8k71y"}
		}
		modules[id] = m
	}
	for i := range providers {
		d := modules[providers[i].ID].descriptor
		if d.Capabilities["responses"] == "unsupported" {
			providers[i].Capabilities.Responses = upstream.Unsupported
		}
		if d.Capabilities["chat_completions"] == "unsupported" {
			providers[i].Capabilities.ChatCompletions = upstream.Unsupported
		}
		if d.Capabilities["streaming"] == "unsupported" {
			providers[i].Capabilities.Streaming = upstream.Unsupported
		}
		if d.Capabilities["model_discovery"] == "unsupported" {
			providers[i].Capabilities.ModelDiscovery = upstream.Unsupported
		}
	}
	r, err := upstream.NewRegistry(providers...)
	if err != nil {
		panic(err)
	}
	return modules, r
}

func UpstreamProviderCatalog() []ProviderDescriptor {
	out := make([]ProviderDescriptor, 0, len(upstreamModules))
	for _, id := range builtinUpstreamProviders.IDs() {
		d := upstreamModules[id].descriptor
		d.AccountTypes = slices.Clone(d.AccountTypes)
		d.Protocols = slices.Clone(d.Protocols)
		d.Authentication = slices.Clone(d.Authentication)
		d.Operations = slices.Clone(d.Operations)
		d.Conditions = slices.Clone(d.Conditions)
		d.EvidenceURLs = slices.Clone(d.EvidenceURLs)
		caps := make(map[string]string, len(d.Capabilities))
		for k, v := range d.Capabilities {
			caps[k] = v
		}
		d.Capabilities = caps
		out = append(out, d)
	}
	return out
}

func defaultUpstreamProviderID(account *Account) string {
	if account == nil {
		return ""
	}
	if account.Type == AccountTypeAPIKey {
		if account.providerProfile() != nil {
			return account.Platform
		}
		switch account.Platform {
		case PlatformOpenAI:
			return openai_compat.ProviderID
		case PlatformAnthropic:
			return "anthropic-apikey"
		case PlatformGemini:
			return "gemini-apikey"
		case PlatformGrok:
			return "grok-apikey"
		case PlatformAntigravity:
			return "antigravity-apikey"
		case PlatformTypeSafe:
			return "typesafe"
		}
	}
	if account.Platform == PlatformAnthropic {
		if account.Type == AccountTypeBedrock {
			return "bedrock"
		}
		if account.Type == AccountTypeServiceAccount {
			return "anthropic-vertex"
		}
	}
	if account.Platform == PlatformGemini && account.Type == AccountTypeServiceAccount {
		return "gemini-vertex"
	}
	if account.Platform == PlatformAntigravity && account.Type == AccountTypeUpstream {
		return "antigravity-upstream"
	}
	return ""
}

// resolveAccountUpstreamModule leaves legacy OAuth/session accounts untouched.
// An explicit binding cannot change platform, account type, native profile,
// endpoint security checks, or the credential lifecycle.
func resolveAccountUpstreamModule(account *Account) (upstream.Provider, error) {
	if account == nil {
		return upstream.Provider{}, nil
	}
	raw, explicit := account.Extra[ExtraKeyUpstreamProvider]
	id := ""
	if explicit {
		var ok bool
		id, ok = raw.(string)
		if !ok || id == "" {
			return upstream.Provider{}, fmt.Errorf("upstream_provider must be a non-empty provider ID")
		}
	}
	if profile := account.providerProfile(); profile != nil && profile.SimpleCompatible {
		if account.Type != AccountTypeAPIKey {
			return upstream.Provider{}, fmt.Errorf("compatible provider requires API-key credentials")
		}
		protocol := account.GetCredential("api_protocol")
		if protocol != "" && !profile.SupportsProtocol(account.GetCredential("account_mode"), protocol) {
			return upstream.Provider{}, fmt.Errorf("compatible provider protocol is not supported")
		}
		if strings.Contains(account.GetCredential("api_key"), "|") {
			return upstream.Provider{}, fmt.Errorf("compatible provider does not support compound or legacy credentials")
		}
	}
	fallback := defaultUpstreamProviderID(account)
	if fallback == "" && !explicit {
		return upstream.Provider{}, nil
	}
	p, err := builtinUpstreamProviders.Resolve(id, fallback)
	if err != nil {
		return upstream.Provider{}, err
	}
	d := upstreamModules[p.ID].descriptor
	if d.Platform != account.Platform || !slices.Contains(d.AccountTypes, account.Type) {
		return upstream.Provider{}, fmt.Errorf("upstream provider %q does not support platform %q and account type %q", p.ID, account.Platform, account.Type)
	}
	return p, nil
}

func validateUpstreamProviderBinding(account *Account) error {
	_, err := resolveAccountUpstreamModule(account)
	return err
}

// Only the unprofiled legacy probe decision uses this selector. Profiles retain
// their existing model/pinned/catalog authority and supply NativeProtocol below.
func openAIAPIKeyProtocol(account *Account) (upstream.Protocol, error) {
	p, err := resolveAccountUpstreamModule(account)
	if err != nil {
		return "", err
	}
	if p.ID == openai_compat.ProviderID {
		return p.Selector.SelectProtocol(openai_compat.RoutingHints(account.Extra)), nil
	}
	// Preserve callers that historically applied the probe policy to other
	// unprofiled compatible accounts (including connection-test routing).
	return openai_compat.Provider().Selector.SelectProtocol(openai_compat.RoutingHints(account.Extra)), nil
}

func moduleNativeProtocol(account *Account, legacy string) string {
	p, err := resolveAccountUpstreamModule(account)
	if err != nil || p.Selector == nil || p.ID == openai_compat.ProviderID {
		return legacy
	} // execution entry rejects invalid bindings
	return string(p.Selector.SelectProtocol(upstream.RouteHints{NativeProtocol: upstream.Protocol(legacy)}))
}

// moduleCredentialType delegates to existing credential-specific clients, not
// generic token code. OAuth/session dispatch never enters this mapping.
func moduleCredentialType(account *Account) (string, error) {
	p, err := resolveAccountUpstreamModule(account)
	if err != nil {
		return "", err
	}
	switch p.Authentication {
	case "anthropic_api_key", "google_api_key", "bearer_api_key", "profile_api_key", "compat_api_key", "typesafe_api_key":
		return AccountTypeAPIKey, nil
	case "vertex_service_account", "vertex_service_account_or_api_key":
		return AccountTypeServiceAccount, nil
	case "bedrock_sigv4_or_bearer":
		return AccountTypeBedrock, nil
	case "bearer_and_api_key":
		return AccountTypeUpstream, nil
	default:
		return account.Type, nil
	}
}

func hasUpstreamProviderUpdate(extra map[string]any) bool {
	_, ok := extra[ExtraKeyUpstreamProvider]
	return ok
}

func validateUpstreamProviderUpdates(account *Account, updates map[string]any) error {
	candidate := *account
	candidate.Extra = make(map[string]any, len(account.Extra)+1)
	for k, v := range account.Extra {
		candidate.Extra[k] = v
	}
	if v, ok := updates[ExtraKeyUpstreamProvider]; ok {
		if v == nil {
			delete(candidate.Extra, ExtraKeyUpstreamProvider)
		} else {
			candidate.Extra[ExtraKeyUpstreamProvider] = v
		}
	}
	return validateUpstreamProviderBinding(&candidate)
}
