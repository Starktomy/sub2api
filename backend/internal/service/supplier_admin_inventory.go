package service

import "context"

// AdminSupplierResource deliberately excludes encrypted or plaintext credentials.
type AdminSupplierResource struct {
	Resource       *SupplierResource `json:"resource"`
	SupplierName   string            `json:"supplier_name"`
	SupplierUserID int64             `json:"supplier_user_id"`
}
type supplierAdminInventory interface {
	ListAdminResources(context.Context) ([]AdminSupplierResource, error)
}

func (s *SupplierResourceService) ListAdminResources(ctx context.Context) ([]AdminSupplierResource, error) {
	repo, ok := s.repo.(supplierAdminInventory)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	items, err := repo.ListAdminResources(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.projectAdmission(ctx, items[i].Resource); err != nil {
			return nil, err
		}
		resourceProjection(items[i].Resource)
	}
	return items, nil
}
