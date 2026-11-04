package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// KiroObservationError contains only bounded diagnostics, never response bodies.
type KiroObservationError struct {
	Kind       string
	StatusCode int
	Code       string
	Reason     string
}

// Categorize server messages without exposing arbitrary strings or identifiers.
func kiroObservationReason(body []byte) string {
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "unclassified"
	}
	message := strings.ToLower(v.Message)
	field := ""
	for _, candidate := range []string{"profilearn", "origin", "maxresults", "nexttoken", "modelid", "authorization", "token"} {
		if strings.Contains(message, candidate) {
			field = candidate
			break
		}
	}
	if field != "" {
		for _, reason := range []string{"required", "missing", "invalid", "unsupported", "not supported", "must", "null", "enum", "constraint"} {
			if strings.Contains(message, reason) {
				return field + ":" + strings.ReplaceAll(reason, " ", "_")
			}
		}
		return field + ":rejected"
	}
	for _, reason := range []string{"not supported", "unsupported", "not authorized", "access denied", "invalid request", "validation"} {
		if strings.Contains(message, reason) {
			return strings.ReplaceAll(reason, " ", "_")
		}
	}
	return "unclassified"
}

// Return only recognized protocol error categories, never arbitrary server text.
func kiroObservationCode(header string, body []byte) string {
	var v struct {
		Type    string `json:"__type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &v)
	candidate := header + " " + v.Type + " " + v.Code
	for _, code := range []string{"ValidationException", "AccessDeniedException", "UnauthorizedException", "UnrecognizedClientException", "InvalidRequestException", "UnknownOperationException", "ThrottlingException", "ResourceNotFoundException"} {
		if strings.Contains(candidate, code) {
			return code
		}
	}
	return "unclassified"
}
func (e *KiroObservationError) Error() string {
	return fmt.Sprintf("kiro: observation %s (HTTP %d)", e.Kind, e.StatusCode)
}
func (e *KiroObservationError) Unwrap() error { return ErrKiroUnavailable }

// The optional interface keeps the inference repository contract independent.
type KiroRuntimeRepository interface {
	PatchRuntime(context.Context, *KiroNativeRecord, KiroRuntimePatch, []byte) error
	SaveSnapshot(context.Context, *KiroNativeRecord, string, json.RawMessage) error
}
type KiroRuntimePatch struct {
	ExpectedVersion      int64           `json:"expected_version"`
	ExpectedRefreshOwner string          `json:"expected_refresh_owner,omitempty"`
	Operation            string          `json:"operation"`
	UserAgent            *string         `json:"user_agent,omitempty"`
	AmzUserAgent         *string         `json:"amz_user_agent,omitempty"`
	MachineID            *string         `json:"machine_id,omitempty"`
	Credential           json.RawMessage `json:"credential,omitempty"`
}

func (KiroRuntimePatch) String() string   { return "[redacted Kiro patch]" }
func (KiroRuntimePatch) GoString() string { return "[redacted Kiro patch]" }

type KiroRuntimeStatus struct {
	AccountID         int64              `json:"account_id"`
	CredentialRef     string             `json:"credential_ref"`
	CredentialVersion int64              `json:"credential_version"`
	AuthMode          KiroCredentialMode `json:"auth_mode"`
	Region            string             `json:"region"`
	RefreshState      string             `json:"refresh_state"`
	RefreshOwner      string             `json:"refresh_owner"`
	Paused            bool               `json:"paused"`
	Readiness         string             `json:"readiness"`
	UserAgent         string             `json:"user_agent"`
	AmzUserAgent      string             `json:"amz_user_agent"`
	MachineID         string             `json:"machine_id"`
	Models            json.RawMessage    `json:"models"`
	Quota             json.RawMessage    `json:"quota"`
}

func unknownKiroSnapshot(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage(`{"state":"unknown","source":"not_observed"}`)
	}
	return v
}
func (s *KiroNativeService) Runtime(ctx context.Context, id int64) (*KiroRuntimeStatus, error) {
	if s == nil || s.repo == nil {
		return nil, ErrKiroUnavailable
	}
	r, e := s.repo.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	return &KiroRuntimeStatus{AccountID: id, CredentialRef: r.Binding.ResourceUUID, CredentialVersion: r.Binding.Version, AuthMode: r.Binding.Mode, Region: r.Region, RefreshState: r.RefreshState, RefreshOwner: r.RefreshOwner, Paused: true, Readiness: "settlement_required", UserAgent: r.UserAgent, AmzUserAgent: r.AmzUserAgent, MachineID: r.MachineID, Models: unknownKiroSnapshot(r.ModelsSnapshot), Quota: unknownKiroSnapshot(r.QuotaSnapshot)}, nil
}
func (s *KiroNativeService) PatchRuntime(ctx context.Context, id int64, p KiroRuntimePatch) (*KiroRuntimeStatus, error) {
	if s == nil || s.vault == nil {
		return nil, ErrKiroUnavailable
	}
	repo, ok := s.repo.(KiroRuntimeRepository)
	if !ok {
		return nil, ErrKiroUnavailable
	}
	r, e := s.repo.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	if p.ExpectedVersion != r.Binding.Version {
		return nil, ErrKiroRefreshBarrier
	}
	var envelope []byte
	switch p.Operation {
	case "pause":
		if p.Credential != nil || p.UserAgent != nil || p.AmzUserAgent != nil || p.MachineID != nil {
			return nil, ErrKiroCredentialInvalid
		}
	case "configure":
		if p.Credential != nil || r.RefreshState != "idle" {
			return nil, ErrKiroRefreshBarrier
		}
	case "reimport":
		if len(p.Credential) == 0 || p.ExpectedRefreshOwner != r.RefreshOwner {
			return nil, ErrKiroRefreshBarrier
		}
		// Credential endpoint/region/device values cannot redirect this immutable binding.
		var scope struct {
			Region     string `json:"region"`
			AuthRegion string `json:"authRegion"`
			APIRegion  string `json:"apiRegion"`
			MachineID  string `json:"machineId"`
		}
		if json.Unmarshal(p.Credential, &scope) != nil {
			return nil, ErrKiroCredentialInvalid
		}
		for _, region := range []string{scope.Region, scope.AuthRegion, scope.APIRegion} {
			if region != "" && region != r.Region {
				return nil, ErrKiroCredentialInvalid
			}
		}
		if scope.MachineID != "" {
			m, valid := normalizeKiroMachineID(scope.MachineID)
			if !valid {
				return nil, ErrKiroCredentialInvalid
			}
			p.MachineID = &m
		}
	default:
		return nil, ErrKiroCredentialInvalid
	}
	if p.UserAgent != nil {
		if !kiroSafeHeader(*p.UserAgent) {
			return nil, ErrKiroCredentialInvalid
		}
		r.UserAgent = *p.UserAgent
	}
	if p.AmzUserAgent != nil {
		if !kiroSafeHeader(*p.AmzUserAgent) {
			return nil, ErrKiroCredentialInvalid
		}
		r.AmzUserAgent = *p.AmzUserAgent
	}
	if p.MachineID != nil {
		m := *p.MachineID
		if m != "" {
			var valid bool
			m, valid = normalizeKiroMachineID(m)
			if !valid {
				return nil, ErrKiroCredentialInvalid
			}
		}
		r.MachineID = m
	}
	// Every mutation gets a fresh encrypted version, so stale admin/refresh writers fence out.
	b := r.Binding
	b.Version++
	if p.Operation == "reimport" {
		envelope, _, e = s.vault.ImportKiroRS(ctx, b, p.Credential, time.Now())
	} else {
		var doc map[string]any
		var identity string
		identity, e = kiroCredentialIdentity(r.Binding)
		if e == nil {
			doc, e = s.vault.codec.Decrypt(ctx, identity, r.Envelope)
		}
		if e == nil {
			defer clear(doc)
			identity, e = kiroCredentialIdentity(b)
			if e == nil {
				envelope, e = s.vault.codec.Encrypt(ctx, identity, doc)
			}
		}
	}
	if e == nil && p.Operation == "reimport" && r.RefreshState != "idle" {
		// Re-encrypting the same uncertain refresh token is not reauthorization.
		oldID, err := kiroCredentialIdentity(r.Binding)
		if err != nil {
			return nil, ErrKiroCredentialInvalid
		}
		oldDoc, err := s.vault.codec.Decrypt(ctx, oldID, r.Envelope)
		if err != nil {
			return nil, ErrKiroCredentialInvalid
		}
		defer clear(oldDoc)
		newID, err := kiroCredentialIdentity(b)
		if err != nil {
			return nil, ErrKiroCredentialInvalid
		}
		newDoc, err := s.vault.codec.Decrypt(ctx, newID, envelope)
		if err != nil {
			return nil, ErrKiroCredentialInvalid
		}
		defer clear(newDoc)
		var oldSecret, newSecret kiroSecret
		oldText, oldOK := oldDoc["kiro_secret"].(string)
		newText, newOK := newDoc["kiro_secret"].(string)
		if !oldOK || !newOK || json.Unmarshal([]byte(oldText), &oldSecret) != nil || json.Unmarshal([]byte(newText), &newSecret) != nil || newSecret.RefreshToken == "" || newSecret.RefreshToken == oldSecret.RefreshToken {
			return nil, ErrKiroRefreshBarrier
		}
	}
	if e != nil {
		return nil, ErrKiroCredentialInvalid
	}
	if e = repo.PatchRuntime(ctx, r, p, envelope); e != nil {
		return nil, e
	}
	return s.Runtime(ctx, id)
}

// Observe is operator-triggered only. Never refreshes or runs inference. References:
// Quota RPC: official kiro-cli 3.0.0 decrypted GetUsageLimits request.
// Models RPC: official kiro-cli 3.0.0 decrypted request, verified with test access token.
// Models and quota use the SSO control token when inference is an IdC identity token.
func (s *KiroNativeService) Observe(ctx context.Context, id int64, kind string) (*KiroRuntimeStatus, error) {
	if s == nil || s.vault == nil || s.client == nil || s.client.Transport == nil || s.client.Jar != nil {
		return nil, ErrKiroUnavailable
	}
	repo, ok := s.repo.(KiroRuntimeRepository)
	if !ok {
		return nil, ErrKiroUnavailable
	}
	r, e := s.repo.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	if r.RefreshState != "idle" {
		return nil, ErrKiroRefreshBarrier
	}
	source := "unsupported_reference"
	state := "unknown"
	var data any
	if kind == "quota" || kind == "models" {
		if r.Region != "us-east-1" && r.Region != "us-west-2" {
			return nil, ErrKiroCredentialInvalid
		}
		identity, e := kiroCredentialIdentity(r.Binding)
		if e != nil {
			return nil, e
		}
		doc, e := s.vault.codec.Decrypt(ctx, identity, r.Envelope)
		if e != nil {
			return nil, ErrKiroCredentialInvalid
		}
		defer clear(doc)
		text, ok := doc["kiro_secret"].(string)
		if !ok {
			return nil, ErrKiroCredentialInvalid
		}
		secret, e := parseKiroSecret([]byte(text), r.Binding.Mode, time.Now())
		if e != nil {
			return nil, e
		}
		token := secret.InferenceToken
		if (kind == "quota" || kind == "models") && r.Binding.Mode == KiroCredentialIdC && secret.InferenceTokenType == kiroIdCIdentityToken {
			token = secret.SSOAccessToken
			if token == "" || secret.SSOExpiresAt <= time.Now().Unix() {
				return nil, ErrKiroCredentialRefreshRequired
			}
		}
		if token == "" || secret.InferenceTokenType == kiroRefreshOnly {
			return nil, ErrKiroCredentialRefreshRequired
		}
		source = "https://management." + r.Region + ".kiro.dev/"
		var req *http.Request
		{
			profileARN := secret.ProfileARN
			// Public service default embedded by official kiro-cli 3.0.0, confirmed
			// by a decrypted successful models RPC. Not an account-owned ARN.
			if profileARN == "" && r.Region == "us-east-1" {
				profileARN = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
			}
			req, e = http.NewRequestWithContext(ctx, http.MethodPost, source+"?origin=KIRO_CLI", strings.NewReader(`{"origin":"KIRO_CLI"}`))
			if profileARN != "" {
				if !kiroValidProfileARN(profileARN, r.Region) {
					return nil, ErrKiroCredentialInvalid
				}
				payload, marshalErr := json.Marshal(map[string]string{"origin": "KIRO_CLI", "profileArn": profileARN})
				if marshalErr != nil {
					return nil, ErrKiroCredentialInvalid
				}
				req, e = http.NewRequestWithContext(ctx, http.MethodPost, source+"?"+url.Values{"origin": {"KIRO_CLI"}, "profileArn": {profileARN}}.Encode(), strings.NewReader(string(payload)))
			}
			if e == nil {
				req.Header.Set("Content-Type", "application/x-amz-json-1.0")
				// Official kiro-cli 3.0.0 embeds this control-plane Smithy target.
				target := "KiroControlPlaneBearerService.ListAvailableModels"
				if kind == "quota" {
					target = "AmazonCodeWhispererService.GetUsageLimits"
				}
				req.Header.Set("X-Amz-Target", target)
			}
		}
		if e != nil {
			return nil, ErrKiroCredentialInvalid
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", r.UserAgent)
		if kind != "models" {
			req.Header.Set("X-Amz-User-Agent", r.AmzUserAgent)
			req.Header.Set("Amz-Sdk-Request", "attempt=1; max=1")
		}
		req.Close = true
		client := *s.client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client.Timeout = 60 * time.Second
		resp, e := client.Do(req)
		if e != nil {
			return nil, &KiroObservationError{Kind: "transport"}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			code := kiroObservationCode(resp.Header.Get("X-Amzn-Errortype"), errorBody)
			reason := kiroObservationReason(errorBody)
			clear(errorBody)
			return nil, &KiroObservationError{Kind: "http_status", StatusCode: resp.StatusCode, Code: code, Reason: reason}
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
		if e != nil || len(raw) > 1<<20 {
			return nil, ErrKiroCredentialInvalid
		}
		defer clear(raw)
		if kind == "models" {
			var models struct {
				Models []struct {
					ModelID   string `json:"modelId"`
					ModelName string `json:"modelName,omitempty"`
				} `json:"models"`
			}
			if json.Unmarshal(raw, &models) != nil || models.Models == nil || len(models.Models) > 1000 {
				return nil, ErrKiroCredentialInvalid
			}
			for _, model := range models.Models {
				if len(model.ModelID) == 0 || len(model.ModelID) > 256 || len(model.ModelName) > 256 {
					return nil, ErrKiroCredentialInvalid
				}
			}
			data = models
		} else {
			var quota struct {
				NextDateReset      *float64 `json:"nextDateReset,omitempty"`
				UsageBreakdownList []struct {
					CurrentUsage              *float64 `json:"currentUsage,omitempty"`
					CurrentUsageWithPrecision *float64 `json:"currentUsageWithPrecision,omitempty"`
					UsageLimit                *float64 `json:"usageLimit,omitempty"`
					UsageLimitWithPrecision   *float64 `json:"usageLimitWithPrecision,omitempty"`
					NextDateReset             *float64 `json:"nextDateReset,omitempty"`
				} `json:"usageBreakdownList"`
			}
			if json.Unmarshal(raw, &quota) != nil || (quota.NextDateReset == nil && quota.UsageBreakdownList == nil) {
				return nil, ErrKiroCredentialInvalid
			}
			data = quota
		}
		state = "known"
	} else {
		return nil, ErrKiroCredentialInvalid
	}
	snapshot, e := json.Marshal(map[string]any{"state": state, "source": source, "observed_at": time.Now().UTC(), "data": data})
	if e != nil {
		return nil, ErrKiroCredentialInvalid
	}
	if e = repo.SaveSnapshot(ctx, r, kind, snapshot); e != nil {
		return nil, e
	}
	return s.Runtime(ctx, id)
}
