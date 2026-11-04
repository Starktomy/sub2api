package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRerankFailsClosedWithoutUpstreamOrBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"model":"rerank","query":"q","documents":["d"]}`, http.StatusNotImplemented},
		{`{"model":"rerank","query":"q","documents":[]}`, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(tc.body))
		(&OpenAIGatewayHandler{}).Rerank(c) // nil dependencies prove no execution or charge
		require.Equal(t, tc.status, rec.Code)
		if tc.status == http.StatusNotImplemented {
			require.Contains(t, rec.Body.String(), "no upstream request was sent")
		}
	}
}
