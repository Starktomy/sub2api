package service

import (
	"context"
	"time"

	e "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SupplierConsentVersion = "all_available_continuous_v1"

var (
	ErrSupplierConsent             = e.BadRequest("SUPPLIER_CONSENT_REQUIRED", "Explicit continuous all-available-quota authorization is required")
	ErrSupplierAuthorizationExpiry = e.BadRequest("SUPPLIER_AUTHORIZATION_EXPIRY_INVALID", "Authorization expiry must be in the future and no later than credential expiry")
	ErrSupplierSupplyUnavailable   = e.Conflict("SUPPLIER_SUPPLY_UNAVAILABLE", "Fresh credential verification and current authorization are required")
)

// Optional identifiers are paired: an Update action may never import a different identity.
type SupplierImportOptions struct {
	ConsentVersion         string     `json:"consent_version"`
	AuthorizationExpiresAt *time.Time `json:"authorization_expires_at,omitempty"`
	ResourceID             string     `json:"resource_id,omitempty"`
	ResourceVersion        int64      `json:"resource_version,omitempty"`
}

type supplierSupplyControlRepository interface {
	StartSupply(context.Context, int64, string, int64) (*SupplierResource, error)
	AdminSupplyControl(context.Context, string, int64, bool) (*SupplierResource, error)
}

func (s *SupplierResourceService) Start(ctx context.Context, user int64, id string, version int64) (*SupplierResource, error) {
	p, err := s.profiles.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	repo, ok := s.repo.(supplierSupplyControlRepository)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	r, err := repo.StartSupply(ctx, p.ID, id, version)
	if err != nil {
		return nil, err
	}
	if err = s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	return resourceProjection(r), nil
}

func (s *SupplierResourceService) AdminSupplyControl(ctx context.Context, id string, version int64, disabled bool) (*SupplierResource, error) {
	repo, ok := s.repo.(supplierSupplyControlRepository)
	if !ok {
		return nil, ErrServiceUnavailable
	}
	r, err := repo.AdminSupplyControl(ctx, id, version, disabled)
	if err != nil {
		return nil, err
	}
	if err = s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	// Do not retain secrets in an admin result even though their JSON tags redact them.
	r.Envelope = nil
	r.KeyID = ""
	r.AccountHint = ""
	r.IdentityFingerprint = ""
	r.TokenFingerprint = ""
	return resourceProjection(r), nil
}

func supplierAvailabilityReason(r *SupplierResource, now time.Time) string {
	switch {
	case r.Status == "disconnected":
		return "disconnected"
	case r.ConsentVersion != SupplierConsentVersion || r.AuthorizationVersion < 1 || r.AuthorizationRevokedAt != nil || r.AuthorizationExpiresAt == nil:
		return "reauthorization_required"
	case !now.Before(*r.AuthorizationExpiresAt):
		return "authorization_expired"
	case !now.Before(r.TokenExpiresAt):
		return "reauthorization_required"
	case r.AdminDisabled:
		return "admin_disabled"
	case r.ProbeErrorCode != nil && (*r.ProbeErrorCode == "phone_verification_required" || *r.ProbeErrorCode == "identity_email_mismatch" || *r.ProbeErrorCode == "identity_unknown"):
		return *r.ProbeErrorCode
	case r.ProbeState == "reauthorization_required":
		return "reauthorization_required"
	case r.Usage == nil || !r.Usage.PhoneVerified || !r.Usage.EmailMatched:
		return "identity_unknown"
	case r.SupplyEnablePending:
		return "verification_required"
	case !r.SupplyEnabled:
		return "supply_paused"
	case r.LastVerifiedAt == nil || r.VerifiedCredentialVersion == nil || *r.VerifiedCredentialVersion != r.CredentialVersion:
		return "verification_required"
	case r.ProbeErrorCode != nil || (r.ProbeState != "fresh" && r.ProbeState != "running"):
		return "quota_unknown"
	case r.LastVerifiedAt.After(now) || now.Sub(*r.LastVerifiedAt) > 15*time.Minute:
		return "quota_stale"
	}
	if reason := SupplierQuotaAvailability(r.Usage, now); reason != "" {
		return reason
	}
	if !r.Schedulable {
		return "unavailable"
	}
	return ""
}
