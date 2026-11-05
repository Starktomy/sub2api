package codexsession

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierSessionEmailConsistency(t *testing.T) {
	for _, tc := range []struct {
		claims, declared string
		valid            bool
	}{
		{`"https://api.openai.com/profile":{"email":"u@example.test"}`, `"email":"U@example.test"`, true},
		{`"email":"u@example.test"`, `"user":{"email":"other@example.test"}`, false},
		{`"email":"u@example.test","https://api.openai.com/profile":{"email":"other@example.test"}`, `"email":"u@example.test"`, false},
		{`"email":"u@example.test"`, `"email":"u@example.test","user":{"email":"other@example.test"}`, false},
	} {
		payload := fmt.Sprintf(`{"exp":%d,%s,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`, time.Now().Add(time.Hour).Unix(), tc.claims)
		token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
		s, err := ParseSupplier(`{"access_token":"` + token + `",` + tc.declared + `}`)
		if tc.valid {
			require.NoError(t, err)
			require.Equal(t, "U@example.test", s.Email)
		} else {
			require.ErrorIs(t, err, ErrSession)
		}
	}
}

func TestSupplierSessionParser(t *testing.T) {
	payload := fmt.Sprintf(`{"exp":%d,"email":"u@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"account-1","chatgpt_user_id":"user-1","chatgpt_plan_type":"plus"}}`, time.Now().Add(time.Hour).Unix())
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
	input := `{"accessToken":"` + token + `","refresh_token":"ignored"}`
	s, e := ParseSupplier(input)
	require.NoError(t, e)
	require.Equal(t, "account-1", s.AccountID)
	full := `{"accessToken":"` + token + `","account":{"id":"account-1"},"user":{"email":"u@example.test","mfa":true},"sessionToken":"discard-browser-secret","statsigContext":{"ip":"discard"}}`
	parsed, err := ParseSupplier(full)
	require.NoError(t, err)
	require.Equal(t, token, parsed.AccessToken)
	require.Equal(t, "account-1", parsed.AccountID)
	for _, bad := range []string{`[]`, `{"accessToken":"bad"}`, strings.Repeat("x", (1<<20)+1), `{"accessToken":"` + token + `","cookie":"bad"}`, `{"accessToken":"` + token + `","user":{"password":"bad"}}`} {
		_, e = ParseSupplier(bad)
		require.Error(t, e)
	}
}
