package service

import (
	"context"
	"encoding/base64"
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

type supplierVaultRepo struct {
	SupplierResourceRepository
	saved *SupplierResource
}

func (r *supplierVaultRepo) FindImportResource(_ context.Context, owner int64, identity string) (*SupplierResource, error) {
	if r.saved != nil && r.saved.SupplierID == owner && r.saved.IdentityFingerprint == identity {
		return r.saved, nil
	}
	return nil, nil
}
func (r *supplierVaultRepo) Get(_ context.Context, owner int64, id string) (*SupplierResource, error) {
	if r.saved == nil || r.saved.SupplierID != owner || r.saved.ID != id {
		return nil, ErrSupplierResource
	}
	return r.saved, nil
}
func (r *supplierVaultRepo) Create(_ context.Context, _ int64, v *SupplierResource) error {
	r.saved = v
	return nil
}
func TestSupplierResourceEncryptedOnlyBindingAndMissingKey(t *testing.T) {
	ctx := context.Background()
	repo := &supplierVaultRepo{}
	profile := &supplierTestRepo{p: &SupplierProfile{ID: 3, UserID: 7}}
	cfg := &config.Config{}
	s := NewSupplierResourceService(repo, profile, nil, cfg)
	_, e := s.Import(ctx, 7, "name", `{}`)
	require.ErrorIs(t, e, ErrSupplierVault)
	require.Nil(t, repo.saved)
	path := filepath.Join(t.TempDir(), "keyring")
	require.NoError(t, os.WriteFile(path, []byte(`{"keys":{"v1":"`+strings.Repeat("11", 32)+`"}}`), 0600))
	cfg.SupplierPortal.VaultKeyFile = path
	cfg.SupplierPortal.VaultKeyID = "v1"
	s = NewSupplierResourceService(repo, profile, nil, cfg)
	payload := fmt.Sprintf(`{"exp":%d,"email":"u@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"account","chatgpt_plan_type":"plus"}}`, time.Now().Add(time.Hour).Unix())
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
	input, _ := json.Marshal(map[string]any{"access_token": token, "refresh_token": "discard-refresh-canary", "id_token": "discard-id-canary"})
	_, e = s.Import(ctx, 7, "name", string(input))
	require.ErrorIs(t, e, ErrSupplierConsent)
	tooLate := time.Now().Add(2 * time.Hour)
	_, e = s.Import(ctx, 7, "name", string(input), SupplierImportOptions{ConsentVersion: SupplierConsentVersion, AuthorizationExpiresAt: &tooLate})
	require.ErrorIs(t, e, ErrSupplierAuthorizationExpiry)
	r, e := s.Import(ctx, 7, "name", string(input), SupplierImportOptions{ConsentVersion: SupplierConsentVersion})
	require.NoError(t, e)
	require.Equal(t, "paused", r.Status)
	require.False(t, r.Schedulable)
	require.NotContains(t, string(repo.saved.Envelope), token)
	codec, e := credentialcrypto.New(s.wrapper, "v1")
	require.NoError(t, e)
	plain, e := codec.Decrypt(ctx, resourceBinding(r), r.Envelope)
	require.NoError(t, e)
	require.Equal(t, token, plain["access_token"])
	require.NotContains(t, plain, "refresh_token")
	require.NotContains(t, plain, "id_token")
	swapped := *r
	swapped.SupplierID++
	_, e = codec.Decrypt(ctx, resourceBinding(&swapped), r.Envelope)
	require.Error(t, e)
	b, e := json.Marshal(r)
	require.NoError(t, e)
	require.NotContains(t, string(b), token)
	require.NotContains(t, string(b), "account_hint")
	_, e = s.Import(ctx, 7, "name", string(input), SupplierImportOptions{ConsentVersion: SupplierConsentVersion, ResourceID: r.ID, ResourceVersion: r.Version + 1})
	require.ErrorIs(t, e, ErrSupplierResourceVersion)
	wrongInput := strings.ReplaceAll(string(input), token, "e30."+base64.RawURLEncoding.EncodeToString([]byte(strings.ReplaceAll(payload, `:"account"`, `:"other"`)))+".synthetic")
	_, e = s.Import(ctx, 7, "name", wrongInput, SupplierImportOptions{ConsentVersion: SupplierConsentVersion, ResourceID: r.ID, ResourceVersion: r.Version})
	require.ErrorIs(t, e, ErrSupplierResourceConflict)
	_, e = s.Import(ctx, 7, "name", string(input), SupplierImportOptions{ConsentVersion: SupplierConsentVersion, ResourceID: r.ID})
	require.ErrorIs(t, e, ErrSupplierResourceVersion)
	// Reimport must encrypt under the original UUID, never a freshly generated
	// ID that repository persistence would subsequently replace.
	r.Version = 7
	r.CredentialVersion = 4
	r.Status = "disconnected"
	again, e := s.Import(ctx, 7, "reconnected", string(input), SupplierImportOptions{ConsentVersion: SupplierConsentVersion})
	require.NoError(t, e)
	require.Equal(t, r.ID, again.ID)
	require.Equal(t, int64(7), again.Version, "carry observed CAS into repository")
	require.Equal(t, int64(4), again.CredentialVersion)
	plain, e = codec.Decrypt(ctx, resourceBinding(r), again.Envelope)
	require.NoError(t, e)
	require.Equal(t, token, plain["access_token"])
}
