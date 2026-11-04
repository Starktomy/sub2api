package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
	"github.com/google/uuid"
)

var (
	ErrKiroUnavailable    = errors.New("kiro: encrypted vault unavailable")
	ErrKiroRefreshBarrier = errors.New("kiro: refresh pending or ambiguous; reauthorization required")
	ErrKiroSettlement     = errors.New("kiro: usage settlement contract unavailable")
)

type KiroNativeImport struct {
	Name         string             `json:"name"`
	Credential   json.RawMessage    `json:"credential"`
	Region       string             `json:"region"`
	AuthMode     KiroCredentialMode `json:"auth_mode"`
	GroupIDs     []int64            `json:"group_ids,omitempty"`
	MachineID    string             `json:"machine_id,omitempty"`
	UserAgent    string             `json:"user_agent"`
	AmzUserAgent string             `json:"amz_user_agent"`
}

func (KiroNativeImport) String() string   { return "[redacted Kiro import]" }
func (KiroNativeImport) GoString() string { return "[redacted Kiro import]" }

type KiroNativeRecord struct {
	Binding                                    KiroCredentialBinding
	Region, UserAgent, AmzUserAgent, MachineID string
	Envelope                                   []byte
	RefreshState                               string
	RefreshOwner                               string
	ModelsSnapshot, QuotaSnapshot              json.RawMessage
}

func (KiroNativeRecord) String() string               { return "[redacted Kiro credential record]" }
func (KiroNativeRecord) GoString() string             { return "[redacted Kiro credential record]" }
func (KiroNativeRecord) MarshalJSON() ([]byte, error) { return nil, ErrKiroCredentialInvalid }

type KiroNativeImportResult struct {
	AccountID         int64  `json:"account_id"`
	CredentialRef     string `json:"credential_ref"`
	CredentialVersion int64  `json:"credential_version"`
	Readiness         string `json:"readiness"`
}

type KiroNativeRepository interface {
	// Create executes encrypt inside a short transaction after assigning account ID.
	Create(context.Context, KiroNativeImport, string, func(KiroCredentialBinding) ([]byte, error)) (*KiroNativeRecord, error)
	Get(context.Context, int64) (*KiroNativeRecord, error)
	BeginRefresh(context.Context, *KiroNativeRecord, string) error
	CompleteRefresh(context.Context, *KiroNativeRecord, string, []byte) error
	AmbiguousRefresh(context.Context, *KiroNativeRecord, string) error
}

type KiroNativeService struct {
	repo    KiroNativeRepository
	vault   *KiroCredentialVault
	client  *http.Client
	journal UsageAttemptJournal
}

func NewKiroNativeService(repo KiroNativeRepository, vault *KiroCredentialVault, client *http.Client, journal UsageAttemptJournal) *KiroNativeService {
	return &KiroNativeService{repo: repo, vault: vault, client: client, journal: journal}
}

// ProvideKiroVault requires a dedicated keyring, or an explicitly enabled legacy alias.
// There is no generated key, plaintext fallback, or runtime secret bootstrap.
func ProvideKiroVault(cfg *config.Config) *KiroCredentialVault {
	if cfg == nil {
		return nil
	}
	file, id := cfg.KiroVault.VaultKeyFile, cfg.KiroVault.VaultKeyID
	if cfg.KiroVault.UseSupplierVault {
		file, id = cfg.SupplierPortal.VaultKeyFile, cfg.SupplierPortal.VaultKeyID
	}
	if file == "" || id == "" {
		return nil
	}
	wrapper, err := credentialcrypto.LoadLocalKeyWrapper(file)
	if err != nil {
		return nil
	}
	vault, err := NewKiroCredentialVault(wrapper, id)
	if err != nil {
		return nil
	}
	return vault
}
func kiroSafeHeader(s string) bool {
	if len(s) == 0 || len(s) > 512 || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
func (s *KiroNativeService) Import(ctx context.Context, in KiroNativeImport) (*KiroNativeImportResult, error) {
	if s == nil || s.repo == nil || s.vault == nil {
		return nil, ErrKiroUnavailable
	}
	if len(in.Name) == 0 || len(in.Name) > 100 || strings.TrimSpace(in.Name) != in.Name || len(in.GroupIDs) > 0 || (in.Region != "us-east-1" && in.Region != "us-west-2") || (in.AuthMode != KiroCredentialSocial && in.AuthMode != KiroCredentialIdC) {
		return nil, ErrKiroCredentialInvalid
	}
	if in.UserAgent == "" {
		in.UserAgent = "Sub2API-Kiro-Integration/1.0"
	}
	if in.AmzUserAgent == "" {
		in.AmzUserAgent = "Sub2API-Kiro-Integration/1.0"
	}
	if !kiroSafeHeader(in.UserAgent) || !kiroSafeHeader(in.AmzUserAgent) || len(in.MachineID) > 256 || strings.ContainsAny(in.MachineID, "\r\n\x00") {
		return nil, ErrKiroCredentialInvalid
	}
	// Match the reference's legitimate identity priority: explicit account export
	// overrides operator default. Unlike its fallback, never derive from rotating
	// tokens, fabricate OS/SDK versions, or generate random device identities.
	var exported struct {
		MachineID  string `json:"machineId"`
		Region     string `json:"region"`
		AuthRegion string `json:"authRegion"`
		APIRegion  string `json:"apiRegion"`
	}
	if json.Unmarshal(in.Credential, &exported) != nil {
		return nil, ErrKiroCredentialInvalid
	}
	// This slice has one explicit trusted region. Conflicting exported scopes
	// cannot be silently discarded or used to redirect authentication/inference.
	for _, region := range []string{exported.Region, exported.AuthRegion, exported.APIRegion} {
		if region != "" && region != in.Region {
			return nil, ErrKiroCredentialInvalid
		}
	}
	if exported.MachineID != "" {
		in.MachineID = exported.MachineID
	}
	if in.MachineID != "" {
		var valid bool
		in.MachineID, valid = normalizeKiroMachineID(in.MachineID)
		if !valid {
			return nil, ErrKiroCredentialInvalid
		}
	}
	// Explicit UA templates may reference this stable legitimate ID; no hidden
	// KiroIDE claim or automatic SDK/system-version substitution is introduced.
	if strings.Contains(in.UserAgent, "{machine_id}") || strings.Contains(in.AmzUserAgent, "{machine_id}") {
		if in.MachineID == "" {
			return nil, ErrKiroCredentialInvalid
		}
		in.UserAgent = strings.ReplaceAll(in.UserAgent, "{machine_id}", in.MachineID)
		in.AmzUserAgent = strings.ReplaceAll(in.AmzUserAgent, "{machine_id}", in.MachineID)
		if !kiroSafeHeader(in.UserAgent) || !kiroSafeHeader(in.AmzUserAgent) {
			return nil, ErrKiroCredentialInvalid
		}
	}
	ref := uuid.NewString()
	r, err := s.repo.Create(ctx, in, ref, func(b KiroCredentialBinding) ([]byte, error) {
		envelope, _, err := s.vault.ImportKiroRS(ctx, b, in.Credential, time.Now())
		return envelope, err
	})
	if err != nil {
		return nil, err
	}
	return &KiroNativeImportResult{AccountID: r.Binding.AccountID, CredentialRef: ref, CredentialVersion: 1, Readiness: "settlement_required"}, nil
}

// Inference loads secrets only from durable encrypted storage. Refresh is fenced
// before network I/O and committed encrypted under the next version before any
// inference token is released. Claim/commit uncertainty never permits a replay.
func (s *KiroNativeService) Inference(ctx context.Context, accountID int64) (*KiroNativeRecord, KiroInferenceCredentials, error) {
	var empty KiroInferenceCredentials
	if s == nil || s.repo == nil || s.vault == nil {
		return nil, empty, ErrKiroUnavailable
	}
	r, err := s.repo.Get(ctx, accountID)
	if err != nil {
		return nil, empty, err
	}
	if r.RefreshState != "idle" {
		return nil, empty, ErrKiroRefreshBarrier
	}
	token, err := s.vault.DecryptInference(ctx, r.Binding, r.Envelope, time.Now().Add(30*time.Second))
	if err == nil {
		return r, token, nil
	}
	if !errors.Is(err, ErrKiroCredentialRefreshRequired) && !errors.Is(err, ErrKiroCredentialExpired) {
		return nil, empty, err
	}
	owner := uuid.NewString()
	if err = s.repo.BeginRefresh(ctx, r, owner); err != nil {
		return nil, empty, err
	}
	envelope, err := s.vault.refresh(ctx, r, s.client)
	if err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.repo.AmbiguousRefresh(recovery, r, owner)
		return nil, empty, err
	}
	// A cancelled request must still attempt to persist a rotated token; it must
	// not make the upstream exchange disappear or release the refresh barrier.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.repo.CompleteRefresh(commitCtx, r, owner, envelope); err != nil {
		return nil, empty, ErrKiroRefreshBarrier
	}
	r.Binding.Version++
	r.Envelope = envelope
	if ctx.Err() != nil {
		return nil, empty, ctx.Err()
	}
	token, err = s.vault.DecryptInference(ctx, r.Binding, r.Envelope, time.Now())
	return r, token, err
}

// normalizeKiroMachineID follows kiro.rs's 64-hex / UUID compatibility rule.
// It normalizes caller material, never manufactures a new client identity.
func normalizeKiroMachineID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) == 36 {
		if _, err := uuid.Parse(value); err != nil {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	}
	if len(value) == 32 {
		value += value
	}
	if len(value) != 64 {
		return "", false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return strings.ToLower(value), true
}
