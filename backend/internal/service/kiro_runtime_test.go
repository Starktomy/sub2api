package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type kiroRuntimeMemory struct{ *kiroMemoryRepo }

func (r *kiroRuntimeMemory) PatchRuntime(_ context.Context, v *KiroNativeRecord, p KiroRuntimePatch, env []byte) error {
	if r.r.Binding.Version != p.ExpectedVersion || r.r.RefreshOwner != v.RefreshOwner {
		return ErrKiroRefreshBarrier
	}
	v.Binding.Version++
	v.Envelope = env
	if p.Operation == "reimport" {
		v.RefreshState = "idle"
		v.RefreshOwner = ""
	}
	r.r = v
	return nil
}
func (r *kiroRuntimeMemory) SaveSnapshot(_ context.Context, v *KiroNativeRecord, k string, b json.RawMessage) error {
	if v.Binding.Version != r.r.Binding.Version {
		return ErrKiroRefreshBarrier
	}
	if k == "quota" {
		r.r.QuotaSnapshot = b
	} else {
		r.r.ModelsSnapshot = b
	}
	return nil
}
func TestKiroRuntimeExplicitObservationAndRecovery(t *testing.T) {
	calls := 0
	s, mem := kiroRefreshSetup(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("X-Amz-Target") == "KiroControlPlaneBearerService.ListAvailableModels" {
			require.Equal(t, "POST", req.Method)
			require.Equal(t, "KiroControlPlaneBearerService.ListAvailableModels", req.Header.Get("X-Amz-Target"))
			require.Equal(t, "Bearer fake-sso-control", req.Header.Get("Authorization"))
			require.Equal(t, "KIRO_CLI", req.URL.Query().Get("origin"))
			require.NotEmpty(t, req.URL.Query().Get("profileArn"))
			var input map[string]string
			require.NoError(t, json.NewDecoder(req.Body).Decode(&input))
			require.Equal(t, req.URL.Query().Get("profileArn"), input["profileArn"])
			require.Equal(t, "KIRO_CLI", input["origin"])
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"modelId":"explicit-model","modelName":"Model","rateMultiplier":99,"secret":"canary"}]}`))}, nil
		}
		require.Equal(t, "management.us-east-1.kiro.dev", req.URL.Host)
		require.Equal(t, "POST", req.Method)
		require.Equal(t, "AmazonCodeWhispererService.GetUsageLimits", req.Header.Get("X-Amz-Target"))
		require.Equal(t, "Bearer fake-sso-control", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"email":"secret-canary","accessToken":"secret-canary","usageBreakdownList":[{"usageLimit":25,"currentUsage":3,"email":"secret-canary"}]}`)), Header: http.Header{}}, nil
	})
	repo := &kiroRuntimeMemory{mem}
	s.repo = repo
	secret := kiroTestSecret(time.Now(), KiroCredentialIdC)
	secret.SSOAccessToken = "fake-sso-control"
	secret.SSOExpiresAt = time.Now().Add(time.Hour).Unix()
	var err error
	mem.r.Envelope, _, err = s.vault.Import(context.Background(), mem.r.Binding, kiroTestJSON(t, secret), time.Now())
	require.NoError(t, err)
	status, err := s.Runtime(context.Background(), 7)
	require.NoError(t, err)
	require.Contains(t, string(status.Quota), "unknown")
	require.Zero(t, calls)
	status, err = s.Observe(context.Background(), 7, "models")
	require.NoError(t, err)
	require.Contains(t, string(status.Models), "management.us-east-1.kiro.dev")
	require.Contains(t, string(status.Models), "explicit-model")
	require.NotContains(t, string(status.Models), "canary")
	require.NotContains(t, string(status.Models), "rateMultiplier")
	require.Equal(t, 1, calls)
	status, err = s.Observe(context.Background(), 7, "quota")
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Contains(t, string(status.Quota), "known")
	require.NotContains(t, string(status.Quota), "secret-canary")
	mem.r.RefreshState = "pending"
	mem.r.RefreshOwner = "fake-owner"
	_, err = s.Observe(context.Background(), 7, "quota")
	require.ErrorIs(t, err, ErrKiroRefreshBarrier)
	require.Equal(t, 2, calls)
	p := KiroRuntimePatch{ExpectedVersion: 1, Operation: "reimport", Credential: json.RawMessage(`{"authMethod":"idc","clientId":"new-client","clientSecret":"new-client-secret","refreshToken":"new-refresh"}`)}
	_, err = s.PatchRuntime(context.Background(), 7, p)
	require.ErrorIs(t, err, ErrKiroRefreshBarrier)
	p.ExpectedRefreshOwner = "fake-owner"
	fresh := p.Credential
	p.Credential = json.RawMessage(`{"authMethod":"idc","clientId":"new-client","clientSecret":"new-client-secret","refreshToken":"fake-refresh-token"}`)
	_, err = s.PatchRuntime(context.Background(), 7, p)
	require.ErrorIs(t, err, ErrKiroRefreshBarrier)
	p.Credential = fresh
	status, err = s.PatchRuntime(context.Background(), 7, p)
	require.NoError(t, err)
	require.Equal(t, int64(2), status.CredentialVersion)
	require.True(t, status.Paused)
	require.Equal(t, "idle", status.RefreshState)
	_, err = s.PatchRuntime(context.Background(), 7, p)
	require.ErrorIs(t, err, ErrKiroRefreshBarrier)
	require.Equal(t, 2, calls)
}

func TestKiroRuntimeNoHiddenRefreshRetryOrTokenFallback(t *testing.T) {
	calls := 0
	s, mem := kiroRefreshSetup(t, func(req *http.Request) (*http.Response, error) {
		calls++
		require.NotContains(t, req.URL.Path, "token")
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"message":"upstream-secret-canary"}`))}, nil
	})
	s.repo = &kiroRuntimeMemory{mem}
	secret := kiroTestSecret(time.Now(), KiroCredentialIdC) // SSO expired, inference identity valid.
	var err error
	mem.r.Envelope, _, err = s.vault.Import(context.Background(), mem.r.Binding, kiroTestJSON(t, secret), time.Now())
	require.NoError(t, err)
	_, err = s.Observe(context.Background(), 7, "quota")
	require.ErrorIs(t, err, ErrKiroCredentialRefreshRequired)
	require.Zero(t, calls)
	_, err = s.Observe(context.Background(), 7, "models")
	require.ErrorIs(t, err, ErrKiroCredentialRefreshRequired)
	require.Zero(t, calls)
	secret.SSOAccessToken = "fake-sso-control"
	secret.SSOExpiresAt = time.Now().Add(time.Hour).Unix()
	mem.r.Envelope, _, err = s.vault.Import(context.Background(), mem.r.Binding, kiroTestJSON(t, secret), time.Now())
	require.NoError(t, err)
	_, err = s.Observe(context.Background(), 7, "models")
	require.ErrorIs(t, err, ErrKiroUnavailable)
	require.Equal(t, 1, calls)
	require.NotContains(t, err.Error(), "upstream-secret-canary")
	require.Equal(t, "idle", mem.r.RefreshState)
	require.Equal(t, int64(1), mem.r.Binding.Version)
}
