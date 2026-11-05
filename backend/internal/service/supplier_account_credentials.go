package service

import (
	"context"
	"strings"
)

type SupplierAccountResourceReader interface {
	GetExecutionResource(context.Context, int64, string) (*SupplierResource, error)
}

// supplierAccountMarked checks presence, not truthiness: malformed or removed
// credential references must never select the legacy plaintext/cache path.
func supplierAccountMarked(account *Account) bool {
	if account == nil {
		return false
	}
	if _, ok := account.Credentials["supplier_resource_id"]; ok {
		return true
	}
	if _, ok := account.Extra["supplier_resource_id"]; ok {
		return true
	}
	_, ok := account.Extra["supplier_id"]
	return ok
}

func (s *SupplierResourceService) ResolveAccountCredentials(ctx context.Context, account *Account) (*SupplierExecutionCredentials, error) {
	if s == nil || account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsOpenAIAgentIdentity() {
		return nil, ErrSupplierResource
	}
	ref := account.GetCredential("supplier_resource_id")
	hint := account.GetCredential("chatgpt_account_id")
	if strings.TrimSpace(ref) == "" || strings.TrimSpace(hint) == "" {
		return nil, ErrSupplierResource
	}
	repo, ok := s.repo.(SupplierAccountResourceReader)
	if !ok {
		return nil, ErrSupplierResource
	}
	resource, err := repo.GetExecutionResource(ctx, account.ID, ref)
	if err != nil {
		return nil, err
	}
	// Headers are assembled from the scheduler's Account. Reject stale/forged
	// metadata rather than mutating a shared scheduler snapshot or sending a token
	// under a different upstream account identity.
	if resource == nil || resource.ID != ref || resource.AccountHint != hint {
		return nil, ErrSupplierResource
	}
	credentials, err := s.ResolveExecutionResource(ctx, resource)
	if err != nil {
		return nil, err
	}
	if credentials.AccountID != hint {
		return nil, ErrSupplierResource
	}
	return credentials, nil
}
