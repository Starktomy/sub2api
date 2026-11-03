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
