package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexsession"
	"github.com/imroc/req/v3"
)

const supplierIdentityURL = "https://chatgpt.com/backend-api/me"

// VerifySupplierIdentity makes one bounded read-only authenticated request. It
// never sends OTPs, refreshes credentials, changes privacy or generates traffic.
// Only conclusions are returned: no identity response or phone number is stored.
func (s *OpenAIQuotaService) VerifySupplierIdentity(ctx context.Context, token, account string) error {
	if s == nil || s.privacyClientFactory == nil {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	claims, err := codexsession.DecodeClaims(token)
	if err != nil || claims.Exp <= time.Now().Unix() || claims.OpenAIAuth == nil || claims.OpenAIAuth.ChatGPTAccountID != account {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	email, ok := claims.IdentityEmail()
	if !ok {
		return &SupplierProbeFailure{Code: "identity_email_mismatch"}
	}
	if strings.TrimSpace(email) == "" {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	client, err := s.privacyClientFactory("")
	if err != nil {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	client.SetRedirectPolicy(req.NoRedirectPolicy()).SetCommonRetryCount(0)
	call, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	defer cancel()
	resp, err := client.R().SetContext(call).SetRetryCount(0).DisableAutoReadResponse().SetHeaders(buildCodexCommonHeaders(token, account, false)).Get(supplierIdentityURL)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp == nil || !resp.IsSuccessState() {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return &SupplierProbeFailure{Code: "unauthorized"}
			case http.StatusForbidden:
				return &SupplierProbeFailure{Code: "forbidden"}
			case http.StatusTooManyRequests:
				return &SupplierProbeFailure{Code: "rate_limited", RetryAfter: time.Minute}
			}
		}
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	var me struct {
		ID    string          `json:"id"`
		Email string          `json:"email"`
		Phone json.RawMessage `json:"phone_number"`
	}
	if json.Unmarshal(raw, &me) != nil || strings.TrimSpace(me.ID) == "" || strings.TrimSpace(me.Email) == "" {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	// Prefer the provider's stable user claim; sub is the fallback for older tokens.
	user := claims.OpenAIAuth.ChatGPTUserID
	if user == "" {
		user = claims.OpenAIAuth.UserID
	}
	if user == "" {
		user = claims.Sub
	}
	if user == "" || me.ID != user {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	if !strings.EqualFold(strings.TrimSpace(me.Email), strings.TrimSpace(email)) {
		return &SupplierProbeFailure{Code: "identity_email_mismatch"}
	}
	if len(me.Phone) == 0 {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	if string(me.Phone) == "null" {
		return &SupplierProbeFailure{Code: "phone_verification_required"}
	}
	var phone string
	if json.Unmarshal(me.Phone, &phone) != nil {
		return &SupplierProbeFailure{Code: "identity_unknown"}
	}
	if strings.TrimSpace(phone) == "" {
		return &SupplierProbeFailure{Code: "phone_verification_required"}
	}
	return nil
}
