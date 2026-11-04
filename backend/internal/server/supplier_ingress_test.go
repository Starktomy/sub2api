package server

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierRouterNeverServesConsumerSPAOrAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleControl}, SupplierPortal: config.SupplierPortalConfig{Enabled: true, Host: "supplier.example.test", Origin: "https://supplier.example.test"}}
	h := &handler.Handlers{Auth: &handler.AuthHandler{}, Setting: &handler.SettingHandler{}, Supplier: handler.NewSupplierHandler(nil, nil), SupplierResource: handler.NewSupplierResourceHandler(nil, nil)}
	r := SetupRouter(gin.New(), h, middleware.JWTAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(401) }), nil, nil, nil, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, nil, nil, nil, nil, nil, cfg, nil)
	// Assert the full method/path allowlist, including the new upstream admin
	// blacklist endpoints remaining absent from the supplier ingress.
	var registered []string
	for _, route := range r.Routes() {
		registered = append(registered, route.Method+" "+route.Path)
	}
	require.ElementsMatch(t, []string{
		"POST /api/v1/auth/register",
		"POST /api/v1/auth/send-verify-code",
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/login/2fa",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/logout",
		"GET /api/v1/auth/me",
		"GET /api/v1/settings/public",
		"GET /api/v1/supplier/profile",
		"PUT /api/v1/supplier/profile",
		"POST /api/v1/supplier/send-verify-code",
		"POST /api/v1/supplier/enroll",
		"GET /api/v1/supplier/resources",
		"GET /api/v1/supplier/resources/:id",
		"POST /api/v1/supplier/resources/import",
		"POST /api/v1/supplier/resources/:id/usage",
		"POST /api/v1/supplier/resources/:id/resume-sync",
		"POST /api/v1/supplier/resources/:id/pause",
		"POST /api/v1/supplier/resources/:id/disconnect",
	}, registered)
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/user/profile", "/api/v1/missing", "/api/v1/auth/validate-promo-code", "/v1/messages", "/dashboard", "/supplier.html", "/"} {
		req := httptest.NewRequest("GET", "https://supplier.example.test"+path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 404, w.Code, path)
		require.Contains(t, w.Header().Get("Content-Type"), "application/json")
		require.NotContains(t, w.Body.String(), "<html")
	}
	reqUnknown := httptest.NewRequest("OPTIONS", "https://supplier.example.test/api/v1/missing", nil)
	reqUnknown.Header.Set("Origin", "https://supplier.example.test")
	out := httptest.NewRecorder()
	r.ServeHTTP(out, reqUnknown)
	require.Equal(t, 404, out.Code)
	require.Contains(t, out.Header().Get("Content-Type"), "application/json")

	req := httptest.NewRequest("OPTIONS", "https://supplier.example.test/api/v1/auth/login", nil)
	req.Header.Set("Origin", "https://consumer.example.test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 403, w.Code)
}
