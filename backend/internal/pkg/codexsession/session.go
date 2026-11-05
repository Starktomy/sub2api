package codexsession

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrSession = errors.New("invalid or unsupported supplier session JSON")

type Session struct {
	AccessToken, AccountID, UserID, Email, Plan string
	ExpiresAt                                   time.Time
}

// ParseSupplier retains only access_token and unverified identity hints. Identity
// claims are not proof of account ownership or commercial supply permission.
func ParseSupplier(content string) (Session, error) {
	var out Session
	if len(content) == 0 || len(content) > 1<<20 {
		return out, ErrSession
	}
	var raw map[string]any
	if json.Unmarshal([]byte(content), &raw) != nil || raw == nil {
		return out, ErrSession
	}
	// ChatGPT's full session response contains a browser sessionToken and a
	// boolean MFA flag. Discard those known metadata fields before validation;
	// neither is needed or persisted by supplier import.
	if _, ok := raw["accessToken"].(string); ok {
		if _, accountOK := raw["account"].(map[string]any); accountOK {
			if user, userOK := raw["user"].(map[string]any); userOK {
				delete(raw, "sessionToken")
				if _, isBool := user["mfa"].(bool); isBool {
					delete(user, "mfa")
				}
			}
		}
	}
	var forbidden func(any) bool
	forbidden = func(v any) bool {
		switch m := v.(type) {
		case map[string]any:
			for k, v := range m {
				switch strings.ToLower(strings.ReplaceAll(k, "_", "")) {
				case "password", "cookie", "cookies", "sessiontoken", "2fa", "mfa", "totp", "agentidentity", "agentprivatekey", "groupids", "proxyid", "status", "rate", "ratemultiplier", "role", "admin":
					return true
				}
				if forbidden(v) {
					return true
				}
			}
		case []any:
			for _, v := range m {
				if forbidden(v) {
					return true
				}
			}
		}
		return false
	}
	if forbidden(raw) {
		return out, ErrSession
	}
	get := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	out.AccessToken = get(raw, "access_token", "accessToken")
	if tokens, ok := raw["tokens"].(map[string]any); ok && out.AccessToken == "" {
		out.AccessToken = get(tokens, "access_token", "accessToken")
	}
	if out.AccessToken == "" || len(out.AccessToken) > 65536 {
		return Session{}, ErrSession
	}
	claims, err := DecodeClaims(out.AccessToken)
	if err != nil || claims.Exp <= time.Now().Unix() {
		return Session{}, ErrSession
	}
	out.ExpiresAt = time.Unix(claims.Exp, 0).UTC()
	var emailOK bool
	out.Email, emailOK = claims.IdentityEmail()
	if !emailOK {
		return Session{}, ErrSession
	}
	// Compare every supplied email, not only the first fallback. Never silently
	// accept a pasted session for a different identity.
	declared := []string{get(raw, "email")}
	if user, ok := raw["user"].(map[string]any); ok {
		declared = append(declared, get(user, "email"))
	}
	for _, email := range declared {
		if email == "" {
			continue
		}
		if out.Email != "" && !strings.EqualFold(out.Email, email) {
			return Session{}, ErrSession
		}
		out.Email = email
	}
	out.UserID = claims.Sub
	if claims.OpenAIAuth != nil {
		out.AccountID = claims.OpenAIAuth.ChatGPTAccountID
		out.Plan = claims.OpenAIAuth.ChatGPTPlanType
		if claims.OpenAIAuth.ChatGPTUserID != "" {
			out.UserID = claims.OpenAIAuth.ChatGPTUserID
		}
	}
	if out.AccountID == "" {
		out.AccountID = get(raw, "chatgpt_account_id", "account_id")
		if a, ok := raw["account"].(map[string]any); ok && out.AccountID == "" {
			out.AccountID = get(a, "id", "account_id")
		}
	}
	if out.Email == "" {
		out.Email = get(raw, "email")
		if u, ok := raw["user"].(map[string]any); ok {
			out.Email = get(u, "email")
		}
	}
	if strings.ContainsAny(out.AccessToken, "\r\n") || strings.ContainsAny(out.AccountID, "\r\n") || out.AccountID == "" || len(out.AccountID) > 256 || len(out.UserID) > 256 || len(out.Email) > 320 || len(out.Plan) > 64 {
		return Session{}, ErrSession
	}
	return out, nil
}
