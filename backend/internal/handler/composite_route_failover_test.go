package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompositeTargetFailoverEligible(t *testing.T) {
	tests := []struct {
		name string
		err  *service.UpstreamFailoverError
		want bool
	}{
		{name: "rate limit", err: &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, Scope: service.GatewayFailureScopeProvider}, want: true},
		{name: "provider unavailable", err: &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, Scope: service.GatewayFailureScopeProvider}, want: true},
		{name: "account credential", err: &service.UpstreamFailoverError{StatusCode: http.StatusUnauthorized, Stage: service.GatewayFailureStageAccountAuth, Scope: service.GatewayFailureScopeAccount}, want: true},
		{name: "invalid request", err: &service.UpstreamFailoverError{StatusCode: http.StatusBadRequest, Scope: service.GatewayFailureScopeRequest}, want: false},
		{name: "request scoped conflict", err: &service.UpstreamFailoverError{StatusCode: http.StatusConflict, Scope: service.GatewayFailureScopeRequest}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, compositeTargetFailoverEligible(tt.err))
		})
	}
}

func TestPrepareNextOpenAICompositeTargetRewritesModelAndPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	groupID := int64(7)
	apiKey := &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformComposite}}
	decision := service.CompositeRouteDecision{
		Matched:     true,
		Source:      service.CompositeRouteSourceExplicit,
		GroupID:     groupID,
		PublicModel: "deepseek-v4.1-flash",
		Targets: []service.CompositeRouteTarget{
			{TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "deepseek-v4.1-flash", Priority: 10, Enabled: true},
			{TargetPlatform: service.PlatformDeepseek, UpstreamModel: "deepseek-flash", Priority: 20, Enabled: true},
		},
	}
	c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
	h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}

	model, body, platform, _, index, ok := h.prepareNextOpenAICompositeTarget(
		c, apiKey, []byte(`{"model":"deepseek-v4.1-flash","input":"ping"}`), 1,
	)

	require.True(t, ok)
	require.Equal(t, 1, index)
	require.Equal(t, "deepseek-flash", model)
	require.Equal(t, "deepseek-flash", gjson.GetBytes(body, "model").String())
	require.Equal(t, service.PlatformDeepseek, platform)
	resolvedPlatform, resolved := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	require.True(t, resolved)
	require.Equal(t, service.PlatformDeepseek, resolvedPlatform)
}
