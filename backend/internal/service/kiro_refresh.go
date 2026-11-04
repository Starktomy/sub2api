// Copyright (c) 2026 hank9999. MIT. See internal/pkg/kiro/LICENSE.
// Refresh DTO/paths reference kiro.rs 8fb3dd691d1cedf437bd6a4e9ef9f60b3f217fdd.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func kiroRefreshEndpoint(mode KiroCredentialMode, region string) (string, error) {
	switch region {
	case "us-east-1":
		if mode == KiroCredentialIdC {
			return "https://oidc.us-east-1.amazonaws.com/token", nil
		}
		if mode == KiroCredentialSocial {
			return "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken", nil
		}
	case "us-west-2":
		if mode == KiroCredentialIdC {
			return "https://oidc.us-west-2.amazonaws.com/token", nil
		}
		if mode == KiroCredentialSocial {
			return "https://prod.us-west-2.auth.desktop.kiro.dev/refreshToken", nil
		}
	}
	return "", ErrKiroCredentialInvalid
}
func (v *KiroCredentialVault) refresh(ctx context.Context, r *KiroNativeRecord, client *http.Client) ([]byte, error) {
	if client == nil || client.Transport == nil || client.Jar != nil {
		return nil, ErrKiroUnavailable
	}
	endpoint, err := kiroRefreshEndpoint(r.Binding.Mode, r.Region)
	if err != nil {
		return nil, err
	}
	identity, err := kiroCredentialIdentity(r.Binding)
	if err != nil {
		return nil, err
	}
	document, err := v.codec.Decrypt(ctx, identity, r.Envelope)
	if err != nil {
		return nil, kiroCredentialCryptoError(ctx, err)
	}
	defer clear(document)
	text, ok := document["kiro_secret"].(string)
	if !ok || len(document) != 1 {
		return nil, ErrKiroCredentialInvalid
	}
	// Parse with a pre-expiry clock solely to validate already-stored expired
	// material. Its access token is never used. Refresh/client fields stay private.
	var raw kiroSecret
	if json.Unmarshal([]byte(text), &raw) != nil {
		return nil, ErrKiroCredentialInvalid
	}
	secret, err := parseKiroSecret([]byte(text), r.Binding.Mode, time.Unix(1, 0))
	if err != nil {
		return nil, err
	}
	if !kiroValidProfileARN(secret.ProfileARN, r.Region) {
		return nil, ErrKiroCredentialInvalid
	}
	if secret.RefreshToken == "" {
		return nil, ErrKiroCredentialRefreshRequired
	}
	body := map[string]string{"refreshToken": secret.RefreshToken}
	if r.Binding.Mode == KiroCredentialIdC {
		if secret.ClientID == "" || secret.ClientSecret == "" {
			return nil, ErrKiroCredentialRefreshRequired
		}
		body["clientId"] = secret.ClientID
		body["clientSecret"] = secret.ClientSecret
		body["grantType"] = "refresh_token"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, ErrKiroCredentialInvalid
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrKiroCredentialInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", r.UserAgent)
	req.Header.Set("X-Amz-User-Agent", r.AmzUserAgent)
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=1")
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.Timeout = 60 * time.Second
	// This trusted client must use fresh HTTP/1 connections, no proxy fallback or
	// middleware retries. Production provider below enforces those properties.
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrKiroRefreshBarrier
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrKiroRefreshBarrier
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, kiroMaxSecretBytes+1))
	if err != nil || len(data) > kiroMaxSecretBytes {
		return nil, ErrKiroRefreshBarrier
	}
	defer clear(data)
	var result struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken string  `json:"refreshToken"`
		ExpiresIn    int64   `json:"expiresIn"`
		IDToken      string  `json:"idToken"`
		ProfileARN   *string `json:"profileArn"`
	}
	if json.Unmarshal(data, &result) != nil || !kiroValidSecretText(result.AccessToken, false) || result.ExpiresIn <= 30 || result.ExpiresIn > 86400*90 {
		return nil, ErrKiroRefreshBarrier
	}
	if result.ProfileARN != nil {
		if *result.ProfileARN == "" || !kiroValidProfileARN(*result.ProfileARN, r.Region) {
			return nil, ErrKiroRefreshBarrier
		}
		secret.ProfileARN = *result.ProfileARN
	}
	secret.InferenceToken = result.AccessToken
	secret.InferenceExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Unix()
	secret.InferenceTokenType = kiroSocialAccessToken
	if r.Binding.Mode == KiroCredentialIdC {
		secret.InferenceTokenType = kiroIdCAccessToken
		if result.IDToken != "" {
			if !kiroValidSecretText(result.IDToken, false) || result.IDToken == result.AccessToken {
				return nil, ErrKiroRefreshBarrier
			}
			secret.InferenceToken = result.IDToken
			secret.InferenceTokenType = kiroIdCIdentityToken
			secret.SSOAccessToken = result.AccessToken
			secret.SSOExpiresAt = secret.InferenceExpiresAt
		} else {
			// Explicit compatibility with kiro.rs accessToken-only responses;
			// never guess authority from JWT contents or fall back from an ID token.
			secret.SSOAccessToken = ""
			secret.SSOExpiresAt = 0
		}
	}
	if result.RefreshToken != "" {
		if !kiroValidSecretText(result.RefreshToken, false) {
			return nil, ErrKiroRefreshBarrier
		}
		secret.RefreshToken = result.RefreshToken
	}
	normalized, err := json.Marshal(secret)
	if err != nil {
		return nil, ErrKiroCredentialInvalid
	}
	defer clear(normalized)
	next := r.Binding
	next.Version++
	// Once a rotated response is observed, cancellation cannot discard it before
	// encryption/persistence. The caller separately performs a bounded commit.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	envelope, _, err := v.Import(persistCtx, next, normalized, time.Now())
	return envelope, err
}
