package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func supplierIdentityTestToken() string {
	payload := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/profile":{"email":"match@example.test","email_verified":true},"https://api.openai.com/auth":{"chatgpt_account_id":"acct","chatgpt_user_id":"user"}}`, time.Now().Add(time.Hour).Unix())
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
}
func TestSupplierIdentityPhoneAndEmail(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"valid", `{"id":"user","email":" MATCH@example.test ","phone_number":"+15550000000"}`, "", 200},
		{"empty phone", `{"id":"user","email":"match@example.test","phone_number":" "}`, "phone_verification_required", 200},
		{"null phone", `{"id":"user","email":"match@example.test","phone_number":null}`, "phone_verification_required", 200},
		{"missing phone", `{"id":"user","email":"match@example.test"}`, "identity_unknown", 200},
		{"bad phone", `{"id":"user","email":"match@example.test","phone_number":true}`, "identity_unknown", 200},
		{"wrong email", `{"id":"user","email":"other@example.test","phone_number":"+15550000000"}`, "identity_email_mismatch", 200},
		{"wrong user", `{"id":"other","email":"match@example.test","phone_number":"+15550000000"}`, "identity_unknown", 200},
		{"missing email", `{"id":"user","phone_number":"+15550000000"}`, "identity_unknown", 200},
		{"malformed", "not-json", "identity_unknown", 200},
		{"oversized", strings.Repeat("x", (64<<10)+1), "identity_unknown", 200},
		{"redirect", "private-canary", "identity_unknown", 302},
		{"unauthorized", "private-canary", "unauthorized", 401},
		{"forbidden", "private-canary", "forbidden", 403},
		{"limited", "private-canary", "rate_limited", 429},
		{"upstream failure", "private-canary", "identity_unknown", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			token := supplierIdentityTestToken()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/backend-api/me", r.URL.Path)
				require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				w.Header().Set("Location", "/never-follow")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			svc := &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) {
				return req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
					return func(r *req.Request) (*req.Response, error) {
						r.URL.Scheme = target.Scheme
						r.URL.Host = target.Host
						return rt.RoundTrip(r)
					}
				}), nil
			}}
			err := svc.VerifySupplierIdentity(context.Background(), token, "acct")
			if tc.code == "" {
				require.NoError(t, err)
			} else {
				var f *SupplierProbeFailure
				require.ErrorAs(t, err, &f)
				require.Equal(t, tc.code, f.Code)
				require.NotContains(t, err.Error(), "private-canary")
				require.NotContains(t, err.Error(), "example.test")
			}
			require.Equal(t, 1, calls)
		})
	}
}
func TestSupplierIdentityInvalidTokenDoesNotCallUpstream(t *testing.T) {
	svc := &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) { t.Fatal("unexpected network setup"); return nil, nil }}
	require.Error(t, svc.VerifySupplierIdentity(context.Background(), "invalid", "acct"))
	require.Error(t, svc.VerifySupplierIdentity(context.Background(), supplierIdentityTestToken(), "wrong-account"))
}
