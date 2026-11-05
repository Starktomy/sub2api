package service

import "context"

type SupplierResourceStats struct {
	Calls               int64  `json:"calls"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	FeeAmount           string `json:"fee_amount"`
	Currency            string `json:"currency"`
}
type supplierStatsReader interface {
	ResourceStats(context.Context, int64, string) (*SupplierResourceStats, error)
}

func (s *SupplierResourceService) Stats(ctx context.Context, user int64, id string) (*SupplierResourceStats, error) {
	p, err := s.profiles.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	if _, err = s.repo.Get(ctx, p.ID, id); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(supplierStatsReader)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	stats, err := repo.ResourceStats(ctx, p.ID, id)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	return stats, nil
}
