package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierAvailabilityIdentity(t *testing.T) {
	now := time.Now()
	expires := now.Add(time.Hour)
	version := int64(1)
	resource := func() *SupplierResource {
		return &SupplierResource{Status: "paused", ConsentVersion: SupplierConsentVersion, AuthorizationVersion: 1, AuthorizationExpiresAt: &expires, TokenExpiresAt: expires, SupplyEnabled: true, CredentialVersion: 1, VerifiedCredentialVersion: &version, LastVerifiedAt: &now, ProbeState: "fresh", Usage: &SupplierResourceUsage{PhoneVerified: true, EmailMatched: true}}
	}
	for _, code := range []string{"phone_verification_required", "identity_email_mismatch", "identity_unknown"} {
		t.Run(code, func(t *testing.T) {
			r := resource()
			r.ProbeErrorCode = &code
			r.ProbeState = "reauthorization_required"
			r.SupplyEnablePending = true
			r.Usage = nil
			require.Equal(t, code, supplierAvailabilityReason(r, now))
		})
	}
	for _, usage := range []*SupplierResourceUsage{nil, {}, {PhoneVerified: true}, {EmailMatched: true}} {
		r := resource()
		r.Usage = usage
		require.Equal(t, "identity_unknown", supplierAvailabilityReason(r, now))
	}
	r := resource()
	require.Equal(t, "quota_unknown", supplierAvailabilityReason(r, now), "verified identity proceeds to quota checks")
	r.AdminDisabled = true
	require.Equal(t, "admin_disabled", supplierAvailabilityReason(r, now))
}
