package service

import (
	"errors"
	"slices"

	"github.com/tidwall/gjson"
)

// Explicit provider boundaries apply at the final native Messages executor,
// including converted ingress and management connection tests.
func validateNativeCompatibleMessages(account *Account, body []byte, model string) error {
	if account == nil {
		return nil
	}
	profile := account.providerProfile()
	if profile == nil || !profile.SimpleCompatible {
		return nil
	}
	if !profile.SupportsProtocol(account.GetCredential("account_mode"), APIProtocolAnthropic) {
		return errors.New("native Messages is not supported by this provider")
	}
	if profile.NativeAnthropicNonStreaming && gjson.GetBytes(body, "stream").Bool() {
		return errors.New("native Messages streaming is not verified for this provider; only non-streaming requests are supported")
	}
	if account.Platform == PlatformBailian && !slices.Contains(bailianNativeMessagesModels, model) {
		return errors.New("model is not in the verified Bailian native Messages model set")
	}
	return nil
}

// Snapshot of the official Anthropic compatibility model list, not a live
// discovery result or a promise of account entitlement. New models fail closed.
var bailianNativeMessagesModels = []string{
	"qwen3.8-max", "qwen3.8-max-0902", "qwen3.7-max", "qwen3.7-max-2026-05-20", "qwen3.7-max-2026-06-08",
	"qwen3.7-plus", "qwen3.7-plus-2026-05-26", "qwen3.6-plus", "qwen3.6-plus-2026-04-02", "qwen3.5-plus", "qwen3.5-plus-2026-04-20", "qwen3.5-plus-2026-02-15", "qwen-plus", "qwen-plus-latest",
	"qwen3.8-flash", "qwen3.7-flash", "qwen3.7-flash-2026-07-15", "qwen3.6-flash", "qwen3.6-flash-2026-04-16", "qwen3.5-flash", "qwen3.5-flash-2026-02-23", "qwen-flash", "qwen-flash-2025-07-28", "qwen3-coder-flash", "qwen3-vl-plus", "qwen3.6-27b", "qwen3.5-397b-a17b", "qwen3.5-122b-a10b", "qwen3.5-27b", "qwen3.5-35b-a3b", "qwen3.8-2.4t-a95b", "qwen3.8-27b",
	"deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-pro-0813", "deepseek-v4-flash-0731", "kimi-k3", "kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5", "glm-5.3", "glm-5.2", "glm-5.1", "glm-5", "MiniMax-M2.5", "deepseek-v4.1-flash-1017",
}
