package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierFeePolicyCalculate(t *testing.T) {
	for _, mode := range []SupplierFeeMode{SupplierFeeModeStandardCost, SupplierFeeModeCustomerCharge} {
		t.Run(string(mode), func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				ratio  int
				total  float64
				actual float64
				base   float64
				fee    float64
			}{
				{"zero_ratio", 0, 10, 10, 10, 0},
				{"zero_usage", 7000, 0, 0, 0, 0},
				{"seventy_percent", 7000, 10, 10, 10, 7},
				{"full_ratio", 10000, 10, 10, 10, 10},
				{"fee_below_half", 7000, 0.00000002, 0.00000002, 0.00000002, 0.00000001},
				{"fee_at_half", 7000, 0.00000005, 0.00000005, 0.00000005, 0.00000004},
				{"fee_above_half", 7000, 0.00000008, 0.00000008, 0.00000008, 0.00000006},
				{"base_below_half", 10000, 0.000078124, 0.000078124, 0.00007812, 0.00007812},
				{"base_at_half", 10000, 0.000078125, 0.000078125, 0.00007813, 0.00007813},
				{"normalized_base_before_ratio", 7000, 0.000000014, 0.000000014, 0.00000001, 0.00000001},
			} {
				t.Run(tc.name, func(t *testing.T) {
					policy := SupplierFeePolicy{Mode: mode, RatioBPS: tc.ratio, Version: 3}
					got, err := policy.Calculate(CostBreakdown{TotalCost: tc.total, ActualCost: tc.actual})
					require.NoError(t, err)
					require.Equal(t, SupplierFeeSnapshot{
						Mode: mode, BaseAmount: tc.base, RatioBPS: tc.ratio, Version: 3, FeeAmount: tc.fee,
					}, got)
				})
			}
		})
	}
}

func TestSupplierFeePolicySelectsBase(t *testing.T) {
	cost := CostBreakdown{TotalCost: 10, ActualCost: 2}
	policy := SupplierFeePolicy{Mode: SupplierFeeModeStandardCost, RatioBPS: 7000, Version: 1}
	standard, err := policy.Calculate(cost)
	require.NoError(t, err)
	require.Equal(t, 10.0, standard.BaseAmount)
	require.Equal(t, 7.0, standard.FeeAmount)

	policy.Mode = SupplierFeeModeCustomerCharge
	customer, err := policy.Calculate(cost)
	require.NoError(t, err)
	require.Equal(t, 2.0, customer.BaseAmount)
	require.Equal(t, 1.4, customer.FeeAmount)

	// A free customer charge does not erase the standard model-cost base.
	cost.ActualCost = 0
	free, err := policy.Calculate(cost)
	require.NoError(t, err)
	require.Zero(t, free.FeeAmount)
	policy.Mode = SupplierFeeModeStandardCost
	standard, err = policy.Calculate(cost)
	require.NoError(t, err)
	require.Equal(t, 7.0, standard.FeeAmount)
}

func TestSupplierFeePolicyRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []SupplierFeePolicy{
		{},
		{Mode: "unknown", RatioBPS: 7000, Version: 1},
		{Mode: SupplierFeeModeStandardCost, RatioBPS: -1, Version: 1},
		{Mode: SupplierFeeModeStandardCost, RatioBPS: 10001, Version: 1},
		{Mode: SupplierFeeModeStandardCost, RatioBPS: 7000, Version: 0},
		{Mode: SupplierFeeModeStandardCost, RatioBPS: 7000, Version: -1},
	} {
		require.Error(t, policy.Validate())
		got, err := policy.Calculate(CostBreakdown{TotalCost: 1, ActualCost: 1})
		require.Error(t, err)
		require.Equal(t, SupplierFeeSnapshot{}, got)
	}
}

func TestSupplierFeePolicyRejectsInvalidAmounts(t *testing.T) {
	for _, mode := range []SupplierFeeMode{SupplierFeeModeStandardCost, SupplierFeeModeCustomerCharge} {
		for _, ratio := range []int{0, 7000} {
			policy := SupplierFeePolicy{Mode: mode, RatioBPS: ratio, Version: 1}
			for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, -0.000000001} {
				for _, cost := range []CostBreakdown{
					{TotalCost: invalid, ActualCost: 1},
					{TotalCost: 1, ActualCost: invalid},
				} {
					got, err := policy.Calculate(cost)
					require.Error(t, err)
					require.Equal(t, SupplierFeeSnapshot{}, got)
				}
			}
		}
	}
}

func TestSupplierFeeSnapshotPreservesCalculation(t *testing.T) {
	policy := SupplierFeePolicy{Mode: SupplierFeeModeStandardCost, RatioBPS: 7000, Version: 5}
	cost := CostBreakdown{TotalCost: 10, ActualCost: 2}
	frozen, err := policy.Calculate(cost)
	require.NoError(t, err)

	policy.Mode = SupplierFeeModeCustomerCharge
	policy.RatioBPS = 1000
	policy.Version = 6
	cost.TotalCost = 20
	cost.ActualCost = 5
	next, err := policy.Calculate(cost)
	require.NoError(t, err)
	require.Equal(t, SupplierFeeSnapshot{
		Mode: SupplierFeeModeStandardCost, BaseAmount: 10, RatioBPS: 7000, Version: 5, FeeAmount: 7,
	}, frozen)
	require.Equal(t, SupplierFeeSnapshot{
		Mode: SupplierFeeModeCustomerCharge, BaseAmount: 5, RatioBPS: 1000, Version: 6, FeeAmount: 0.5,
	}, next)
}
