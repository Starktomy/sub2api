package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKiroCLICredentialsSeparateAccessAndProfile(t *testing.T) {
	s, mem := kiroRefreshSetup(t, nil)
	now := time.Now()
	secret := kiroTestSecret(now, KiroCredentialIdC)
	secret.SSOAccessToken = "fake-cli-access"
	secret.SSOExpiresAt = now.Add(time.Hour).Unix()
	var err error
	mem.r.Envelope, _, err = s.vault.Import(context.Background(), mem.r.Binding, kiroTestJSON(t, secret), now)
	require.NoError(t, err)
	token, profile, err := s.vault.cliCredentials(context.Background(), mem.r, now)
	require.NoError(t, err)
	require.Equal(t, "fake-cli-access", token)
	require.True(t, kiroValidProfileARN(profile, mem.r.Region))
	require.NoError(t, bindCLIProfile("", profile))
	require.NoError(t, bindCLIProfile(profile, profile))
	require.ErrorIs(t, bindCLIProfile("different", profile), ErrKiroCredentialInvalid)
	secret.SSOExpiresAt = now.Add(-time.Minute).Unix()
	mem.r.Envelope, _, err = s.vault.Import(context.Background(), mem.r.Binding, kiroTestJSON(t, secret), now)
	require.NoError(t, err)
	_, _, err = s.vault.cliCredentials(context.Background(), mem.r, now)
	require.ErrorIs(t, err, ErrKiroCredentialRefreshRequired)
}
