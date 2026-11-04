package service

import (
	"context"
	"time"
)

// cliCredentials selects the control/access token used by official CLI runtime,
// independently of the IDE identity token. It never refreshes or mutates state.
func (v *KiroCredentialVault) cliCredentials(ctx context.Context, r *KiroNativeRecord, now time.Time) (string, string, error) {
	identity, err := kiroCredentialIdentity(r.Binding)
	if err != nil {
		return "", "", err
	}
	doc, err := v.codec.Decrypt(ctx, identity, r.Envelope)
	if err != nil {
		return "", "", ErrKiroCredentialInvalid
	}
	defer clear(doc)
	text, ok := doc["kiro_secret"].(string)
	if !ok {
		return "", "", ErrKiroCredentialInvalid
	}
	secret, err := parseKiroSecret([]byte(text), r.Binding.Mode, now)
	if err != nil {
		return "", "", err
	}
	token := secret.InferenceToken
	if r.Binding.Mode == KiroCredentialIdC && secret.InferenceTokenType == kiroIdCIdentityToken {
		if secret.SSOAccessToken == "" || secret.SSOExpiresAt <= now.Unix() {
			return "", "", ErrKiroCredentialRefreshRequired
		}
		token = secret.SSOAccessToken
	}
	if token == "" || secret.InferenceTokenType == kiroRefreshOnly {
		return "", "", ErrKiroCredentialRefreshRequired
	}
	profile := secret.ProfileARN
	if profile == "" && r.Region == "us-east-1" {
		profile = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	}
	if profile == "" || !kiroValidProfileARN(profile, r.Region) {
		return "", "", ErrKiroCredentialInvalid
	}
	return token, profile, nil
}

// bindCLIProfile rejects caller redirection rather than silently replacing it.
func bindCLIProfile(inputProfile, credentialProfile string) error {
	if inputProfile != "" && inputProfile != credentialProfile {
		return ErrKiroCredentialInvalid
	}
	return nil
}
