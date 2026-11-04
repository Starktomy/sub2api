package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type supplierSignupUsers struct {
	UserRepository
	created *User
}

func (r *supplierSignupUsers) ExistsByEmail(context.Context, string) (bool, error) { return false, nil }
func (r *supplierSignupUsers) ExistsByEmailAlias(context.Context, string) (bool, error) {
	return false, nil
}
func (r *supplierSignupUsers) CreateWithEmailAliasGuard(_ context.Context, u *User) error {
	u.ID = 9
	r.created = u
	return nil
}

// Reuse the supplier SMTP settings stub without configuring any delivery transport.
type supplierSignupSettings struct {
	supplierSMTPSettings
}

func (s supplierSignupSettings) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func newSupplierSignupPolicyService(users *supplierSignupUsers, cache *supplierProofCache) *AuthService {
	cfg := &config.Config{JWT: config.JWTConfig{ExpireHour: 1}, SupplierPortal: config.SupplierPortalConfig{Enabled: true, RegistrationEnabled: true, JWTSecret: strings.Repeat("s", 32)}}
	settings := NewSettingService(supplierSignupSettings{supplierSMTPSettings{values: map[string]string{
		SettingKeyRegistrationEnabled:              "false",
		SettingKeyEmailVerifyEnabled:               "false",
		SettingKeyRegistrationEmailBlacklist:       `["spam.user@gmail.com","blocked@example.com"]`,
		SettingKeyRegistrationEmailSuffixWhitelist: `["@gmail.com","@example.com"]`,
		SettingKeyDefaultBalance:                   "100",
		SettingKeyDefaultConcurrency:               "20",
		SettingKeyInvitationCodeEnabled:            "true",
	}}}, cfg)
	return NewAuthService(nil, users, nil, nil, cfg, settings, &EmailService{cache: cache}, nil, nil, nil, nil, nil, nil)
}

func TestSupplierSignupBlacklist(t *testing.T) {
	for _, email := range []string{"spam.user@gmail.com", "SpamUser+supplier@googlemail.com", "blocked+supplier@example.com"} {
		t.Run(email, func(t *testing.T) {
			ctx := context.Background()
			users := &supplierSignupUsers{}
			cache := &supplierProofCache{data: map[string]*VerificationCodeData{
				"supplier_signup:" + email: {Code: "654321", ExpiresAt: time.Now().Add(time.Minute)},
			}}
			s := newSupplierSignupPolicyService(users, cache)
			// A valid supplier-purpose proof must not bypass the blacklist, even
			// when the domain is explicitly whitelisted.
			token, user, err := s.RegisterWithVerification(ctx, email, "password", "654321", "", "", "")
			require.ErrorIs(t, err, ErrEmailBlocked)
			require.Equal(t, "EMAIL_BLOCKED", infraerrors.FromError(err).Reason)
			require.Empty(t, token)
			require.Nil(t, user)
			require.Nil(t, users.created)
		})
	}
}

func TestSupplierSendSignupCodeBlacklist(t *testing.T) {
	for _, email := range []string{"spam.user@gmail.com", "SpamUser+supplier@googlemail.com", "blocked+supplier@example.com"} {
		t.Run(email, func(t *testing.T) {
			s := newSupplierSignupPolicyService(&supplierSignupUsers{}, nil)
			require.ErrorIs(t, s.SendVerifyCode(context.Background(), email), ErrEmailBlocked)
			result, err := s.SendVerifyCodeAsync(context.Background(), email)
			require.ErrorIs(t, err, ErrEmailBlocked)
			require.Nil(t, result)
			// No SMTP configuration exists: an allowed address reaches the safe
			// supplier delivery boundary instead of attempting a live send.
			require.ErrorIs(t, s.SendVerifyCode(context.Background(), "allowed@example.com"), ErrEmailNotConfigured)
			result, err = s.SendVerifyCodeAsync(context.Background(), "allowed@example.com")
			require.ErrorIs(t, err, ErrEmailNotConfigured)
			require.Nil(t, result)
		})
	}
}

func TestSupplierSignupRequiresPurposeProofWithoutConsumerGrants(t *testing.T) {
	ctx := context.Background()
	users := &supplierSignupUsers{}
	cache := &supplierProofCache{data: map[string]*VerificationCodeData{}}
	s := newSupplierSignupPolicyService(users, cache)
	require.True(t, s.IsEmailVerifyEnabled(ctx))
	_, _, err := s.RegisterWithVerification(ctx, "u@example.com", "password", "", "", "", "")
	require.ErrorIs(t, err, ErrEmailVerifyRequired)
	require.Nil(t, users.created)
	cache.data["u@example.com"] = &VerificationCodeData{Code: "123456", ExpiresAt: time.Now().Add(time.Minute)}
	_, _, err = s.RegisterWithVerification(ctx, "u@example.com", "password", "123456", "", "", "")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Nil(t, users.created)
	cache.data["supplier:u@example.com"] = &VerificationCodeData{Code: "234567", ExpiresAt: time.Now().Add(time.Minute)}
	_, _, err = s.RegisterWithVerification(ctx, "u@example.com", "password", "234567", "", "", "")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Nil(t, users.created)
	cache.data["supplier_signup:u@example.com"] = &VerificationCodeData{Code: "654321", ExpiresAt: time.Now().Add(time.Minute)}
	for _, incentives := range [][3]string{{"promo", "", ""}, {"", "invitation", ""}, {"", "", "affiliate"}} {
		_, _, err = s.RegisterWithVerification(ctx, "u@example.com", "password", "654321", incentives[0], incentives[1], incentives[2])
		require.Error(t, err)
		require.Equal(t, "SUPPLIER_REGISTRATION_FIELDS", infraerrors.FromError(err).Reason)
		require.Nil(t, users.created)
		require.NotNil(t, cache.data["supplier_signup:u@example.com"])
	}
	_, u, err := s.RegisterWithVerification(ctx, "u@example.com", "password", "654321", "", "", "")
	require.NoError(t, err)
	require.Equal(t, RoleUser, u.Role)
	require.Zero(t, u.Balance)
	require.Equal(t, 1, u.Concurrency)
	require.True(t, s.CheckPassword("password", u.PasswordHash))
	require.NotNil(t, users.created)
	require.Nil(t, cache.data["supplier_signup:u@example.com"])
}
