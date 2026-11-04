package handler

import (
	"net/http"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Rerank reserves the standard compatible route but deliberately performs no
// upstream request. Existing billing cannot durably represent successful rerank
// consumption with missing/non-token usage, so returning unbilled success or
// blindly replaying the request is forbidden.
func (h *OpenAIGatewayHandler) Rerank(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxRerankBodyBytes)
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Rerank body exceeds 1 MiB or is unreadable")
		return
	}
	if err := service.ValidateRerankRequest(body); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	h.errorResponse(c, http.StatusNotImplemented, "unsupported_operation", "Rerank is unavailable until durable unknown-usage accounting is supported; no upstream request was sent")
}
