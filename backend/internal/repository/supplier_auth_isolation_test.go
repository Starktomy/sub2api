package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type supplierIsolationUsers struct {
	service.UserRepository
	u *service.User
}

func (u supplierIsolationUsers) GetByID(context.Context, int64) (*service.User, error) {
	return u.u, nil
}
func TestSupplierAccessRefreshAndSessionIsolation(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer rdb.Close()
	consumer := &config.Config{JWT: config.JWTConfig{Secret: strings.Repeat("c", 32), ExpireHour: 1, RefreshTokenExpireDays: 1}}
	supplier := *consumer
	supplier.SupplierPortal = config.SupplierPortalConfig{Enabled: true, JWTSecret: strings.Repeat("s", 32)}
	u := &service.User{ID: 7, Email: "u@example.com", Status: service.StatusActive, Role: service.RoleUser, PasswordHash: "synthetic-hash"}
	users := supplierIsolationUsers{u: u}
	makeAuth := func(cfg *config.Config) *service.AuthService {
		return service.NewAuthService(nil, users, nil, ProvideScopedRefreshTokenCache(rdb, cfg), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	}
	c, s := makeAuth(consumer), makeAuth(&supplier)
	cp, err := c.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	sp, err := s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	_, err = c.ValidateToken(sp.AccessToken)
	require.Error(t, err)
	_, err = s.ValidateToken(cp.AccessToken)
	require.Error(t, err)
	claims, err := s.ValidateToken(sp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"supplier"}, []string(claims.Audience))
	_, err = c.RefreshTokenPair(ctx, sp.RefreshToken)
	require.ErrorIs(t, err, service.ErrRefreshTokenInvalid)
	_, err = s.RefreshTokenPair(ctx, cp.RefreshToken)
	require.ErrorIs(t, err, service.ErrRefreshTokenInvalid)
	_, err = c.RefreshTokenPair(ctx, cp.RefreshToken)
	require.NoError(t, err)
	_, err = s.RefreshTokenPair(ctx, sp.RefreshToken)
	require.NoError(t, err)
	// Logout/revocation uses only the current domain's family and user indexes.
	cp2, err := c.GenerateTokenPair(ctx, u, "shared-family-label")
	require.NoError(t, err)
	sp2, err := s.GenerateTokenPair(ctx, u, "shared-family-label")
	require.NoError(t, err)
	require.NoError(t, s.RevokeSessionFamily(ctx, "shared-family-label"))
	_, err = s.RefreshTokenPair(ctx, sp2.RefreshToken)
	require.ErrorIs(t, err, service.ErrRefreshTokenInvalid)
	_, err = c.RefreshTokenPair(ctx, cp2.RefreshToken)
	require.NoError(t, err)
	// Even accidental same-secret misconfiguration cannot cross the audience gate.
	same := supplier
	same.SupplierPortal.JWTSecret = consumer.JWT.Secret
	ss := makeAuth(&same)
	st, err := ss.GenerateToken(ctx, u)
	require.NoError(t, err)
	_, err = c.ValidateToken(st)
	require.Error(t, err)
	_, err = ss.ValidateToken(cp.AccessToken)
	require.Error(t, err)
	ct := ProvideScopedTotpCache(rdb, consumer)
	stc := ProvideScopedTotpCache(rdb, &supplier)
	session := &service.TotpLoginSession{UserID: 7, Email: u.Email, TokenExpiry: time.Now().Add(time.Minute)}
	require.NoError(t, ct.SetLoginSession(ctx, "consumer-temp", session, time.Minute))
	require.NoError(t, stc.SetLoginSession(ctx, "supplier-temp", session, time.Minute))
	cross, err := stc.GetLoginSession(ctx, "consumer-temp")
	require.NoError(t, err)
	require.Nil(t, cross)
	cross, err = ct.GetLoginSession(ctx, "supplier-temp")
	require.NoError(t, err)
	require.Nil(t, cross)
	require.NoError(t, ct.SetStepUpGrant(ctx, 7, "session", time.Minute))
	allowed, err := stc.HasStepUpGrant(ctx, 7, "session")
	require.NoError(t, err)
	require.False(t, allowed)
	// 2FA failure budgets are scoped along with pending login tokens.
	_, err = ct.IncrementVerifyAttempts(ctx, 7)
	require.NoError(t, err)
	attempts, err := stc.GetVerifyAttempts(ctx, 7)
	require.NoError(t, err)
	require.Zero(t, attempts)
	ce := ProvideScopedEmailCache(rdb, consumer)
	se := ProvideScopedEmailCache(rdb, &supplier)
	require.NoError(t, ce.SetVerificationCode(ctx, "supplier_signup:u@example.com", &service.VerificationCodeData{Code: "123456"}, time.Minute))
	proof, err := se.GetVerificationCode(ctx, "supplier_signup:u@example.com")
	require.Error(t, err)
	require.Nil(t, proof)
}
