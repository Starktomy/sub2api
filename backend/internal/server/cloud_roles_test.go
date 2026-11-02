package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRuntimeHTTPRole(t *testing.T) {
	require.Equal(t, config.RuntimeRoleAll, runtimeHTTPRole(nil))
	require.True(t, servesGatewayRoutes(nil))
	require.True(t, servesControlRoutes(nil))

	for _, tc := range []struct {
		role    string
		gateway bool
		control bool
	}{
		{"", true, true},
		{" ALL ", true, true},
		{config.RuntimeRoleGateway, true, false},
		{" CONTROL ", false, true},
		{config.RuntimeRoleWorker, false, false},
		{"unknown", false, false},
		{"all,gateway", false, false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := &config.Config{Runtime: config.RuntimeConfig{Role: tc.role}}
			require.Equal(t, tc.gateway, servesGatewayRoutes(cfg))
			require.Equal(t, tc.control, servesControlRoutes(cfg))
		})
	}
}

func TestCloudRoleRouteRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registered := make(map[string]map[string]string)
	for _, tc := range []struct {
		role    string
		gateway bool
		control bool
	}{
		{config.RuntimeRoleAll, true, true},
		{"", true, true},
		{config.RuntimeRoleGateway, true, false},
		{config.RuntimeRoleControl, false, true},
		{config.RuntimeRoleWorker, false, false},
		{"unknown", false, false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := cloudRoleConfig(t, tc.role)
			repo := &cloudRoleSettings{
				values: map[string]string{service.SettingKeyHomeContent: "https://frames.example.test/home"},
			}
			settings := service.NewSettingService(repo, cfg)
			r := cloudRoleRouter(gin.New(), cloudRoleHandlers(cfg), cfg, settings)
			got := cloudRoleRouteTable(r)
			registered[tc.role] = got

			require.Contains(t, got, "GET /health")
			for _, path := range []string{
				"POST /v1/messages", "POST /v1/messages/count_tokens", "POST /v1/systemone",
				"GET /v1/models", "GET /v1/models/:model", "GET /v1/usage", "GET /v1/sub2api/billing",
				"POST /v1/responses", "POST /v1/responses/*subpath", "GET /v1/responses",
				"POST /responses", "GET /responses", "GET /models", "POST /messages/count_tokens",
				"POST /v1/chat/completions", "POST /chat/completions", "POST /v1/embeddings",
				"POST /v1/images/generations", "POST /images/edits", "POST /v1/images/generations/async",
				"GET /v1/images/tasks/:task_id", "POST /v1/images/batches", "GET /v1/images/batches/:id/download",
				"POST /v1/videos", "GET /videos/:request_id/content", "GET /v1/realtime", "POST /v1/tts",
				"POST /api/v3/contents/generations/tasks", "POST /v3/contents/generations/tasks",
				"POST /contents/generations/tasks", "GET /v1beta/models", "POST /v1beta/models/*modelAction",
				"POST /v1beta/cachedContents", "POST /backend-api/codex/responses", "GET /backend-api/codex/models",
				"POST /antigravity/v1/messages", "GET /antigravity/models", "GET /antigravity/v1beta/models",
				"POST /api/event_logging/batch",
			} {
				_, exists := got[path]
				require.Equal(t, tc.gateway, exists, path)
			}
			for _, path := range []string{
				"GET /setup/status", "POST /api/v1/auth/register", "POST /api/v1/auth/login",
				"GET /api/v1/auth/me", "GET /api/v1/user/profile", "GET /api/v1/keys",
				"GET /api/v1/model-plaza", "GET /api/v1/settings/public", "GET /api/v1/admin/settings",
				"GET /api/v1/admin/accounts", "GET /api/v1/admin/payment/config",
				"POST /api/v1/mcp", "GET /api/v1/mcp", "DELETE /api/v1/mcp",
				"GET /.well-known/oauth-protected-resource/api/v1/mcp", "GET /.well-known/oauth-authorization-server",
				"POST /api/v1/mcp/oauth/token", "GET /api/v1/user/mcp/grants",
				"GET /api/v1/auth/oauth/oidc/callback", "GET /api/v1/auth/oauth/github/callback",
				"POST /api/v1/payment/orders", "POST /api/v1/payment/webhook/stripe",
				"GET /api/v1/payment/webhook/easypay", "GET /api/v1/pages/:slug", "GET /api/v1/pages",
				"GET /api/v1/pages/:slug/images/*filename", "GET /api/v1/plugin-ui/:token/*path",
			} {
				_, exists := got[path]
				require.Equal(t, tc.control, exists, path)
			}

			if tc.control {
				require.Positive(t, repo.reads, "control surface initializes dynamic CSP origins")
				require.DirExists(t, filepath.Join(cfg.Pricing.DataDir, "pages"))
			} else {
				require.Zero(t, repo.reads, "no panel settings queries during gateway/worker registration")
				require.NoDirExists(t, filepath.Join(cfg.Pricing.DataDir, "pages"))
			}
			if !tc.control && !tc.gateway {
				require.Len(t, got, 1, "worker and invalid roles expose only liveness")
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
			require.Equal(t, http.StatusOK, w.Code)
			require.JSONEq(t, `{"status":"ok"}`, w.Body.String())
			if tc.control {
				require.Contains(t, w.Header().Get("Content-Security-Policy"), "https://frames.example.test")
			} else {
				require.NotContains(t, w.Header().Get("Content-Security-Policy"), "https://frames.example.test")
				for _, path := range []string{"/", "/login", "/assets/nonexistent.js"} {
					w = httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
					require.Equal(t, http.StatusNotFound, w.Code, "frontend must not intercept %s", path)
				}
			}
		})
	}

	union := make(map[string]string)
	for _, role := range []string{config.RuntimeRoleGateway, config.RuntimeRoleControl} {
		for path, h := range registered[role] {
			union[path] = h
		}
	}
	require.Equal(t, registered[config.RuntimeRoleAll], union, "split surfaces retain every legacy route and handler")
	require.Equal(t, registered[config.RuntimeRoleAll], registered[""], "zero-valued role remains legacy all")

	// Compare against the complete existing registrar, not a hand-picked subset
	// of protocol routes. The production auth middleware remains installed.
	cfg := cloudRoleConfig(t, config.RuntimeRoleGateway)
	expectedGateway := gin.New()
	routes.RegisterCommonRoutes(expectedGateway, config.RuntimeRoleGateway)
	routes.RegisterGatewayRoutes(expectedGateway, cloudRoleHandlers(cfg), middleware.NewAPIKeyAuthMiddleware(nil, nil, cfg), nil, nil, nil, nil, nil, cfg)
	require.Equal(t, cloudRoleRouteTable(expectedGateway), registered[config.RuntimeRoleGateway])
}

func TestCloudRoleRoutesKeepRealAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{config.RuntimeRoleAll, config.RuntimeRoleGateway, config.RuntimeRoleControl, config.RuntimeRoleWorker} {
		t.Run(role, func(t *testing.T) {
			cfg := cloudRoleConfig(t, role)
			settings := service.NewSettingService(&cloudRoleSettings{}, cfg)
			r := cloudRoleRouter(gin.New(), cloudRoleHandlers(cfg), cfg, settings)
			for _, tc := range []struct {
				method  string
				path    string
				control bool
			}{
				{http.MethodPost, "/v1/messages", false},
				{http.MethodPost, "/responses", false},
				{http.MethodGet, "/v1/responses", false},
				{http.MethodGet, "/v1beta/models", false},
				{http.MethodGet, "/antigravity/v1beta/models", false},
				{http.MethodPost, "/api/v3/contents/generations/tasks", false},
				{http.MethodGet, "/api/v1/auth/me", true},
				{http.MethodGet, "/api/v1/user/profile", true},
				{http.MethodGet, "/api/v1/admin/settings", true},
				{http.MethodGet, "/api/v1/payment/config", true},
				{http.MethodGet, "/api/v1/pages/example", true},
				{http.MethodPost, "/api/v1/mcp", true},
			} {
				want := http.StatusNotFound
				if role == config.RuntimeRoleAll || (tc.control && role == config.RuntimeRoleControl) || (!tc.control && role == config.RuntimeRoleGateway) {
					want = http.StatusUnauthorized
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
				require.Equal(t, want, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
			}
		})
	}
}

func TestCloudGatewaySetupDoesNotRequirePanelSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := cloudRoleConfig(t, config.RuntimeRoleGateway)
	// Nil settings would panic on a frame-origin query or callback registration,
	// including frontend initialization paths used by embed builds.
	r := cloudRoleRouter(gin.New(), cloudRoleHandlers(cfg), cfg, nil)
	require.Contains(t, cloudRoleRouteTable(r), "POST /v1/messages")
}

func TestCloudWorkerProvideRouterNeedsNoBusinessDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{config.RuntimeRoleWorker, "unknown"} {
		t.Run(role, func(t *testing.T) {
			cfg := cloudRoleConfig(t, role)
			// Nil settings proves ProvideRouter neither installs a websearch
			// builder nor accesses frontend settings for health-only roles.
			r := ProvideRouter(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			require.Len(t, r.Routes(), 1)
			require.Contains(t, cloudRoleRouteTable(r), "GET /health")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestCloudCommonRoutesPreserveDefaultContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	routes.RegisterCommonRoutes(r)
	require.Len(t, r.Routes(), 3)
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/health", `{"status":"ok"}`},
		{http.MethodPost, "/api/event_logging/batch", ""},
		{http.MethodGet, "/setup/status", `{"code":0,"data":{"needs_setup":false,"step":"completed"}}`},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusOK, w.Code)
		if tc.body == "" {
			require.Empty(t, w.Body.String())
		} else {
			require.JSONEq(t, tc.body, w.Body.String())
		}
	}
}

func cloudRoleConfig(t *testing.T, role string) *config.Config {
	t.Helper()
	return &config.Config{
		Runtime:  config.RuntimeConfig{Role: role},
		Gateway:  config.GatewayConfig{MaxBodySize: 1024, TextMaxBodySize: 1024},
		Pricing:  config.PricingConfig{DataDir: t.TempDir()},
		Security: config.SecurityConfig{CSP: config.CSPConfig{Enabled: true}},
		MCP:      config.MCPConfig{PublicURL: "https://control.example.test/api/v1/mcp"},
	}
}

func cloudRoleHandlers(cfg *config.Config) *handler.Handlers {
	// Method values may have nil receivers during registration, as in the
	// existing MCP route tests. Only MCP requires a concrete object at setup;
	// no business handler is invoked before the real auth middleware rejects.
	return &handler.Handlers{
		Admin:    &handler.AdminHandlers{},
		MCPOAuth: handler.NewMCPOAuthHandler(nil, nil, nil, nil, cfg),
	}
}

func cloudRoleRouter(r *gin.Engine, h *handler.Handlers, cfg *config.Config, settings *service.SettingService) *gin.Engine {
	return SetupRouter(r, h,
		middleware.NewJWTAuthMiddleware(nil, nil, settings, nil),
		middleware.NewOptionalJWTAuthMiddleware(nil, nil, settings, nil),
		middleware.NewAdminAuthMiddleware(nil, nil, settings, nil),
		middleware.NewAPIKeyAuthMiddleware(nil, nil, cfg),
		middleware.NewAuditLogMiddleware(nil),
		middleware.NewStepUpAuthMiddleware(nil, nil, settings),
		nil, nil, nil, settings, nil, cfg, nil)
}

func cloudRoleRouteTable(r *gin.Engine) map[string]string {
	registered := make(map[string]string)
	for _, route := range r.Routes() {
		registered[route.Method+" "+route.Path] = route.Handler
	}
	return registered
}

type cloudRoleSettings struct {
	service.SettingRepository // Unexpected operations panic instead of reaching a database.
	values                    map[string]string
	reads                     int
}

func (s *cloudRoleSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.reads++
	values := make(map[string]string)
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}
