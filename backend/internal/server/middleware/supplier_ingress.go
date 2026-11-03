package middleware

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// SupplierIngress rejects alternate Host/Origin values before CORS or routing.
// Forwarded Host is never an authority. Missing Origin is allowed for native
// bearer clients; this listener neither issues nor authenticates session cookies.
func SupplierIngress(cfg config.SupplierPortalConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Host != cfg.Host {
			c.AbortWithStatus(http.StatusMisdirectedRequest)
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" && origin != cfg.Origin {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if site := c.GetHeader("Sec-Fetch-Site"); site == "cross-site" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		// The dedicated frontend is same-origin. Do not install consumer CORS/MCP
		// exceptions or globally acknowledge OPTIONS on unknown API paths.
		if c.GetHeader("Origin") == cfg.Origin {
			c.Header("Access-Control-Allow-Origin", cfg.Origin)
			c.Header("Vary", "Origin")
		}
		c.Next()
	}
}
