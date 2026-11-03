package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bedrockTestFile(t *testing.T, expiry time.Time) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root-owned credential fixture requires root")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	writeBedrockTestFile(t, path, "first", expiry)
	return path
}

func writeBedrockTestFile(t *testing.T, path, key string, expiry time.Time) {
	t.Helper()
	data, err := json.Marshal(map[string]string{"AccessKeyId": key, "SecretAccessKey": "test-secret", "SessionToken": "test-token-" + key, "Expiration": expiry.UTC().Format(time.RFC3339Nano)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
}

func credentialFileAccount() *Account {
	return &Account{Credentials: map[string]any{"aws_auth_mode": "credential_file", "aws_region": "us-west-2"}}
}

func TestBedrockCredentialFileSigningAndRollover(t *testing.T) {
	path := bedrockTestFile(t, time.Now().Add(bedrockCredentialSkew+300*time.Millisecond))
	t.Setenv(bedrockCredentialsFileEnv, path)
	signer, err := NewBedrockSignerFromAccount(credentialFileAccount())
	require.NoError(t, err)
	secondSigner, err := NewBedrockSignerFromAccount(credentialFileAccount())
	require.NoError(t, err)
	require.Same(t, signer.provider, secondSigner.provider)
	sign := func() *http.Request {
		req, e := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-west-2.amazonaws.com/model/test/invoke", nil)
		require.NoError(t, e)
		require.NoError(t, signer.SignRequest(context.Background(), req, []byte("{}")))
		return req
	}
	first := sign()
	require.Contains(t, first.Header.Get("Authorization"), "Credential=first/")
	require.Contains(t, first.Header.Get("Authorization"), "x-amz-security-token")
	require.Equal(t, "test-token-first", first.Header.Get("X-Amz-Security-Token"))
	// Atomic replacement matches an operator rotating a mounted credential file.
	writeBedrockTestFile(t, path+".next", "second", time.Now().Add(time.Hour))
	require.NoError(t, os.Rename(path+".next", path))
	require.Eventually(t, func() bool {
		req := sign()
		return strings.Contains(req.Header.Get("Authorization"), "Credential=second/") && req.Header.Get("X-Amz-Security-Token") == "test-token-second"
	}, 3*time.Second, 25*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-west-2.amazonaws.com/model/test/invoke", nil)
	require.NoError(t, err)
	require.ErrorIs(t, signer.SignRequest(ctx, req, nil), context.Canceled)
	require.Empty(t, req.Header.Get("Authorization"))
}

func TestBedrockCredentialFileValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"expired", func(t *testing.T, p string) { writeBedrockTestFile(t, p, "expired", time.Now().Add(-time.Minute)) }},
		{"skew", func(t *testing.T, p string) {
			writeBedrockTestFile(t, p, "soon", time.Now().Add(bedrockCredentialSkew-time.Second))
		}},
		{"world_readable", func(t *testing.T, p string) { require.NoError(t, os.Chmod(p, 0644)) }},
		{"group_writable", func(t *testing.T, p string) { require.NoError(t, os.Chmod(p, 0660)) }},
		{"non_root_owner", func(t *testing.T, p string) { require.NoError(t, os.Chown(p, 65534, -1)) }},
		{"symlink", func(t *testing.T, p string) {
			require.NoError(t, os.Rename(p, p+".real"))
			require.NoError(t, os.Symlink(p+".real", p))
		}},
		{"oversize", func(t *testing.T, p string) {
			require.NoError(t, os.WriteFile(p, []byte(strings.Repeat("x", bedrockCredentialMaxSize+1)), 0600))
		}},
		{"directory", func(t *testing.T, p string) { require.NoError(t, os.Remove(p)); require.NoError(t, os.Mkdir(p, 0700)) }},
		{"malformed", func(t *testing.T, p string) {
			require.NoError(t, os.WriteFile(p, []byte(`{"SecretAccessKey":"do-not-expose"`), 0600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := bedrockTestFile(t, time.Now().Add(time.Hour))
			test.change(t, path)
			_, err := (&bedrockFileCredentials{path: path}).Retrieve(context.Background())
			require.ErrorIs(t, err, errBedrockCredentials)
			require.NotContains(t, err.Error(), path)
			require.NotContains(t, err.Error(), "do-not-expose")
		})
	}
}

func TestBedrockCredentialFileStrictJSON(t *testing.T) {
	good := `{"AccessKeyId":"test","SecretAccessKey":"secret","SessionToken":"token","Expiration":"2099-01-01T00:00:00Z"}`
	for _, data := range []string{
		`{}`, `null`, `[]`, good + `{}`, strings.Replace(good, `"AccessKeyId":"test"`, `"AccessKeyId":"test","AccessKeyId":"other"`, 1),
		strings.Replace(good, `AccessKeyId`, `accesskeyid`, 1), strings.Replace(good, `"token"`, `null`, 1),
		strings.Replace(good, `"token"`, `"token\n"`, 1), strings.Replace(good, `"SessionToken":"token",`, ``, 1),
		strings.Replace(good, `"token"`, `123`, 1), strings.Replace(good, `"token"`, `""`, 1),
		strings.Replace(good, `"AccessKeyId"`, `"Extra":"x","AccessKeyId"`, 1),
	} {
		_, err := decodeBedrockCredentialFile([]byte(data))
		require.Error(t, err)
	}
	_, err := decodeBedrockCredentialFile([]byte(good))
	require.NoError(t, err)
}

func TestBedrockCredentialFileOptIn(t *testing.T) {
	t.Setenv(bedrockCredentialsFileEnv, "")
	t.Setenv("AWS_ACCESS_KEY_ID", "must-not-use")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-use")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://127.0.0.1:1/credentials")
	account := credentialFileAccount()
	_, err := NewBedrockSignerFromAccount(account)
	require.ErrorIs(t, err, errBedrockCredentials)
	account.Credentials["aws_credentials_file"] = "/any/account/chosen/path"
	_, err = NewBedrockSignerFromAccount(account)
	require.ErrorIs(t, err, errBedrockCredentials)
	t.Setenv(bedrockCredentialsFileEnv, "relative.json")
	_, err = NewBedrockSignerFromAccount(account)
	require.ErrorIs(t, err, errBedrockCredentials)
	t.Setenv(bedrockCredentialsFileEnv, "/operator/credentials.json")
	for _, region := range []string{"", "us-west-2/escape", "https://example.com", " us-west-2", "us-west-2\n"} {
		account.Credentials["aws_region"] = region
		_, err = NewBedrockSignerFromAccount(account)
		require.Error(t, err)
	}
	_, err = NewBedrockSignerFromAccount(nil)
	require.Error(t, err)
	account.Credentials["aws_auth_mode"] = "default_chain"
	_, err = NewBedrockSignerFromAccount(account)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&bedrockFileCredentials{path: "/not/read"}).Retrieve(ctx)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestBedrockCredentialFileRejectsSymlinkParent(t *testing.T) {
	path := bedrockTestFile(t, time.Now().Add(time.Hour))
	link := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(filepath.Dir(path), link))
	_, err := (&bedrockFileCredentials{path: filepath.Join(link, "credentials.json")}).Retrieve(context.Background())
	require.ErrorIs(t, err, errBedrockCredentials)
}

func TestBedrockCredentialFileGroupRead(t *testing.T) {
	path := bedrockTestFile(t, time.Now().Add(time.Hour))
	require.NoError(t, os.Chmod(path, 0440))
	require.NoError(t, os.Chown(path, 0, 10001))
	creds, err := (&bedrockFileCredentials{path: path}).Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "first", creds.AccessKeyID)
}

func TestBedrockCredentialFileRefreshFailsClosed(t *testing.T) {
	path := bedrockTestFile(t, time.Now().Add(bedrockCredentialSkew+200*time.Millisecond))
	t.Setenv(bedrockCredentialsFileEnv, path)
	signer, err := NewBedrockSignerFromAccount(credentialFileAccount())
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-west-2.amazonaws.com/model/test/invoke", nil)
	require.NoError(t, err)
	require.NoError(t, signer.SignRequest(context.Background(), req, nil))
	require.NoError(t, os.Remove(path))
	require.Eventually(t, func() bool {
		fresh, e := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-west-2.amazonaws.com/model/test/invoke", nil)
		require.NoError(t, e)
		e = signer.SignRequest(context.Background(), fresh, nil)
		if e == nil {
			return false
		}
		require.ErrorIs(t, e, errBedrockCredentials)
		require.Empty(t, fresh.Header.Get("Authorization"))
		return true
	}, 3*time.Second, 25*time.Millisecond)
}

func TestBedrockCredentialFileRejectsWritableParent(t *testing.T) {
	path := bedrockTestFile(t, time.Now().Add(time.Hour))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0777))
	_, err := (&bedrockFileCredentials{path: path}).Retrieve(context.Background())
	require.ErrorIs(t, err, errBedrockCredentials)
}
