package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierRoutesRequireSharedJWT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{Supplier: handler.NewSupplierHandler(nil, nil)}
	RegisterUserRoutes(r.Group("/api/v1"), h, middleware.JWTAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }), middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, nil)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/supplier/profile"}, {"PUT", "/api/v1/supplier/profile"}, {"POST", "/api/v1/supplier/enroll"}, {"POST", "/api/v1/supplier/send-verify-code"}} {
		for _, key := range []string{"", "adm_sc_restricted", "full-admin-key"} {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			req.Header.Set("x-api-key", key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code)
		}
	}
}
