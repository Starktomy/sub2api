package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/refreshpolicy"
	"github.com/stretchr/testify/require"
)

// Even a server offering h2 must see independent HTTP/1 token requests.
func TestMaintenanceRefreshForcesHTTP1OverTLS(t *testing.T) {
	for _, chrome := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "impersonated"}[chrome], func(t *testing.T) {
			var requests, badProtocol atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.ProtoMajor != 1 {
					badProtocol.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			client, err := refreshRequestClient(refreshpolicy.WithSingleAttempt(context.Background()), "", time.Second, chrome, createOpenAIReqClient)
			require.NoError(t, err)
			// Only the disposable loopback fixture uses an untrusted self-signed cert.
			client.EnableInsecureSkipVerify()
			for range 2 {
				response, err := client.R().SetBody("fixture").Post(server.URL)
				require.NoError(t, err)
				require.Equal(t, 200, response.StatusCode)
			}
			require.Equal(t, int32(2), requests.Load())
			require.Zero(t, badProtocol.Load())
		})
	}
}
