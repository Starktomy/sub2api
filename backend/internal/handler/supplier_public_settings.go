package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// SupplierPublicSettings intentionally excludes consumer purchase links, custom
// endpoints, wallet settings, OAuth clients and administrator configuration.
func (h *SettingHandler) SupplierPublicSettings(cfg config.SupplierPortalConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		s, err := h.settingService.GetPublicSettings(c.Request.Context())
		if response.ErrorFrom(c, err) {
			return
		}
		response.Success(c, gin.H{
			"email_delivery_configured": h.supplierEmail.EmailDeliveryConfigured(c.Request.Context()),
			"supplier_portal":           true, "registration_enabled": cfg.RegistrationEnabled, "email_verify_enabled": true,
			"site_name": s.SiteName, "site_logo": s.SiteLogo, "totp_enabled": s.TotpEnabled,
			"turnstile_enabled": s.TurnstileEnabled, "turnstile_site_key": s.TurnstileSiteKey,
			"tencent_captcha_enabled": s.TencentCaptchaEnabled, "tencent_captcha_app_id": s.TencentCaptchaAppID, "tencent_captcha_region": s.TencentCaptchaRegion,
			"aliyun_captcha_enabled": s.AliyunCaptchaEnabled, "aliyun_captcha_scene_id": s.AliyunCaptchaSceneID, "aliyun_captcha_prefix": s.AliyunCaptchaPrefix, "aliyun_captcha_region": s.AliyunCaptchaRegion,
			"login_agreement_enabled": s.LoginAgreementEnabled, "login_agreement_mode": s.LoginAgreementMode, "login_agreement_revision": s.LoginAgreementRevision, "login_agreement_documents": publicLoginAgreementDocumentsToDTO(s.LoginAgreementDocuments),
		})
	}
}
