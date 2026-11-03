package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type SupplierResourceHandler struct {
	svc    *service.SupplierResourceService
	probes service.SupplierProbeRepository
}

func NewSupplierResourceHandler(svc *service.SupplierResourceService, probes service.SupplierProbeRepository) *SupplierResourceHandler {
	return &SupplierResourceHandler{svc: svc, probes: probes}
}
func (h *SupplierResourceHandler) List(c *gin.Context) {
	id, ok := supplierSubject(c)
	if !ok {
		return
	}
	items, e := h.svc.List(c.Request.Context(), id)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, gin.H{"items": items, "vault_configured": h.svc.Configured()})
}
func resourceID(c *gin.Context) (string, bool) {
	id, e := uuid.Parse(c.Param("id"))
	if e != nil {
		response.ErrorFrom(c, service.ErrSupplierResource)
		return "", false
	}
	return id.String(), true
}
func (h *SupplierResourceHandler) Get(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := resourceID(c)
	if !ok {
		return
	}
	r, e := h.svc.Get(c.Request.Context(), u, id)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, r)
}
func (h *SupplierResourceHandler) Import(c *gin.Context) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Content     string `json:"content"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, (1<<20)+4096)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil {
		response.BadRequest(c, "Invalid session import")
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		response.BadRequest(c, "Invalid session import")
		return
	}
	r, e := h.svc.Import(c.Request.Context(), u, req.DisplayName, req.Content)
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, r)
}
func (h *SupplierResourceHandler) mutation(c *gin.Context, action string) {
	u, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := resourceID(c)
	if !ok {
		return
	}
	var req struct {
		Version int64 `json:"version"`
	}
	if !supplierJSON(c, &req) {
		return
	}
	if req.Version < 1 {
		response.ErrorFrom(c, service.ErrSupplierResourceVersion)
		return
	}
	var r *service.SupplierResource
	var e error
	if action == "resume-sync" {
		current, err := h.svc.Get(c.Request.Context(), u, id)
		if response.ErrorFrom(c, err) {
			return
		}
		if !time.Now().Before(current.TokenExpiresAt) || current.ProbeState == "reauthorization_required" {
			response.ErrorFrom(c, service.ErrSupplierReimport)
			return
		}
		r, e = h.probes.Resume(c.Request.Context(), current.SupplierID, id, req.Version)
		if e == nil {
			r, e = h.svc.Get(c.Request.Context(), u, id)
		}
	} else if action == "usage" {
		r, e = h.svc.Usage(c.Request.Context(), u, id, req.Version)
	} else {
		r, e = h.svc.Transition(c.Request.Context(), u, id, req.Version, action == "disconnect")
	}
	if response.ErrorFrom(c, e) {
		return
	}
	response.Success(c, r)
}
func (h *SupplierResourceHandler) Usage(c *gin.Context)      { h.mutation(c, "usage") }
func (h *SupplierResourceHandler) Pause(c *gin.Context)      { h.mutation(c, "pause") }
func (h *SupplierResourceHandler) Disconnect(c *gin.Context) { h.mutation(c, "disconnect") }

func (h *SupplierResourceHandler) Resume(c *gin.Context) { h.mutation(c, "resume-sync") }
