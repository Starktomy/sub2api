package handler

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// SupplierRegistrationRequest rejects consumer incentive and ownership fields,
// while retaining shared captcha verification and shared user/password storage.
func (h *AuthHandler) SupplierRegistrationRequest(c *gin.Context) {
	var req struct {
		Email                 string `json:"email"`
		Password              string `json:"password"`
		VerifyCode            string `json:"verify_code"`
		TurnstileToken        string `json:"turnstile_token"`
		TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
		TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
	}
	if !supplierJSON(c, &req) {
		c.Abort()
		return
	}
	if len(req.Email) > 320 || len(req.Password) > 128 || len(req.VerifyCode) != 6 {
		response.BadRequest(c, "Invalid supplier registration")
		c.Abort()
		return
	}
	raw, _ := json.Marshal(req)
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	c.Next()
}
func (h *AuthHandler) SupplierLogout(c *gin.Context) {
	var req LogoutRequest
	if !supplierJSON(c, &req) {
		return
	}
	if req.RefreshToken != "" {
		if response.ErrorFrom(c, h.authService.RevokeRefreshToken(c.Request.Context(), req.RefreshToken)) {
			return
		}
	}
	response.Success(c, LogoutResponse{Message: "Logged out successfully"})
}

// Supplier auth has no agreement acceptance flow yet. Do not silently bypass a
// configured mandatory agreement via direct API calls while the UI fails closed.
func (h *AuthHandler) SupplierAgreementGuard(c *gin.Context) {
	settings, err := h.settingSvc.GetPublicSettings(c.Request.Context())
	if response.ErrorFrom(c, err) {
		c.Abort()
		return
	}
	if settings.LoginAgreementEnabled {
		response.Error(c, 503, "Supplier agreement acceptance is not supported yet")
		c.Abort()
		return
	}
	c.Next()
}
