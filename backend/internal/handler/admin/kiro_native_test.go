package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroImportRejectsInjectionAndNeverEchoesSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, payload := range []string{
		`{"name":"test","credential":{"refreshToken":"secret-canary"},"region":"us-east-1","auth_mode":"idc","endpoint":"https://attacker.test/secret-canary"}`,
		`{"name":"test","credential":{"refreshToken":"secret-canary","proxyUrl":"https://attacker.test"},"region":"us-east-1","auth_mode":"idc"}`,
		`{"name":"secret-canary","credential":null} {"second":"secret-canary"}`,
		`{"credential":"secret-canary"}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/kiro/import", strings.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		h := &AccountHandler{kiroNative: service.NewKiroNativeService(nil, nil, nil, nil)}
		h.ImportKiro(c)
		require.True(t, w.Code == 400 || w.Code == 503)
		require.NotContains(t, w.Body.String(), "secret-canary")
		require.NotContains(t, w.Body.String(), "attacker.test")
	}
}

func TestKiroRuntimePatchRejectsUnknownAndOmitsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"operation":"reimport","credential":{"refreshToken":"canary-secret"},"endpoint":"https://evil.test"}`, `{"operation":"configure","region":"us-west-2"}`, `{"operation":"pause"} {"secret":"canary-secret"}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "7"}}
		c.Request = httptest.NewRequest("PATCH", "/admin/accounts/7/kiro/runtime", strings.NewReader(body))
		h := &AccountHandler{kiroNative: service.NewKiroNativeService(nil, nil, nil, nil)}
		h.PatchKiroRuntime(c)
		require.Equal(t, 400, w.Code)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.NotContains(t, w.Body.String(), "canary-secret")
	}
}
