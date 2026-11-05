package service

import "testing"

func TestSupplierOperatingPolicyValidation(t *testing.T) {
	for _, p := range []SupplierOperatingPolicy{
		{FeeMode: "standard_cost", FeeRatioBPS: 7000},
		{FeeMode: "customer_charge", FeeRatioBPS: 10000, GroupIDs: []int64{1}},
	} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []SupplierOperatingPolicy{
		{FeeMode: "unknown"},
		{FeeMode: "standard_cost", FeeRatioBPS: 10001},
		{FeeMode: "standard_cost", GroupIDs: []int64{1, 1}},
		{FeeMode: "standard_cost", AutoAdmission: true},
	} {
		if p.Validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
