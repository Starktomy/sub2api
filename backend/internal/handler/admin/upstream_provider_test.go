package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGetUpstreamProvidersSafeMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-providers", nil)
	(&AccountHandler{}).GetUpstreamProviders(c)
	require.Equal(t, http.StatusOK, rec.Code)
	providers := gjson.Get(rec.Body.String(), "data.providers").Array()
	require.Len(t, providers, len(service.UpstreamProviderCatalog()))
	for _, p := range providers {
		require.NotEmpty(t, p.Get("id").String())
		require.NotEmpty(t, p.Get("authentication").Array())
		require.NotEmpty(t, p.Get("operations").Array())
		for _, key := range []string{"credentials", "api_key", "access_token", "base_url", "secret_access_key", "private_key"} {
			require.False(t, p.Get(key).Exists(), key)
		}
	}
}
