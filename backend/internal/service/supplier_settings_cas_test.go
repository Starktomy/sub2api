package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type supplierPolicyCASStub struct {
	SettingRepository
	raw      string
	conflict bool
}

func (r *supplierPolicyCASStub) GetValue(context.Context, string) (string, error) {
	if r.raw == "" {
		return "", ErrSettingNotFound
	}
	return r.raw, nil
}
func (r *supplierPolicyCASStub) CompareAndSetSupplierPolicy(_ context.Context, old, new string) error {
	if r.conflict || r.raw != old {
		return ErrSupplierPolicyVersion
	}
	r.raw = new
	return nil
}

type supplierPolicyGroups struct{}

func (supplierPolicyGroups) GetByID(context.Context, int64) (*Group, error) {
	return &Group{Status: StatusActive, Platform: PlatformOpenAI}, nil
}
func TestSupplierPolicyVersionCAS(t *testing.T) {
	ctx := context.Background()
	r := &supplierPolicyCASStub{}
	s := &SettingService{settingRepo: r, defaultSubGroupReader: supplierPolicyGroups{}}
	p, err := s.GetSupplierOperatingPolicy(ctx)
	require.NoError(t, err)
	require.Zero(t, p.Version)
	require.False(t, p.AutoAdmission)
	p.AutoAdmission = true
	p.GroupIDs = []int64{1}
	p.FeeRatioBPS = 0
	require.NoError(t, s.SetSupplierOperatingPolicy(ctx, *p))
	current, err := s.GetSupplierOperatingPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), current.Version)
	require.True(t, current.AutoAdmission)
	require.ErrorIs(t, s.SetSupplierOperatingPolicy(ctx, *p), ErrSupplierPolicyVersion)
	r.conflict = true
	require.ErrorIs(t, s.SetSupplierOperatingPolicy(ctx, *current), ErrSupplierPolicyVersion)
	after, err := s.GetSupplierOperatingPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), after.Version)
}
