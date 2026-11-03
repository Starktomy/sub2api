package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceConcreteOAuthClientsSingleRequest(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "gemini", "grok"} {
		for _, outcome := range []string{"success", "server-error", "rate-limit", "redirect", "redirect-308", "disconnect", "malformed", "canceled", "cancel-after-read"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				var calls, targets atomic.Int32
				var cancelRequest context.CancelFunc
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/token" {
						targets.Add(1)
						w.WriteHeader(500)
						return
					}
					calls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					switch outcome {
					case "cancel-after-read":
						cancelRequest()
						<-r.Context().Done()
						return
					case "server-error":
						w.WriteHeader(503)
					case "rate-limit":
						w.WriteHeader(429)
					case "redirect-308":
						w.Header().Set("Location", "/redirect-target")
						w.WriteHeader(308)
					case "redirect":
						w.Header().Set("Location", "/redirect-target")
						w.WriteHeader(307)
					case "disconnect":
						hijacker, ok := w.(http.Hijacker)
						if !ok {
							panic("loopback server does not support hijacking")
						}
						conn, _, err := hijacker.Hijack()
						if err == nil {
							_ = conn.Close()
						}
					case "malformed":
						_, _ = w.Write([]byte(`{"access_token":`))
					default:
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-rotated","expires_in":3600,"token_type":"Bearer"}`))
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(refreshpolicy.WithSingleAttempt(context.Background()))
				defer cancel()
				cancelRequest = cancel
				if outcome == "canceled" {
					cancel()
				}
				var err error
				switch provider {
				case "anthropic":
					_, err = (&claudeOAuthService{tokenURL: server.URL + "/token", clientFactory: createReqClient}).RefreshToken(ctx, "fixture-refresh", "")
				case "openai":
					_, err = (&openaiOAuthService{tokenURL: server.URL + "/token"}).RefreshTokenWithClientID(ctx, "fixture-refresh", "", "fixture-client")
				case "gemini":
					_, err = (&geminiOAuthClient{tokenURL: server.URL + "/token", cfg: &config.Config{}}).RefreshToken(ctx, "code_assist", "fixture-refresh", "")
				case "grok":
					_, err = (&grokOAuthClient{tokenURL: server.URL + "/token"}).RefreshToken(ctx, "fixture-refresh", "", "fixture-client")
				}
				if outcome == "success" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				want := int32(1)
				if outcome == "canceled" {
					want = 0
				}
				require.Equal(t, want, calls.Load())
				require.Zero(t, targets.Load(), "maintenance must never follow a token redirect")
			})
		}
	}
}

func TestMaintenanceRefreshClientDoesNotMutateLegacy(t *testing.T) {
	legacy := req.C()
	factoryCalls := 0
	factory := func(string) (*req.Client, error) { factoryCalls++; return legacy, nil }
	got, err := refreshRequestClient(context.Background(), "", time.Second, false, factory)
	require.NoError(t, err)
	require.Same(t, legacy, got)
	strict, err := refreshRequestClient(refreshpolicy.WithSingleAttempt(context.Background()), "", time.Second, false, factory)
	require.NoError(t, err)
	require.NotSame(t, legacy, strict)
	require.Equal(t, 1, factoryCalls)
	require.False(t, legacy.GetTransport().DisableKeepAlives)
	require.True(t, strict.GetTransport().DisableKeepAlives)
}
