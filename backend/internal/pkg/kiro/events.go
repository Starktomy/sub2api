// Copyright (c) 2026 hank9999. Licensed under MIT.
// Copyright (c) 2026 Harllan He. Licensed under MIT.
// See provenance.go and LICENSE for adaptation sources.

package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var (
	ErrEvent      = errors.New("kiro: invalid event")
	ErrUpstream   = errors.New("kiro: upstream failure")
	ErrState      = errors.New("kiro: invalid event sequence")
	ErrIncomplete = errors.New("kiro: generation completion unestablished")
)

// Request is an envelope only. Raw conversation state is caller-owned and is
// never rewritten, paired, assigned identities, or otherwise converted here.
type Request struct {
	ConversationState            json.RawMessage `json:"conversationState"`
	ProfileARN                   string          `json:"profileArn,omitempty"`
	AdditionalModelRequestFields json.RawMessage `json:"additionalModelRequestFields,omitempty"`
}

type AssistantResponseEvent struct {
	Content string `json:"content"`
	ModelID string `json:"modelId,omitempty"`
}
type ToolUseEvent struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
	Input     string `json:"input"`
	Stop      bool   `json:"stop"`
}

// MetadataEvent preserves the upstream reason without treating arbitrary values
// as verified success. Capture established this field, not its full enum contract.
type MetadataEvent struct {
	StopReason string      `json:"stopReason"`
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`
}

// TokenUsage follows the official Smithy wire schema. Pointer counts retain
// unknown versus explicitly reported zero; credits never populate these fields.
type TokenUsage struct {
	UncachedInputTokens   *int64 `json:"uncachedInputTokens"`
	OutputTokens          *int64 `json:"outputTokens"`
	TotalTokens           *int64 `json:"totalTokens"`
	CacheReadInputTokens  *int64 `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens,omitempty"`
}

// ValidComplete requires the mandatory official fields and nonnegative counts.
// It does not infer absent caches or reconstruct missing input/output splits.
func (u *TokenUsage) ValidComplete() bool {
	if u == nil || u.UncachedInputTokens == nil || u.OutputTokens == nil || u.TotalTokens == nil {
		return false
	}
	for _, n := range []*int64{u.UncachedInputTokens, u.OutputTokens, u.TotalTokens, u.CacheReadInputTokens, u.CacheWriteInputTokens} {
		if n != nil && (*n < 0 || *n > 1<<53) {
			return false
		}
	}
	return true
}

type ContextUsageEvent struct {
	ContextUsagePercentage *float64 `json:"contextUsagePercentage,omitempty"`
}

// MeteringEvent reports the upstream fields without interpreting units or
// converting usage to input/output tokens. Pointers distinguish absent from zero.
// The fork declares the cache fields in snake_case (no per-field rename).
type MeteringEvent struct {
	Unit                     string   `json:"unit,omitempty"`
	UnitPlural               string   `json:"unitPlural,omitempty"`
	Usage                    *float64 `json:"usage"`
	CacheReadInputTokens     *int32   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int32   `json:"cache_creation_input_tokens,omitempty"`
}

// Event owns a bounded copy of the raw payload, including unknown events and
// upstream errors. Payloads may contain secrets: callers must not log them.
// Typed accessors return fresh values; modifying them cannot affect the event.
type Event struct {
	messageType, eventType string
	raw                    []byte
	headers                []Header
}

func (e Event) Headers() []Header   { return (Frame{headers: e.headers}).Headers() }
func (e Event) MessageType() string { return e.messageType }
func (e Event) Type() string        { return e.eventType }
func (e Event) Raw() []byte         { return append([]byte(nil), e.raw...) }
func (e Event) Assistant() (AssistantResponseEvent, bool) {
	var v AssistantResponseEvent
	if e.eventType != "assistantResponseEvent" || e.messageType != "event" {
		return v, false
	}
	err := json.Unmarshal(e.raw, &v)
	return v, err == nil
}
func (e Event) Tool() (ToolUseEvent, bool) {
	var v ToolUseEvent
	if e.eventType != "toolUseEvent" || e.messageType != "event" {
		return v, false
	}
	err := json.Unmarshal(e.raw, &v)
	return v, err == nil
}
func (e Event) Metadata() (MetadataEvent, bool) {
	var v MetadataEvent
	if e.eventType != "metadataEvent" || e.messageType != "event" {
		return v, false
	}
	err := json.Unmarshal(e.raw, &v)
	return v, err == nil
}

func (e Event) Context() (ContextUsageEvent, bool) {
	var v ContextUsageEvent
	if e.eventType != "contextUsageEvent" || e.messageType != "event" {
		return v, false
	}
	err := json.Unmarshal(e.raw, &v)
	return v, err == nil
}
func (e Event) Metering() (MeteringEvent, bool) {
	var v MeteringEvent
	if e.eventType != "meteringEvent" || e.messageType != "event" {
		return v, false
	}
	err := json.Unmarshal(e.raw, &v)
	return v, err == nil
}

// DecodeEvent accepts only frames produced by Decoder. Unknown event payloads
// need not be JSON. maxPayloadBytes <= 0 selects a 1 MiB bound.
func DecodeEvent(f Frame, maxPayloadBytes int) (Event, error) {
	if maxPayloadBytes <= 0 {
		maxPayloadBytes = 1024 * 1024
	}
	if len(f.payload) > maxPayloadBytes {
		return Event{}, ErrLimit
	}
	mt, ok := f.stringHeader(":message-type")
	if !ok {
		return Event{}, ErrEvent
	}
	et, _ := f.stringHeader(":event-type")
	if mt != "event" && mt != "error" && mt != "exception" {
		return Event{}, ErrEvent
	}
	if mt == "event" && et == "" {
		return Event{}, ErrEvent
	}
	e := Event{messageType: mt, eventType: et, raw: append([]byte(nil), f.payload...), headers: f.Headers()}
	if mt != "event" {
		return e, nil
	}
	var target any
	switch et {
	case "assistantResponseEvent":
		target = &AssistantResponseEvent{}
	case "toolUseEvent":
		target = &ToolUseEvent{}
	case "metadataEvent":
		target = &MetadataEvent{}
	case "contextUsageEvent":
		target = &ContextUsageEvent{}
	case "meteringEvent":
		target = &MeteringEvent{}
	default:
		return e, nil
	}
	b := bytes.TrimSpace(e.raw)
	if len(b) == 0 || b[0] != '{' || !utf8.Valid(b) || json.Unmarshal(b, target) != nil {
		return Event{}, ErrEvent
	}
	switch v := target.(type) {
	case *ToolUseEvent:
		// Continuation payloads may omit name/ID. Attempt resolves only an
		// existing unambiguous tool; decoding alone never invents identity.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(b, &fields)
		if (v.Name != "" && v.ToolUseID == "") || (v.Name == "" && fields["input"] == nil && !v.Stop) {
			return Event{}, ErrEvent
		}
	case *MeteringEvent:
		if v.Usage == nil {
			return Event{}, ErrEvent
		}
	}
	return e, nil
}

// NextEvent makes event validation failures sticky as well as framing errors.
// Error/exception events are returned for inspection; Attempt rejects them.
func (d *Decoder) NextEvent(maxPayloadBytes int) (Event, error) {
	f, err := d.Decode()
	if err != nil {
		return Event{}, err
	}
	e, err := DecodeEvent(f, maxPayloadBytes)
	if err != nil {
		d.terminal = err
	}
	return e, err
}
