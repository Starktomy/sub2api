package service

import (
	"context"
	"encoding/json"
	"errors"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const supplierSettingsKey = "supplier_operating_policy_v1"

var ErrSupplierPolicyVersion = apperrors.Conflict("SUPPLIER_POLICY_VERSION_CONFLICT", "Reload supplier settings before editing")

// SupplierOperatingPolicy is configuration, not permission to bypass eligibility
// checks or create a customer/supplier monetary balance.
type SupplierOperatingPolicy struct {
	Version       int64   `json:"version"`
	AutoAdmission bool    `json:"auto_admission"`
	GroupIDs      []int64 `json:"group_ids"`
	FeeMode       string  `json:"fee_mode"`
	FeeRatioBPS   int     `json:"fee_ratio_bps"`
}

func (p SupplierOperatingPolicy) Validate() error {
	if p.FeeMode != string(SupplierFeeModeStandardCost) && p.FeeMode != string(SupplierFeeModeCustomerCharge) {
		return errors.New("invalid supplier fee mode")
	}
	if p.Version < 0 || p.Version == int64(9223372036854775807) {
		return errors.New("invalid supplier policy version")
	}
	if p.FeeRatioBPS < 0 || p.FeeRatioBPS > 10000 || len(p.GroupIDs) > 100 {
		return errors.New("invalid supplier policy limits")
	}
	seen := map[int64]bool{}
	for _, id := range p.GroupIDs {
		if id <= 0 || seen[id] {
			return errors.New("invalid supplier group")
		}
		seen[id] = true
	}
	if p.AutoAdmission && len(p.GroupIDs) == 0 {
		return errors.New("supplier admission requires target groups")
	}
	return nil
}
func (s *SettingService) GetSupplierOperatingPolicy(ctx context.Context) (*SupplierOperatingPolicy, error) {
	raw, err := s.settingRepo.GetValue(ctx, supplierSettingsKey)
	if errors.Is(err, ErrSettingNotFound) {
		return &SupplierOperatingPolicy{GroupIDs: []int64{}, FeeMode: "standard_cost"}, nil
	}
	if err != nil {
		return nil, err
	}
	var p SupplierOperatingPolicy
	if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() != nil {
		return nil, ErrServiceUnavailable
	}
	return &p, nil
}
func (s *SettingService) SetSupplierOperatingPolicy(ctx context.Context, p SupplierOperatingPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for _, id := range p.GroupIDs {
		if s.defaultSubGroupReader == nil {
			return ErrServiceUnavailable
		}
		g, err := s.defaultSubGroupReader.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if g == nil || g.Status != StatusActive || (g.Platform != PlatformOpenAI && g.Platform != PlatformComposite) {
			return errors.New("supplier target must be an active OpenAI or composite group")
		}
	}
	current, err := s.GetSupplierOperatingPolicy(ctx)
	if err != nil {
		return err
	}
	if current.Version != p.Version {
		return ErrSupplierPolicyVersion
	}
	p.Version++
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	old, err := s.settingRepo.GetValue(ctx, supplierSettingsKey)
	if errors.Is(err, ErrSettingNotFound) {
		old = ""
	} else if err != nil {
		return err
	}
	var oldPolicy SupplierOperatingPolicy
	if old != "" && (json.Unmarshal([]byte(old), &oldPolicy) != nil || oldPolicy.Version != current.Version) {
		return ErrSupplierPolicyVersion
	}
	cas, ok := s.settingRepo.(interface {
		CompareAndSetSupplierPolicy(context.Context, string, string) error
	})
	if !ok {
		return ErrServiceUnavailable
	}
	return cas.CompareAndSetSupplierPolicy(ctx, old, string(raw))
}
