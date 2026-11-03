package codexsession

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierSessionParser(t *testing.T) {
	payload := fmt.Sprintf(`{"exp":%d,"email":"u@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"account-1","chatgpt_user_id":"user-1","chatgpt_plan_type":"plus"}}`, time.Now().Add(time.Hour).Unix())
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".synthetic"
	input := `{"accessToken":"` + token + `","refresh_token":"ignored"}`
	s, e := ParseSupplier(input)
	require.NoError(t, e)
	require.Equal(t, "account-1", s.AccountID)
	for _, bad := range []string{`[]`, `{"accessToken":"bad"}`, strings.Repeat("x", (1<<20)+1), `{"accessToken":"` + token + `","cookie":"bad"}`, `{"accessToken":"` + token + `","user":{"password":"bad"}}`} {
		_, e = ParseSupplier(bad)
		require.Error(t, e)
	}
}
