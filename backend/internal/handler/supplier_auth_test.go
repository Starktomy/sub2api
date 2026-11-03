package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierRegistrationRejectsConsumerFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{}
	for _, field := range []string{"promo_code", "invitation_code", "aff_code", "role", "user_id", "balance"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"email":"u@example.com","password":"password","verify_code":"123456","`+field+`":"x"}`))
		h.SupplierRegistrationRequest(c)
		require.Equal(t, 400, w.Code)
		require.True(t, c.IsAborted())
	}
}
