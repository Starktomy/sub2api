package handler

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierResourceDTOHasNoCredentialsOrUpstreamIdentity(t *testing.T) {
	r := service.SupplierResource{ID: "uuid", Envelope: []byte("secret-canary"), KeyID: "key-canary", AccountHint: "upstream-id-canary", TokenFingerprint: "fingerprint-canary", IdentityFingerprint: "identity-canary", MaskedEmail: "u***@example.test"}
	b, e := json.Marshal(r)
	require.NoError(t, e)
	for _, canary := range []string{"secret-canary", "key-canary", "upstream-id-canary", "fingerprint-canary", "identity-canary", "access_token", "refresh_token"} {
		require.NotContains(t, string(b), canary)
	}
}
