package service

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrUsageJournalUnavailable = errors.New("usage journal unavailable")
	ErrUsageJournalInvalid     = errors.New("invalid usage journal input")
	ErrUsageJournalConflict    = errors.New("usage journal content conflict")
	ErrUsageJournalFence       = errors.New("usage journal owner fence rejected")
	ErrUsageJournalFull        = errors.New("usage journal pending capacity reached")
	ErrUsageJournalAmbiguous   = errors.New("usage journal commit ambiguous; recover by local lookup only")
)

// IDs and evidence are SHA256 hex digests, never prompts, credentials or raw responses.
type UsageAttemptInput struct {
	ID                string `json:"id"`
	OperationID       string `json:"operation_id"`
	AccountID         int64  `json:"account_id"`
	CredentialVersion int64  `json:"credential_version"`
	RequestDigest     string `json:"request_digest"`
}
type UsageAttemptFence struct {
	AttemptID  string
	Owner      string
	Generation int64
}
type UsageAttempt struct {
	UsageAttemptInput
	Owner      string
	Generation int64
	LeaseUntil time.Time
	SendIntent bool
	Status     string
}
type UsagePending struct {
	AttemptID string
	State     string
	Reason    string
	Revision  int64
}

// No method grants permission to replay upstream. Reclaim only permits local recovery.
// MarkSendIntent must succeed before making exactly one external attempt; an ambiguous
// commit is NOT permission to send even if a later lookup finds send_intent=true.
type UsageAttemptJournal interface {
	Create(context.Context, UsageAttemptInput) error
	Claim(context.Context, string, string, time.Duration) (*UsageAttemptFence, error)
	MarkSendIntent(context.Context, UsageAttemptFence) error
	Observe(context.Context, UsageAttemptFence, UsageFactInput) (*UsageFactRevision, error)
	Lookup(context.Context, string) (*UsageAttempt, error)
	LookupObservation(context.Context, string, string) (*UsageFactRevision, error)
	Pending(context.Context, int) ([]UsagePending, error)
}

var usageDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidUsageDigest(s string) bool { return usageDigest.MatchString(s) }
func (a UsageAttemptInput) Validate() error {
	if !ValidUsageDigest(a.ID) || !ValidUsageDigest(a.OperationID) || !ValidUsageDigest(a.RequestDigest) || a.AccountID <= 0 || a.CredentialVersion <= 0 {
		return ErrUsageJournalInvalid
	}
	return nil
}
func (f UsageAttemptFence) Validate() error {
	if !ValidUsageDigest(f.AttemptID) || !ValidUsageDigest(f.Owner) || f.Generation <= 0 {
		return ErrUsageJournalInvalid
	}
	return nil
}
