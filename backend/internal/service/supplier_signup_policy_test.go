package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
func TestSupplierSignupRequiresPurposeProofWithoutConsumerGrants(t *testing.T) {
	ctx := context.Background()
	users := &supplierSignupUsers{}
	cfg := &config.Config{JWT: config.JWTConfig{ExpireHour: 1}, SupplierPortal: config.SupplierPortalConfig{Enabled: true, RegistrationEnabled: true, JWTSecret: strings.Repeat("s", 32)}}
	cache := &supplierProofCache{data: map[string]*VerificationCodeData{}}
	s := NewAuthService(nil, users, nil, nil, cfg, nil, &EmailService{cache: cache}, nil, nil, nil, nil, nil, nil)
	_, _, err := s.RegisterWithVerification(ctx, "u@example.com", "password", "", "", "", "")
	require.ErrorIs(t, err, ErrEmailVerifyRequired)
	require.Nil(t, users.created)
	cache.data["u@example.com"] = &VerificationCodeData{Code: "123456", ExpiresAt: time.Now().Add(time.Minute)}
	_, _, err = s.RegisterWithVerification(ctx, "u@example.com", "password", "123456", "", "", "")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Nil(t, users.created)
	cache.data["supplier_signup:u@example.com"] = &VerificationCodeData{Code: "654321", ExpiresAt: time.Now().Add(time.Minute)}
	_, _, err = s.RegisterWithVerification(ctx, "u@example.com", "password", "654321", "promo", "", "")
	require.Error(t, err)
	require.Nil(t, users.created)
	_, u, err := s.RegisterWithVerification(ctx, "u@example.com", "password", "654321", "", "", "")
	require.NoError(t, err)
	require.Equal(t, RoleUser, u.Role)
	require.Zero(t, u.Balance)
	require.Equal(t, 1, u.Concurrency)
	require.True(t, s.CheckPassword("password", u.PasswordHash))
	require.NotNil(t, users.created)
	require.Nil(t, cache.data["supplier_signup:u@example.com"])
}
