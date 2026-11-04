package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
)

const (
	kiroSecretVersion  = 1
	kiroSecretPurpose  = "sub2api/kiro-inference-credentials"
	kiroMaxSecretBytes = 64 << 10
	kiroMaxTokenBytes  = 16 << 10
)

type KiroCredentialMode string

const (
	KiroCredentialSocial KiroCredentialMode = "social"
	KiroCredentialIdC    KiroCredentialMode = "idc"
)

type kiroInferenceTokenType string

const (
	kiroSocialAccessToken kiroInferenceTokenType = "social_access_token"
	kiroIdCIdentityToken  kiroInferenceTokenType = "idc_id_token"
	kiroIdCAccessToken    kiroInferenceTokenType = "idc_access_token"
	kiroRefreshOnly       kiroInferenceTokenType = "refresh_only"
)

var (
	ErrKiroCredentialRefreshRequired = errors.New("kiro credentials: refresh required before inference")
	ErrKiroCredentialUnsupported     = errors.New("kiro credentials: unsupported import mode or field")
	ErrKiroCredentialConfiguration   = errors.New("kiro credentials: missing or invalid encryption configuration")
	ErrKiroCredentialInvalid         = errors.New("kiro credentials: invalid secret or binding")
	ErrKiroCredentialExpired         = errors.New("kiro credentials: inference token expired")
	ErrKiroCredentialCrypto          = errors.New("kiro credentials: encryption operation failed")
)

// KiroCredentialBinding must come from trusted resource/account state, never from
// the supplied secret or encrypted envelope. Both identities and the credential
// version are authenticated. This boundary performs no storage or account lookup.
type KiroCredentialBinding struct {
	ResourceUUID string
	AccountID    int64
	Version      int64
	Mode         KiroCredentialMode
}

// KiroCredentialMetadata is safe to persist separately from the envelope. It is
// descriptive, not authorization; DecryptInference revalidates the encrypted data.
type KiroCredentialMetadata struct {
	SchemaVersion      int
	Mode               KiroCredentialMode
	InferenceExpiresAt time.Time
	HasInactiveRefresh bool
	RefreshRequired    bool
}

// KiroInferenceCredentials is transient executor-only material. It deliberately
// contains neither an SSO token nor refresh/client secrets. Do not retain it in
// account snapshots. Go strings cannot guarantee memory erasure.
type KiroInferenceCredentials struct {
	bearer    string
	expiresAt time.Time
}

func (KiroInferenceCredentials) String() string               { return "[redacted Kiro inference credentials]" }
func (KiroInferenceCredentials) GoString() string             { return "[redacted Kiro inference credentials]" }
func (KiroInferenceCredentials) MarshalJSON() ([]byte, error) { return nil, ErrKiroCredentialInvalid }

// BearerToken is an intentional secret access point for the executor caller only.
func (c KiroInferenceCredentials) BearerToken() string  { return c.bearer }
func (c KiroInferenceCredentials) ExpiresAt() time.Time { return c.expiresAt }

// kiroSecret is the versioned import schema. No exported secret-bearing DTO or
// JSON marshaling API exists. IdC inference and SSO/control-plane tokens are
// separate, explicit fields; no JWT claims are inspected to assign authority.
type kiroSecret struct {
	SchemaVersion      int                    `json:"schema_version"`
	Purpose            string                 `json:"purpose"`
	Mode               KiroCredentialMode     `json:"mode"`
	InferenceTokenType kiroInferenceTokenType `json:"inference_token_type"`
	InferenceToken     string                 `json:"inference_token"`
	InferenceExpiresAt int64                  `json:"inference_expires_at"`
	SSOAccessToken     string                 `json:"sso_access_token,omitempty"`
	SSOExpiresAt       int64                  `json:"sso_expires_at,omitempty"`
	RefreshToken       string                 `json:"refresh_token,omitempty"`
	ClientID           string                 `json:"client_id,omitempty"`
	ClientSecret       string                 `json:"client_secret,omitempty"`
	ProfileARN         string                 `json:"profile_arn,omitempty"`
}

// KiroCredentialVault is an opt-in codec boundary, not a registered provider,
// account bridge, refresher, or endpoint client. The caller supplies the wrapper
// and selected key explicitly; there is no filesystem or plaintext fallback.
type KiroCredentialVault struct{ codec *credentialcrypto.Codec }

func NewKiroCredentialVault(wrapper credentialcrypto.KMS, keyID string) (*KiroCredentialVault, error) {
	if wrapper == nil {
		return nil, ErrKiroCredentialConfiguration
	}
	v := reflect.ValueOf(wrapper)
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return nil, ErrKiroCredentialConfiguration
	}
	codec, err := credentialcrypto.New(wrapper, keyID)
	if err != nil {
		return nil, ErrKiroCredentialConfiguration
	}
	return &KiroCredentialVault{codec: codec}, nil
}

// Import encrypts explicitly supplied JSON only. Required fields are
// schema_version=1, purpose="sub2api/kiro-inference-credentials", mode,
// inference_token_type (social_access_token or idc_id_token), inference_token,
// and inference_expires_at (Unix seconds). Optional refresh/client/SSO fields are
// encrypted but inactive: this code never exchanges, refreshes, or returns them.
// Duplicate keys, aliases, unknown fields, nulls and trailing JSON fail closed.
func (v *KiroCredentialVault) Import(ctx context.Context, binding KiroCredentialBinding, input []byte, now time.Time) ([]byte, KiroCredentialMetadata, error) {
	var metadata KiroCredentialMetadata
	if v == nil || v.codec == nil {
		return nil, metadata, ErrKiroCredentialConfiguration
	}
	identity, err := kiroCredentialIdentity(binding)
	if err != nil {
		return nil, metadata, err
	}
	secret, err := parseKiroSecret(input, binding.Mode, now)
	if err != nil {
		return nil, metadata, err
	}
	// Store a string inside the codec's whole-document envelope so strict parsing
	// also applies after decrypt; the generic codec must not normalize duplicate
	// JSON keys or unknown fields before this schema validates them.
	document, err := json.Marshal(secret)
	if err != nil {
		return nil, metadata, ErrKiroCredentialInvalid
	}
	defer clear(document)
	encrypted, err := v.codec.Encrypt(ctx, identity, map[string]any{"kiro_secret": string(document)})
	if err != nil {
		return nil, metadata, kiroCredentialCryptoError(ctx, err)
	}
	return encrypted, secret.metadata(), nil
}

// DecryptInference selects only the schema's explicitly designated inference
// token. Expired/missing inference material never falls back to SSO or refresh.
// now is supplied by the executor's trusted clock, not by imported metadata.
func (v *KiroCredentialVault) DecryptInference(ctx context.Context, binding KiroCredentialBinding, envelope []byte, now time.Time) (KiroInferenceCredentials, error) {
	var result KiroInferenceCredentials
	if v == nil || v.codec == nil {
		return result, ErrKiroCredentialConfiguration
	}
	identity, err := kiroCredentialIdentity(binding)
	if err != nil {
		return result, err
	}
	document, err := v.codec.Decrypt(ctx, identity, envelope)
	if err != nil {
		return result, kiroCredentialCryptoError(ctx, err)
	}
	defer clear(document)
	text, ok := document["kiro_secret"].(string)
	if !ok || len(document) != 1 {
		return result, ErrKiroCredentialInvalid
	}
	plain := []byte(text)
	defer clear(plain)
	secret, err := parseKiroSecret(plain, binding.Mode, now)
	if err != nil {
		return result, err
	}
	if secret.InferenceTokenType == kiroRefreshOnly {
		return result, ErrKiroCredentialRefreshRequired
	}
	return KiroInferenceCredentials{bearer: secret.InferenceToken, expiresAt: time.Unix(secret.InferenceExpiresAt, 0).UTC()}, nil
}

func kiroCredentialCryptoError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrKiroCredentialCrypto
}

func kiroCredentialIdentity(b KiroCredentialBinding) (string, error) {
	if len(b.ResourceUUID) != 36 || b.AccountID <= 0 || b.Version <= 0 || (b.Mode != KiroCredentialSocial && b.Mode != KiroCredentialIdC) {
		return "", ErrKiroCredentialInvalid
	}
	for i, c := range b.ResourceUUID {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", ErrKiroCredentialInvalid
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", ErrKiroCredentialInvalid
		}
	}
	// Hash a domain-separated, unambiguous tuple to stay within the codec's
	// identity limit. The resulting identity participates in both KMS context and
	// AES-GCM AAD, including when a wrapper fails to enforce its context.
	raw, _ := json.Marshal([]string{kiroSecretPurpose, strconv.Itoa(kiroSecretVersion), b.ResourceUUID, strconv.FormatInt(b.AccountID, 10), strconv.FormatInt(b.Version, 10), string(b.Mode)})
	sum := sha256.Sum256(raw)
	return "kiro:v1:" + hex.EncodeToString(sum[:]), nil
}

func parseKiroSecret(input []byte, mode KiroCredentialMode, now time.Time) (kiroSecret, error) {
	var s kiroSecret
	if len(input) == 0 || len(input) > kiroMaxSecretBytes || !utf8.Valid(input) {
		return s, ErrKiroCredentialInvalid
	}
	d := json.NewDecoder(bytes.NewReader(input))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return s, ErrKiroCredentialInvalid
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return s, ErrKiroCredentialInvalid
		}
		name, ok := token.(string)
		if !ok {
			return s, ErrKiroCredentialInvalid
		}
		switch name {
		case "schema_version", "purpose", "mode", "inference_token_type", "inference_token", "inference_expires_at", "sso_access_token", "sso_expires_at", "refresh_token", "client_id", "client_secret", "profile_arn":
		default:
			return s, ErrKiroCredentialInvalid
		}
		if _, exists := fields[name]; exists {
			return s, ErrKiroCredentialInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return s, ErrKiroCredentialInvalid
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return s, ErrKiroCredentialInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return s, ErrKiroCredentialInvalid
	}
	if json.Unmarshal(input, &s) != nil || s.SchemaVersion != kiroSecretVersion || s.Purpose != kiroSecretPurpose || s.Mode != mode {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	inactive := s.InferenceTokenType == kiroRefreshOnly
	if inactive && (s.InferenceToken != "" || s.InferenceExpiresAt != 0 || s.RefreshToken == "") {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	if mode == KiroCredentialSocial {
		if (!inactive && s.InferenceTokenType != kiroSocialAccessToken) || s.SSOAccessToken != "" || s.SSOExpiresAt != 0 || s.ClientID != "" || s.ClientSecret != "" {
			return kiroSecret{}, ErrKiroCredentialInvalid
		}
	} else if mode == KiroCredentialIdC {
		if (!inactive && s.InferenceTokenType != kiroIdCIdentityToken && s.InferenceTokenType != kiroIdCAccessToken) || (s.InferenceToken != "" && s.InferenceToken == s.SSOAccessToken) || (s.ClientID == "") != (s.ClientSecret == "") {
			return kiroSecret{}, ErrKiroCredentialInvalid
		}
	} else {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	if !kiroValidProfileARN(s.ProfileARN, "") {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	if !kiroValidSecretText(s.InferenceToken, inactive) || !kiroValidSecretText(s.SSOAccessToken, true) || !kiroValidSecretText(s.RefreshToken, true) || !kiroValidSecretText(s.ClientID, true) || !kiroValidSecretText(s.ClientSecret, true) {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	if (!inactive && s.InferenceExpiresAt <= 0) || s.InferenceExpiresAt > 253402300799 || now.IsZero() || (s.SSOAccessToken == "") != (s.SSOExpiresAt == 0) || s.SSOExpiresAt < 0 || s.SSOExpiresAt > 253402300799 {
		return kiroSecret{}, ErrKiroCredentialInvalid
	}
	if !inactive && !now.Before(time.Unix(s.InferenceExpiresAt, 0)) {
		return kiroSecret{}, ErrKiroCredentialExpired
	}
	return s, nil
}
func kiroValidSecretText(s string, optional bool) bool {
	if s == "" {
		return optional
	}
	if len(s) > kiroMaxTokenBytes || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c <= 32 || c == 127 {
			return false
		}
	}
	return true
}
func (s kiroSecret) metadata() KiroCredentialMetadata {
	return KiroCredentialMetadata{SchemaVersion: s.SchemaVersion, Mode: s.Mode, InferenceExpiresAt: kiroMetadataExpiry(s.InferenceExpiresAt), HasInactiveRefresh: s.RefreshToken != "", RefreshRequired: s.InferenceTokenType == kiroRefreshOnly}
}

// ImportKiroRS imports one explicitly supplied kiro.rs camelCase JSON object.
// Arrays are intentionally unsupported. Refresh-only exports are encrypted in
// an inactive state; no refresh, login, activation or identity synthesis occurs.
// Non-secret display/region metadata is validated then discarded. Machine identity is discarded. Endpoint and proxy configuration is rejected.
func (v *KiroCredentialVault) ImportKiroRS(ctx context.Context, binding KiroCredentialBinding, input []byte, now time.Time) ([]byte, KiroCredentialMetadata, error) {
	var empty KiroCredentialMetadata
	if len(input) == 0 || len(input) > kiroMaxSecretBytes || !utf8.Valid(input) {
		return nil, empty, ErrKiroCredentialInvalid
	}
	d := json.NewDecoder(bytes.NewReader(input))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, empty, ErrKiroCredentialInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, empty, ErrKiroCredentialInvalid
		}
		name, ok := key.(string)
		if !ok {
			return nil, empty, ErrKiroCredentialInvalid
		}
		if _, exists := fields[name]; exists {
			return nil, empty, ErrKiroCredentialInvalid
		}
		switch name {
		case "accessToken", "refreshToken", "expiresAt", "authMethod", "clientId", "clientSecret", "profileArn", "id", "priority", "region", "authRegion", "apiRegion", "email", "subscriptionTitle", "disabled", "machineId", "provider":
		case "kiroApiKey", "endpoint", "proxyUrl", "proxyUsername", "proxyPassword":
			return nil, empty, ErrKiroCredentialUnsupported
		default:
			return nil, empty, ErrKiroCredentialInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return nil, empty, ErrKiroCredentialInvalid
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, empty, ErrKiroCredentialInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, empty, ErrKiroCredentialInvalid
	}
	if provider, ok := fields["provider"]; ok {
		var ignored string
		if json.Unmarshal(provider, &ignored) != nil || len(ignored) > 256 {
			return nil, empty, ErrKiroCredentialInvalid
		}
	}
	if machine, ok := fields["machineId"]; ok {
		var ignored string
		if json.Unmarshal(machine, &ignored) != nil || len(ignored) > 256 {
			return nil, empty, ErrKiroCredentialInvalid
		}
	}
	var raw struct {
		AccessToken       string `json:"accessToken"`
		RefreshToken      string `json:"refreshToken"`
		ExpiresAt         string `json:"expiresAt"`
		AuthMethod        string `json:"authMethod"`
		ClientID          string `json:"clientId"`
		ClientSecret      string `json:"clientSecret"`
		ProfileARN        string `json:"profileArn"`
		ID                uint64 `json:"id"`
		Priority          uint32 `json:"priority"`
		Region            string `json:"region"`
		AuthRegion        string `json:"authRegion"`
		APIRegion         string `json:"apiRegion"`
		Email             string `json:"email"`
		SubscriptionTitle string `json:"subscriptionTitle"`
		Disabled          bool   `json:"disabled"`
	}
	if json.Unmarshal(input, &raw) != nil {
		return nil, empty, ErrKiroCredentialInvalid
	}
	mode := KiroCredentialMode(raw.AuthMethod)
	switch strings.ToLower(raw.AuthMethod) {
	case "":
		// Some explicit IdC exports omit authMethod. Trusted binding selects the
		// mode; client credentials are required, provider is never authorization.
		if binding.Mode != KiroCredentialIdC || raw.ClientID == "" || raw.ClientSecret == "" {
			return nil, empty, ErrKiroCredentialInvalid
		}
		mode = KiroCredentialIdC
	case "social":
		mode = KiroCredentialSocial
	case "idc", "builder-id", "iam":
		mode = KiroCredentialIdC
	case "api_key", "apikey":
		return nil, empty, ErrKiroCredentialUnsupported
	default:
		return nil, empty, ErrKiroCredentialInvalid
	}
	if mode != binding.Mode {
		return nil, empty, ErrKiroCredentialInvalid
	}
	// disabled exports must not silently become runnable imports.
	if raw.Disabled {
		return nil, empty, ErrKiroCredentialUnsupported
	}
	for _, region := range []string{raw.Region, raw.AuthRegion, raw.APIRegion} {
		if !kiroValidProfileARN(raw.ProfileARN, region) {
			return nil, empty, ErrKiroCredentialInvalid
		}
	}
	s := kiroSecret{SchemaVersion: kiroSecretVersion, Purpose: kiroSecretPurpose, Mode: mode, InferenceToken: raw.AccessToken, RefreshToken: raw.RefreshToken, ClientID: raw.ClientID, ClientSecret: raw.ClientSecret, ProfileARN: raw.ProfileARN}
	if raw.AccessToken == "" {
		s.InferenceTokenType = kiroRefreshOnly
		// A refresh-only expiresAt describes stale/absent access material, not an
		// authorization lifetime. Validate its syntax but do not activate it.
		if raw.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, raw.ExpiresAt); err != nil {
				return nil, empty, ErrKiroCredentialInvalid
			}
		}
	} else {
		expires, err := time.Parse(time.RFC3339Nano, raw.ExpiresAt)
		if err != nil {
			return nil, empty, ErrKiroCredentialInvalid
		}
		s.InferenceExpiresAt = expires.Unix()
		s.InferenceTokenType = kiroSocialAccessToken
		if mode == KiroCredentialIdC {
			s.InferenceTokenType = kiroIdCAccessToken
		}
	}
	normalized, err := json.Marshal(s)
	if err != nil {
		return nil, empty, ErrKiroCredentialInvalid
	}
	defer clear(normalized)
	return v.Import(ctx, binding, normalized, now)
}

func kiroMetadataExpiry(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// kiroValidProfileARN accepts only bounded commercial Q/CodeWhisperer profile
// identifiers. Empty preserves compatibility with pre-profile encrypted schema v1.
// region, when supplied by the trusted runtime binding, must match exactly.
func kiroValidProfileARN(value, region string) bool {
	if value == "" {
		return true
	}
	if len(value) > 512 {
		return false
	}
	p := strings.SplitN(value, ":", 6)
	if len(p) != 6 || p[0] != "arn" || p[1] != "aws" || (p[2] != "codewhisperer" && p[2] != "q") || (p[3] != "us-east-1" && p[3] != "us-west-2") || (region != "" && p[3] != region) || len(p[4]) != 12 || !strings.HasPrefix(p[5], "profile/") {
		return false
	}
	for _, c := range p[4] {
		if c < '0' || c > '9' {
			return false
		}
	}
	id := strings.TrimPrefix(p[5], "profile/")
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
