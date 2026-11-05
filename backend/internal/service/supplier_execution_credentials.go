package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
)

// SupplierExecutionCredentials is a short-lived plaintext result for the vault-backed
// Account bridge. Do not cache it or attach it to an Account's stored credentials.
// Explicit field access is required to use secrets; formatting and JSON redact
// both the access token and the upstream account identity.
type SupplierExecutionCredentials struct {
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
}

func (SupplierExecutionCredentials) String() string {
	return "SupplierExecutionCredentials{[REDACTED]}"
}

func (c SupplierExecutionCredentials) GoString() string { return c.String() }

func (SupplierExecutionCredentials) MarshalJSON() ([]byte, error) {
	return []byte(`{"access_token":"[REDACTED]","account_id":"[REDACTED]"}`), nil
}

// ResolveExecutionResource decrypts an already-authorized vault resource without
// retaining plaintext or performing repository, quota, or upstream calls. The
// caller must obtain a fresh, trusted resource through an owner-scoped lookup and
// verify an explicit durable Account binding first. This
// method does not establish that binding or authorize scheduling: "paused" is the
// current vault's only connected state, not admission to the gateway. Gateway
// callers must use ResolveAccountCredentials, not this lower-level primitive.
//
// SupplierID and ID participate in the same authenticated binding as Import and
// queryResource; changing either prevents decryption. This is not a substitute
// for checking the caller's ownership before supplying the resource.
func (s *SupplierResourceService) ResolveExecutionResource(ctx context.Context, resource *SupplierResource) (*SupplierExecutionCredentials, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource == nil || resource.SupplierID <= 0 || strings.TrimSpace(resource.ID) == "" {
		return nil, ErrSupplierResource
	}
	if resource.Status != "paused" || resource.ProbeState == "reauthorization_required" || !time.Now().Before(resource.TokenExpiresAt) {
		return nil, ErrSupplierReimport
	}
	// Raw repository records omit these projection-only fields. When present,
	// they must still describe the OpenAI Codex vault this resolver supports.
	if (resource.Provider != "" && resource.Provider != "openai") || (resource.Product != "" && resource.Product != "codex") {
		return nil, ErrSupplierResource
	}
	if s == nil || !s.Configured() {
		return nil, ErrSupplierVault
	}
	codec, err := credentialcrypto.New(s.wrapper, resource.KeyID)
	if err != nil {
		return nil, ErrSupplierVault
	}
	plain, err := codec.Decrypt(ctx, resourceBinding(resource), resource.Envelope)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSupplierVault
	}
	// clear drops map references; Go strings cannot be reliably zeroized. The
	// codec wipes its temporary plaintext byte buffers before returning.
	defer clear(plain)
	token, tokenOK := plain["access_token"].(string)
	account, accountOK := plain["account_id"].(string)
	if !tokenOK || !accountOK || strings.TrimSpace(token) == "" || strings.TrimSpace(account) == "" {
		return nil, ErrSupplierVault
	}
	if resource.AccountHint != "" && resource.AccountHint != account {
		return nil, ErrSupplierVault
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &SupplierExecutionCredentials{AccessToken: token, AccountID: account}, nil
}
