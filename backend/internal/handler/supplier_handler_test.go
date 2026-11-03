package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSupplierHandlerRequiresSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SupplierHandler{}
	for _, fn := range []gin.HandlerFunc{h.GetProfile, h.SendVerifyCode, h.Enroll, h.UpdateProfile} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		fn(c)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
}
func TestSupplierHandlerRejectsPrivilegeAndOwnershipFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SupplierHandler{}
	for _, body := range []string{`{"display_name":"name","role":"admin"}`, `{"user_id":99,"display_name":"name"}`, `{"display_name":"name","status":"approved"}`, `{"display_name":"name","earnings":100}`, `{} {}`, strings.Repeat("x", 4097)} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		h.Enroll(c)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}

func TestSupplierRateLimitFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: -1})
	defer rdb.Close()
	h := NewSupplierHandler(nil, rdb)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	h.RateLimit()(c)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
