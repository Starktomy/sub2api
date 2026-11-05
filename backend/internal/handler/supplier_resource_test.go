package handler

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierResourceDTOHasNoCredentialsOrUpstreamIdentity(t *testing.T) {
	r := service.SupplierResource{ID: "uuid", Envelope: []byte("secret-canary"), KeyID: "key-canary", AccountHint: "upstream-id-canary", TokenFingerprint: "fingerprint-canary", IdentityFingerprint: "identity-canary", MaskedEmail: "u***@example.test"}
	b, e := json.Marshal(r)
	require.NoError(t, e)
	for _, canary := range []string{"secret-canary", "key-canary", "upstream-id-canary", "fingerprint-canary", "identity-canary", "access_token", "refresh_token"} {
		require.NotContains(t, string(b), canary)
	}
}

func TestSupplierAdminSupplyControlOrdinaryOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SupplierResourceHandler{}
	for _, full := range []bool{false, true} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"version":1,"disabled":true}`)).WithContext(adminaccess.WithPrincipal(context.Background(), &adminaccess.Principal{Policy: adminaccess.Policy{FullAccess: full}}))
		h.AdminSupplyControl(c)
		require.Equal(t, 403, w.Code)
	}
}
func TestSupplierSupplyControlValidatesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SupplierResourceHandler{}
	for _, body := range []string{`{"version":1}`, `{"version":0,"disabled":true}`, `{"version":1,"disabled":true,"supply_enabled":true}`, `{"version":1,"disabled":true} {}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "00000000-0000-4000-8000-000000000001"}}
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(body))
		h.AdminSupplyControl(c)
		require.GreaterOrEqual(t, w.Code, 400)
	}
	for _, body := range []string{`{"version":0}`, `{"version":1,"admin_disabled":false}`, `{"version":1} {}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "00000000-0000-4000-8000-000000000001"}}
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		h.Start(c)
		require.GreaterOrEqual(t, w.Code, 400)
	}
}
