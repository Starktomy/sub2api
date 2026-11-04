package main

import (
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"testing"
)

func TestSafeImportCodeNeverIncludesSecretDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{service.ErrKiroUnavailable, "vault_unavailable"},
		{service.ErrKiroCredentialInvalid, "credential_validation"},
		{service.ErrKiroCredentialCrypto, "credential_encryption"},
		{&pq.Error{Code: "23502", Message: "secret-token", Detail: "secret-token"}, "database_not_null_constraint"},
		{errors.New("secret-token"), "local_import_unknown"},
	} {
		if got := safeImportCode(tc.err); got != tc.code {
			t.Fatalf("unexpected safe category %q", got)
		}
	}
}
func TestSafeFixtureShapeSingleObjectOnly(t *testing.T) {
	for _, tc := range []struct{ input, code string }{
		{`[{"refreshToken":"secret-token"}]`, "credential_array_not_supported"},
		{`[]`, "credential_array_not_supported"},
		{`null`, "credential_object_required"},
		{`{"refreshToken":"secret-token"}`, ""},
	} {
		if got := safeFixtureShape([]byte(tc.input)); got != tc.code {
			t.Fatalf("unexpected safe category %q", got)
		}
	}
}
