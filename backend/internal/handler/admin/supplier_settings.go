package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetSupplierOperatingPolicy(c *gin.Context) {
	p, err := h.settingService.GetSupplierOperatingPolicy(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, p)
}
func (h *SettingHandler) SetSupplierOperatingPolicy(c *gin.Context) {
	// Required pointer fields distinguish deliberate zero fees/disabled admission
	// from accidentally omitted settings.
	var req struct {
		Version       *int64  `json:"version"`
		AutoAdmission *bool   `json:"auto_admission"`
		GroupIDs      []int64 `json:"group_ids"`
		FeeMode       string  `json:"fee_mode"`
		FeeRatioBPS   *int    `json:"fee_ratio_bps"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&req) != nil || d.Decode(&extra) != io.EOF || req.Version == nil || *req.Version < 0 || req.AutoAdmission == nil || req.FeeRatioBPS == nil {
		response.BadRequest(c, "Invalid supplier policy")
		return
	}
	p := service.SupplierOperatingPolicy{Version: *req.Version, AutoAdmission: *req.AutoAdmission, GroupIDs: req.GroupIDs, FeeMode: req.FeeMode, FeeRatioBPS: *req.FeeRatioBPS}
	if err := h.settingService.SetSupplierOperatingPolicy(c.Request.Context(), p); err != nil {
		if errors.Is(err, service.ErrSupplierPolicyVersion) {
			response.ErrorFrom(c, err)
			return
		}
		response.BadRequest(c, "Supplier policy rejected; check groups, fee mode and admission readiness")
		return
	}
	updated, err := h.settingService.GetSupplierOperatingPolicy(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, updated)
}
