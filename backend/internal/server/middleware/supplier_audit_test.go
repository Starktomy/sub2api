package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierEnrollmentAuditOmitsVerificationProof(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/supplier/enroll")
}

func TestSupplierResourceImportAuditOmitsCredentialDocument(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/supplier/resources/import")
}

func TestSupplierSupplyControlAuditActions(t *testing.T) {
	for route, action := range map[string]string{
		"POST /api/v1/supplier/resources/import":                  "supplier.authorization.accept",
		"POST /api/v1/supplier/resources/:id/start":               "supplier.supply.start",
		"POST /api/v1/supplier/resources/:id/pause":               "supplier.supply.pause",
		"POST /api/v1/supplier/resources/:id/disconnect":          "supplier.authorization.revoke",
		"PUT /api/v1/admin/supplier/resources/:id/supply-control": "admin.supplier.supply_control",
	} {
		require.Equal(t, action, auditActionOverrides[route])
	}
	require.Contains(t, auditBodyOmittedRoutes, "POST /api/v1/supplier/resources/import")
}
