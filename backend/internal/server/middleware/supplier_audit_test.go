package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierEnrollmentAuditOmitsVerificationProof(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/supplier/enroll")
}
