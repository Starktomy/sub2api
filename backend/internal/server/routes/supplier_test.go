package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierRouteAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{SupplierResource: &handler.SupplierResourceHandler{}, Auth: &handler.AuthHandler{}, Setting: &handler.SettingHandler{}, Supplier: handler.NewSupplierHandler(nil, nil)}
	RegisterSupplierRoutes(r, h, middleware.JWTAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(401) }), middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), &config.Config{}, nil)
	expected := map[string]bool{"POST /api/v1/supplier/resources/:id/start": true, "GET /api/v1/supplier/resources/:id/stats": true, "POST /api/v1/supplier/resources/:id/resume-sync": true, "GET /api/v1/supplier/resources": true, "GET /api/v1/supplier/resources/:id": true, "POST /api/v1/supplier/resources/import": true, "POST /api/v1/supplier/resources/:id/usage": true, "POST /api/v1/supplier/resources/:id/pause": true, "POST /api/v1/supplier/resources/:id/disconnect": true, "POST /api/v1/auth/register": true, "POST /api/v1/auth/send-verify-code": true, "POST /api/v1/auth/login": true, "POST /api/v1/auth/login/2fa": true, "POST /api/v1/auth/refresh": true, "POST /api/v1/auth/logout": true, "GET /api/v1/auth/me": true, "GET /api/v1/settings/public": true, "GET /api/v1/supplier/profile": true, "PUT /api/v1/supplier/profile": true, "POST /api/v1/supplier/send-verify-code": true, "POST /api/v1/supplier/enroll": true}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		require.True(t, expected[key], key)
		delete(expected, key)
	}
	require.Empty(t, expected)
	for _, path := range []string{"/api/v1/user/profile", "/api/v1/admin/users", "/api/v1/auth/oauth/google/start", "/api/v1/auth/validate-promo-code", "/api/v1/payment/orders", "/v1/messages", "/api/v1/mcp"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 404, w.Code)
	}
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/supplier/profile"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("x-api-key", "adm_sc_restricted")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 401, w.Code)
	}
}
func TestSupplierRoutesAbsentOnConsumer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterUserRoutes(r.Group("/api/v1"), &handler.Handlers{}, middleware.JWTAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(401) }), middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/supplier/profile", nil))
	require.Equal(t, 404, w.Code)
}
