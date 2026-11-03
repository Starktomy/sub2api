package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type SupplierHandler struct {
	svc     *service.SupplierService
	limiter *ratelimit.RateLimiter
}

func NewSupplierHandler(svc *service.SupplierService, rdb *redis.Client) *SupplierHandler {
	return &SupplierHandler{svc: svc, limiter: ratelimit.NewRateLimiter(rdb)}
}

// Enrollment email controls are mandatory, independent of optional panel limits/admin exemptions.
func (h *SupplierHandler) RateLimit() gin.HandlerFunc {
	ipLimit := h.limiter.LimitWithOptions("supplier-email", 10, time.Minute, ratelimit.RateLimitOptions{FailureMode: ratelimit.RateLimitFailClose, IPv6PrefixBits: 64})
	return func(c *gin.Context) {
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		if !ok {
			response.Unauthorized(c, "User not authenticated")
			c.Abort()
			return
		}
		result, err := h.limiter.Allow(c.Request.Context(), "supplier-email:user:"+strconv.FormatInt(subject.UserID, 10), 5, time.Minute)
		if err != nil {
			response.Error(c, http.StatusServiceUnavailable, "Rate limit unavailable")
			c.Abort()
			return
		}
		if !result.Allowed {
			response.Error(c, http.StatusTooManyRequests, "Too many requests")
			c.Abort()
			return
		}
		ipLimit(c)
	}
}
func supplierSubject(c *gin.Context) (int64, bool) {
	s, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || s.UserID < 1 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return s.UserID, true
}

// Strict decoding prevents callers from setting role, ownership, status or earnings.
func supplierJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		response.BadRequest(c, "Invalid supplier request")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		response.BadRequest(c, "Invalid supplier request")
		return false
	}
	return true
}
func (h *SupplierHandler) GetProfile(c *gin.Context) {
	id, ok := supplierSubject(c)
	if !ok {
		return
	}
	p, err := h.svc.GetProfile(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, p)
}
func (h *SupplierHandler) SendVerifyCode(c *gin.Context) {
	id, ok := supplierSubject(c)
	if !ok {
		return
	}
	if response.ErrorFrom(c, h.svc.SendVerifyCode(c.Request.Context(), id)) {
		return
	}
	response.Success(c, gin.H{"message": "Verification code sent to your account email"})
}
func (h *SupplierHandler) Enroll(c *gin.Context) {
	id, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		VerifyCode  string `json:"verify_code"`
	}
	if !supplierJSON(c, &req) {
		return
	}
	p, err := h.svc.Enroll(c.Request.Context(), id, req.DisplayName, req.VerifyCode)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, p)
}
func (h *SupplierHandler) UpdateProfile(c *gin.Context) {
	id, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Version     int64  `json:"version"`
	}
	if !supplierJSON(c, &req) {
		return
	}
	p, err := h.svc.UpdateProfile(c.Request.Context(), id, req.DisplayName, req.Version)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, p)
}
