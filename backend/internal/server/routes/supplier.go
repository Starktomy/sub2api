package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RegisterSupplierRoutes is an explicit allowlist, not the ordinary Control API.
func RegisterSupplierRoutes(r *gin.Engine, h *handler.Handlers, jwt middleware.JWTAuthMiddleware, audit middleware.AuditLogMiddleware, cfg *config.Config, rdb *redis.Client) {
	limiter := ratelimit.NewRateLimiter(rdb)
	v1 := r.Group("/api/v1")
	v1.Use(gin.HandlerFunc(audit))
	limited := func(key string, n int) gin.HandlerFunc {
		return limiter.LimitWithOptions("supplier-auth:"+key, n, time.Minute, ratelimit.RateLimitOptions{FailureMode: ratelimit.RateLimitFailClose, IPv6PrefixBits: 64})
	}
	auth := v1.Group("/auth")
	auth.POST("/register", limited("register", 5), h.Auth.SupplierAgreementGuard, h.Auth.SupplierRegistrationRequest, h.Auth.Register)
	auth.POST("/send-verify-code", limited("email", 5), h.Auth.SendVerifyCode)
	auth.POST("/login", limited("login", 20), h.Auth.SupplierAgreementGuard, h.Auth.Login)
	auth.POST("/login/2fa", limited("2fa", 20), h.Auth.SupplierAgreementGuard, h.Auth.Login2FA)
	auth.POST("/refresh", limited("refresh", 30), h.Auth.RefreshToken)
	auth.POST("/logout", limited("logout", 30), h.Auth.SupplierLogout)
	auth.GET("/me", gin.HandlerFunc(jwt), h.Auth.GetCurrentUser)
	v1.GET("/settings/public", h.Setting.SupplierPublicSettings(cfg.SupplierPortal))
	supplier := v1.Group("/supplier", gin.HandlerFunc(jwt))
	if h.SupplierResource != nil {
		supplier.GET("/resources", h.SupplierResource.List)
		supplier.GET("/resources/:id", h.SupplierResource.Get)
		supplier.POST("/resources/import", h.Supplier.RateLimit(), h.SupplierResource.Import)
		supplier.POST("/resources/:id/usage", h.Supplier.RateLimit(), h.SupplierResource.Usage)
		supplier.POST("/resources/:id/resume-sync", h.SupplierResource.Resume)
		supplier.POST("/resources/:id/pause", h.SupplierResource.Pause)
		supplier.POST("/resources/:id/disconnect", h.SupplierResource.Disconnect)
	}
	supplier.GET("/profile", h.Supplier.GetProfile)
	supplier.PUT("/profile", h.Supplier.UpdateProfile)
	supplier.POST("/send-verify-code", h.Supplier.RateLimit(), h.Supplier.SendVerifyCode)
	supplier.POST("/enroll", h.Supplier.RateLimit(), h.Supplier.Enroll)
}
