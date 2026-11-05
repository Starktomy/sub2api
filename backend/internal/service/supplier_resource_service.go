package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codexsession"
	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
	e "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"strings"
	"time"
)

var (
	ErrSupplierVault            = e.ServiceUnavailable("SUPPLIER_VAULT_NOT_CONFIGURED", "Supplier credential vault is not configured")
	ErrSupplierResource         = e.NotFound("SUPPLIER_RESOURCE_NOT_FOUND", "Supplier resource not found")
	ErrSupplierResourceConflict = e.Conflict("SUPPLIER_RESOURCE_CONFLICT", "Resource identity is unavailable")
	ErrSupplierResourceVersion  = e.Conflict("SUPPLIER_RESOURCE_VERSION_CONFLICT", "Reload resource before editing")
	ErrSupplierReimport         = e.BadRequest("SUPPLIER_RESOURCE_REIMPORT_REQUIRED", "Valid session JSON required; expired access tokens must be imported again")
)

type SupplierResource struct {
	AutoSyncEnabled     bool                   `json:"auto_sync_enabled"`
	CredentialVersion   int64                  `json:"-"`
	ProbeState          string                 `json:"probe_state"`
	LastProbeAt         *time.Time             `json:"last_probe_at"`
	NextProbeAt         *time.Time             `json:"next_probe_at"`
	ProbeErrorCode      *string                `json:"probe_error_code"`
	ID                  string                 `json:"id"`
	SupplierID          int64                  `json:"-"`
	DisplayName         string                 `json:"display_name"`
	Status              string                 `json:"status"`
	Version             int64                  `json:"version"`
	Provider            string                 `json:"provider"`
	Product             string                 `json:"product"`
	Schedulable         bool                   `json:"schedulable"`
	AccountID           int64                  `json:"account_id,omitempty"`
	MaskedEmail         string                 `json:"masked_email"`
	PlanType            string                 `json:"plan_type"`
	TokenExpiresAt      time.Time              `json:"token_expires_at"`
	LastUsageAt         *time.Time             `json:"last_usage_at"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
	Usage               *SupplierResourceUsage `json:"usage"`
	UsageState          string                 `json:"usage_state"`
	Envelope            []byte                 `json:"-"`
	KeyID               string                 `json:"-"`
	AccountHint         string                 `json:"-"`
	IdentityFingerprint string                 `json:"-"`
	TokenFingerprint    string                 `json:"-"`
}
type SupplierResourceUsage struct {
	PlanType  string           `json:"plan_type"`
	RateLimit *OpenAIRateLimit `json:"rate_limit"`
	FetchedAt int64            `json:"fetched_at"`
}
type SupplierResourceRepository interface {
	List(context.Context, int64) ([]*SupplierResource, error)
	Get(context.Context, int64, string) (*SupplierResource, error)
	Create(context.Context, int64, *SupplierResource) error
	Transition(context.Context, int64, string, int64, bool) (*SupplierResource, error)
	SaveUsage(context.Context, int64, string, int64, *SupplierResourceUsage) (*SupplierResource, error)
}
type SupplierResourceService struct {
	repo     SupplierResourceRepository
	profiles SupplierRepository
	quota    *OpenAIQuotaService
	wrapper  *credentialcrypto.LocalKeyWrapper
	active   string
}

func NewSupplierResourceService(repo SupplierResourceRepository, profiles SupplierRepository, quota *OpenAIQuotaService, cfg *config.Config) *SupplierResourceService {
	s := &SupplierResourceService{repo: repo, profiles: profiles, quota: quota, active: cfg.SupplierPortal.VaultKeyID}
	if cfg.SupplierPortal.VaultKeyFile != "" && s.active != "" {
		s.wrapper, _ = credentialcrypto.LoadLocalKeyWrapper(cfg.SupplierPortal.VaultKeyFile)
	}
	return s
}
func (s *SupplierResourceService) Configured() bool {
	return s.wrapper != nil && s.wrapper.HasKey(s.active)
}
func resourceBinding(r *SupplierResource) string {
	return fmt.Sprintf("supplier-resource/v1/%d/%s", r.SupplierID, r.ID)
}
func resourceProjection(r *SupplierResource) *SupplierResource {
	r.Provider = "openai"
	r.Product = "codex"
	r.UsageState = "unknown"
	if !time.Now().Before(r.TokenExpiresAt) && r.Status != "disconnected" {
		r.ProbeState = "reauthorization_required"
		code := "expired"
		r.ProbeErrorCode = &code
		r.AutoSyncEnabled = false
		r.NextProbeAt = nil
	}
	if r.Usage != nil && r.Usage.PlanType != "" {
		r.PlanType = r.Usage.PlanType
	}
	if r.LastUsageAt != nil {
		r.UsageState = "fresh"
		if r.Status == "disconnected" || !time.Now().Before(r.TokenExpiresAt) || time.Since(*r.LastUsageAt) > 5*time.Minute {
			r.UsageState = "stale"
		}
	}
	return r
}
func (s *SupplierResourceService) List(ctx context.Context, user int64) ([]*SupplierResource, error) {
	p, e := s.profiles.GetProfile(ctx, user)
	if e != nil {
		return nil, e
	}
	items, e := s.repo.List(ctx, p.ID)
	for _, r := range items {
		if err := s.projectAdmission(ctx, r); err != nil {
			return nil, err
		}
		resourceProjection(r)
	}
	return items, e
}
func (s *SupplierResourceService) Get(ctx context.Context, user int64, id string) (*SupplierResource, error) {
	p, e := s.profiles.GetProfile(ctx, user)
	if e != nil {
		return nil, e
	}
	r, e := s.repo.Get(ctx, p.ID, id)
	if e != nil {
		return nil, e
	}
	if err := s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	return resourceProjection(r), nil
}
func digest(v string) string { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }

// SupplierImportResourceReader finds an owner's existing identity before envelope
// encryption, so reimports retain the original authenticated resource binding.
type SupplierImportResourceReader interface {
	FindImportResource(context.Context, int64, string) (*SupplierResource, error)
}

func (s *SupplierResourceService) Import(ctx context.Context, user int64, name, content string) (*SupplierResource, error) {
	if !s.Configured() {
		return nil, ErrSupplierVault
	}
	p, err := s.profiles.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	name, err = supplierDisplayName(name)
	if err != nil {
		return nil, err
	}
	session, err := codexsession.ParseSupplier(content)
	if err != nil {
		return nil, ErrSupplierReimport
	}
	id, err := generateRedeemRequestID()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	switch session.Plan {
	case "free", "plus", "pro", "team", "business", "enterprise", "edu":
	default:
		session.Plan = ""
	}
	r := &SupplierResource{ID: id, SupplierID: p.ID, DisplayName: name, Status: "paused", Version: 1, AutoSyncEnabled: true, CredentialVersion: 1, ProbeState: "pending", KeyID: s.active, AccountHint: session.AccountID, IdentityFingerprint: digest(session.AccountID), TokenFingerprint: digest(session.AccessToken), MaskedEmail: MaskEmail(session.Email), PlanType: session.Plan, TokenExpiresAt: session.ExpiresAt}
	if reader, ok := s.repo.(SupplierImportResourceReader); ok {
		existing, lookupErr := reader.FindImportResource(ctx, p.ID, r.IdentityFingerprint)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing != nil {
			if existing.SupplierID != p.ID || existing.IdentityFingerprint != r.IdentityFingerprint || existing.AccountHint != r.AccountHint {
				return nil, ErrSupplierResourceConflict
			}
			r.ID, r.Version, r.CredentialVersion = existing.ID, existing.Version, existing.CredentialVersion
		}
	}
	codec, err := credentialcrypto.New(s.wrapper, s.active)
	if err != nil {
		return nil, ErrSupplierVault
	}
	r.Envelope, err = codec.Encrypt(ctx, resourceBinding(r), map[string]any{"access_token": session.AccessToken, "account_id": session.AccountID})
	if err != nil {
		return nil, ErrSupplierVault
	}
	if err = s.repo.Create(ctx, p.ID, r); err != nil {
		return nil, err
	}
	if err := s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	return resourceProjection(r), nil
}
func (s *SupplierResourceService) Transition(ctx context.Context, user int64, id string, version int64, disconnect bool) (*SupplierResource, error) {
	r, e := s.Get(ctx, user, id)
	if e != nil {
		return nil, e
	}
	r, e = s.repo.Transition(ctx, r.SupplierID, id, version, disconnect)
	if e != nil {
		return nil, e
	}
	if err := s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	return resourceProjection(r), nil
}
func (s *SupplierResourceService) Usage(ctx context.Context, user int64, id string, version int64) (*SupplierResource, error) {
	r, e := s.Get(ctx, user, id)
	if e != nil {
		return nil, e
	}
	if r.Version != version {
		return nil, ErrSupplierResourceVersion
	}
	if r.Status == "disconnected" || r.ProbeState == "reauthorization_required" || !time.Now().Before(r.TokenExpiresAt) {
		return nil, ErrSupplierReimport
	}
	usage, e := s.queryResource(ctx, r)
	if e != nil {
		return nil, e
	}
	r, e = s.repo.SaveUsage(ctx, r.SupplierID, id, version, usage)
	if e != nil {
		return nil, e
	}
	if err := s.projectAdmission(ctx, r); err != nil {
		return nil, err
	}
	return resourceProjection(r), nil
}

func (s *SupplierResourceService) queryResource(ctx context.Context, r *SupplierResource) (*SupplierResourceUsage, error) {
	if !s.Configured() {
		return nil, ErrSupplierVault
	}
	codec, e := credentialcrypto.New(s.wrapper, r.KeyID)
	if e != nil {
		return nil, ErrSupplierVault
	}
	plain, e := codec.Decrypt(ctx, resourceBinding(r), r.Envelope)
	if e != nil {
		return nil, ErrSupplierVault
	}
	defer clear(plain)
	token, _ := plain["access_token"].(string)
	account, _ := plain["account_id"].(string)
	if token == "" || strings.TrimSpace(account) == "" {
		return nil, ErrSupplierVault
	}
	return s.quota.QuerySupplierUsage(ctx, token, account)
}

// This projection is informational; execution always rechecks vault eligibility.
func (s *SupplierResourceService) projectAdmission(ctx context.Context, r *SupplierResource) error {
	r.Schedulable = false
	reader, ok := s.repo.(interface {
		AdmissionState(context.Context, int64, string) (int64, bool, error)
	})
	if !ok {
		return nil
	}
	id, enabled, err := reader.AdmissionState(ctx, r.SupplierID, r.ID)
	if err != nil {
		return err
	}
	r.AccountID, r.Schedulable = id, enabled
	return nil
}
