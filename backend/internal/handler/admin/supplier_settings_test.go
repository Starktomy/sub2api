package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierPolicyStrictInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{}`, `{"version":0,"auto_admission":false,"fee_mode":"standard_cost"}`,
		`{"version":0,"fee_ratio_bps":0,"fee_mode":"standard_cost"}`,
		`{"version":-1,"auto_admission":false,"fee_ratio_bps":0}`,
		`{"version":0,"auto_admission":false,"fee_ratio_bps":0,"secret":"forbidden"}`,
		`{"version":0,"auto_admission":false,"fee_ratio_bps":0} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/supplier/settings", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			(&SettingHandler{}).SetSupplierOperatingPolicy(c)
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}
