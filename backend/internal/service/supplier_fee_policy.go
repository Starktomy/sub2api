package service

import (
	"fmt"
	"math"

	"github.com/shopspring/decimal"
)

// SupplierFeeMode selects the usage amount on which the supplier fee is based.
type SupplierFeeMode string

const (
	SupplierFeeModeStandardCost   SupplierFeeMode = "standard_cost"
	SupplierFeeModeCustomerCharge SupplierFeeMode = "customer_charge"
)

// SupplierFeePolicy is an explicitly selected, versioned calculation policy.
// Its zero value is invalid; it grants no financial authorization and supplies no
// default policy. RatioBPS is a fraction in basis points (7000 means 0.7).
type SupplierFeePolicy struct {
	Mode     SupplierFeeMode `json:"mode"`
	RatioBPS int             `json:"ratio_bps"`
	Version  int64           `json:"version"`
}

// SupplierFeeSnapshot freezes the calculation inputs and result by value, with
// no references to a mutable policy or usage record. Later policy changes must
// not be applied to this historical result. This is not a ledger or permission
// to credit, settle, or withdraw funds.
type SupplierFeeSnapshot struct {
	Mode       SupplierFeeMode `json:"mode"`
	BaseAmount float64         `json:"base_amount"`
	RatioBPS   int             `json:"ratio_bps"`
	Version    int64           `json:"version"`
	FeeAmount  float64         `json:"fee_amount"`
}

func (p SupplierFeePolicy) Validate() error {
	switch p.Mode {
	case SupplierFeeModeStandardCost, SupplierFeeModeCustomerCharge:
	default:
		return fmt.Errorf("supplier fee policy: unsupported mode %q", p.Mode)
	}
	if p.RatioBPS < 0 || p.RatioBPS > 10000 {
		return fmt.Errorf("supplier fee policy: ratio_bps must be between 0 and 10000")
	}
	if p.Version < 1 {
		return fmt.Errorf("supplier fee policy: version must be at least 1")
	}
	return nil
}

// Calculate is a pure calculation, not a financial transition. Both usage
// amounts must be finite and nonnegative, even if the mode selects only one.
// The selected base is first normalized with the existing billing quantizer;
// its product with the exact decimal BPS ratio is then rounded to eight places
// (half away from zero). The snapshot preserves that normalized base.
func (p SupplierFeePolicy) Calculate(cost CostBreakdown) (SupplierFeeSnapshot, error) {
	if err := p.Validate(); err != nil {
		return SupplierFeeSnapshot{}, err
	}
	if math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
		return SupplierFeeSnapshot{}, fmt.Errorf("supplier fee policy: total_cost must be finite and nonnegative")
	}
	if math.IsNaN(cost.ActualCost) || math.IsInf(cost.ActualCost, 0) || cost.ActualCost < 0 {
		return SupplierFeeSnapshot{}, fmt.Errorf("supplier fee policy: actual_cost must be finite and nonnegative")
	}

	base := cost.TotalCost
	if p.Mode == SupplierFeeModeCustomerCharge {
		base = cost.ActualCost
	}
	base = QuantizeUsageBillingAmount(base)
	fee, _ := decimal.NewFromFloat(base).
		Mul(decimal.NewFromInt(int64(p.RatioBPS))).
		Shift(-4).
		Round(UsageBillingMonetaryScale).Float64()

	return SupplierFeeSnapshot{
		Mode:       p.Mode,
		BaseAmount: base,
		RatioBPS:   p.RatioBPS,
		Version:    p.Version,
		FeeAmount:  QuantizeUsageBillingAmount(fee),
	}, nil
}
