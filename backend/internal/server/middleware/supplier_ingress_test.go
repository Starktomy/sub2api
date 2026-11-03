package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierIngressHostAndOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SupplierIngress(config.SupplierPortalConfig{Host: "supplier.example.test", Origin: "https://supplier.example.test"}))
	r.GET("/", func(c *gin.Context) { c.Status(204) })
	for _, tc := range []struct {
		host, origin, site string
		status             int
	}{{"supplier.example.test", "", "", 204}, {"supplier.example.test", "https://supplier.example.test", "same-origin", 204}, {"other.example.test", "", "", 421}, {"supplier.example.test", "https://consumer.example.test", "", 403}, {"supplier.example.test", "null", "", 403}, {"supplier.example.test", "", "cross-site", 403}} {
		req := httptest.NewRequest(http.MethodGet, "https://"+tc.host+"/", nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Sec-Fetch-Site", tc.site)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, tc.status, w.Code)
	}
}
