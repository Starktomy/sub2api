package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type supplierStatsProfileFake struct{ SupplierRepository }

func (supplierStatsProfileFake) GetProfile(context.Context, int64) (*SupplierProfile, error) {
	return &SupplierProfile{ID: 7}, nil
}

type supplierStatsFake struct {
	SupplierResourceRepository
	found bool
	calls int
	fail  bool
}

func (f *supplierStatsFake) Get(_ context.Context, owner int64, id string) (*SupplierResource, error) {
	if owner != 7 || id != "owned" || !f.found {
		return nil, ErrSupplierResource
	}
	return &SupplierResource{ID: id, SupplierID: owner}, nil
}
func (f *supplierStatsFake) ResourceStats(_ context.Context, owner int64, id string) (*SupplierResourceStats, error) {
	f.calls++
	if owner != 7 || id != "owned" {
		panic("owner scope lost")
	}
	if f.fail {
		return nil, errors.New("private database detail")
	}
	return &SupplierResourceStats{Calls: 3, InputTokens: 10, FeeAmount: "0.00000000", Currency: "USD"}, nil
}
func TestSupplierStatsOwnerScope(t *testing.T) {
	f := &supplierStatsFake{found: true}
	s := &SupplierResourceService{repo: f, profiles: supplierStatsProfileFake{}}
	stats, err := s.Stats(context.Background(), 42, "owned")
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.Calls)
	_, err = s.Stats(context.Background(), 42, "foreign")
	require.ErrorIs(t, err, ErrSupplierResource)
	require.Equal(t, 1, f.calls)
	f.fail = true
	_, err = s.Stats(context.Background(), 42, "owned")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.NotContains(t, err.Error(), "private")
}
