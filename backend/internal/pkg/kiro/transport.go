// Copyright (c) 2026 hank9999. Licensed under MIT.
// See provenance.go and LICENSE for protocol reference attribution.

package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrConfig    = errors.New("kiro: invalid transport configuration")
	ErrEndpoint  = errors.New("kiro: unsupported endpoint or region")
	ErrIdentity  = errors.New("kiro: unsupported missing caller protocol identity")
	ErrRequest   = errors.New("kiro: invalid request envelope")
	ErrAuth      = errors.New("kiro: authentication rejected")
	ErrForbidden = errors.New("kiro: access forbidden")
	ErrThrottled = errors.New("kiro: throttled")
	ErrServer    = errors.New("kiro: upstream server failure")
	ErrHTTP      = errors.New("kiro: unexpected HTTP response")
	ErrTransport = errors.New("kiro: transport failure")
	ErrCanceled  = errors.New("kiro: canceled")
	ErrTimeout   = errors.New("kiro: timed out")
	ErrCallback  = errors.New("kiro: event consumer failed")
)

// Phase is an observation, not a retry recommendation. Once Do is invoked even
// an error cannot prove the generation was not accepted. No outcome means success.
type Phase string

const (
	NotSent         Phase = "not_sent"
	OutcomeUnknown  Phase = "outcome_unknown"
	ResponseStarted Phase = "response_started"
)

type Result struct {
	Phase      Phase
	StatusCode int
	Events     int
	// Bounded allowlisted diagnostics; never arbitrary upstream text.
	ErrorCode  string
	ErrorField string
	// RetryAfter is metadata only. No sleep, retry, refresh or failover occurs.
	RetryAfter        time.Duration
	RetryAfterPresent bool
	// CompletionVerified requires captured END_TURN and clean framing/EOF.
	CompletionVerified bool
	StopReason         string
	// TransportEOF proves only clean framing/transport exhaustion, not an
	// application terminal or complete tool input. A one-event text response
	// can legitimately have this shape; its completion remains unverified.
	TransportEOF bool
}

// Auth is supplied by the caller, never derived from tokens or host identity.
// The IDE identity strings and invocation ID must be legitimate caller material;
// this package cannot establish their provenance or whether the service accepts
// them. Missing material fails closed, rather than inventing a KiroIDE identity.
// API-key mode and arbitrary additional headers are deliberately unsupported.
type Auth struct {
	BearerToken  string
	UserAgent    string
	AmzUserAgent string
	InvocationID string
}

// Formatting and JSON serialization never reveal caller authentication material.
func (Auth) String() string               { return "kiro.Auth(redacted)" }
func (Auth) GoString() string             { return "kiro.Auth(redacted)" }
func (Auth) MarshalJSON() ([]byte, error) { return []byte(`"kiro.Auth(redacted)"`), nil }

type TransportConfig struct {
	Endpoint         string // "ide" or captured us-east-1 "cli"
	Region           string // explicitly allowlisted below; never interpolated into a host
	MaxRequestBytes  int
	MaxResponseBytes int64
	MaxEvents        int
	MaxPayloadBytes  int
	FrameLimits      Limits
	Timeout          time.Duration
}

// Transport is immutable and safe to reuse; each Execute owns a fresh Attempt.
// No production endpoint override or generic Doer is accepted.
type Transport struct {
	client   http.Client
	endpoint string
	config   TransportConfig
}

// NewTransport requires an explicit trusted client AND explicit RoundTripper.
// Transport must honor context and perform exactly one exchange: no redirects,
// retry, credential logging/forwarding, or fallback. That trust is a caller
// integration contract; arbitrary Doers cannot be made safe by wrapping Do.
// The client is copied (including its timeout); redirect policy is replaced and
// cookies are rejected. Caller must not mutate the underlying transport in use.
func NewTransport(client *http.Client, c TransportConfig) (*Transport, error) {
	if client == nil || client.Transport == nil || client.Jar != nil {
		return nil, ErrConfig
	}
	var endpoint string
	if c.Endpoint != "ide" && c.Endpoint != "cli" {
		return nil, ErrEndpoint
	}
	switch c.Region {
	case "us-east-1":
		endpoint = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
	case "us-west-2":
		endpoint = "https://q.us-west-2.amazonaws.com/generateAssistantResponse"
	default:
		return nil, ErrEndpoint
	}
	if c.Endpoint == "cli" {
		// Same-version official CLI capture establishes only this regional host.
		if c.Region != "us-east-1" {
			return nil, ErrEndpoint
		}
		endpoint = "https://runtime.us-east-1.kiro.dev/"
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 1024 * 1024
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 64 * 1024 * 1024
	}
	if c.MaxEvents == 0 {
		c.MaxEvents = 65536
	}
	if c.MaxPayloadBytes == 0 {
		c.MaxPayloadBytes = 1024 * 1024
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Minute
	}
	if c.MaxRequestBytes < 1 || c.MaxResponseBytes < 1 || c.MaxResponseBytes == 1<<63-1 || c.MaxEvents < 1 || c.MaxPayloadBytes < 1 || c.Timeout < 0 {
		return nil, ErrConfig
	}
	var err error
	c.FrameLimits, err = c.FrameLimits.normalized()
	if err != nil {
		return nil, err
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Transport{client: copyClient, endpoint: endpoint, config: c}, nil
}

func validMaterial(s string) bool {
	if s == "" || len(s) > 8192 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}
func object(raw json.RawMessage) bool {
	b := bytes.TrimSpace(raw)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}
func contextFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return ErrCanceled
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrTimeout
	}
	return nil
}

// Retain a read error only long enough to classify timeout/cancellation; never
// return or format it. Decoder intentionally sanitizes arbitrary reader errors.
type classifiedReader struct {
	reader io.Reader
	err    error
}

func (r *classifiedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.err = err
	return n, err
}

func retryAfter(s string) (time.Duration, bool) {
	// Parse only bounded numeric seconds or HTTP dates; never expose raw headers.
	if len(s) == 0 || len(s) > 128 {
		return 0, false
	}
	var seconds int64
	numeric := true
	for _, c := range s {
		if c < '0' || c > '9' {
			numeric = false
			break
		}
		if seconds > 922337203 {
			return 0, false
		}
		seconds = seconds*10 + int64(c-'0')
	}
	if numeric {
		if seconds > 9223372036 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if d, err := http.ParseTime(s); err == nil {
		wait := time.Until(d)
		if wait < 0 {
			wait = 0
		}
		return wait, true
	}
	return 0, false
}

// Execute sends the exact envelope without history conversion or profile
// injection. Callback receives bounded raw events, including exceptions before
// Attempt rejects them. It must not log secrets; its errors are sanitized. EOF
// is unverified completion (ErrIncomplete), even after text or metering events.
func (t *Transport) Execute(ctx context.Context, input Request, auth Auth, callback func(Event) error) (result Result, err error) {
	result.Phase = NotSent
	if t == nil || ctx == nil {
		return result, ErrConfig
	}
	if err = contextFailure(ctx, nil); err != nil {
		return result, err
	}
	if !validMaterial(auth.BearerToken) || strings.ContainsAny(auth.BearerToken, " \t") {
		return result, ErrAuth
	}
	if !validMaterial(auth.UserAgent) || !validMaterial(auth.AmzUserAgent) || !validMaterial(auth.InvocationID) {
		return result, ErrIdentity
	}
	if t.config.Endpoint == "cli" && len(input.AdditionalModelRequestFields) != 0 {
		return result, ErrRequest
	}
	max := t.config.MaxRequestBytes
	// Bound caller fields before serialization, including worst-case JSON escaping.
	if len(input.ConversationState) > max || len(input.ProfileARN) > max || len(input.AdditionalModelRequestFields) > max {
		return result, ErrLimit
	}
	if !object(input.ConversationState) || (len(input.AdditionalModelRequestFields) > 0 && !object(input.AdditionalModelRequestFields)) {
		return result, ErrRequest
	}
	body, e := json.Marshal(input)
	if e != nil {
		return result, ErrRequest
	}
	if len(body) > max {
		return result, ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, t.config.Timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if e != nil {
		return result, ErrConfig
	}
	// No replay body: even an injected standard transport must not replay POST.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Header.Set("Authorization", "Bearer "+auth.BearerToken)
	req.Header.Set("User-Agent", auth.UserAgent)
	req.Header.Set("x-amz-user-agent", auth.AmzUserAgent)
	req.Header.Set("amz-sdk-invocation-id", auth.InvocationID)
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if t.config.Endpoint == "cli" {
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("X-Amz-Target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
	} else {
		req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	}
	if err = contextFailure(ctx, nil); err != nil {
		return result, err
	}
	result.Phase = OutcomeUnknown
	resp, e := t.client.Do(req)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp != nil {
		result.Phase = ResponseStarted
		result.StatusCode = resp.StatusCode
	}
	if e != nil {
		if err = contextFailure(ctx, e); err != nil {
			return result, err
		}
		return result, ErrTransport
	}
	if resp == nil || resp.Body == nil {
		return result, ErrTransport
	}
	result.RetryAfter, result.RetryAfterPresent = retryAfter(resp.Header.Get("Retry-After"))
	switch {
	case resp.StatusCode == 401:
		return result, ErrAuth
	case resp.StatusCode == 403:
		return result, ErrForbidden
	case resp.StatusCode == 429:
		return result, ErrThrottled
	case resp.StatusCode >= 500 && resp.StatusCode <= 599:
		return result, ErrServer
	case resp.StatusCode != 200:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		var diagnostic struct {
			Message string `json:"message"`
			Type    string `json:"__type"`
		}
		_ = json.Unmarshal(body, &diagnostic)
		for _, code := range []string{"ValidationException", "InvalidRequestException", "UnknownOperationException"} {
			if strings.Contains(diagnostic.Type+resp.Header.Get("X-Amzn-Errortype"), code) {
				result.ErrorCode = code
				break
			}
		}
		message := strings.ToLower(diagnostic.Message)
		for _, field := range []string{"agenttasktype", "conversationid", "additionalmodelrequestfields", "maxtokens", "profilearn", "origin", "modelid", "history", "currentmessage", "content"} {
			if strings.Contains(message, field) {
				result.ErrorField = field
				break
			}
		}
		clear(body)
		return result, ErrHTTP
	}
	reader := &classifiedReader{reader: resp.Body}
	limited := &io.LimitedReader{R: reader, N: t.config.MaxResponseBytes + 1}
	decoder, _ := NewDecoder(limited, t.config.FrameLimits)
	attempt := new(Attempt)
	for {
		if err = contextFailure(ctx, nil); err != nil {
			return result, err
		}
		event, e := decoder.NextEvent(t.config.MaxPayloadBytes)
		if limited.N == 0 {
			return result, ErrLimit
		}
		if err = contextFailure(ctx, reader.err); err != nil {
			return result, err
		}
		if e != nil {
			result.TransportEOF = e == io.EOF
			endErr := attempt.End(e)
			result.CompletionVerified = attempt.CompletionVerified()
			result.StopReason = attempt.StopReason()
			return result, endErr
		}
		if result.Events >= t.config.MaxEvents {
			return result, ErrLimit
		}
		result.Events++
		_, applyErr := attempt.Apply(event)
		if callback != nil {
			if callback(event) != nil {
				return result, ErrCallback
			}
		}
		if applyErr != nil {
			return result, applyErr
		}
	}
}
