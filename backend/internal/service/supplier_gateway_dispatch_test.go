package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type supplierDispatchRepoStub struct {
	// Batch-image balance holds are outside these native text dispatch tests.
	UsageBillingRepository
	resource                                           *SupplierResource
	accepted                                           SupplierDispatchRequest
	observations                                       []SupplierUsageObservation
	order                                              []string
	reviews                                            []string
	acceptErr, markErr, observeErr, stageErr, applyErr error
	command                                            *UsageBillingCommand
	observeHook                                        func()
}

func (r *supplierDispatchRepoStub) AcceptSupplierDispatch(_ context.Context, req SupplierDispatchRequest) (*SupplierAcceptedDispatch, error) {
	r.order = append(r.order, "accept")
	r.accepted = req
	if r.acceptErr != nil {
		return nil, r.acceptErr
	}
	return &SupplierAcceptedDispatch{Resource: r.resource, Snapshot: SupplierDispatchSnapshot{
		AttemptID: req.AttemptID, RequestID: req.RequestID, ResourceID: req.ResourceID,
		SupplierID: r.resource.SupplierID, AccountID: req.AccountID,
		CredentialVersion: r.resource.CredentialVersion, AuthorizationVersion: r.resource.AuthorizationVersion,
		ConsentVersion: r.resource.ConsentVersion, UserID: req.UserID, APIKeyID: req.APIKeyID, GroupID: req.GroupID,
		Model: req.Model, FeePolicy: SupplierFeePolicy{Mode: SupplierFeeModeStandardCost, RatioBPS: 7000, Version: 3},
		AcceptedAt: time.Now(), Digest: strings.Repeat("a", 64),
	}}, nil
}
func (r *supplierDispatchRepoStub) MarkSupplierDispatched(context.Context, string, string) error {
	r.order = append(r.order, "dispatch")
	return r.markErr
}
func (r *supplierDispatchRepoStub) ObserveSupplierUsage(ctx context.Context, _, _ string, observation SupplierUsageObservation) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.order = append(r.order, "observe")
	if r.observeHook != nil {
		r.observeHook()
	}
	if r.observeErr != nil {
		return r.observeErr
	}
	r.observations = append(r.observations, observation)
	return nil
}
func (r *supplierDispatchRepoStub) MarkSupplierReview(_ context.Context, _, _, reason string) error {
	r.reviews = append(r.reviews, reason)
	return nil
}
func (r *supplierDispatchRepoStub) StageSupplierBilling(_ context.Context, cmd *UsageBillingCommand) error {
	r.order = append(r.order, "stage")
	r.command = cmd
	return r.stageErr
}
func (r *supplierDispatchRepoStub) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.order = append(r.order, "apply")
	r.command = cmd
	return &UsageBillingApplyResult{Applied: false}, r.applyErr
}
func (*supplierDispatchRepoStub) RecoverSupplierBilling(context.Context, int) (SupplierRecoveryResult, error) {
	return SupplierRecoveryResult{}, nil
}

func supplierGatewayFixture(t *testing.T, payload string, stream bool) (*OpenAIGatewayService, *supplierDispatchRepoStub, *httpUpstreamRecorder, *Account, *APIKey, *gin.Context, context.Context) {
	t.Helper()
	vault, resource := supplierExecutionFixture(t)
	resource.AccountID = 17
	resource.CredentialVersion = 4
	resource.AuthorizationVersion = 2
	resource.ConsentVersion = SupplierConsentVersion
	repo := &supplierDispatchRepoStub{resource: resource}
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(payload))}}
	svc := newOpenAIRejectedFieldTestService(upstream)
	svc.usageBillingRepo = repo
	svc.openAITokenProvider = &OpenAITokenProvider{supplierResources: vault}
	account := &Account{ID: 17, Name: "supplier", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"supplier_resource_id": resource.ID, "chatgpt_account_id": resource.AccountHint}}
	group := &Group{ID: 7, Platform: PlatformOpenAI, RateMultiplier: 1}
	apiKey := &APIKey{ID: 9, User: &User{ID: 8}, GroupID: &group.ID, Group: group}
	body := `{"model":"gpt-5.1","input":"hello","stream":false}`
	if stream {
		body = `{"model":"gpt-5.1","input":"hello","stream":true}`
	}
	c := newOpenAIRejectedFieldTestContext([]byte(body))
	ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "untrusted-client-id")
	ctx = WithSupplierDispatchSlot(ctx, account.ID, apiKey)
	c.Request = c.Request.WithContext(ctx)
	return svc, repo, upstream, account, apiKey, c, ctx
}

func TestSupplierUpstreamRejectionCategoryNeverEchoesBody(t *testing.T) {
	for _, tc := range []struct{ body, category string }{
		{`{"error":{"message":"Unsupported parameter: private-canary"}}`, "unsupported_parameter"},
		{`{"detail":"model unavailable for private-canary"}`, "model_unavailable"},
		{`{"detail":"stream must be true"}`, "stream_required"},
		{`{"detail":"private-canary"}`, "unclassified"},
	} {
		require.Equal(t, tc.category, supplierUpstreamRejectionCategory([]byte(tc.body)))
	}
}

func TestSupplierTrustedUsageRejectsAbsentMalformedAndPartial(t *testing.T) {
	for _, raw := range []string{`{}`, `{"usage":{}}`, `{"usage":null}`, `{"usage":{"input_tokens":0}}`, `{"usage":{"input_tokens":"0","output_tokens":0}}`, `{"usage":{"input_tokens":-1,"output_tokens":0}}`, `{"usage":{"input_tokens":1.5,"output_tokens":0}}`, `{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":2}}}`, `{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":"0"}}}`} {
		t.Run(raw, func(t *testing.T) { _, _, valid := supplierTrustedUsage([]byte(raw)); require.False(t, valid) })
	}
	usage, present, valid := supplierTrustedUsage([]byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`))
	require.True(t, present)
	require.True(t, valid)
	require.Equal(t, OpenAIUsage{}, usage)
}

func TestSupplierNativeForwardDurableOrderAndNoAccountMutation(t *testing.T) {
	payload := `{"id":"resp_1","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}`
	svc, repo, upstream, account, _, c, ctx := supplierGatewayFixture(t, payload, false)
	before := account.GetCredential("chatgpt_account_id")
	// Check the raw observer ran before response output without depending on its format.
	repo.observeHook = func() { require.False(t, c.Writer.Written()) }
	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","input":"hello","stream":false}`))
	require.NoError(t, err)
	require.NotNil(t, result.SupplierDispatch)
	require.Equal(t, []string{"accept", "dispatch", "observe"}, repo.order)
	require.NoError(t, uuid.Validate(repo.accepted.AttemptID))
	require.NoError(t, uuid.Validate(repo.accepted.RequestID))
	require.NotEqual(t, "untrusted-client-id", repo.accepted.RequestID)
	require.Equal(t, before, account.GetCredential("chatgpt_account_id"))
	require.Empty(t, account.GetCredential("access_token"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.lastReq.Context()))
	require.Nil(t, upstream.lastReq.GetBody)
	require.True(t, result.SupplierUsage.Complete)
	require.Equal(t, 10, result.Usage.InputTokens)
}

func TestSupplierNativeUnknownAndRejectedRequestsNeverReplay(t *testing.T) {
	for _, kind := range []string{"transport", "bad_request", "mark_ambiguous", "accept_ambiguous"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo, upstream, account, _, c, ctx := supplierGatewayFixture(t, `{"error":{"code":"invalid_encrypted_content","message":"bad"}}`, false)
			switch kind {
			case "transport":
				upstream.err = io.ErrUnexpectedEOF
			case "bad_request":
				upstream.resp.StatusCode = 400
			case "mark_ambiguous":
				repo.markErr = errors.New("commit unknown")
			case "accept_ambiguous":
				repo.acceptErr = errors.New("commit unknown")
			}
			_, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","input":"hello"}`))
			require.ErrorIs(t, err, ErrSupplierAccountingPending)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			want := 1
			if kind == "mark_ambiguous" || kind == "accept_ambiguous" {
				want = 0
			}
			require.Len(t, upstream.requests, want)
		})
	}
}

func TestSupplierStreamingObservationBeforeClientAndParserDiscard(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed"} {
		t.Run(terminal, func(t *testing.T) {
			payload := "event: " + terminal + "\ndata: {\"type\":\"" + terminal + "\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.1\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
			svc, repo, upstream, account, _, c, ctx := supplierGatewayFixture(t, payload, true)
			result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","input":"hello","stream":true}`))
			require.Len(t, upstream.requests, 1)
			require.NotEmpty(t, repo.observations)
			require.NotNil(t, result)
			require.Equal(t, 10, result.Usage.InputTokens)
			if terminal == "response.failed" {
				require.ErrorIs(t, err, ErrSupplierAccountingPending)
				require.False(t, result.SupplierUsage.Complete)
			} else {
				require.NoError(t, err)
				require.True(t, result.SupplierUsage.Complete)
			}
		})
	}
}

func TestSupplierObservationFailurePreventsTerminalOutput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		payload := `{"status":"completed","usage":{"input_tokens":10,"output_tokens":2}}`
		body := `{"model":"gpt-5.1","input":"hello"}`
		if stream {
			payload = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
			body = `{"model":"gpt-5.1","input":"hello","stream":true}`
		}
		svc, repo, _, account, _, c, ctx := supplierGatewayFixture(t, payload, stream)
		repo.observeErr = errors.New("database unavailable")
		result, err := svc.Forward(ctx, c, account, []byte(body))
		require.ErrorIs(t, err, ErrSupplierAccountingPending)
		require.False(t, result.SupplierUsage.Complete)
		require.False(t, c.Writer.Written())
		require.Contains(t, repo.reviews, "usage_unresolved")
	}
}

func TestSupplierMissingUsageCannotBecomeZeroBilling(t *testing.T) {
	for _, payload := range []string{`{"usage":{}}`, `{"status":"completed"}`} {
		svc, repo, _, account, _, c, ctx := supplierGatewayFixture(t, payload, false)
		result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","input":"hello"}`))
		require.ErrorIs(t, err, ErrSupplierAccountingPending)
		require.NotNil(t, result)
		require.False(t, result.SupplierUsage.Complete)
		require.Contains(t, repo.reviews, "usage_unresolved")
	}
}

func TestSupplierRequiresSlotAndRejectsUnsupportedBeforeSend(t *testing.T) {
	svc, repo, upstream, account, _, c, _ := supplierGatewayFixture(t, `{}`, false)
	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","input":"hello"}`))
	require.ErrorIs(t, err, ErrSupplierDispatchUnavailable)
	require.Empty(t, repo.order)
	require.Empty(t, upstream.requests)
	for _, body := range []string{`{"model":"gpt-5.1","tools":[{"type":"web_search"}]}`, `{"model":"gpt-5.1","tools":[{"type":"namespace","tools":[{"type":"image_generation"}]}]}`, `{"model":"gpt-5.1","input":[{"type":"input_image","image_url":"https://example.invalid"}]}`} {
		require.False(t, SupplierResponsesRequestEligible(c, []byte(body)))
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.False(t, SupplierResponsesRequestEligible(c, []byte(`{"model":"gpt-5.1"}`)))
}

func TestSupplierPhysicalSendFenceCannotBeReused(t *testing.T) {
	svc, repo, upstream, account, _, _, ctx := supplierGatewayFixture(t, `{}`, false)
	ctx, _, err := svc.acceptSupplierDispatch(ctx, account, "gpt-5.1")
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := svc.doOpenAIUpstream(req, "", account)
	require.NoError(t, err)
	_ = resp.Body.Close()
	_, err = svc.doOpenAIUpstream(req, "", account)
	require.ErrorIs(t, err, ErrSupplierAccountingPending)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, []string{"accept", "dispatch"}, repo.order)
}

func TestSupplierUnmarkedShadowCannotDecryptParent(t *testing.T) {
	svc, _, upstream, parent, _, _, ctx := supplierGatewayFixture(t, `{}`, false)
	svc.accountRepo = &stubOpenAIAccountRepo{accounts: []Account{*parent}}
	shadow := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID}
	token, _, err := svc.GetAccessToken(ctx, shadow)
	require.ErrorIs(t, err, ErrSupplierResource)
	require.Empty(t, token)
	require.Empty(t, upstream.requests)
}

type supplierLateReadError struct{ reader *strings.Reader }

func (r *supplierLateReadError) Read(p []byte) (int, error) {
	if r.reader.Len() == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return r.reader.Read(p)
}
func (*supplierLateReadError) Close() error { return nil }

func TestSupplierBufferedUsageSurvivesLateReadError(t *testing.T) {
	payload := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
	svc, repo, upstream, account, _, c, ctx := supplierGatewayFixture(t, payload, false)
	upstream.resp.Header.Set("Content-Type", "text/event-stream")
	upstream.resp.Body = &supplierLateReadError{reader: strings.NewReader(payload)}
	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","input":"hello","stream":false}`))
	require.ErrorIs(t, err, ErrSupplierAccountingPending)
	require.Len(t, upstream.requests, 1)
	require.Len(t, repo.observations, 1)
	require.True(t, result.SupplierUsage.Complete, "trusted terminal survives later read failure")
	require.Equal(t, 10, result.Usage.InputTokens)
	require.False(t, c.Writer.Written())
}

func TestSupplierStageFailureNeverAppliesAndPreservesCommand(t *testing.T) {
	svc, repo, _, account, apiKey, _, ctx := supplierGatewayFixture(t, `{}`, false)
	_, dispatch, err := svc.acceptSupplierDispatch(ctx, account, "gpt-5.1")
	require.NoError(t, err)
	repo.stageErr = errors.New("commit unknown")
	_, err = applyUsageBilling(ctx, "client:unsafe", &UsageLog{Model: "gpt-5.1"}, &postUsageBillingParams{
		SupplierDispatch: &dispatch.accepted.Snapshot, Account: account, APIKey: apiKey, User: apiKey.User,
		Cost: &CostBreakdown{TotalCost: 1, ActualCost: 1},
	}, &billingDeps{}, repo)
	require.ErrorIs(t, err, ErrSupplierAccountingPending)
	require.Equal(t, []string{"accept", "stage"}, repo.order)
	require.NotNil(t, repo.command)
}

func TestSupplierObservationSurvivesCancellation(t *testing.T) {
	svc, repo, _, account, _, _, ctx := supplierGatewayFixture(t, `{}`, false)
	ctx, dispatch, err := svc.acceptSupplierDispatch(ctx, account, "gpt-5.1")
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, observeSupplierResponse(cancelled, []byte(`{"usage":{"input_tokens":4,"output_tokens":0}}`), "json", "native_json", true))
	require.True(t, dispatch.observation.Complete)
	require.Len(t, repo.observations, 1)
}

func TestSupplierStagePrecedesApplyAndPricingFailureStaysPending(t *testing.T) {
	for _, missingPrice := range []bool{false, true} {
		t.Run(map[bool]string{false: "priced", true: "missing_price"}[missingPrice], func(t *testing.T) {
			svc, repo, _, account, apiKey, _, ctx := supplierGatewayFixture(t, `{}`, false)
			svc.billingService = NewBillingService(&config.Config{}, nil)
			svc.deferredService = &DeferredService{}
			_, dispatch, err := svc.acceptSupplierDispatch(ctx, account, "gpt-5.1")
			require.NoError(t, err)
			snapshot := dispatch.accepted.Snapshot
			if missingPrice {
				snapshot.Model = "unpriced-supplier-model-xyz"
			}
			usage := OpenAIUsage{InputTokens: 10, OutputTokens: 2, CacheReadInputTokens: 3}
			result := &OpenAIForwardResult{Model: snapshot.Model, UpstreamModel: snapshot.Model, Usage: usage, SupplierDispatch: &snapshot, SupplierUsage: SupplierUsageObservation{Usage: usage, UsagePresent: true, Complete: true, Model: snapshot.Model}}
			err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, Account: account, APIKey: apiKey, User: apiKey.User})
			if missingPrice {
				require.ErrorIs(t, err, ErrSupplierAccountingPending)
				require.Nil(t, repo.command)
				require.Contains(t, repo.reviews, "pricing_unavailable")
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"accept", "stage", "apply"}, repo.order)
			require.Equal(t, snapshot.AttemptID, repo.command.SupplierAttemptID)
			require.Equal(t, snapshot.Digest, repo.command.SupplierSnapshotDigest)
			require.Equal(t, "supplier:"+snapshot.AttemptID, repo.command.RequestID)
			require.Empty(t, repo.command.RequestPayloadHash)
			require.Equal(t, 7, repo.command.InputTokens)
		})
	}
}

func TestSupplierCompositeSourceAcceptsExactMappedModelOnce(t *testing.T) {
	payload := `{"id":"resp_mapped","status":"completed","model":"gpt-5.2","output":[],"usage":{"input_tokens":10,"output_tokens":2}}`
	svc, repo, upstream, account, _, c, ctx := supplierGatewayFixture(t, payload, false)
	account.Credentials["model_mapping"] = map[string]any{"supplier-alias": "gpt-5.2"}
	source := CompositeRouteCandidate{TargetPlatform: PlatformOpenAI, UpstreamModel: "supplier-alias", Enabled: true}
	route := CompositeModelRoute{GroupID: 7, PublicModel: "gpt6luna", Candidates: []CompositeRouteCandidate{source}}
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{Matched: true, Source: CompositeRouteSourceExplicit, GroupID: 7, PublicModel: "gpt6luna", TargetPlatform: PlatformOpenAI, UpstreamModel: "supplier-alias", Route: &route})
	ctx = ApplyCompositeSelection(ctx, &AccountSelectionResult{Account: account, CompositeSource: &source})
	c.Request = c.Request.WithContext(ctx)
	result, err := svc.Forward(ctx, c, account, []byte(`{"model":"supplier-alias","input":"hello","stream":false}`))
	require.NoError(t, err)
	require.NotNil(t, result.SupplierDispatch)
	require.Equal(t, "gpt-5.2", repo.accepted.Model, "durable acceptance must bind actual mapped upstream model")
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "gpt-5.2", gjson.GetBytes(upstream.lastBody, "model").String())
	public, _ := RequestedPublicModelFromContext(ctx)
	require.Equal(t, "gpt6luna", public)
}
