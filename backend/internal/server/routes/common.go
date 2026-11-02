package routes

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// RegisterCommonRoutes registers role-specific common endpoints. Omitting the
// role preserves the legacy all-in-one surface for existing callers.
func RegisterCommonRoutes(r *gin.Engine, roles ...string) {
	role := config.RuntimeRoleAll
	if len(roles) > 0 {
		role = strings.ToLower(strings.TrimSpace(roles[0]))
		if role == "" {
			role = config.RuntimeRoleAll
		}
	}
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	if role == config.RuntimeRoleAll || role == config.RuntimeRoleGateway {
		// Client telemetry is ignored, preserving the gateway protocol contract.
		r.POST("/api/event_logging/batch", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
	}

	if role == config.RuntimeRoleAll || role == config.RuntimeRoleControl {
		// Setup status endpoint (always returns needs_setup: false in normal mode).
		// The frontend uses this after the service has restarted following setup.
		r.GET("/setup/status", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"data": gin.H{
					"needs_setup": false,
					"step":        "completed",
				},
			})
		})
	}
}
