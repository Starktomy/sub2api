package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceRefreshSingleRequest(t *testing.T) {
	for _, outcome := range []string{"success", "server-error", "rate-limit", "redirect", "redirect-308", "disconnect", "malformed", "canceled", "cancel-after-read"} {
		t.Run(outcome, func(t *testing.T) {
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
					_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-rotated","expires_in":3600}`))
				}
			}))
			defer server.Close()
			legacy := &http.Client{}
			client := &Client{httpClient: legacy, tokenURL: server.URL + "/token"}
			ctx, cancel := context.WithCancel(refreshpolicy.WithSingleAttempt(context.Background()))
			defer cancel()
			cancelRequest = cancel
			if outcome == "canceled" {
				cancel()
			}
			_, err := client.RefreshToken(ctx, "fixture-refresh")
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
			require.Zero(t, targets.Load())
			require.Nil(t, legacy.Transport, "strict transport must not mutate legacy client")
			require.Nil(t, legacy.CheckRedirect)
		})
	}
}
