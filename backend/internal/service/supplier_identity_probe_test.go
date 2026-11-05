package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestSupplierProbeRequiresIdentityBeforeQuota(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "phone absent blocks quota", true: "verified identity then quota"}[verified], func(t *testing.T) {
			vault, r := supplierExecutionFixture(t)
			token := supplierIdentityTestToken()
			r.AccountHint = "acct"
			supplierExecutionEncrypt(t, vault, r, map[string]any{"access_token": token, "account_id": "acct"})
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				paths = append(paths, request.URL.Path)
				switch request.URL.Path {
				case "/backend-api/me":
					phone := ""
					if verified {
						phone = "synthetic-phone"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "user", "email": "match@example.test", "phone_number": phone})
				case "/backend-api/wham/usage":
					_ = json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{"allowed": true, "limit_reached": false, "primary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 18000, "reset_at": time.Now().Add(time.Hour).Unix()}, "secondary_window": nil}})
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			vault.quota = &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) {
				return req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
					return func(r *req.Request) (*req.Response, error) {
						r.URL.Scheme = target.Scheme
						r.URL.Host = target.Host
						return rt.RoundTrip(r)
					}
				}), nil
			}}
			usage, err := vault.queryResource(context.Background(), r)
			if verified {
				require.NoError(t, err)
				require.True(t, usage.PhoneVerified)
				require.True(t, usage.EmailMatched)
				require.Equal(t, []string{"/backend-api/me", "/backend-api/wham/usage"}, paths)
				raw, err := json.Marshal(usage)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "synthetic-phone")
				require.NotContains(t, string(raw), "match@example.test")
				// Simulate the successful probe's durable version stamp. Later
				// quota probes reuse identity proof without another /me request.
				r.CredentialVersion = 1
				version := int64(1)
				r.VerifiedCredentialVersion = &version
				r.Usage = usage
				paths = nil
				refreshed, err := vault.queryResource(context.Background(), r)
				require.NoError(t, err)
				require.True(t, refreshed.PhoneVerified)
				require.True(t, refreshed.EmailMatched)
				require.Equal(t, []string{"/backend-api/wham/usage"}, paths)
				// Even if old flags survive in memory, rotated credentials cannot
				// inherit the old version's identity proof.
				r.CredentialVersion = 2
				paths = nil
				_, err = vault.queryResource(context.Background(), r)
				require.NoError(t, err)
				require.Equal(t, []string{"/backend-api/me", "/backend-api/wham/usage"}, paths)
			} else {
				var failure *SupplierProbeFailure
				require.ErrorAs(t, err, &failure)
				require.Equal(t, "phone_verification_required", failure.Code)
				require.Nil(t, usage)
				require.Equal(t, []string{"/backend-api/me"}, paths)
			}
		})
	}
}
