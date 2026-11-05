package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// SupplierAccountingError deliberately does not unwrap an upstream error: an
// ambiguous local commit or upstream send must never trigger generation replay.
type SupplierAccountingError struct{}

func (*SupplierAccountingError) Error() string        { return ErrSupplierAccountingPending.Error() }
func (*SupplierAccountingError) Is(target error) bool { return target == ErrSupplierAccountingPending }

var errSupplierUnsupported = errors.New("supplier execution supports native HTTP Responses only")

type supplierResponsesEligibilityKey struct{}

func WithSupplierResponsesEligibility(ctx context.Context, eligible bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, supplierResponsesEligibilityKey{}, eligible)
}

func supplierResponsesEligible(ctx context.Context) bool {
	return ctx != nil && ctx.Value(supplierResponsesEligibilityKey{}) == true
}

// SupplierResponsesRequestEligible is a server-side capability check, never a
// client-provided scheduling override. Account-specific gates still run later.
func SupplierResponsesRequestEligible(c *gin.Context, body []byte) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodPost || GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return false
	}
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	if path != "/v1/responses" && path != "/responses" && path != "/v1/responses/compact" && path != "/responses/compact" {
		return false
	}
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "background").Bool() {
		return false
	}
	var validTools func(gjson.Result) bool
	validTools = func(tools gjson.Result) bool {
		for _, tool := range tools.Array() {
			switch tool.Get("type").String() {
			case "function", "custom":
			case "namespace":
				if !validTools(tool.Get("tools")) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !validTools(gjson.GetBytes(body, "tools")) {
		return false
	}
	// The first journal version prices text only; reject image/audio/video input
	// before spending rather than discovering unsupported usage after generation.
	var textOnly func(gjson.Result) bool
	textOnly = func(value gjson.Result) bool {
		valid := true
		if value.IsObject() {
			switch value.Get("type").String() {
			case "input_image", "input_audio", "input_video", "image_url", "input_file":
				return false
			}
		}
		if value.IsObject() || value.IsArray() {
			value.ForEach(func(_, child gjson.Result) bool { valid = textOnly(child); return valid })
		}
		return valid
	}
	return textOnly(gjson.GetBytes(body, "input"))
}

type supplierSlotKey struct{}
type supplierDispatchKey struct{}
type supplierSlot struct {
	requestID                            string
	accountID, userID, apiKeyID, groupID int64
}

// WithSupplierDispatchSlot must be called by the handler only after the existing
// user/group/model/account concurrency gates succeed. It is not a replacement
// scheduler and cannot authorize a different selected account.
func WithSupplierDispatchSlot(ctx context.Context, accountID int64, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil || apiKey.User == nil || apiKey.GroupID == nil {
		return ctx
	}
	return context.WithValue(ctx, supplierSlotKey{}, supplierSlot{
		requestID: uuid.NewString(), accountID: accountID, userID: apiKey.User.ID,
		apiKeyID: apiKey.ID, groupID: *apiKey.GroupID,
	})
}

type supplierGatewayDispatch struct {
	mu                           sync.Mutex
	repo                         SupplierDispatchRepository
	accepted                     SupplierAcceptedDispatch
	sendStarted                  bool
	observation                  SupplierUsageObservation
	observed                     bool
	observationErr               bool
	serviceTier, reasoningEffort string
}

func supplierDispatchFromContext(ctx context.Context) *supplierGatewayDispatch {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(supplierDispatchKey{}).(*supplierGatewayDispatch)
	return d
}

// Supplier Responses are intentionally narrower than ordinary channels: no
// plugin, passthrough, WS, shadow, hosted media/search, or protocol conversion.
func (s *OpenAIGatewayService) validateSupplierResponses(c *gin.Context, account *Account, body []byte) error {
	if !supplierAccountMarked(account) {
		return nil
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsShadow() || account.IsOpenAIAgentIdentity() || account.IsOpenAIPassthroughEnabled() ||
		(s.cfg != nil && s.cfg.RunMode == config.RunModeSimple) || s.pluginManager.ShouldRouteOpenAIOAuth(account) {
		return errSupplierUnsupported
	}
	if !SupplierResponsesRequestEligible(c, body) || !account.IsModelSupported(gjson.GetBytes(body, "model").String()) {
		return errSupplierUnsupported
	}
	return nil
}

func (s *OpenAIGatewayService) acceptSupplierDispatch(ctx context.Context, account *Account, model string) (context.Context, *supplierGatewayDispatch, error) {
	if !supplierAccountMarked(account) {
		return ctx, nil, nil
	}
	slot, ok := ctx.Value(supplierSlotKey{}).(supplierSlot)
	repo, supported := s.usageBillingRepo.(SupplierDispatchRepository)
	if !ok || !supported || slot.accountID != account.ID || slot.userID <= 0 || slot.apiKeyID <= 0 || slot.groupID <= 0 || s.openAITokenProvider == nil || s.openAITokenProvider.supplierResources == nil {
		return ctx, nil, ErrSupplierDispatchUnavailable
	}
	request := SupplierDispatchRequest{AttemptID: uuid.NewString(), RequestID: slot.requestID,
		ResourceID: account.GetCredential("supplier_resource_id"), AccountID: account.ID,
		UserID: slot.userID, APIKeyID: slot.apiKeyID, GroupID: slot.groupID, Model: model}
	accepted, err := repo.AcceptSupplierDispatch(ctx, request)
	if err != nil {
		return ctx, nil, &SupplierAccountingError{}
	}
	if accepted == nil || accepted.Resource == nil {
		return ctx, nil, ErrSupplierDispatchUnavailable
	}
	snapshot, resource := accepted.Snapshot, accepted.Resource
	if snapshot.AttemptID != request.AttemptID || snapshot.RequestID != request.RequestID || snapshot.ResourceID != request.ResourceID || snapshot.AccountID != account.ID || snapshot.UserID != slot.userID || snapshot.APIKeyID != slot.apiKeyID || snapshot.GroupID != slot.groupID || snapshot.Model != model || snapshot.Digest == "" ||
		snapshot.ConsentVersion != SupplierConsentVersion || snapshot.AuthorizationVersion < 1 || snapshot.FeePolicy.Validate() != nil ||
		resource.ID != snapshot.ResourceID || resource.AccountID != account.ID || resource.SupplierID != snapshot.SupplierID || resource.CredentialVersion != snapshot.CredentialVersion || resource.AuthorizationVersion != snapshot.AuthorizationVersion || resource.ConsentVersion != snapshot.ConsentVersion || resource.AccountHint != account.GetCredential("chatgpt_account_id") {
		return ctx, nil, ErrSupplierDispatchConflict
	}
	d := &supplierGatewayDispatch{repo: repo, accepted: *accepted}
	return context.WithValue(ctx, supplierDispatchKey{}, d), d, nil
}

func (d *supplierGatewayDispatch) review(ctx context.Context, reason string) {
	if d == nil {
		return
	}
	billingCtx, cancel := detachedBillingContext(ctx)
	defer cancel()
	_ = d.repo.MarkSupplierReview(billingCtx, d.accepted.Snapshot.AttemptID, d.accepted.Snapshot.Digest, reason)
}

func (d *supplierGatewayDispatch) beforeSend(ctx context.Context, account *Account) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sendStarted || d.accepted.Snapshot.AccountID != account.ID {
		return &SupplierAccountingError{}
	}
	// Fence even a commit-ambiguous mark. No retry can get through this object.
	d.sendStarted = true
	if err := d.repo.MarkSupplierDispatched(ctx, d.accepted.Snapshot.AttemptID, d.accepted.Snapshot.Digest); err != nil {
		return &SupplierAccountingError{}
	}
	return nil
}

// Return only fixed categories: never log upstream bodies, identifiers or secrets.
func supplierUpstreamRejectionCategory(body []byte) string {
	message := strings.ToLower(extractUpstreamErrorMessage(body))
	switch {
	case strings.Contains(message, "unsupported parameter"), strings.Contains(message, "unknown parameter"), strings.Contains(message, "unrecognized request argument"):
		return "unsupported_parameter"
	case strings.Contains(message, "model"):
		return "model_unavailable"
	case strings.Contains(message, "stream"):
		return "stream_required"
	case strings.Contains(message, "instructions"):
		return "instructions_required"
	case strings.Contains(message, "token"), strings.Contains(message, "unauthorized"), strings.Contains(message, "authentication"):
		return "authentication"
	case strings.Contains(message, "quota"), strings.Contains(message, "rate limit"):
		return "quota"
	default:
		return "unclassified"
	}
}

func supplierTerminal(event string) string {
	switch event {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error", "json", "[DONE]":
		return event
	default:
		return ""
	}
}

func supplierUsageScalar(value gjson.Result) bool {
	if value.Type != gjson.Number {
		return false
	}
	_, err := strconv.ParseUint(value.Raw, 10, strconv.IntSize-1)
	return err == nil
}

// Unlike the legacy compatibility parser, {} / null / coerced strings are not
// evidence of zero consumption. Validate optional scalar details as well.
func supplierTrustedUsage(body []byte) (OpenAIUsage, bool, bool) {
	if !gjson.ValidBytes(body) {
		return OpenAIUsage{}, false, false
	}
	value := gjson.GetBytes(body, "usage")
	if !value.Exists() {
		value = gjson.GetBytes(body, "response.usage")
	}
	if !value.IsObject() {
		return OpenAIUsage{}, false, false
	}
	valid := supplierUsageScalar(value.Get("input_tokens")) && supplierUsageScalar(value.Get("output_tokens"))
	var validate func(gjson.Result)
	validate = func(object gjson.Result) {
		object.ForEach(func(key, v gjson.Result) bool {
			if strings.HasSuffix(key.String(), "_tokens") && !supplierUsageScalar(v) {
				valid = false
			}
			if strings.HasSuffix(key.String(), "_details") {
				if !v.IsObject() {
					valid = false
				} else {
					validate(v)
				}
			}
			return true
		})
	}
	validate(value)
	if !valid {
		return OpenAIUsage{}, true, false
	}
	usage, ok := openAIUsageFromGJSON(value)
	if !ok || usage.CacheReadInputTokens > usage.InputTokens || usage.CacheCreationInputTokens > usage.InputTokens-usage.CacheReadInputTokens {
		return OpenAIUsage{}, true, false
	}
	return usage, true, true
}

// Called at the raw parser boundary, before model rewriting, client writes or
// any asynchronous usage queue. Only metering scalars enter the journal.
func observeSupplierResponse(ctx context.Context, body []byte, event, source string, disconnected bool) error {
	d := supplierDispatchFromContext(ctx)
	if d == nil {
		return nil
	}
	event = supplierTerminal(event)
	usage, present, valid := supplierTrustedUsage(body)
	if !present && event == "" {
		return nil
	}
	complete := valid && (event == "response.completed" || event == "response.done" || event == "json")
	observation := SupplierUsageObservation{Usage: usage, UsagePresent: present, Complete: complete,
		Source: source, Terminal: event, Model: d.accepted.Snapshot.Model,
		ServiceTier: d.serviceTier, ReasoningEffort: d.reasoningEffort,
		ClientDisconnected: disconnected, ObservedAt: time.Now().UTC()}
	d.mu.Lock()
	defer d.mu.Unlock()
	// A framing [DONE] must not erase an already persisted terminal snapshot.
	if d.observed && d.observation.Complete && event == "[DONE]" {
		return nil
	}
	billingCtx, cancel := detachedBillingContext(ctx)
	defer cancel()
	if err := d.repo.ObserveSupplierUsage(billingCtx, d.accepted.Snapshot.AttemptID, d.accepted.Snapshot.Digest, observation); err != nil {
		d.observationErr = true
		return &SupplierAccountingError{}
	}
	d.observation, d.observed = observation, true
	return nil
}

func supplierSafeTier(tier string) string {
	switch tier {
	case "auto", "default", "flex", "priority", "scale", "standard":
		return tier
	}
	return ""
}

func supplierSafeEffort(effort string) string {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return effort
	}
	return ""
}

func (s *OpenAIGatewayService) readSupplierResponseBody(ctx context.Context, resp *http.Response) ([]byte, error) {
	limit := resolveUpstreamResponseReadLimit(s.cfg)
	// Keep partial bytes even on EOF/read failure, unlike the legacy bounded
	// reader. Metering already delivered by upstream must not be discarded.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if isEventStreamResponse(resp.Header) || bodyHasSSEFraming(body) {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		scanner.Buffer(make([]byte, 4096), int(limit+1))
		documents := newOpenAISSEJSONDocumentScanner(scanner)
		event := ""
		for documents.Scan() {
			line := documents.Text()
			if kind, ok := extractOpenAISSEEventLine(line); ok {
				event = kind
			}
			if data, ok := extractOpenAISSEDataLine(line); ok {
				payload := []byte(data)
				if err := observeSupplierResponse(ctx, payload, effectiveOpenAISSEEventType(payload, event), "buffered_sse", ctx.Err() != nil); err != nil {
					return nil, err
				}
			}
			if line == "" {
				event = ""
			}
		}
		if scanErr := documents.Err(); scanErr != nil {
			return nil, scanErr
		}
	} else {
		terminal := "json"
		switch gjson.GetBytes(body, "status").String() {
		case "failed":
			terminal = "response.failed"
		case "incomplete":
			terminal = "response.incomplete"
		case "cancelled", "canceled":
			terminal = "response.cancelled"
		case "queued", "in_progress":
			terminal = "unknown"
		}
		if err := observeSupplierResponse(ctx, body, terminal, "native_json", ctx.Err() != nil); err != nil {
			return nil, err
		}
	}
	if int64(len(body)) > limit {
		return nil, ErrUpstreamResponseBodyTooLarge
	}
	d := supplierDispatchFromContext(ctx)
	if d != nil && (!d.observed || !d.observation.Complete || d.observationErr) {
		return nil, &SupplierAccountingError{}
	}
	return body, readErr
}

func (s *OpenAIGatewayService) supplierUsageReview(ctx context.Context, result *OpenAIForwardResult, reason string) error {
	if result != nil && result.SupplierDispatch != nil {
		if repo, ok := s.usageBillingRepo.(SupplierDispatchRepository); ok {
			billingCtx, cancel := detachedBillingContext(ctx)
			defer cancel()
			_ = repo.MarkSupplierReview(billingCtx, result.SupplierDispatch.AttemptID, result.SupplierDispatch.Digest, reason)
		}
	}
	return &SupplierAccountingError{}
}

func (s *OpenAIGatewayService) validateSupplierUsageInput(ctx context.Context, input *OpenAIRecordUsageInput) error {
	r, account := input.Result, input.Account
	snapshot := r.SupplierDispatch
	if snapshot == nil || input.APIKey == nil || input.User == nil || account == nil || input.APIKey.GroupID == nil || snapshot.AccountID != account.ID || snapshot.UserID != input.User.ID || snapshot.APIKeyID != input.APIKey.ID || snapshot.GroupID != *input.APIKey.GroupID {
		return ErrSupplierDispatchUnavailable
	}
	if !r.SupplierUsage.UsagePresent || !r.SupplierUsage.Complete || r.SupplierUsage.Model != snapshot.Model || r.Usage != r.SupplierUsage.Usage {
		return s.supplierUsageReview(ctx, r, "usage_unresolved")
	}
	if r.ImageCount != 0 || r.VideoCount != 0 || r.SearchCount != 0 || r.WebSearchCalls != 0 || r.AudioUsage != nil || r.Usage.ImageOutputTokens != 0 || r.Usage.ImageInputTokens != 0 || r.Usage.ImageCacheReadTokens != 0 || (s.cfg != nil && s.cfg.RunMode == config.RunModeSimple) {
		return s.supplierUsageReview(ctx, r, "unsupported_usage")
	}
	if _, ok := s.usageBillingRepo.(SupplierDispatchRepository); !ok {
		return ErrSupplierDispatchUnavailable
	}
	if s.billingService == nil {
		return s.supplierUsageReview(ctx, r, "pricing_unavailable")
	}
	return nil
}

func (d *supplierGatewayDispatch) finish(ctx context.Context, result **OpenAIForwardResult, err *error) {
	if d == nil {
		return
	}
	if !d.sendStarted {
		d.review(ctx, "not_dispatched")
		return
	}
	if *result == nil {
		*result = &OpenAIForwardResult{Model: d.accepted.Snapshot.Model, UpstreamModel: d.accepted.Snapshot.Model}
	}
	r := *result
	snapshot := d.accepted.Snapshot
	r.SupplierDispatch = &snapshot
	r.SupplierUsage = d.observation
	if d.observationErr {
		r.SupplierUsage.Complete = false
	}
	if d.observed {
		r.Usage = d.observation.Usage
	}
	if !d.observed || !d.observation.Complete || d.observationErr {
		d.review(ctx, "usage_unresolved")
		*err = &SupplierAccountingError{}
	} else if *err != nil {
		// Complete observed usage remains billable even when downstream delivery
		// failed. Do not leak an UpstreamFailoverError through the wrapper.
		*err = &SupplierAccountingError{}
	}
}
