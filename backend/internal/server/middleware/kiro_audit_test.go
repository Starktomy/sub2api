package middleware

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKiroAuditCredentialBodiesOmitted(t *testing.T) {
	for _, route := range []string{"POST /api/v1/admin/accounts/kiro/import", "PATCH /api/v1/admin/accounts/:id/kiro/runtime"} {
		_, ok := auditBodyOmittedRoutes[route]
		require.True(t, ok)
	}
	require.Equal(t, "admin.kiro.runtime.read", auditSensitiveReads["GET /api/v1/admin/accounts/:id/kiro/runtime"])
}
