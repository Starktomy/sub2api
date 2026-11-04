package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/google/uuid"
)

func kiroDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ExecutePrivate is an operator-controlled integration seam, not a public or
// billable route. It still requires the durable send-intent/usage journal. The
// caller supplies a fresh operation identifier; reuse cannot replay generation.
// Invocation UUID is correlation only, NOT provider-guaranteed idempotency.
func (s *KiroNativeService) ExecutePrivate(ctx context.Context, accountID int64, operationID string, input kiro.Request, consume func(kiro.Event) error) (kiro.Result, error) {
	var result kiro.Result
	if s == nil || s.journal == nil || !ValidUsageDigest(operationID) {
		return result, ErrUsageJournalUnavailable
	}
	if consume == nil {
		return result, kiro.ErrConfig
	}
	// Load/refresh commits a rotated token before generation is eligible to send.
	r, _, err := s.Inference(ctx, accountID)
	if err != nil {
		return result, err
	}
	bearer, profile, err := s.vault.cliCredentials(ctx, r, time.Now().Add(30*time.Second))
	if err != nil {
		return result, err
	}
	if err = bindCLIProfile(input.ProfileARN, profile); err != nil {
		return result, err
	}
	input.ProfileARN = profile
	document, err := json.Marshal(input)
	if err != nil {
		return result, kiro.ErrRequest
	}
	defer clear(document)
	id := kiroDigest([]byte("kiro-attempt/v1/" + operationID))
	owner := kiroDigest([]byte(uuid.NewString()))
	if err = s.journal.Create(ctx, UsageAttemptInput{ID: id, OperationID: operationID, AccountID: accountID, CredentialVersion: r.Binding.Version, RequestDigest: kiroDigest(document)}); err != nil {
		return result, err
	}
	fence, err := s.journal.Claim(ctx, id, owner, 3*time.Minute)
	if err != nil {
		return result, err
	}
	transport, err := kiro.NewTransport(s.client, kiro.TransportConfig{Endpoint: "cli", Region: r.Region})
	if err != nil {
		return result, err
	}
	if err = s.journal.MarkSendIntent(ctx, *fence); err != nil {
		return result, err
	}
	evidence := sha256.New()
	partial := false
	meteringCount := 0
	result, err = transport.Execute(ctx, input, kiro.Auth{BearerToken: bearer, UserAgent: r.UserAgent, AmzUserAgent: r.AmzUserAgent, InvocationID: uuid.NewString()}, func(e kiro.Event) error {
		evidence.Write([]byte(e.Type()))
		evidence.Write(e.Raw())
		if a, ok := e.Assistant(); ok && a.Content != "" {
			partial = true
		}
		if _, ok := e.Tool(); ok {
			partial = true
		}
		if m, ok := e.Metering(); ok && meteringCount < 254 {
			meteringCount++
			metrics := []UsageMetric{{Name: "usage", Value: m.Usage, Unit: m.Unit, UnitPlural: m.UnitPlural}}
			fact := UsageFactInput{ObservationID: kiroDigest([]byte(id + "/metering/" + strconv.Itoa(meteringCount))), Source: "provider_reported", Quality: "reported", Partial: true, Outcome: "observed", EvidenceDigest: kiroDigest(e.Raw()), Metrics: metrics}
			// Unknown provider units are not silently reinterpreted. Preserve the
			// digest and unknown final fact instead; never estimate or sum events.
			if fact.Validate() == nil {
				if _, err := s.journal.Observe(ctx, *fence, fact); err != nil {
					return err
				}
			}
		}
		// Do not store prompts/output/token-looking unknown payloads as evidence.
		// Raw frames are transient; durable evidence is a digest only.
		return consume(e)
	})
	outcome := "usage_unknown"
	if errors.Is(err, kiro.ErrCanceled) || errors.Is(ctx.Err(), context.Canceled) {
		outcome = "cancelled"
	} else if errors.Is(err, kiro.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		outcome = "timeout"
	} else if err != nil && !errors.Is(err, kiro.ErrIncomplete) {
		outcome = "transport_error"
	}
	fact := UsageFactInput{ObservationID: kiroDigest([]byte(id + "/terminal")), Source: "none", Quality: "unknown", Partial: partial, Outcome: outcome, EvidenceDigest: hex.EncodeToString(evidence.Sum(nil))}
	durable, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, observeErr := s.journal.Observe(durable, *fence, fact); observeErr != nil {
		return result, observeErr
	}
	return result, err
}
