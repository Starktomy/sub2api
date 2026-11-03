package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrSupplierNotEnrolled  = apperrors.NotFound("NOT_ENROLLED", "Supplier portal enrollment required")
	ErrSupplierEmailChanged = apperrors.Forbidden("SUPPLIER_EMAIL_VERIFICATION_REQUIRED", "Verify your current email to access the supplier portal")
	ErrSupplierVersion      = apperrors.Conflict("SUPPLIER_VERSION_CONFLICT", "Supplier profile changed; reload before editing")
	ErrSupplierInput        = apperrors.BadRequest("SUPPLIER_INVALID_INPUT", "Display name must contain 1-100 characters without control characters")
)

// SupplierProfile grants portal access only, never supply, admin or payment authority.
type SupplierProfile struct {
	ID              int64     `json:"id"`
	UserID          int64     `json:"user_id"`
	DisplayName     string    `json:"display_name"`
	Status          string    `json:"status"`
	VerifiedEmail   string    `json:"verified_email"`
	EmailVerifiedAt time.Time `json:"email_verified_at"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type SupplierRepository interface {
	GetProfile(context.Context, int64) (*SupplierProfile, error)
	Enroll(context.Context, int64, string, string) (*SupplierProfile, error)
	UpdateProfile(context.Context, int64, string, int64) (*SupplierProfile, error)
}
type supplierUserReader interface {
	GetByID(context.Context, int64) (*User, error)
}
type supplierEmailVerifier interface {
	SendSupplierVerifyCode(context.Context, string, string, ...string) error
	VerifySupplierCode(context.Context, string, string) error
}
type SupplierService struct {
	repo  SupplierRepository
	users supplierUserReader
	email supplierEmailVerifier
}

func NewSupplierService(repo SupplierRepository, users *UserService, email *EmailService) *SupplierService {
	return &SupplierService{repo: repo, users: users, email: email}
}
func supplierDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 {
		return "", ErrSupplierInput
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrSupplierInput
		}
	}
	return name, nil
}
func (s *SupplierService) currentEmail(ctx context.Context, id int64) (string, error) {
	u, err := s.users.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if u == nil || !u.IsActive() {
		return "", ErrUserNotFound
	}
	address, err := mail.ParseAddress(u.Email)
	if err != nil || address.Address != u.Email || len(u.Email) > 320 || isReservedEmail(u.Email) {
		return "", apperrors.BadRequest("SUPPLIER_EMAIL_REQUIRED", "A real email address is required")
	}
	return u.Email, nil
}
func (s *SupplierService) GetProfile(ctx context.Context, id int64) (*SupplierProfile, error) {
	return s.repo.GetProfile(ctx, id)
}
func (s *SupplierService) SendVerifyCode(ctx context.Context, id int64) error {
	email, err := s.currentEmail(ctx, id)
	if err != nil {
		return err
	}
	return s.email.SendSupplierVerifyCode(ctx, email, "Supplier portal")
}
func (s *SupplierService) Enroll(ctx context.Context, id int64, name, code string) (*SupplierProfile, error) {
	name, err := supplierDisplayName(name)
	if err != nil {
		return nil, err
	}
	// Durable current-email membership makes retries idempotent without reusing a code.
	p, err := s.repo.GetProfile(ctx, id)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, ErrSupplierNotEnrolled) && !errors.Is(err, ErrSupplierEmailChanged) {
		return nil, err
	}
	email, err := s.currentEmail(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(code) != 6 {
		return nil, ErrEmailVerifyRequired
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return nil, ErrEmailVerifyRequired
		}
	}
	if err := s.email.VerifySupplierCode(ctx, email, code); err != nil {
		// A concurrent enrollment may have consumed the single-use proof and
		// committed membership. Only durable, current-email membership is a retry success.
		if profile, readErr := s.repo.GetProfile(ctx, id); readErr == nil {
			return profile, nil
		}
		return nil, err
	}
	return s.repo.Enroll(ctx, id, email, name)
}
func (s *SupplierService) UpdateProfile(ctx context.Context, id int64, name string, version int64) (*SupplierProfile, error) {
	name, err := supplierDisplayName(name)
	if err != nil {
		return nil, err
	}
	if version < 1 {
		return nil, ErrSupplierVersion
	}
	return s.repo.UpdateProfile(ctx, id, name, version)
}
