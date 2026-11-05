package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestSupplierQuotaSingleGETNoResetRefreshOrPrivateMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/backend-api/wham/usage", r.URL.Path)
		require.Equal(t, "Bearer synthetic", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"email":"must-not-expose@example.test","account_id":"hidden","plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_at":9999999999}},"credits":{"balance":"999"}}`))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	s := &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) {
		return req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
			return func(r *req.Request) (*req.Response, error) {
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				return rt.RoundTrip(r)
			}
		}), nil
	}}
	u, e := s.QuerySupplierUsage(context.Background(), "synthetic", "account")
	require.NoError(t, e)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "plus", u.PlanType)
	require.Equal(t, float64(10), u.RateLimit.PrimaryWindow.UsedPercent)
}

func TestSupplierQuotaDoesNotFollowRedirectOrEchoFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/private-reset")
		w.WriteHeader(302)
		w.Write([]byte("token-secret-canary"))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	s := &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) {
		return req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
			return func(r *req.Request) (*req.Response, error) {
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				return rt.RoundTrip(r)
			}
		}), nil
	}}
	_, err := s.QuerySupplierUsage(context.Background(), "synthetic", "account")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "token-secret-canary")
	require.Equal(t, int32(1), calls.Load())
}

func TestSupplierQuotaClassifiesRetryAfterAndUnauthorized(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(status)
		}))
		target, _ := url.Parse(server.URL)
		s := &OpenAIQuotaService{privacyClientFactory: func(string) (*req.Client, error) {
			return req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
				return func(r *req.Request) (*req.Response, error) {
					r.URL.Scheme = target.Scheme
					r.URL.Host = target.Host
					return rt.RoundTrip(r)
				}
			}), nil
		}}
		_, e := s.QuerySupplierUsage(context.Background(), "synthetic", "account")
		var failure *SupplierProbeFailure
		require.ErrorAs(t, e, &failure)
		if status == 429 {
			require.Equal(t, "rate_limited", failure.Code)
			require.Equal(t, 2*time.Minute, failure.RetryAfter)
		}
		server.Close()
	}
}
