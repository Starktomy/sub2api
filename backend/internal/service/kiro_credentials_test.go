package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
	"github.com/stretchr/testify/require"
)

// Deliberately context-ignoring wrapper: codec AAD must still prevent swapping.
// All material is fixed, fake, local test data, not usable provider credentials.
type kiroTestWrapper struct {
	fail  bool
	calls int
}

func (w *kiroTestWrapper) GenerateDataKey(context.Context, string, map[string]string) ([]byte, []byte, error) {
	w.calls++
	if w.fail {
		return nil, nil, errors.New("fake-sensitive-token")
	}
	return bytes.Repeat([]byte{42}, 32), []byte("fake-wrapped-key"), nil
}
func (w *kiroTestWrapper) Decrypt(_ context.Context, id string, _ []byte, _ map[string]string) ([]byte, error) {
	w.calls++
	if w.fail || id != "test-key" {
		return nil, errors.New("fake-sensitive-token")
	}
	return bytes.Repeat([]byte{42}, 32), nil
}
func kiroTestSetup(t *testing.T) (*KiroCredentialVault, *kiroTestWrapper, KiroCredentialBinding, time.Time) {
	t.Helper()
	w := &kiroTestWrapper{}
	v, err := NewKiroCredentialVault(w, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return v, w, KiroCredentialBinding{ResourceUUID: "12345678-1234-1234-1234-123456789abc", AccountID: 7, Version: 1, Mode: KiroCredentialSocial}, time.Unix(1800000000, 0).UTC()
}
func kiroTestSecret(now time.Time, mode KiroCredentialMode) kiroSecret {
	s := kiroSecret{SchemaVersion: 1, Purpose: kiroSecretPurpose, Mode: mode, InferenceTokenType: kiroSocialAccessToken, InferenceToken: "fake-inference-token", InferenceExpiresAt: now.Add(time.Hour).Unix(), RefreshToken: "fake-refresh-token"}
	if mode == KiroCredentialIdC {
		s.InferenceTokenType = kiroIdCIdentityToken
		s.SSOAccessToken = "fake-sso-token"
		s.SSOExpiresAt = now.Add(-time.Hour).Unix() // Inactive, never used for inference.
		s.ClientID = "fake-client-id"
		s.ClientSecret = "fake-client-secret"
	}
	return s
}
func kiroTestJSON(t *testing.T, s kiroSecret) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestKiroCredentialRoundTrip(t *testing.T) {
	for _, mode := range []KiroCredentialMode{KiroCredentialSocial, KiroCredentialIdC} {
		t.Run(string(mode), func(t *testing.T) {
			v, _, binding, now := kiroTestSetup(t)
			binding.Mode = mode
			s := kiroTestSecret(now, mode)
			encrypted, metadata, err := v.Import(context.Background(), binding, kiroTestJSON(t, s), now)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encrypted, []byte("fake-")) || bytes.Contains(encrypted, []byte("refresh_token")) {
				t.Fatal("plaintext in encrypted envelope")
			}
			if metadata.Mode != mode || metadata.SchemaVersion != 1 || !metadata.HasInactiveRefresh || !metadata.InferenceExpiresAt.Equal(now.Add(time.Hour)) {
				t.Fatal("incorrect safe metadata")
			}
			safe, _ := json.Marshal(metadata)
			if bytes.Contains(safe, []byte("fake-")) {
				t.Fatal("metadata exposes secret")
			}
			got, err := v.DecryptInference(context.Background(), binding, encrypted, now)
			if err != nil || got.BearerToken() != s.InferenceToken || !got.ExpiresAt().Equal(metadata.InferenceExpiresAt) {
				t.Fatalf("roundtrip failed: %v", err)
			}
			for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
				if strings.Contains(fmt.Sprintf(format, got), "fake-") || strings.Contains(fmt.Sprintf(format, &got), "fake-") {
					t.Fatal("formatted credential leaks")
				}
			}
			if data, err := json.Marshal(got); err == nil || len(data) != 0 {
				t.Fatal("secret JSON API exposed")
			}
			if data, err := json.Marshal(&got); err == nil || len(data) != 0 {
				t.Fatal("pointer JSON API exposed")
			}
		})
	}
}
func TestKiroCredentialBindingAndTamper(t *testing.T) {
	v, w, b, now := kiroTestSetup(t)
	encrypted, _, err := v.Import(context.Background(), b, kiroTestJSON(t, kiroTestSecret(now, b.Mode)), now)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]KiroCredentialBinding{}
	other := b
	other.AccountID++
	cases["account"] = other
	other = b
	other.Version++
	cases["credential-version"] = other
	other = b
	other.ResourceUUID = "22345678-1234-1234-1234-123456789abc"
	cases["resource"] = other
	other = b
	other.Mode = KiroCredentialIdC
	cases["mode"] = other
	for name, binding := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := v.DecryptInference(context.Background(), binding, encrypted, now)
			if err == nil || got.BearerToken() != "" {
				t.Fatal("context swap accepted")
			}
		})
	}
	// Independently encrypted second account cannot be substituted either.
	second := b
	second.AccountID++
	swapped, _, err := v.Import(context.Background(), second, kiroTestJSON(t, kiroTestSecret(now, b.Mode)), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.DecryptInference(context.Background(), b, swapped, now); err == nil {
		t.Fatal("swapped envelope accepted")
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(encrypted, &env) != nil {
		t.Fatal("invalid envelope")
	}
	for _, field := range []string{"ciphertext", "nonce", "encrypted_data_key"} {
		t.Run(field, func(t *testing.T) {
			var value []byte
			if json.Unmarshal(env[field], &value) != nil {
				t.Fatal("bad bytes")
			}
			value[0] ^= 1
			copyEnv := make(map[string]json.RawMessage)
			for k, v := range env {
				copyEnv[k] = v
			}
			copyEnv[field], _ = json.Marshal(value)
			data, _ := json.Marshal(copyEnv)
			if got, err := v.DecryptInference(context.Background(), b, data, now); err == nil || got.BearerToken() != "" {
				t.Fatal("tamper accepted")
			}
		})
	}
	wrong, _ := NewKiroCredentialVault(w, "wrong-key")
	if _, err := wrong.DecryptInference(context.Background(), b, encrypted, now); err == nil {
		t.Fatal("wrong key accepted")
	}
	env["version"] = json.RawMessage("2")
	data, _ := json.Marshal(env)
	if _, err := v.DecryptInference(context.Background(), b, data, now); err == nil {
		t.Fatal("wrong envelope version accepted")
	}
}
func TestKiroCredentialStrictSchema(t *testing.T) {
	v, w, b, now := kiroTestSetup(t)
	valid := kiroTestJSON(t, kiroTestSecret(now, b.Mode))
	cases := map[string][]byte{
		"duplicate":  bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"unknown":    bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"unknown":"fake-sensitive-token","schema_version":1`), 1),
		"case-alias": bytes.Replace(valid, []byte(`"mode"`), []byte(`"Mode"`), 1),
		"trailing":   append(bytes.Clone(valid), []byte(` {}`)...),
		"null":       bytes.Replace(valid, []byte(`"inference_token":"fake-inference-token"`), []byte(`"inference_token":null`), 1),
		"nested":     bytes.Replace(valid, []byte(`"inference_token":"fake-inference-token"`), []byte(`"inference_token":{"a":1,"a":2}`), 1),
		"empty":      nil, "array": []byte(`[]`), "null-document": []byte(`null`), "truncated": valid[:len(valid)-1],
		"oversize": bytes.Repeat([]byte(" "), kiroMaxSecretBytes+1), "bad-utf8": {255},
	}
	mutations := map[string]func(*kiroSecret){
		"version":          func(s *kiroSecret) { s.SchemaVersion = 2 },
		"purpose":          func(s *kiroSecret) { s.Purpose = "other" },
		"mode":             func(s *kiroSecret) { s.Mode = "external_idp" },
		"type":             func(s *kiroSecret) { s.InferenceTokenType = kiroIdCIdentityToken },
		"missing-token":    func(s *kiroSecret) { s.InferenceToken = "" },
		"long-token":       func(s *kiroSecret) { s.InferenceToken = strings.Repeat("x", kiroMaxTokenBytes+1) },
		"header-injection": func(s *kiroSecret) { s.InferenceToken = "fake\r\nheader" },
		"whitespace":       func(s *kiroSecret) { s.InferenceToken = " fake" },
		"expiry-missing":   func(s *kiroSecret) { s.InferenceExpiresAt = 0 },
		"expiry-range":     func(s *kiroSecret) { s.InferenceExpiresAt = 253402300800 },
		"social-sso":       func(s *kiroSecret) { s.SSOAccessToken = "fake-sso"; s.SSOExpiresAt = now.Unix() },
		"social-client":    func(s *kiroSecret) { s.ClientID = "fake-client"; s.ClientSecret = "fake-client-secret" },
	}
	for name, mutate := range mutations {
		s := kiroTestSecret(now, b.Mode)
		mutate(&s)
		cases[name] = kiroTestJSON(t, s)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			before := w.calls
			encrypted, metadata, err := v.Import(context.Background(), b, data, now)
			if err == nil || encrypted != nil || metadata != (KiroCredentialMetadata{}) || before != w.calls {
				t.Fatal("malformed input accepted or reached wrapper")
			}
			if strings.Contains(err.Error(), "fake-") {
				t.Fatal("secret in error")
			}
		})
	}
}
func TestKiroCredentialIdCNoninterchangeability(t *testing.T) {
	v, _, b, now := kiroTestSetup(t)
	b.Mode = KiroCredentialIdC
	for _, mutation := range []func(*kiroSecret){
		func(s *kiroSecret) { s.InferenceTokenType = kiroSocialAccessToken },
		func(s *kiroSecret) { s.InferenceTokenType = "sso_access_token" },
		func(s *kiroSecret) { s.InferenceToken = "" },
		func(s *kiroSecret) { s.InferenceToken = s.SSOAccessToken },
		func(s *kiroSecret) { s.ClientSecret = "" },
		func(s *kiroSecret) { s.SSOExpiresAt = 0 },
	} {
		s := kiroTestSecret(now, b.Mode)
		mutation(&s)
		if data, _, err := v.Import(context.Background(), b, kiroTestJSON(t, s), now); err == nil || data != nil {
			t.Fatal("invalid IdC material accepted")
		}
	}
	// Opaque JWT-shaped text does not create authorization or derive expiry.
	s := kiroTestSecret(now, b.Mode)
	s.InferenceToken = "eyJhbGciOiJub25lIn0.eyJleHAiOjF9.fake"
	s.InferenceExpiresAt = 0
	if _, _, err := v.Import(context.Background(), b, kiroTestJSON(t, s), now); err == nil {
		t.Fatal("JWT claims substituted for explicit expiry")
	}
}
func TestKiroCredentialExpiry(t *testing.T) {
	v, _, b, now := kiroTestSetup(t)
	input := kiroTestJSON(t, kiroTestSecret(now, b.Mode))
	encrypted, _, err := v.Import(context.Background(), b, input, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, clock := range []time.Time{now.Add(time.Hour), now.Add(2 * time.Hour)} {
		if got, err := v.DecryptInference(context.Background(), b, encrypted, clock); !errors.Is(err, ErrKiroCredentialExpired) || got.BearerToken() != "" {
			t.Fatal("expired credential selected")
		}
		if data, _, err := v.Import(context.Background(), b, input, clock); !errors.Is(err, ErrKiroCredentialExpired) || data != nil {
			t.Fatal("expired credential imported")
		}
	}
	if _, _, err := v.Import(context.Background(), b, input, time.Time{}); err == nil {
		t.Fatal("zero clock accepted")
	}
}
func TestKiroCredentialConfigurationAndFailures(t *testing.T) {
	v, w, b, now := kiroTestSetup(t)
	input := kiroTestJSON(t, kiroTestSecret(now, b.Mode))
	var typedNil *kiroTestWrapper
	for _, wrapper := range []credentialcrypto.KMS{nil, typedNil} {
		if _, err := NewKiroCredentialVault(wrapper, "test-key"); !errors.Is(err, ErrKiroCredentialConfiguration) {
			t.Fatal("missing wrapper accepted")
		}
	}
	if _, err := NewKiroCredentialVault(w, ""); !errors.Is(err, ErrKiroCredentialConfiguration) {
		t.Fatal("missing key accepted")
	}
	for _, vault := range []*KiroCredentialVault{nil, {}} {
		if _, _, err := vault.Import(context.Background(), b, input, now); !errors.Is(err, ErrKiroCredentialConfiguration) {
			t.Fatal("unconfigured import accepted")
		}
		if _, err := vault.DecryptInference(context.Background(), b, nil, now); !errors.Is(err, ErrKiroCredentialConfiguration) {
			t.Fatal("unconfigured decrypt accepted")
		}
	}
	for _, mutate := range []func(*KiroCredentialBinding){
		func(b *KiroCredentialBinding) { b.AccountID = 0 }, func(b *KiroCredentialBinding) { b.Version = 0 }, func(b *KiroCredentialBinding) { b.Mode = "external" }, func(b *KiroCredentialBinding) { b.ResourceUUID = "bad" }, func(b *KiroCredentialBinding) { b.ResourceUUID = "12345678_1234-1234-1234-123456789abc" },
	} {
		bad := b
		mutate(&bad)
		if _, _, err := v.Import(context.Background(), bad, input, now); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	encrypted, _, err := v.Import(context.Background(), b, input, now)
	if err != nil {
		t.Fatal(err)
	}
	w.fail = true
	if data, _, err := v.Import(context.Background(), b, input, now); !errors.Is(err, ErrKiroCredentialCrypto) || data != nil || strings.Contains(err.Error(), "fake-") {
		t.Fatal("unsafe encrypt failure")
	}
	if got, err := v.DecryptInference(context.Background(), b, encrypted, now); !errors.Is(err, ErrKiroCredentialCrypto) || got.BearerToken() != "" || strings.Contains(err.Error(), "fake-") {
		t.Fatal("unsafe decrypt failure")
	}
	w.fail = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := v.Import(ctx, b, input, now); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled import accepted")
	}
	if _, err := v.DecryptInference(ctx, b, encrypted, now); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled decrypt accepted")
	}
	if _, err := v.DecryptInference(context.Background(), b, input, now); err == nil {
		t.Fatal("plaintext fallback")
	}
}
func TestKiroCredentialRevalidateDecryptedSchema(t *testing.T) {
	v, _, b, now := kiroTestSetup(t)
	identity, err := kiroCredentialIdentity(b)
	if err != nil {
		t.Fatal(err)
	}
	valid := kiroTestJSON(t, kiroTestSecret(now, b.Mode))
	invalid := bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	wrongPurpose := kiroTestSecret(now, b.Mode)
	wrongPurpose.Purpose = "other"
	wrongMode := kiroTestSecret(now, KiroCredentialIdC)
	for _, document := range []map[string]any{
		{"kiro_secret": string(invalid)}, {"kiro_secret": string(kiroTestJSON(t, wrongPurpose))}, {"kiro_secret": string(kiroTestJSON(t, wrongMode))},
		{"kiro_secret": string(valid), "unknown": "fake-secret"}, {"kiro_secret": 1}, {"access_token": "fake-plaintext"},
	} {
		encrypted, err := v.codec.Encrypt(context.Background(), identity, document)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := v.DecryptInference(context.Background(), b, encrypted, now); err == nil || got.BearerToken() != "" {
			t.Fatal("decrypted schema not revalidated")
		}
	}
}

func TestKiroCredentialKiroRSImport(t *testing.T) {
	v, _, binding, now := kiroTestSetup(t)
	for _, mode := range []KiroCredentialMode{KiroCredentialSocial, KiroCredentialIdC} {
		binding.Mode = mode
		for _, refreshOnly := range []bool{false, true} {
			input := map[string]any{"authMethod": string(mode), "refreshToken": "fake-rs-refresh", "expiresAt": now.Add(time.Hour).Format(time.RFC3339Nano), "machineId": "fake-ignored-machine", "profileArn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/Synthetic", "priority": 2, "region": "us-east-1"}
			if !refreshOnly {
				input["accessToken"] = "fake-rs-access"
			} else {
				input["expiresAt"] = "2020-01-01T00:00:00Z"
			}
			if mode == KiroCredentialIdC {
				input["clientId"] = "fake-rs-client"
				input["clientSecret"] = "fake-rs-secret"
			}
			data, _ := json.Marshal(input)
			encrypted, metadata, err := v.ImportKiroRS(context.Background(), binding, data, now)
			if err != nil || metadata.RefreshRequired != refreshOnly {
				t.Fatalf("import failed: %v", err)
			}
			got, err := v.DecryptInference(context.Background(), binding, encrypted, now)
			if refreshOnly {
				if !errors.Is(err, ErrKiroCredentialRefreshRequired) || got.BearerToken() != "" {
					t.Fatal("refresh-only became executor auth")
				}
			} else if err != nil || got.BearerToken() != "fake-rs-access" {
				t.Fatal("explicit access import failed")
			}
		}
	}
	binding.Mode = KiroCredentialSocial
	for _, data := range []string{
		`[{"authMethod":"social","refreshToken":"fake-rs-refresh"}]`,
		`{"authMethod":"social","authMethod":"social","refreshToken":"fake-rs-refresh"}`,
		`{"authMethod":"social","refreshToken":"fake-rs-refresh","unknownSecret":"fake"}`,
		`{"authMethod":"api_key","kiroApiKey":"fake-api-key"}`,
		`{"authMethod":"social","refreshToken":"fake-rs-refresh","endpoint":"custom"}`,
		`{"authMethod":"social","refreshToken":"fake-rs-refresh","proxyUrl":"custom"}`,
		`{"authMethod":"social","refreshToken":"fake-rs-refresh","disabled":true}`,
		`{"authMethod":"social","accessToken":"fake-rs-access"}`,
		`{"authMethod":"social","refreshToken":"fake-rs-refresh","machineId":42}`,
	} {
		if encrypted, _, err := v.ImportKiroRS(context.Background(), binding, []byte(data), now); err == nil || encrypted != nil {
			t.Fatal("invalid/unsupported reference input accepted")
		}
	}
}

func TestKiroCredentialKiroRSExplicitIdCBinding(t *testing.T) {
	v, _, binding, now := kiroTestSetup(t)
	binding.Mode = KiroCredentialIdC
	// Shape only; no real fixture is read by this test.
	input := []byte(`{"clientId":"fake-client","clientSecret":"fake-client-secret","email":"fake@example.invalid","provider":"opaque-provider-metadata","refreshToken":"fake-refresh"}`)
	encrypted, metadata, err := v.ImportKiroRS(context.Background(), binding, input, now)
	if err != nil || !metadata.RefreshRequired || !metadata.InferenceExpiresAt.IsZero() {
		t.Fatalf("explicit IdC binding import failed: %v", err)
	}
	if got, err := v.DecryptInference(context.Background(), binding, encrypted, now); !errors.Is(err, ErrKiroCredentialRefreshRequired) || got.BearerToken() != "" {
		t.Fatal("inactive IdC export became runnable")
	}
	binding.Mode = KiroCredentialSocial
	if _, _, err := v.ImportKiroRS(context.Background(), binding, input, now); err == nil {
		t.Fatal("provider inferred social authorization")
	}
}

func TestKiroProfileARNEncryptedImportAndLegacyCompatibility(t *testing.T) {
	v, _, b, now := kiroTestSetup(t)
	b.Mode = KiroCredentialIdC
	secret := kiroTestSecret(now, b.Mode)
	legacy, _, err := v.Import(context.Background(), b, kiroTestJSON(t, secret), now)
	require.NoError(t, err)
	_, err = v.DecryptInference(context.Background(), b, legacy, now)
	require.NoError(t, err)
	arn := "arn:aws:codewhisperer:us-east-1:123456789012:profile/SyntheticProfile"
	input := []byte(`{"authMethod":"idc","refreshToken":"fake-refresh","clientId":"fake-client","clientSecret":"fake-secret","region":"us-east-1","profileArn":"` + arn + `"}`)
	env, _, err := v.ImportKiroRS(context.Background(), b, input, now)
	require.NoError(t, err)
	require.NotContains(t, string(env), arn)
	identity, err := kiroCredentialIdentity(b)
	require.NoError(t, err)
	doc, err := v.codec.Decrypt(context.Background(), identity, env)
	require.NoError(t, err)
	text := doc["kiro_secret"].(string)
	parsed, err := parseKiroSecret([]byte(text), b.Mode, now)
	require.NoError(t, err)
	require.Equal(t, arn, parsed.ProfileARN)
	for _, value := range []string{"arn:aws:test", arn + "\r\n", strings.Repeat("x", 513), "arn:aws:codewhisperer:us-east-1:123:profile/X", "arn:aws:codewhisperer:us-east-1:123456789012:profile/../bad"} {
		require.False(t, kiroValidProfileARN(value, "us-east-1"))
	}
	require.False(t, kiroValidProfileARN(arn, "us-west-2"))
	conflicting := bytes.ReplaceAll(input, []byte(`"region":"us-east-1"`), []byte(`"region":"us-west-2"`))
	_, _, err = v.ImportKiroRS(context.Background(), b, conflicting, now)
	require.ErrorIs(t, err, ErrKiroCredentialInvalid)
}
