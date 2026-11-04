package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

// Pointer values preserve reported zero versus unreported/null. No raw JSON is
// accepted: only explicitly whitelisted numeric metrics cross the durable boundary.
type UsageMetric struct {
	Name       string   `json:"name"`
	Value      *float64 `json:"value"`
	Unit       string   `json:"unit"`
	UnitPlural string   `json:"unit_plural,omitempty"`
}
type UsageFactInput struct {
	ObservationID  string        `json:"observation_id"`
	Source         string        `json:"source"`
	Quality        string        `json:"quality"`
	Partial        bool          `json:"partial"`
	Outcome        string        `json:"outcome"`
	EvidenceDigest string        `json:"evidence_digest"`
	Supersedes     int64         `json:"supersedes"`
	Metrics        []UsageMetric `json:"metrics"`
}
type UsageFactRevision struct {
	UsageFactInput
	AttemptID     string `json:"attempt_id"`
	Revision      int64  `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

func (f UsageFactInput) Validate() error {
	if !ValidUsageDigest(f.ObservationID) || !ValidUsageDigest(f.EvidenceDigest) || f.Supersedes < 0 || len(f.Metrics) > 12 {
		return ErrUsageJournalInvalid
	}
	switch f.Source {
	case "provider_reported", "local_estimated", "display_simulated", "none":
	default:
		return ErrUsageJournalInvalid
	}
	switch f.Quality {
	case "reported", "estimated", "simulated", "unknown":
	default:
		return ErrUsageJournalInvalid
	}
	if (f.Source == "provider_reported" && f.Quality != "reported") || (f.Source == "local_estimated" && f.Quality != "estimated") || (f.Source == "display_simulated" && f.Quality != "simulated") || (f.Source == "none" && (f.Quality != "unknown" || len(f.Metrics) != 0)) {
		return ErrUsageJournalInvalid
	}
	switch f.Outcome {
	case "observed", "usage_unknown", "timeout", "cancelled", "transport_error", "truncated":
	default:
		return ErrUsageJournalInvalid
	}
	seen := map[string]bool{}
	for _, m := range f.Metrics {
		switch m.Name {
		case "input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens", "usage", "credits", "context_percent":
		default:
			return ErrUsageJournalInvalid
		}
		switch m.Unit {
		case "token", "tokens", "credit", "credits", "unit", "units", "percent":
		default:
			return ErrUsageJournalInvalid
		}
		if m.UnitPlural != "" && m.UnitPlural != "tokens" && m.UnitPlural != "credits" && m.UnitPlural != "units" && m.UnitPlural != "percent" {
			return ErrUsageJournalInvalid
		}
		if seen[m.Name] {
			return ErrUsageJournalInvalid
		}
		seen[m.Name] = true
		if m.Value != nil && (math.IsNaN(*m.Value) || math.IsInf(*m.Value, 0) || *m.Value < 0) {
			return ErrUsageJournalInvalid
		}
	}
	return nil
}
func (f UsageFactInput) ContentHash() (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", ErrUsageJournalInvalid
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
