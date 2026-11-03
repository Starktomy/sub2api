package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type supplierProofCache struct {
	EmailCache
	data     map[string]*VerificationCodeData
	consumed bool
}

func (c *supplierProofCache) GetVerificationCode(_ context.Context, key string) (*VerificationCodeData, error) {
	return c.data[key], nil
}
func (c *supplierProofCache) IncrVerificationCodeAttempts(_ context.Context, key string) (int, error) {
	d := c.data[key]
	if d == nil {
		return -1, nil
	}
	d.Attempts++
	return d.Attempts, nil
}
func (c *supplierProofCache) ConsumeSupplierVerificationCode(_ context.Context, key, code string) (bool, error) {
	if c.data[key] == nil {
		return false, nil
	}
	delete(c.data, key)
	c.consumed = true
	return true, nil
}
func TestSupplierEmailProofPurposeAndReplay(t *testing.T) {
	ctx := context.Background()
	c := &supplierProofCache{data: map[string]*VerificationCodeData{"u@example.com": {Code: "123456", ExpiresAt: time.Now().Add(time.Minute)}}}
	s := &EmailService{cache: c}
	require.ErrorIs(t, s.VerifySupplierCode(ctx, "u@example.com", "123456"), ErrInvalidVerifyCode)
	require.False(t, c.consumed)
	require.NotNil(t, c.data["u@example.com"])
	c.data["supplier:u@example.com"] = &VerificationCodeData{Code: "654321", ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, s.VerifySupplierCode(ctx, "u@example.com", "654321"))
	require.True(t, c.consumed)
	require.ErrorIs(t, s.VerifySupplierCode(ctx, "u@example.com", "654321"), ErrInvalidVerifyCode)
}

func TestSupplierEmailProofRejectsExpiryAndMissingAtomicCache(t *testing.T) {
	ctx := context.Background()
	c := &supplierProofCache{data: map[string]*VerificationCodeData{"supplier:u@example.com": {Code: "123456", ExpiresAt: time.Now().Add(-time.Minute)}}}
	s := &EmailService{cache: c}
	require.ErrorIs(t, s.VerifySupplierCode(ctx, "u@example.com", "123456"), ErrInvalidVerifyCode)
	require.False(t, c.consumed)
	s.cache = nil
	require.ErrorIs(t, s.VerifySupplierCode(ctx, "u@example.com", "123456"), ErrServiceUnavailable)
}
