package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ImportKiro accepts an explicitly submitted single credential object only.
// No error incorporates decoder/input text and no secret-bearing DTO is echoed.
func (h *AccountHandler) ImportKiro(c *gin.Context) {
	if h.kiroNative == nil {
		response.Error(c, http.StatusServiceUnavailable, "Kiro encrypted vault unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 72<<10)
	defer c.Request.Body.Close()
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	var in service.KiroNativeImport
	if err := d.Decode(&in); err != nil {
		response.BadRequest(c, "Invalid Kiro import")
		return
	}
	defer clear(in.Credential)
	if err := d.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "Invalid Kiro import")
		return
	}
	result, err := h.kiroNative.Import(c.Request.Context(), in)
	if err != nil {
		if errors.Is(err, service.ErrKiroUnavailable) {
			response.Error(c, http.StatusServiceUnavailable, "Kiro encrypted vault unavailable")
			return
		}
		response.BadRequest(c, "Kiro import rejected")
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) kiroRuntime(c *gin.Context, operation string) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid Kiro account")
		return
	}
	if h.kiroNative == nil {
		response.Error(c, 503, "Kiro runtime unavailable")
		return
	}
	var result *service.KiroRuntimeStatus
	switch operation {
	case "get":
		result, err = h.kiroNative.Runtime(c.Request.Context(), id)
	case "patch":
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 72<<10)
		defer c.Request.Body.Close()
		d := json.NewDecoder(c.Request.Body)
		d.DisallowUnknownFields()
		var in service.KiroRuntimePatch
		if d.Decode(&in) != nil {
			response.BadRequest(c, "Invalid Kiro operation")
			return
		}
		defer clear(in.Credential)
		if d.Decode(new(any)) != io.EOF {
			response.BadRequest(c, "Invalid Kiro operation")
			return
		}
		result, err = h.kiroNative.PatchRuntime(c.Request.Context(), id, in)
	default:
		result, err = h.kiroNative.Observe(c.Request.Context(), id, operation)
	}
	if err != nil {
		if errors.Is(err, service.ErrKiroRefreshBarrier) {
			response.Error(c, 409, "Kiro credential version or refresh barrier conflict")
		} else if errors.Is(err, service.ErrKiroUnavailable) {
			response.Error(c, 503, "Kiro runtime unavailable")
		} else {
			response.BadRequest(c, "Kiro operation rejected")
		}
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) GetKiroRuntime(c *gin.Context)   { h.kiroRuntime(c, "get") }
func (h *AccountHandler) PatchKiroRuntime(c *gin.Context) { h.kiroRuntime(c, "patch") }
func (h *AccountHandler) KiroModels(c *gin.Context)       { h.kiroRuntime(c, "models") }
func (h *AccountHandler) KiroQuota(c *gin.Context)        { h.kiroRuntime(c, "quota") }
