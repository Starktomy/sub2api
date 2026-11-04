package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type supplierTokenUsers struct {
	UserRepository
	user *User
}

func (r *supplierTokenUsers) GetByEmail(context.Context, string) (*User, error) {
	return r.user, nil
}

func (r *supplierTokenUsers) GetByID(context.Context, int64) (*User, error) {
	return r.user, nil
}

func TestSupplierDisabledUserCannotLoginOrRefresh(t *testing.T) {
	ctx := context.Background()
	users := &supplierTokenUsers{user: &User{ID: 7, Email: "blocked@example.com", Role: RoleUser, Status: StatusActive}}
	s := &AuthService{userRepo: users, cfg: &config.Config{
		JWT:            config.JWTConfig{ExpireHour: 1},
		SupplierPortal: config.SupplierPortalConfig{Enabled: true, JWTSecret: strings.Repeat("s", 32)},
	}}
	hash, err := s.HashPassword("password")
	require.NoError(t, err)
	users.user.PasswordHash = hash
	token, user, err := s.Login(ctx, users.user.Email, "password")
	require.NoError(t, err)
	require.Equal(t, users.user, user)
	claims, err := s.ValidateToken(token)
	require.NoError(t, err)
	require.Equal(t, jwt.ClaimStrings{"supplier"}, claims.Audience)
	refreshed, err := s.RefreshToken(ctx, token)
	require.NoError(t, err)
	require.NotEmpty(t, refreshed)

	// The upstream admin blacklist operation disables users. Auth must consult
	// current user status rather than treating an already-issued JWT as proof
	// that login or refresh is still permitted.
	users.user.Status = StatusDisabled
	tokenOut, user, err := s.Login(ctx, users.user.Email, "password")
	require.ErrorIs(t, err, ErrUserNotActive)
	require.Empty(t, tokenOut)
	require.Nil(t, user)
	tokenOut, err = s.RefreshToken(ctx, token)
	require.ErrorIs(t, err, ErrUserNotActive)
	require.Empty(t, tokenOut)
}

func TestSupplierExpiredAccessCannotCrossLegacyRefreshAudience(t *testing.T) {
	secret := strings.Repeat("c", 32)
	c := &AuthService{cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}}
	s := &AuthService{cfg: &config.Config{SupplierPortal: config.SupplierPortalConfig{Enabled: true, JWTSecret: secret}}}
	for _, tc := range []struct {
		auth *AuthService
		aud  jwt.ClaimStrings
	}{{c, jwt.ClaimStrings{"supplier"}}, {s, nil}} {
		claims := &JWTClaims{UserID: 7, RegisteredClaims: jwt.RegisteredClaims{Audience: tc.aud, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
		require.NoError(t, err)
		out, err := tc.auth.ValidateToken(token)
		require.Nil(t, out)
		require.ErrorIs(t, err, ErrInvalidToken)
		_, err = tc.auth.RefreshToken(context.Background(), token)
		require.ErrorIs(t, err, ErrInvalidToken)
	}
}

func TestSupplierExpiredAccessWithWrongSignatureNeverRefreshes(t *testing.T) {
	s := &AuthService{cfg: &config.Config{SupplierPortal: config.SupplierPortalConfig{Enabled: true, JWTSecret: strings.Repeat("s", 32)}}}
	claims := &JWTClaims{UserID: 7, RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"supplier"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(strings.Repeat("x", 32)))
	require.NoError(t, err)
	out, err := s.ValidateToken(raw)
	require.Nil(t, out)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = s.RefreshToken(context.Background(), raw)
	require.ErrorIs(t, err, ErrInvalidToken)
}
