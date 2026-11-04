package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type kiroMemoryRepo struct {
	mu         sync.Mutex
	r          *KiroNativeRecord
	commitFail bool
	commits    int
}

func (r *kiroMemoryRepo) Create(_ context.Context, in KiroNativeImport, id string, encrypt func(KiroCredentialBinding) ([]byte, error)) (*KiroNativeRecord, error) {
	b := KiroCredentialBinding{ResourceUUID: id, AccountID: 7, Version: 1, Mode: in.AuthMode}
	env, err := encrypt(b)
	if err != nil {
		return nil, err
	}
	r.r = &KiroNativeRecord{Binding: b, Region: in.Region, Envelope: env, RefreshState: "idle", UserAgent: in.UserAgent, AmzUserAgent: in.AmzUserAgent, MachineID: in.MachineID}
	return r.r, nil
}
func (r *kiroMemoryRepo) Get(context.Context, int64) (*KiroNativeRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := *r.r
	v.Envelope = append([]byte(nil), v.Envelope...)
	return &v, nil
}
func (r *kiroMemoryRepo) BeginRefresh(_ context.Context, v *KiroNativeRecord, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.r.RefreshState != "idle" || r.r.Binding.Version != v.Binding.Version {
		return ErrKiroRefreshBarrier
	}
	r.r.RefreshState = "pending"
	return nil
}
func (r *kiroMemoryRepo) CompleteRefresh(_ context.Context, v *KiroNativeRecord, _ string, env []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commitFail {
		return ErrKiroRefreshBarrier
	}
	if r.r.RefreshState != "pending" || r.r.Binding.Version != v.Binding.Version {
		return ErrKiroRefreshBarrier
	}
	r.r.Envelope = append([]byte(nil), env...)
	r.r.Binding.Version++
	r.r.RefreshState = "idle"
	r.commits++
	return nil
}
func (r *kiroMemoryRepo) AmbiguousRefresh(context.Context, *KiroNativeRecord, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.r.RefreshState = "ambiguous"
	return nil
}

type kiroNativeRT func(*http.Request) (*http.Response, error)

func (f kiroNativeRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func kiroRefreshSetup(t *testing.T, rt kiroNativeRT) (*KiroNativeService, *kiroMemoryRepo) {
	t.Helper()
	vault, _, _, _ := kiroTestSetup(t)
	repo := &kiroMemoryRepo{}
	s := NewKiroNativeService(repo, vault, &http.Client{Transport: rt}, nil)
	_, err := s.Import(context.Background(), KiroNativeImport{Name: "synthetic", Region: "us-east-1", AuthMode: KiroCredentialIdC, Credential: json.RawMessage(`{"clientId":"fake-client","clientSecret":"fake-client-secret","refreshToken":"fake-refresh","provider":"ignored"}`)})
	require.NoError(t, err)
	return s, repo
}
func TestKiroNativeRefreshDurableRotationOnce(t *testing.T) {
	calls := 0
	s, repo := kiroRefreshSetup(t, func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https://oidc.us-east-1.amazonaws.com/token", req.URL.String())
		b, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Contains(t, string(b), `"grantType":"refresh_token"`)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"accessToken":"fake-rotated-access","refreshToken":"fake-rotated-refresh","expiresIn":3600}`))}, nil
	})
	_ = repo
	r, token, err := s.Inference(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(2), r.Binding.Version)
	require.Equal(t, "fake-rotated-access", token.BearerToken())
	require.Equal(t, 1, repo.commits)
	require.NotContains(t, string(repo.r.Envelope), "fake-rotated")
	_, _, err = s.Inference(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	identity, err := kiroCredentialIdentity(r.Binding)
	require.NoError(t, err)
	plain, err := s.vault.codec.Decrypt(context.Background(), identity, repo.r.Envelope)
	require.NoError(t, err)
	require.Contains(t, plain["kiro_secret"], "fake-rotated-refresh")
}
func TestKiroNativeRefreshAmbiguousNeverReplay(t *testing.T) {
	for _, commitFail := range []bool{false, true} {
		t.Run(map[bool]string{false: "network", true: "commit"}[commitFail], func(t *testing.T) {
			calls := 0
			s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
				calls++
				if !commitFail {
					return nil, errors.New("secret-do-not-log")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accessToken":"fake-access","refreshToken":"fake-next","expiresIn":3600}`))}, nil
			})
			repo.commitFail = commitFail
			_, _, err := s.Inference(context.Background(), 7)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-do-not-log")
			_, _, err = s.Inference(context.Background(), 7)
			require.ErrorIs(t, err, ErrKiroRefreshBarrier)
			require.Equal(t, 1, calls)
		})
	}
}
func TestKiroNativeConcurrentRefreshBarrier(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s, _ := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return nil, errors.New("unknown")
	})
	done := make(chan error)
	go func() { _, _, err := s.Inference(context.Background(), 7); done <- err }()
	<-started
	_, _, err := s.Inference(context.Background(), 7)
	require.ErrorIs(t, err, ErrKiroRefreshBarrier)
	close(release)
	require.Error(t, <-done)
}
func TestKiroNativeExpiredAccessRefreshAndMachineIdentity(t *testing.T) {
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })
	secret := kiroTestSecret(time.Now().Add(-2*time.Hour), KiroCredentialIdC)
	env, _, err := s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), time.Now().Add(-3*time.Hour))
	require.NoError(t, err)
	repo.r.Envelope = env
	_, _, err = s.Inference(context.Background(), 7)
	require.Error(t, err)
	require.Equal(t, "ambiguous", repo.r.RefreshState)
	normalized, ok := normalizeKiroMachineID("12345678-1234-1234-1234-123456789abc")
	require.True(t, ok)
	require.Len(t, normalized, 64)
	_, ok = normalizeKiroMachineID("random-machine")
	require.False(t, ok)
}

func TestKiroNativeIDCRefreshSeparatesExplicitIDAndSSOTokens(t *testing.T) {
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accessToken":"fake-sso-only","idToken":"fake-id-inference","refreshToken":"fake-new-refresh","expiresIn":3600}`))}, nil
	})
	r, token, err := s.Inference(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "fake-id-inference", token.BearerToken())
	identity, err := kiroCredentialIdentity(r.Binding)
	require.NoError(t, err)
	plain, err := s.vault.codec.Decrypt(context.Background(), identity, repo.r.Envelope)
	require.NoError(t, err)
	secret, err := parseKiroSecret([]byte(plain["kiro_secret"].(string)), KiroCredentialIdC, time.Now())
	require.NoError(t, err)
	require.Equal(t, kiroIdCIdentityToken, secret.InferenceTokenType)
	require.Equal(t, "fake-sso-only", secret.SSOAccessToken)
	require.NotEqual(t, token.BearerToken(), secret.SSOAccessToken)
}

func TestKiroNativeCancellationAfterRotationStillPersists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		// A transport can deliver the response concurrently with cancellation. Once
		// response bytes exist, encryption+durable CAS must not reuse caller ctx.
		cancel()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accessToken":"fake-cancel-access","refreshToken":"fake-cancel-rotation","expiresIn":3600}`))}, nil
	})
	_, _, err := s.Inference(ctx, 7)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(2), repo.r.Binding.Version)
	require.Equal(t, 1, repo.commits)
	require.Equal(t, "idle", repo.r.RefreshState)
	token, err := s.vault.DecryptInference(context.Background(), repo.r.Binding, repo.r.Envelope, time.Now())
	require.NoError(t, err)
	require.Equal(t, "fake-cancel-access", token.BearerToken())
}

func TestKiroRefreshPreservesAndValidatesProfileARN(t *testing.T) {
	for _, tc := range []struct {
		name, response, expected string
		rejected                 bool
	}{
		{"omitted", `{"accessToken":"fake-next","expiresIn":3600}`, "arn:aws:codewhisperer:us-east-1:123456789012:profile/Old", false},
		{"updated", `{"accessToken":"fake-next","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/New"}`, "arn:aws:codewhisperer:us-east-1:123456789012:profile/New", false},
		{"wrong-region", `{"accessToken":"fake-next","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-west-2:123456789012:profile/New"}`, "", true},
		{"empty", `{"accessToken":"fake-next","expiresIn":3600,"profileArn":""}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})
			secret := kiroTestSecret(time.Now(), KiroCredentialIdC)
			secret.ProfileARN = "arn:aws:codewhisperer:us-east-1:123456789012:profile/Old"
			env, _, err := s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), time.Now())
			require.NoError(t, err)
			repo.r.Envelope = env
			next, err := s.vault.refresh(context.Background(), repo.r, s.client)
			if tc.rejected {
				require.ErrorIs(t, err, ErrKiroRefreshBarrier)
				return
			}
			require.NoError(t, err)
			binding := repo.r.Binding
			binding.Version++
			identity, err := kiroCredentialIdentity(binding)
			require.NoError(t, err)
			doc, err := s.vault.codec.Decrypt(context.Background(), identity, next)
			require.NoError(t, err)
			parsed, err := parseKiroSecret([]byte(doc["kiro_secret"].(string)), binding.Mode, time.Now())
			require.NoError(t, err)
			require.Equal(t, tc.expected, parsed.ProfileARN)
		})
	}
}
