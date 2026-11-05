package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
	"github.com/stretchr/testify/require"
)

func supplierExecutionFixture(t *testing.T) (*SupplierResourceService, *SupplierResource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keyring")
	require.NoError(t, os.WriteFile(path, []byte(`{"keys":{"v1":"`+strings.Repeat("11", 32)+`","v2":"`+strings.Repeat("22", 32)+`"}}`), 0600))
	cfg := &config.Config{}
	cfg.SupplierPortal.VaultKeyFile = path
	cfg.SupplierPortal.VaultKeyID = "v2"
	// Nil collaborators ensure the resolver needs neither a repository nor quota
	// client. All secrets and key material in this fixture are synthetic.
	s := NewSupplierResourceService(nil, nil, nil, cfg)
	r := &SupplierResource{
		ID: "synthetic-resource", SupplierID: 3, Status: "paused",
		KeyID: "v1", TokenExpiresAt: time.Now().Add(time.Hour),
		AccountHint: "synthetic-account",
	}
	supplierExecutionEncrypt(t, s, r, map[string]any{
		"access_token": "synthetic-execution-token", "account_id": "synthetic-account",
	})
	return s, r
}

func supplierExecutionEncrypt(t *testing.T, s *SupplierResourceService, r *SupplierResource, plain map[string]any) {
	t.Helper()
	codec, err := credentialcrypto.New(s.wrapper, r.KeyID)
	require.NoError(t, err)
	r.Envelope, err = codec.Encrypt(context.Background(), resourceBinding(r), plain)
	require.NoError(t, err)
}

func TestSupplierExecutionCredentialsResolve(t *testing.T) {
	s, r := supplierExecutionFixture(t)
	before := *r
	credentials, err := s.ResolveExecutionResource(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, "synthetic-execution-token", credentials.AccessToken)
	require.Equal(t, "synthetic-account", credentials.AccountID)
	require.Equal(t, before, *r, "resolution must not mutate or admit the resource")
	require.False(t, r.Schedulable)

	// The resource key, not the active import key, decrypts historical envelopes.
	require.NotEqual(t, s.active, r.KeyID)
	// Each call decrypts anew; changing a returned object must not affect later
	// results, nor may a prior success hide newly invalid ciphertext or state.
	credentials.AccessToken = "caller-mutation"
	next, err := s.ResolveExecutionResource(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, "synthetic-execution-token", next.AccessToken)
	r.Envelope = []byte(`{"access_token":"plaintext-fallback-forbidden"}`)
	next, err = s.ResolveExecutionResource(context.Background(), r)
	require.ErrorIs(t, err, ErrSupplierVault)
	require.Nil(t, next)
}

func TestSupplierExecutionCredentialsRejectResource(t *testing.T) {
	tests := []struct {
		name string
		edit func(*SupplierResource)
		err  error
	}{
		{"missing owner", func(r *SupplierResource) { r.SupplierID = 0 }, ErrSupplierResource},
		{"negative owner", func(r *SupplierResource) { r.SupplierID = -1 }, ErrSupplierResource},
		{"missing identity", func(r *SupplierResource) { r.ID = " " }, ErrSupplierResource},
		{"disconnected", func(r *SupplierResource) { r.Status = "disconnected" }, ErrSupplierReimport},
		{"unknown status", func(r *SupplierResource) { r.Status = "active" }, ErrSupplierReimport},
		{"empty status", func(r *SupplierResource) { r.Status = "" }, ErrSupplierReimport},
		{"expired", func(r *SupplierResource) { r.TokenExpiresAt = time.Now().Add(-time.Second) }, ErrSupplierReimport},
		{"missing expiry", func(r *SupplierResource) { r.TokenExpiresAt = time.Time{} }, ErrSupplierReimport},
		{"reauthorization", func(r *SupplierResource) { r.ProbeState = "reauthorization_required" }, ErrSupplierReimport},
		{"other provider", func(r *SupplierResource) { r.Provider = "other" }, ErrSupplierResource},
		{"other product", func(r *SupplierResource) { r.Product = "other" }, ErrSupplierResource},
		{"swapped owner", func(r *SupplierResource) { r.SupplierID++ }, ErrSupplierVault},
		{"swapped identity", func(r *SupplierResource) { r.ID += "-other" }, ErrSupplierVault},
		{"swapped key", func(r *SupplierResource) { r.KeyID = "v2" }, ErrSupplierVault},
		{"missing key", func(r *SupplierResource) { r.KeyID = "missing" }, ErrSupplierVault},
		{"empty key", func(r *SupplierResource) { r.KeyID = "" }, ErrSupplierVault},
		{"missing envelope", func(r *SupplierResource) { r.Envelope = nil }, ErrSupplierVault},
		{"corrupt envelope", func(r *SupplierResource) { r.Envelope = []byte("corrupt") }, ErrSupplierVault},
		{"mismatched account hint", func(r *SupplierResource) { r.AccountHint = "other-account" }, ErrSupplierVault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, r := supplierExecutionFixture(t)
			tt.edit(r)
			credentials, err := s.ResolveExecutionResource(context.Background(), r)
			require.ErrorIs(t, err, tt.err)
			require.Nil(t, credentials)
			require.NotContains(t, err.Error(), "synthetic-execution-token")
			require.NotContains(t, err.Error(), "synthetic-account")
		})
	}
}

func TestSupplierExecutionCredentialsRejectPlaintext(t *testing.T) {
	for _, tt := range []struct {
		name  string
		plain map[string]any
	}{
		{"missing token", map[string]any{"account_id": "synthetic-account"}},
		{"missing account", map[string]any{"access_token": "synthetic-execution-token"}},
		{"blank token", map[string]any{"access_token": " \t", "account_id": "synthetic-account"}},
		{"blank account", map[string]any{"access_token": "synthetic-execution-token", "account_id": " \t"}},
		{"wrong token type", map[string]any{"access_token": 12, "account_id": "synthetic-account"}},
		{"wrong account type", map[string]any{"access_token": "synthetic-execution-token", "account_id": 12}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r := supplierExecutionFixture(t)
			supplierExecutionEncrypt(t, s, r, tt.plain)
			credentials, err := s.ResolveExecutionResource(context.Background(), r)
			require.ErrorIs(t, err, ErrSupplierVault)
			require.Nil(t, credentials)
		})
	}
}

func TestSupplierExecutionCredentialsUnavailableAndCanceled(t *testing.T) {
	s, r := supplierExecutionFixture(t)
	credentials, err := s.ResolveExecutionResource(context.Background(), nil)
	require.ErrorIs(t, err, ErrSupplierResource)
	require.Nil(t, credentials)
	for _, unavailable := range []*SupplierResourceService{nil, {}, {wrapper: s.wrapper, active: "missing"}} {
		credentials, err = unavailable.ResolveExecutionResource(context.Background(), r)
		require.ErrorIs(t, err, ErrSupplierVault)
		require.Nil(t, credentials)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	credentials, err = s.ResolveExecutionResource(ctx, r)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, credentials)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	credentials, err = s.ResolveExecutionResource(ctx, r)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, credentials)
}

func TestSupplierExecutionCredentialsRedacted(t *testing.T) {
	credentials := SupplierExecutionCredentials{AccessToken: "token-redaction-canary", AccountID: "account-redaction-canary"}
	for _, value := range []any{credentials, &credentials} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			text := fmt.Sprintf(format, value)
			require.Contains(t, text, "REDACTED")
			require.NotContains(t, text, credentials.AccessToken)
			require.NotContains(t, text, credentials.AccountID)
		}
		data, err := json.Marshal(value)
		require.NoError(t, err)
		require.JSONEq(t, `{"access_token":"[REDACTED]","account_id":"[REDACTED]"}`, string(data))
	}
}
