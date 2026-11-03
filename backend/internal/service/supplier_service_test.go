package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

type supplierTestRepo struct {
	p        *SupplierProfile
	err      error
	enrolled int
}

func (r *supplierTestRepo) GetProfile(context.Context, int64) (*SupplierProfile, error) {
	return r.p, r.err
}
func (r *supplierTestRepo) Enroll(_ context.Context, id int64, email, name string) (*SupplierProfile, error) {
	r.enrolled++
	r.p = &SupplierProfile{UserID: id, VerifiedEmail: email, DisplayName: name, Status: "active", Version: 1}
	r.err = nil
	return r.p, nil
}
func (r *supplierTestRepo) UpdateProfile(_ context.Context, id int64, name string, v int64) (*SupplierProfile, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.p.UserID != id {
		return nil, ErrSupplierNotEnrolled
	}
	if v != r.p.Version {
		return nil, ErrSupplierVersion
	}
	r.p.Version++
	r.p.DisplayName = name
	return r.p, nil
}

type supplierTestUsers struct{ email string }

func (u supplierTestUsers) GetByID(context.Context, int64) (*User, error) {
	return &User{Email: u.email, Status: StatusActive}, nil
}

type supplierTestEmail struct {
	calls int
	err   error
}

func (e *supplierTestEmail) SendSupplierVerifyCode(context.Context, string, string, ...string) error {
	return e.err
}
func (e *supplierTestEmail) VerifySupplierCode(context.Context, string, string) error {
	e.calls++
	return e.err
}
func TestSupplierValidation(t *testing.T) {
	for _, name := range []string{"", "   ", "a\nb", strings.Repeat("界", 101), string([]byte{255})} {
		_, err := supplierDisplayName(name)
		require.ErrorIs(t, err, ErrSupplierInput)
	}
	name, err := supplierDisplayName("  合作方  ")
	require.NoError(t, err)
	require.Equal(t, "合作方", name)
}
func TestSupplierEnrollmentProofAndIdempotency(t *testing.T) {
	ctx := context.Background()
	r := &supplierTestRepo{err: ErrSupplierNotEnrolled}
	e := &supplierTestEmail{}
	s := &SupplierService{repo: r, users: supplierTestUsers{"supplier@example.com"}, email: e}
	for _, code := range []string{"", "12345", "1234567", "abcdef"} {
		_, err := s.Enroll(ctx, 7, "name", code)
		require.Error(t, err)
	}
	require.Zero(t, e.calls)
	require.Zero(t, r.enrolled)
	p, err := s.Enroll(ctx, 7, "name", "123456")
	require.NoError(t, err)
	require.Equal(t, int64(7), p.UserID)
	require.Equal(t, "active", p.Status)
	require.Equal(t, 1, e.calls)
	again, err := s.Enroll(ctx, 7, "other", "")
	require.NoError(t, err)
	require.Same(t, p, again)
	require.Equal(t, 1, e.calls)
	require.Equal(t, 1, r.enrolled)
	_, err = s.UpdateProfile(ctx, 7, "new", 1)
	require.NoError(t, err)
	_, err = s.UpdateProfile(ctx, 7, "stale", 1)
	require.ErrorIs(t, err, ErrSupplierVersion)
	_, err = s.UpdateProfile(ctx, 8, "attack", 2)
	require.ErrorIs(t, err, ErrSupplierNotEnrolled)
}
func TestSupplierEmailChangeRequiresProof(t *testing.T) {
	r := &supplierTestRepo{err: ErrSupplierEmailChanged}
	e := &supplierTestEmail{}
	s := &SupplierService{repo: r, users: supplierTestUsers{"new@example.com"}, email: e}
	_, err := s.Enroll(context.Background(), 1, "name", "")
	require.ErrorIs(t, err, ErrEmailVerifyRequired)
	p, err := s.Enroll(context.Background(), 1, "name", "123456")
	require.NoError(t, err)
	require.Equal(t, "new@example.com", p.VerifiedEmail)
	require.Equal(t, 1, e.calls)
}

func TestSupplierEnrollmentFailedProofCannotCreateMembership(t *testing.T) {
	r := &supplierTestRepo{err: ErrSupplierNotEnrolled}
	e := &supplierTestEmail{err: ErrInvalidVerifyCode}
	s := &SupplierService{repo: r, users: supplierTestUsers{"supplier@example.com"}, email: e}
	_, err := s.Enroll(context.Background(), 7, "name", "123456")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Zero(t, r.enrolled)
}
