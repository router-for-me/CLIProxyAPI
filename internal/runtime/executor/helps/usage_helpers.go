package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type UsageReporter struct {
	requestID            string
	traceID              string
	selectionID          string
	provider             string
	baseURL              string
	executorType         string
	model                string
	alias                string
	authID               string
	authIndex            string
	authMu               sync.RWMutex
	accessTokenHash      string
	authType             string
	apiKey               string
	accessProvider       string
	isNativeKey          bool
	sessionID            string
	parentSessionID      string
	source               string
	reasoning            string
	serviceTier          string
	effectiveServiceTier string
	generate             bool
	stream               bool
	requestedAt          time.Time
	ttftMu               sync.RWMutex
	ttft                 time.Duration
	firstPacketDuration  time.Duration
	firstPacketSet       bool
	ttftStart            time.Time
	ttftSet              bool
	once                 sync.Once

	responseModelMu sync.RWMutex
	// responseModel holds the latest model name reported by the upstream response.
	responseModel string
	// responseModelFinal marks that a terminal event already reported the served
	// model, so later frames skip parsing entirely.
	responseModelFinal atomic.Bool

	upstreamModelMu sync.RWMutex
	// upstreamModel holds the canonical upstream model expected to be served when
	// it differs from the requested model (e.g. local Kimi model mappings).
	upstreamModel string
}

type usageExecutor interface {
	Identifier() string
}

func NewExecutorUsageReporter(ctx context.Context, executor usageExecutor, model string, auth *cliproxyauth.Auth) *UsageReporter {
	provider := ""
	if executor != nil {
		provider = executor.Identifier()
	}
	reporter := NewUsageReporter(ctx, provider, model, auth)
	reporter.executorType = ExecutorTypeName(executor)
	return reporter
}

func NewUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	apiKey := APIKeyFromContext(ctx)
	accessProvider := AccessProviderFromContext(ctx)
	isNativeKey := false
	if nativeExplicit, ok := usage.IsNativeKeyFromContext(ctx); ok {
		isNativeKey = nativeExplicit
	} else if accessProvider != "" {
		isNativeKey = usage.IsNativeAccessProvider(accessProvider)
	}
	alias := usage.RequestedModelAliasFromContext(ctx)
	if alias == "" {
		alias = model
	}
	sessionID := ""
	parentSessionID := ""
	clientMeta := internallogging.GetClientRequestMetadata(ctx)
	if clientMeta.SessionID != "" {
		sessionID = clientMeta.SessionID
		parentSessionID = clientMeta.ParentSessionID
		if sessionID == parentSessionID || !isHierarchyParent(sessionID, parentSessionID) {
			parentSessionID = ""
		}
	}
	baseURL := ""
	if auth != nil {
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
		if baseURL == "" && auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok {
				baseURL = strings.TrimSpace(v)
			}
		}
	}
	traceID := usage.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = internallogging.GetRequestID(ctx)
	}
	reporter := &UsageReporter{
		requestID:       uuid.NewString(),
		traceID:         traceID,
		selectionID:     usage.SelectionIDFromContext(ctx),
		provider:        provider,
		baseURL:         baseURL,
		model:           model,
		alias:           strings.TrimSpace(alias),
		requestedAt:     time.Now(),
		apiKey:          apiKey,
		accessProvider:  accessProvider,
		isNativeKey:     isNativeKey,
		sessionID:       sessionID,
		parentSessionID: parentSessionID,
		source:          resolveUsageSource(auth, apiKey),
		authType:        resolveUsageAuthType(auth),
		reasoning:       usage.ReasoningEffortFromContext(ctx),
		serviceTier:     usage.ServiceTierFromContext(ctx),
		generate:        usage.GenerateFromContext(ctx),
		stream:          usage.StreamFromContext(ctx),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
		reporter.accessTokenHash = authAccessTokenSHA256(auth)
	}
	return reporter
}

// SetStream records whether the request was executed in streaming mode.
func (r *UsageReporter) SetStream(stream bool) {
	if r == nil {
		return
	}
	r.stream = stream
}

// SetSessionHierarchy sets the explicit session and parent session identifiers.
// Callers should invoke this method before Publish or EnsurePublished on the request thread.
func (r *UsageReporter) SetSessionHierarchy(sessionID, parentSessionID string) {
	if r == nil {
		return
	}
	r.sessionID = strings.TrimSpace(sessionID)
	r.parentSessionID = strings.TrimSpace(parentSessionID)
	if r.sessionID == "" || r.sessionID == r.parentSessionID || !isHierarchyParent(r.sessionID, r.parentSessionID) {
		r.parentSessionID = ""
	}
}

func isHierarchyParent(primary, parent string) bool {
	if parent == "" || primary == "" || primary == parent {
		return false
	}
	if strings.Contains(primary, ":agent:") {
		return true
	}
	idx1 := strings.Index(primary, ":")
	idx2 := strings.Index(parent, ":")
	if idx1 > 0 && idx2 > 0 && primary[:idx1] == parent[:idx2] {
		return true
	}
	if idx1 == -1 && idx2 == -1 {
		return true
	}
	return false
}

// UpdateAccessTokenFingerprint records the token version actually used upstream.
func (r *UsageReporter) UpdateAccessTokenFingerprint(auth *cliproxyauth.Auth) {
	if r == nil {
		return
	}
	r.authMu.Lock()
	r.accessTokenHash = authAccessTokenSHA256(auth)
	r.authMu.Unlock()
}

func (r *UsageReporter) accessTokenFingerprint() string {
	if r == nil {
		return ""
	}
	r.authMu.RLock()
	defer r.authMu.RUnlock()
	return r.accessTokenHash
}

// ObserveResponseModel stores the model reported by an upstream response or event and
// ignores payloads without one; the substitution warning is emitted at publish time.
func (r *UsageReporter) ObserveResponseModel(payload []byte) {
	if r == nil || r.responseModelFinal.Load() {
		return
	}
	provider := ""
	if r != nil {
		provider = r.provider
	}
	served, terminal := extractResponseModelEvent(payload, provider)
	if served == "" {
		if terminal {
			r.responseModelFinal.Store(true)
		}
		return
	}
	r.responseModelMu.Lock()
	r.responseModel = served
	r.responseModelMu.Unlock()
	if terminal {
		r.responseModelFinal.Store(true)
	}
}

// SetResponseModel sets the reported model directly if valid and not already marked final.
func (r *UsageReporter) SetResponseModel(model string) {
	if r == nil || r.responseModelFinal.Load() {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" || len(model) > maxResponseModelLength {
		return
	}
	r.responseModelMu.Lock()
	r.responseModel = model
	r.responseModelMu.Unlock()
}

// SetUpstreamModel records the upstream model expected to be served when it differs
// from the requested model (e.g. due to provider-specific mapping or canonicalization).
// Model substitution detection compares the response against this upstream model,
// while usage accounting preserves the client's requested model.
func (r *UsageReporter) SetUpstreamModel(model string) {
	if r == nil {
		return
	}
	r.upstreamModelMu.Lock()
	r.upstreamModel = strings.TrimSpace(model)
	r.upstreamModelMu.Unlock()
}

// UpstreamModel returns the expected upstream model, or an empty string if not explicitly set.
func (r *UsageReporter) UpstreamModel() string {
	if r == nil {
		return ""
	}
	r.upstreamModelMu.RLock()
	defer r.upstreamModelMu.RUnlock()
	return r.upstreamModel
}

// IsResponseModelFinal reports whether the response model was already finalized by a terminal event.
func (r *UsageReporter) IsResponseModelFinal() bool {
	return r != nil && r.responseModelFinal.Load()
}

// warnModelSubstitution warns about a silent upstream model swap, and labels the
// credential by index only, never by account.
func (r *UsageReporter) warnModelSubstitution(ctx context.Context) {
	if r == nil {
		return
	}
	served := r.ResponseModel()
	expectedModel := r.UpstreamModel()
	if expectedModel == "" {
		expectedModel = r.model
	}
	if served == "" || !IsModelSubstituted(expectedModel, served) {
		return
	}
	if r.model != "" && !IsModelSubstituted(r.model, served) {
		return
	}
	providerName := strings.TrimSpace(r.provider)
	if providerName == "" {
		providerName = "unknown"
	}
	LogWithRequestID(ctx).Warnf("%s executor: upstream served model %q for requested model %q (auth_index=%s)", providerName, served, r.model, r.authIndexForLog())
}

// authIndexForLog labels the credential without exposing its file name or account.
func (r *UsageReporter) authIndexForLog() string {
	if r == nil {
		return "nil"
	}
	if authIndex := strings.TrimSpace(r.authIndex); authIndex != "" {
		return authIndex
	}
	return "nil"
}

// ResponseModel returns the latest model reported by the upstream response.
func (r *UsageReporter) ResponseModel() string {
	if r == nil {
		return ""
	}
	r.responseModelMu.RLock()
	defer r.responseModelMu.RUnlock()
	return r.responseModel
}

func ExecutorTypeName(executor any) string {
	if executor == nil {
		return ""
	}
	executorType := reflect.TypeOf(executor)
	for executorType.Kind() == reflect.Pointer {
		executorType = executorType.Elem()
	}
	return strings.TrimSpace(executorType.Name())
}

func (r *UsageReporter) Publish(ctx context.Context, detail usage.Detail) {
	r.publishWithOutcome(ctx, detail, false, usage.Failure{})
}

func (r *UsageReporter) PublishAdditionalModel(ctx context.Context, model string, detail usage.Detail) {
	record, ok := r.buildAdditionalModelRecord(model, detail)
	if !ok {
		return
	}
	r.publishRecord(ctx, record)
}

// SetTranslatedRequestMetadata captures usage-relevant values from the final
// outbound payload after translation and payload defaults or overrides.
func (r *UsageReporter) SetTranslatedRequestMetadata(payload []byte, format string) {
	if r == nil {
		return
	}
	r.reasoning = thinking.ExtractTranslatedReasoningEffort(payload, format)
	r.effectiveServiceTier = extractEffectiveServiceTier(payload)
}

// SetTranslatedReasoningEffort is retained for existing executor call sites.
// It captures all post-translation request metadata used by usage records.
func (r *UsageReporter) SetTranslatedReasoningEffort(payload []byte, format string) {
	r.SetTranslatedRequestMetadata(payload, format)
}

func extractEffectiveServiceTier(payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{"service_tier", "request.service_tier", "interaction.service_tier"} {
		if node := gjson.GetBytes(payload, path); node.Type == gjson.String {
			if tier := strings.TrimSpace(node.String()); tier != "" {
				return tier
			}
		}
	}
	return ""
}

func (r *UsageReporter) TrackHTTPClient(client *http.Client) *http.Client {
	return r.trackHTTPClient(client, false)
}

// TrackHTTPClientRoundTripOnly records the TTFT start time upon sending the request
// and captures first-packet arrival fallback on initial body reads, while keeping
// effective TTFT unset. This allows protocol-aware streaming executors (like Codex SSE)
// to mark effective TTFT explicitly upon receiving substantive token events, while
// preserving first-packet fallback metrics for non-2xx error bodies or keepalive streams.
func (r *UsageReporter) TrackHTTPClientRoundTripOnly(client *http.Client) *http.Client {
	return r.trackHTTPClient(client, true)
}

func (r *UsageReporter) trackHTTPClient(client *http.Client, packetOnly bool) *http.Client {
	if r == nil || client == nil {
		return client
	}
	tracked := *client
	transport := tracked.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	tracked.Transport = usageTTFTRoundTripper{
		base:       transport,
		reporter:   r,
		packetOnly: packetOnly,
	}
	return &tracked
}

func (r *UsageReporter) ObserveResponse(resp *http.Response) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	r.StartResponseTTFT()
	resp.Body = &usageTTFTReadCloser{
		ReadCloser: resp.Body,
		mark: func() {
			r.MarkFirstResponseByte()
		},
	}
}

func (r *UsageReporter) ObserveResponsePacketOnly(resp *http.Response) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	r.StartResponseTTFT()
	resp.Body = &usageTTFTReadCloser{
		ReadCloser: resp.Body,
		mark: func() {
			r.RecordFirstPacket()
		},
	}
}

func (r *UsageReporter) StartResponseTTFT() {
	if r == nil {
		return
	}
	r.ttftMu.Lock()
	if !r.ttftSet && r.ttftStart.IsZero() {
		r.ttftStart = time.Now()
	}
	r.ttftMu.Unlock()
}

func (r *UsageReporter) IsTTFTSet() bool {
	if r == nil {
		return false
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	return r.ttftSet
}

func (r *UsageReporter) IsFirstPacketSet() bool {
	if r == nil {
		return false
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	return r.firstPacketSet
}

// RecordFirstPacket records the arrival time of the first packet/chunk from upstream as a fallback.
func (r *UsageReporter) RecordFirstPacket() {
	r.ObserveTokenEvent(false)
}

// ObserveTokenEvent records the first packet fallback time on the first frame,
// and if isToken is true, records the effective TTFT. It uses a fast-path
// read check to return immediately with zero write lock contention once TTFT is set
// or when subsequent non-token metadata frames arrive after the first packet.
func (r *UsageReporter) ObserveTokenEvent(isToken bool) {
	if r == nil {
		return
	}
	r.ttftMu.RLock()
	if r.ttftSet {
		r.ttftMu.RUnlock()
		return
	}
	start := r.ttftStart
	alreadyRecordedPacket := r.firstPacketSet
	r.ttftMu.RUnlock()

	if start.IsZero() {
		return
	}

	if !isToken && alreadyRecordedPacket {
		return
	}

	r.ttftMu.Lock()
	defer r.ttftMu.Unlock()
	if r.ttftSet {
		return
	}
	if !r.firstPacketSet {
		r.firstPacketDuration = time.Since(start)
		r.firstPacketSet = true
	}
	if isToken {
		r.ttft = time.Since(start)
		r.ttftSet = true
		r.ttftStart = time.Time{}
	}
}

func (r *UsageReporter) MarkFirstResponseByte() {
	if r == nil {
		return
	}
	r.ttftMu.Lock()
	if r.ttftSet {
		r.ttftMu.Unlock()
		return
	}
	start := r.ttftStart
	r.ttftStart = time.Time{}
	r.ttftMu.Unlock()
	if start.IsZero() {
		return
	}
	r.setTTFT(time.Since(start))
}

func (r *UsageReporter) buildAdditionalModelRecord(model string, detail usage.Detail) (usage.Record, bool) {
	if r == nil {
		return usage.Record{}, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return usage.Record{}, false
	}
	detail = normalizeUsageDetailTotal(detail, r.provider, r.executorType)
	if !hasNonZeroTokenUsage(detail) {
		return usage.Record{}, false
	}
	rec := r.buildRecordForModel(model, detail, false, usage.Failure{})
	rec.RequestID = uuid.NewString()
	return rec, true
}

func (r *UsageReporter) PublishFailure(ctx context.Context, errs ...error) {
	r.publishWithOutcome(ctx, usage.Detail{}, true, failFromErrors(errs...))
}

func (r *UsageReporter) PublishFailureWithDetail(ctx context.Context, detail usage.Detail, errs ...error) {
	r.publishWithOutcome(ctx, detail, true, failFromErrors(errs...))
}

func (r *UsageReporter) TrackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		r.PublishFailure(ctx, *errPtr)
	}
}

func (r *UsageReporter) publishWithOutcome(ctx context.Context, detail usage.Detail, failed bool, fail usage.Failure) {
	if r == nil {
		return
	}
	detail = normalizeUsageDetailTotal(detail, r.provider, r.executorType)
	r.once.Do(func() {
		r.publishAttemptRecord(ctx, r.buildRecord(detail, failed, fail))
	})
}

func normalizeUsageDetailTotal(detail usage.Detail, provider, executorType string) usage.Detail {
	return usage.EnsureTokenBreakdownForProvider(detail, provider, executorType)
}

func hasNonZeroTokenUsage(detail usage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0 ||
		detail.TokenBreakdown.TotalTokens != 0
}

// ensurePublished guarantees that a usage record is emitted exactly once.
// It is safe to call multiple times; only the first call wins due to once.Do.
// This is used to ensure request counting even when upstream responses do not
// include any usage fields (tokens), especially for streaming paths.
func (r *UsageReporter) EnsurePublished(ctx context.Context) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.publishAttemptRecord(ctx, r.buildRecord(usage.Detail{}, false, usage.Failure{}))
	})
}

// publishAttemptRecord emits the record for one upstream attempt and the
// observability warnings that belong to the attempt rather than to a single event.
func (r *UsageReporter) publishAttemptRecord(ctx context.Context, record usage.Record) {
	r.publishRecord(ctx, record)
	r.warnModelSubstitution(ctx)
}

func (r *UsageReporter) publishRecord(ctx context.Context, record usage.Record) {
	record.ResponseHeaders = internallogging.GetResponseHeaders(ctx)
	if r != nil {
		ctx = usage.WithCapturedIdentity(ctx, r.apiKey, r.accessProvider, r.isNativeKey)
	}
	usage.PublishRecord(ctx, record)
}

// RequestID returns the execution instance request ID for this reporter.
func (r *UsageReporter) RequestID() string {
	if r == nil {
		return ""
	}
	return r.requestID
}

// TraceID returns the parent inbound request ID for this reporter.
func (r *UsageReporter) TraceID() string {
	if r == nil {
		return ""
	}
	return r.traceID
}

// SelectionID returns the scheduler selection ID for this reporter.
func (r *UsageReporter) SelectionID() string {
	if r == nil {
		return ""
	}
	return r.selectionID
}

// IsNativeKey reports whether the client API key was authenticated by CPA's native config provider.
func (r *UsageReporter) IsNativeKey() bool {
	if r == nil {
		return false
	}
	return r.isNativeKey
}

// AccessProvider identifies the client request authentication provider.
func (r *UsageReporter) AccessProvider() string {
	if r == nil {
		return ""
	}
	return r.accessProvider
}

func (r *UsageReporter) buildRecord(detail usage.Detail, failed bool, failures ...usage.Failure) usage.Record {
	var fail usage.Failure
	if len(failures) > 0 {
		fail = failures[0]
	}
	if r == nil {
		return usage.Record{Detail: detail, Failed: failed, Fail: fail, Generate: usage.GenerateFlag(true)}
	}
	return r.buildRecordForModel(r.model, detail, failed, fail)
}

func (r *UsageReporter) buildRecordForModel(model string, detail usage.Detail, failed bool, fail usage.Failure) usage.Record {
	if r == nil {
		return usage.Record{Model: model, Detail: detail, Failed: failed, Fail: fail, Generate: usage.GenerateFlag(true)}
	}
	// Additional-model records describe a side model (image generation tool usage) that
	// the upstream response model never refers to, so they must stay empty.
	responseModel := ""
	if model == r.model {
		responseModel = r.ResponseModel()
	}
	return usage.Record{
		RequestID:            r.requestID,
		TraceID:              r.traceID,
		SelectionID:          r.selectionID,
		Provider:             r.provider,
		BaseURL:              r.baseURL,
		ExecutorType:         r.executorType,
		Model:                model,
		Alias:                r.alias,
		Source:               r.source,
		APIKey:               r.apiKey,
		IsNativeKey:          r.isNativeKey,
		AccessProvider:       r.accessProvider,
		SessionID:            r.sessionID,
		ParentSessionID:      r.parentSessionID,
		AuthID:               r.authID,
		AuthIndex:            r.authIndex,
		AccessTokenSHA256:    r.accessTokenFingerprint(),
		AuthType:             r.authType,
		ReasoningEffort:      r.reasoning,
		ServiceTier:          r.serviceTier,
		EffectiveServiceTier: r.effectiveServiceTier,
		ResponseServiceTier:  strings.TrimSpace(detail.ResponseServiceTier),
		ResponseModel:        responseModel,
		Generate:             usage.GenerateFlag(r.generate),
		Stream:               r.stream,
		RequestedAt:          r.requestedAt,
		Latency:              r.latency(),
		TTFT:                 r.ttftDuration(),
		Failed:               failed,
		Fail:                 fail,
		Detail:               detail,
	}
}

func failFromErrors(errs ...error) usage.Failure {
	for _, err := range errs {
		if err == nil {
			continue
		}
		body := strings.TrimSpace(err.Error())
		type responseBodyProvider interface {
			ResponseBody() []byte
		}
		var responseErr responseBodyProvider
		if errors.As(err, &responseErr) && responseErr != nil {
			if responseBody := responseErr.ResponseBody(); len(responseBody) > 0 {
				body = string(responseBody)
			}
		}
		fail := usage.Failure{
			Body:       body,
			StatusCode: clienterror.HTTPStatusFromError(err),
		}
		if truncatedErr, ok := errors.AsType[cliproxyexecutor.StreamTruncatedError](err); ok && truncatedErr != nil && truncatedErr.IsStreamTruncated() {
			fail.Code = usage.FailureStreamTruncatedCode
		}
		return fail
	}
	return usage.Failure{}
}

func (r *UsageReporter) latency() time.Duration {
	if r == nil || r.requestedAt.IsZero() {
		return 0
	}
	latency := time.Since(r.requestedAt)
	if latency < 0 {
		return 0
	}
	return latency
}

func (r *UsageReporter) setTTFT(ttft time.Duration) {
	if r == nil {
		return
	}
	if ttft < 0 {
		ttft = 0
	}
	r.ttftMu.Lock()
	if r.ttftSet {
		r.ttftMu.Unlock()
		return
	}
	r.ttft = ttft
	r.ttftSet = true
	r.ttftStart = time.Time{}
	r.ttftMu.Unlock()
}

func (r *UsageReporter) ttftDuration() time.Duration {
	if r == nil {
		return 0
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	if r.ttftSet {
		return r.ttft
	}
	if r.firstPacketSet {
		return r.firstPacketDuration
	}
	return 0
}

type usageTTFTRoundTripper struct {
	base       http.RoundTripper
	reporter   *UsageReporter
	packetOnly bool
}

func (t usageTTFTRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(req.Context())
	t.reporter.StartResponseTTFT()
	resp, errRoundTrip := t.base.RoundTrip(req)
	if errRoundTrip != nil {
		return resp, errRoundTrip
	}
	if t.packetOnly {
		t.reporter.ObserveResponsePacketOnly(resp)
	} else {
		t.reporter.ObserveResponse(resp)
	}
	return resp, nil
}

type usageTTFTReadCloser struct {
	io.ReadCloser
	once sync.Once
	mark func()
}

func (r *usageTTFTReadCloser) Read(p []byte) (int, error) {
	if r == nil || r.ReadCloser == nil {
		return 0, io.ErrClosedPipe
	}
	n, errRead := r.ReadCloser.Read(p)
	if n > 0 && r.mark != nil {
		r.once.Do(r.mark)
	}
	return n, errRead
}

func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if usage.HasCapturedIdentity(ctx) {
		return usage.APIKeyFromContext(ctx)
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return usage.APIKeyFromContext(ctx)
	}
	if v, exists := ginCtx.Get("userApiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return usage.APIKeyFromContext(ctx)
}

// AccessProviderFromContext extracts the client authentication provider identifier from the context.
func AccessProviderFromContext(ctx context.Context) string {
	if provider := usage.AccessProviderFromContext(ctx); provider != "" {
		return provider
	}
	if usage.HasCapturedIdentity(ctx) {
		return ""
	}
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("accessProvider"); exists {
		switch value := v.(type) {
		case string:
			return strings.TrimSpace(value)
		case fmt.Stringer:
			return strings.TrimSpace(value.String())
		default:
			return strings.TrimSpace(fmt.Sprintf("%v", value))
		}
	}
	return ""
}

func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	if auth != nil {
		provider := strings.TrimSpace(auth.Provider)
		if strings.EqualFold(provider, "vertex") {
			if auth.Metadata != nil {
				if projectID, ok := auth.Metadata["project_id"].(string); ok {
					if trimmed := strings.TrimSpace(projectID); trimmed != "" {
						return trimmed
					}
				}
				if project, ok := auth.Metadata["project"].(string); ok {
					if trimmed := strings.TrimSpace(project); trimmed != "" {
						return trimmed
					}
				}
			}
		}
		if _, value := auth.AccountInfo(); value != "" {
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				return key
			}
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return trimmed
	}
	return ""
}

func resolveUsageAuthType(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.AuthKind()
}

// StreamUsageBuffer keeps the latest usage detail observed in a stream.
type StreamUsageBuffer struct {
	detail                       usage.Detail
	ok                           bool
	responseModel                string
	seenClaudeToolUseIDs         map[string]struct{}
	seenClaudeToolResultIDs      map[string]struct{}
	claudeWebSearchQueries       int64
	claudeCompletedSearches      int64
	claudeSearchResultsCount     int64
	claudeHasCompletedSearch     bool
	claudeReportedWebSearchCalls *int64
	seenOpenAIToolCallKeys       map[string]struct{}
	openAIToolCallsRequested     int64
	openAIExplicitToolCalls      bool
	geminiCallCounter            geminiFunctionCallCounter
	openAIStreamImages           int64
	openAIExplicitImageCount     bool
	openAIStreamImageDetails     *usage.ImageDetails
	responsesOutputByIndex       map[int64][]byte
	responsesOutputFallback      [][]byte
	geminiToolDetails            *usage.ToolUsageDetails
	geminiWebSearchRequests      *int64
	geminiImageCount             int64
	geminiImageDetails           *usage.ImageDetails
	requestedImageDetails        *usage.ImageDetails
}

var (
	openAIStreamUsageMarker       = []byte(`"usage"`)
	openAIStreamCacheMarker       = []byte(`"prompt_cache_`)
	openAIStreamServiceTierMarker = []byte(`"service_tier"`)
	openAIStreamToolCallsMarker   = []byte(`"tool_calls"`)
	openAIStreamImageDoneMarker   = []byte(`.completed"`)
	responsesOutputItemDoneMarker = []byte(`"response.output_item.done"`)
	claudeStreamBlockStartMarker  = []byte(`"content_block_start"`)
	geminiUsageMetaMarker         = []byte(`"usageMetadata"`)
	geminiUsageMetaSnakeMarker    = []byte(`"usage_metadata"`)
	geminiGroundingMetaMarker     = []byte(`"groundingMetadata"`)
	geminiGroundingSnakeMarker    = []byte(`"grounding_metadata"`)
	geminiURLContextMetaMarker    = []byte(`"urlContextMetadata"`)
	geminiURLContextSnakeMarker   = []byte(`"url_context_metadata"`)
	geminiExecCodeMarker          = []byte(`"executableCode"`)
	geminiExecCodeSnakeMarker     = []byte(`"executable_code"`)
	geminiFunctionCallMarker      = []byte(`"functionCall"`)
	geminiFunctionCallSnakeMarker = []byte(`"function_call"`)
	geminiInlineDataMarker        = []byte(`"inlineData"`)
	geminiInlineDataSnakeMarker   = []byte(`"inline_data"`)
	geminiPredictionsMarker       = []byte(`"predictions"`)
)

// unsafeBytesToString converts a byte slice to a string without heap allocation.
// The returned string must only be used for read-only gjson traversal within the
// call frame, and any string values stored into long-lived structs are cloned.
func unsafeBytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func cloneTrimmedString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Clone(s)
}

func hasGeminiCandidateArtifactMarker(payload []byte) bool {
	return bytes.Contains(payload, geminiGroundingMetaMarker) ||
		bytes.Contains(payload, geminiGroundingSnakeMarker) ||
		bytes.Contains(payload, geminiURLContextMetaMarker) ||
		bytes.Contains(payload, geminiURLContextSnakeMarker) ||
		bytes.Contains(payload, geminiExecCodeMarker) ||
		bytes.Contains(payload, geminiExecCodeSnakeMarker) ||
		bytes.Contains(payload, geminiFunctionCallMarker) ||
		bytes.Contains(payload, geminiFunctionCallSnakeMarker) ||
		bytes.Contains(payload, geminiInlineDataMarker) ||
		bytes.Contains(payload, geminiInlineDataSnakeMarker) ||
		bytes.Contains(payload, geminiPredictionsMarker)
}

// Observe records detail when ok is true, allowing the final stream usage to win.
func (b *StreamUsageBuffer) Observe(detail usage.Detail, ok bool) {
	if b == nil || !ok {
		return
	}
	responseServiceTier := strings.TrimSpace(detail.ResponseServiceTier)
	if responseServiceTier == "" || hasNonZeroTokenUsage(detail) || hasStructuredUsageDetail(detail) {
		preservedTier := b.detail.ResponseServiceTier
		preservedUpstreamTier := b.detail.UpstreamServiceTier
		b.detail = detail
		if b.detail.ResponseServiceTier == "" {
			b.detail.ResponseServiceTier = preservedTier
		}
		if b.detail.UpstreamServiceTier == "" {
			b.detail.UpstreamServiceTier = preservedUpstreamTier
		}
	} else {
		b.detail.ResponseServiceTier = responseServiceTier
		if b.detail.UpstreamServiceTier == "" {
			b.detail.UpstreamServiceTier = detail.UpstreamServiceTier
		}
	}
	b.applyGeminiStreamObservations()
	b.ok = true
}

func (b *StreamUsageBuffer) rememberGeminiCandidateArtifacts(detail *usage.Detail) {
	if b == nil || detail == nil {
		return
	}
	if detail.ToolUsageDetails != nil {
		// Content parts are incremental executions; grounding metadata is a snapshot.
		previous := b.geminiToolDetails
		b.geminiToolDetails = mergeToolUsageDetails(previous, detail.ToolUsageDetails)
		if previous != nil {
			addCounter := func(prev, delta *int64) *int64 {
				if prev == nil || delta == nil {
					return mergeInt64Ptr(prev, delta)
				}
				if sum, ok := safeUsageTokenSum(*prev, *delta); ok {
					return usage.Int64Ptr(sum)
				}
				return nil
			}
			b.geminiToolDetails.ToolCallsRequested = addCounter(
				previous.ToolCallsRequested, detail.ToolUsageDetails.ToolCallsRequested,
			)
			b.geminiToolDetails.CodeExecutionCalls = addCounter(
				previous.CodeExecutionCalls, detail.ToolUsageDetails.CodeExecutionCalls,
			)
		}
	}
	if detail.WebSearchRequests != nil {
		b.geminiWebSearchRequests = mergeInt64Ptr(b.geminiWebSearchRequests, detail.WebSearchRequests)
	}
	if detail.ImageCount != nil && *detail.ImageCount > 0 {
		b.geminiImageCount += *detail.ImageCount
	}
	if detail.ImageDetails != nil {
		b.geminiImageDetails = detail.ImageDetails.Clone()
	}
}

func (b *StreamUsageBuffer) applyGeminiStreamObservations() {
	if b == nil {
		return
	}
	if b.geminiToolDetails != nil {
		b.detail.ToolUsageDetails = mergeToolUsageDetails(b.detail.ToolUsageDetails, b.geminiToolDetails)
	}
	if b.geminiWebSearchRequests != nil {
		b.detail.WebSearchRequests = usage.CloneInt64Ptr(b.geminiWebSearchRequests)
	}
	if b.geminiImageCount > 0 {
		b.detail.ImageCount = usage.Int64Ptr(b.geminiImageCount)
	}
	if b.geminiImageDetails != nil && b.detail.ImageDetails == nil {
		b.detail.ImageDetails = b.geminiImageDetails.Clone()
	}
	if b.geminiImageCount > 0 && b.requestedImageDetails != nil && b.detail.RequestedImageDetails == nil {
		b.detail.RequestedImageDetails = b.requestedImageDetails.Clone()
	}
}

// SetRequestPayload records request-side image/media configuration on the stream buffer
// so it can be attached as RequestedImageDetails if the stream produces images.
func (b *StreamUsageBuffer) SetRequestPayload(requestPayload []byte, contentType ...string) {
	if b == nil || len(requestPayload) == 0 {
		return
	}
	b.requestedImageDetails = ParseRequestImageDetails(requestPayload, contentType...)
}

// ObserveGeminiStream records and merges usage and candidate tool/media artifacts from a Gemini SSE line.
func (b *StreamUsageBuffer) ObserveGeminiStream(line []byte) {
	b.observeGeminiFamilyStream(line, false)
}

// ObserveAntigravityStream records and merges usage and candidate tool/media artifacts from an Antigravity SSE line.
func (b *StreamUsageBuffer) ObserveAntigravityStream(line []byte) {
	b.observeGeminiFamilyStream(line, true)
}

func (b *StreamUsageBuffer) observeGeminiFamilyStream(line []byte, allowResponseWrapper bool) {
	if b == nil {
		return
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	var (
		node           gjson.Result
		candidatesNode gjson.Result
		predsNode      gjson.Result
	)
	root.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "usageMetadata", "usage_metadata":
			if !node.Exists() {
				node = val
			}
		case "candidates":
			candidatesNode = val
		case "predictions":
			predsNode = val
		case "response":
			if allowResponseWrapper && val.IsObject() {
				val.ForEach(func(rk, rv gjson.Result) bool {
					switch rk.String() {
					case "usageMetadata", "usage_metadata":
						if !node.Exists() {
							node = rv
						}
					case "candidates":
						if !candidatesNode.Exists() {
							candidatesNode = rv
						}
					}
					return true
				})
			}
		}
		return true
	})
	if candidatesNode.Exists() || predsNode.Exists() {
		var artifactDetail usage.Detail
		applyGeminiNodesArtifactsWithCounter(&artifactDetail, candidatesNode, predsNode, &b.geminiCallCounter)
		b.rememberGeminiCandidateArtifacts(&artifactDetail)
		if !node.Exists() && (artifactDetail.ToolUsageDetails != nil || artifactDetail.ImageCount != nil) {
			b.applyGeminiStreamObservations()
			b.ok = true
			return
		}
	}
	if !node.Exists() {
		return
	}
	detail := parseGeminiFamilyUsageDetail(node)
	if !allowResponseWrapper && !hasNonZeroTokenUsage(detail) {
		return
	}
	if b.ok {
		update := detail
		mergeStreamStructuredFields(&detail, b.detail, update)
		// Explicit zero modality counts are authoritative, not absent fields.
		for _, pair := range []struct {
			dst **int64
			src *int64
		}{
			{&detail.TextInputTokens, update.TextInputTokens},
			{&detail.ImageInputTokens, update.ImageInputTokens},
			{&detail.AudioInputTokens, update.AudioInputTokens},
			{&detail.VideoInputTokens, update.VideoInputTokens},
			{&detail.FileInputTokens, update.FileInputTokens},
			{&detail.CachedTextTokens, update.CachedTextTokens},
			{&detail.TextOutputTokens, update.TextOutputTokens},
			{&detail.ImageOutputTokens, update.ImageOutputTokens},
			{&detail.AudioOutputTokens, update.AudioOutputTokens},
		} {
			if pair.src != nil {
				*pair.dst = usage.CloneInt64Ptr(pair.src)
			}
		}
	}
	b.Observe(detail, true)
}

// ObserveOpenAIStream records response-tier state and the latest usage from an
// OpenAI-style stream while avoiding JSON parsing for irrelevant chunks.
func (b *StreamUsageBuffer) ObserveOpenAIStream(line []byte) {
	if b == nil {
		return
	}
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return
	}

	hasUsageCandidate := bytes.Contains(payload, openAIStreamUsageMarker)
	hasCacheCandidate := bytes.Contains(payload, openAIStreamCacheMarker)
	needTier := b.detail.ResponseServiceTier == "" || hasUsageCandidate
	hasTierCandidate := needTier && bytes.Contains(payload, openAIStreamServiceTierMarker)
	hasToolCallsCandidate := bytes.Contains(payload, openAIStreamToolCallsMarker)
	hasImageCompletedCandidate := bytes.Contains(payload, openAIStreamImageDoneMarker)
	if b.responseModel == "" {
		if model, _ := extractGenericResponseModelEvent(payload); model != "" {
			b.responseModel = model
		}
	}
	if !hasUsageCandidate && !hasCacheCandidate && !hasTierCandidate && !hasToolCallsCandidate && !hasImageCompletedCandidate {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}

	if hasToolCallsCandidate {
		b.observeOpenAIStreamToolCalls(payload)
	}
	if hasImageCompletedCandidate {
		b.observeOpenAIImageStreamCompleted(payload)
	}

	detail := usage.Detail{}
	usageOK := false
	if hasUsageCandidate || hasCacheCandidate {
		root := gjson.Parse(unsafeBytesToString(payload))
		usageNode := root.Get("usage")
		detail, usageOK = parseOpenAIStyleUsageWithCache(root, usageNode)
		if detail.ToolUsageDetails != nil && detail.ToolUsageDetails.ToolCallsRequested != nil {
			b.openAIExplicitToolCalls = true
		}
		if detail.ImageCount != nil {
			b.openAIExplicitImageCount = true
		}
		if usageOK && b.ok {
			// Metadata-only frames do not replace the last token snapshot.
			if !hasOpenAIStyleUsageTokenFields(usageNode) {
				detail.InputTokens = b.detail.InputTokens
				detail.OutputTokens = b.detail.OutputTokens
				detail.ReasoningTokens = b.detail.ReasoningTokens
				detail.CachedTokens = b.detail.CachedTokens
				detail.CacheReadTokens = b.detail.CacheReadTokens
				detail.CacheCreationTokens = b.detail.CacheCreationTokens
				detail.TotalTokens = b.detail.TotalTokens
				detail.TokenBreakdown = b.detail.TokenBreakdown
			}
			mergeStreamStructuredFields(&detail, b.detail, detail)
		}
	}
	if hasTierCandidate {
		detail.ResponseServiceTier = extractResponseServiceTierFromValidJSON(payload)
		if detail.UpstreamServiceTier == "" {
			detail.UpstreamServiceTier = detail.ResponseServiceTier
		}
	}
	b.Observe(detail, usageOK || detail.ResponseServiceTier != "")
	b.applyOpenAIStreamObservations()
}

func (b *StreamUsageBuffer) observeOpenAIStreamToolCalls(payload []byte) {
	root := gjson.ParseBytes(payload)
	for choiceIdx, choice := range root.Get("choices").Array() {
		if idx := choice.Get("index"); idx.Type == gjson.Number {
			choiceIdx = int(idx.Int())
		}
		tcNode := choice.Get("delta.tool_calls")
		if !tcNode.IsArray() {
			tcNode = choice.Get("message.tool_calls")
		}
		if !tcNode.IsArray() {
			continue
		}
		for itemIdx, tc := range tcNode.Array() {
			key := ""
			if idxNode := tc.Get("index"); idxNode.Type == gjson.Number {
				key = fmt.Sprintf("%d:%d", choiceIdx, idxNode.Int())
			} else if id := strings.TrimSpace(tc.Get("id").String()); id != "" {
				key = fmt.Sprintf("%d:id:%s", choiceIdx, id)
			} else {
				key = fmt.Sprintf("%d:pos:%d", choiceIdx, itemIdx)
			}
			if b.seenOpenAIToolCallKeys == nil {
				b.seenOpenAIToolCallKeys = make(map[string]struct{})
			}
			if _, exists := b.seenOpenAIToolCallKeys[key]; exists {
				continue
			}
			b.seenOpenAIToolCallKeys[key] = struct{}{}
			b.openAIToolCallsRequested++
		}
	}
}

func (b *StreamUsageBuffer) observeOpenAIImageStreamCompleted(payload []byte) {
	root := gjson.ParseBytes(payload)
	evType := strings.TrimSpace(root.Get("type").String())
	if evType != "image_generation.completed" && evType != "image_edit.completed" {
		return
	}
	b.openAIStreamImages++
	details := &usage.ImageDetails{
		Size:         strings.TrimSpace(root.Get("size").String()),
		Quality:      strings.TrimSpace(root.Get("quality").String()),
		Background:   strings.TrimSpace(root.Get("background").String()),
		OutputFormat: strings.TrimSpace(root.Get("output_format").String()),
		MimeType:     strings.TrimSpace(root.Get("mime_type").String()),
	}
	if !isEmptyImageDetails(details) {
		b.openAIStreamImageDetails = details
	}
}

func (b *StreamUsageBuffer) applyOpenAIStreamObservations() {
	if b == nil {
		return
	}
	if b.openAIToolCallsRequested > 0 {
		if b.detail.ToolUsageDetails == nil {
			b.detail.ToolUsageDetails = &usage.ToolUsageDetails{}
		}
		if !b.openAIExplicitToolCalls {
			b.detail.ToolUsageDetails.ToolCallsRequested = usage.Int64Ptr(b.openAIToolCallsRequested)
		}
		b.ok = true
	}
	if b.openAIStreamImages > 0 {
		if !b.openAIExplicitImageCount || b.detail.ImageCount == nil {
			b.detail.ImageCount = usage.Int64Ptr(b.openAIStreamImages)
		}
		if b.openAIStreamImageDetails != nil {
			b.detail.ImageDetails = b.openAIStreamImageDetails.Clone()
		}
		if b.detail.RequestedImageDetails == nil && b.requestedImageDetails != nil {
			b.detail.RequestedImageDetails = b.requestedImageDetails.Clone()
		}
		b.ok = true
	}
}

// ObserveClaudeStream records and merges usage from a Claude SSE line.
func (b *StreamUsageBuffer) ObserveClaudeStream(line []byte) {
	if b == nil {
		return
	}
	payload := jsonPayload(line)
	if b.responseModel == "" && len(payload) > 0 {
		if model, _ := extractClaudeResponseModelEvent(payload); model != "" {
			b.responseModel = model
		}
	}
	if len(payload) > 0 {
		b.observeClaudeStreamToolBlocks(payload)
	}
	if detail, ok := ParseClaudeStreamUsage(line); ok {
		if detail.ToolUsageDetails != nil && detail.ToolUsageDetails.WebSearchCalls != nil && *detail.ToolUsageDetails.WebSearchCalls > 0 {
			b.claudeReportedWebSearchCalls = usage.CloneInt64Ptr(detail.ToolUsageDetails.WebSearchCalls)
		}
		ObserveMergedStreamUsage(b, detail)
		b.applyClaudeStreamToolObservations()
	}
}

// ObserveClaudeSSEEvent extracts joined multi-line SSE data payloads from rawEvent
// and records any model or usage details when a frame was split across lines.
func (b *StreamUsageBuffer) ObserveClaudeSSEEvent(rawEvent []byte) {
	if b == nil || len(rawEvent) == 0 {
		return
	}
	var dataLines [][]byte
	for _, line := range bytes.Split(rawEvent, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) > 0 {
			dataLines = append(dataLines, payload)
		}
	}
	if len(dataLines) <= 1 {
		return
	}
	joined := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if len(joined) == 0 || !gjson.ValidBytes(joined) {
		return
	}
	b.ObserveClaudeStream(joined)
}

func (b *StreamUsageBuffer) observeClaudeStreamToolBlocks(payload []byte) {
	if b == nil || !bytes.Contains(payload, claudeStreamBlockStartMarker) || !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	if root.Get("type").String() != "content_block_start" {
		return
	}
	cb := root.Get("content_block")
	if !cb.Exists() || !cb.IsObject() {
		return
	}
	switch cb.Get("type").String() {
	case "server_tool_use":
		if strings.TrimSpace(cb.Get("name").String()) != "web_search" {
			return
		}
		id := strings.TrimSpace(cb.Get("id").String())
		if id != "" {
			if b.seenClaudeToolUseIDs == nil {
				b.seenClaudeToolUseIDs = make(map[string]struct{})
			}
			if _, exists := b.seenClaudeToolUseIDs[id]; exists {
				return
			}
			b.seenClaudeToolUseIDs[strings.Clone(id)] = struct{}{}
		}
		b.claudeWebSearchQueries++
		b.applyClaudeStreamToolObservations()
	case "web_search_tool_result":
		content := cb.Get("content")
		if !content.Exists() || !content.IsArray() {
			return
		}
		toolUseID := strings.TrimSpace(cb.Get("tool_use_id").String())
		if toolUseID != "" {
			if b.seenClaudeToolResultIDs == nil {
				b.seenClaudeToolResultIDs = make(map[string]struct{})
			}
			if _, exists := b.seenClaudeToolResultIDs[toolUseID]; exists {
				return
			}
			b.seenClaudeToolResultIDs[strings.Clone(toolUseID)] = struct{}{}
		}
		b.claudeHasCompletedSearch = true
		b.claudeCompletedSearches++
		for _, item := range content.Array() {
			if item.Get("type").String() == "web_search_result" {
				b.claudeSearchResultsCount++
			}
		}
		b.applyClaudeStreamToolObservations()
	}
}

func (b *StreamUsageBuffer) applyClaudeStreamToolObservations() {
	if b == nil {
		return
	}
	if b.claudeWebSearchQueries == 0 && !b.claudeHasCompletedSearch {
		return
	}
	if b.detail.ToolUsageDetails == nil {
		b.detail.ToolUsageDetails = &usage.ToolUsageDetails{}
	}
	if b.claudeWebSearchQueries > 0 {
		b.detail.ToolUsageDetails.WebSearchQueries = usage.Int64Ptr(b.claudeWebSearchQueries)
	}
	if b.claudeHasCompletedSearch {
		b.detail.ToolUsageDetails.NumSearchResults = usage.Int64Ptr(b.claudeSearchResultsCount)
		calls := b.claudeCompletedSearches
		if b.claudeReportedWebSearchCalls != nil {
			calls = *b.claudeReportedWebSearchCalls
		}
		b.detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(calls)
		b.detail.WebSearchRequests = usage.Int64Ptr(calls)
	}
	b.ok = true
}

// Publish emits the latest observed usage detail, if any.
func (b *StreamUsageBuffer) Publish(ctx context.Context, reporter *UsageReporter) bool {
	if b == nil || !b.ok || reporter == nil {
		return false
	}
	if b.responseModel != "" && reporter.ResponseModel() == "" {
		reporter.SetResponseModel(b.responseModel)
	}
	reporter.Publish(ctx, b.detail)
	return true
}

// PublishFailure emits the latest observed usage detail together with failure details.
func (b *StreamUsageBuffer) PublishFailure(ctx context.Context, reporter *UsageReporter, errs ...error) bool {
	if b == nil || reporter == nil {
		return false
	}
	if b.responseModel != "" && reporter.ResponseModel() == "" {
		reporter.SetResponseModel(b.responseModel)
	}
	reporter.PublishFailureWithDetail(ctx, b.detail, errs...)
	return true
}

// Detail returns the latest observed usage detail.
func (b *StreamUsageBuffer) Detail() (usage.Detail, bool) {
	if b == nil || !b.ok {
		return usage.Detail{}, false
	}
	return b.detail, true
}

// ResponseModel returns the latest model observed in the stream buffer.
func (b *StreamUsageBuffer) ResponseModel() string {
	if b == nil {
		return ""
	}
	return b.responseModel
}

// ObserveResponsesStream collects response.output_item.done items across a
// Responses SSE/WebSocket stream and parses terminal response.usage payloads
// with those collected items so tool usage is preserved when response.output is empty.
func (b *StreamUsageBuffer) ObserveResponsesStream(line []byte) bool {
	if b == nil {
		return false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	if b.responseModel == "" {
		if model, _ := extractGenericResponseModelEvent(payload); model != "" {
			b.responseModel = model
		}
	}
	if bytes.Contains(payload, responsesOutputItemDoneMarker) {
		root := gjson.Parse(unsafeBytesToString(payload))
		if strings.TrimSpace(root.Get("type").String()) == "response.output_item.done" {
			if item := root.Get("item"); item.Exists() && item.IsObject() {
				rawItem := bytes.Clone([]byte(item.Raw))
				if idxNode := root.Get("output_index"); idxNode.Type == gjson.Number && idxNode.Int() >= 0 {
					if b.responsesOutputByIndex == nil {
						b.responsesOutputByIndex = make(map[int64][]byte)
					}
					b.responsesOutputByIndex[idxNode.Int()] = rawItem
				} else {
					b.responsesOutputFallback = append(b.responsesOutputFallback, rawItem)
				}
			}
			return true
		}
	}
	detail, ok := ParseCodexUsageWithOutputItems(payload, b.responsesOutputByIndex, b.responsesOutputFallback)
	if !ok || (!hasNonZeroTokenUsage(detail) && !hasStructuredUsageDetail(detail) && detail.ResponseServiceTier == "") {
		return false
	}
	b.Observe(detail, true)
	return true
}

func ParseCodexUsage(data []byte) (usage.Detail, bool) {
	return ParseCodexUsageWithOutputItems(data, nil, nil)
}

func ParseCodexUsageWithOutputItems(data []byte, itemsByIndex map[int64][]byte, fallback [][]byte) (usage.Detail, bool) {
	if len(data) == 0 {
		return usage.Detail{}, false
	}
	var responseServiceTier string
	if bytes.Contains(data, openAIStreamServiceTierMarker) {
		responseServiceTier = extractResponseServiceTier(data)
	}
	root := gjson.Parse(unsafeBytesToString(data))
	usageNode := root.Get("response.usage")
	detail, ok := parseOpenAIStyleUsageWithCache(root, usageNode)
	if !ok {
		if responseServiceTier == "" {
			return usage.Detail{}, false
		}
		return usage.Detail{
			ResponseServiceTier: responseServiceTier,
			UpstreamServiceTier: responseServiceTier,
		}, true
	}
	detail.ResponseServiceTier = responseServiceTier
	if detail.UpstreamServiceTier == "" {
		detail.UpstreamServiceTier = responseServiceTier
	}
	if wsReq := root.Get("response.tool_usage.web_search.num_requests"); wsReq.Type == gjson.Number {
		v := wsReq.Int()
		if detail.ToolUsageDetails == nil {
			detail.ToolUsageDetails = &usage.ToolUsageDetails{}
		}
		if detail.ToolUsageDetails.WebSearchCalls == nil || (v > 0 && *detail.ToolUsageDetails.WebSearchCalls == 0) {
			detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(v)
		}
		if detail.WebSearchRequests == nil || (v > 0 && *detail.WebSearchRequests == 0) {
			detail.WebSearchRequests = usage.Int64Ptr(v)
		}
	}
	outputNode := root.Get("response.output")
	if outputNode.Exists() && outputNode.IsArray() && len(outputNode.Array()) > 0 {
		applyResponsesOutputItemsUsage(&detail, outputNode)
	} else if len(itemsByIndex) > 0 || len(fallback) > 0 {
		applyResponsesCollectedOutputItemsUsage(&detail, itemsByIndex, fallback)
	}
	// When response.tool_usage.image_gen is present, CodexExecutor publishes a dedicated
	// additional usage record for the image model (e.g. gpt-image-2) carrying ImageCount
	// and ImageDetails; clear them on the orchestrator model record to avoid double-counting.
	if hasOpenAIStyleUsageTokenFields(root.Get("response.tool_usage.image_gen")) {
		detail.ImageCount = nil
		detail.ImageDetails = nil
	}
	return detail, true
}

func applyResponsesCacheRetention(detail *usage.Detail, root, usageNode gjson.Result) {
	if detail == nil {
		return
	}
	retention := strings.TrimSpace(firstExistingUsageNode(
		root,
		"response.prompt_cache_retention",
		"prompt_cache_retention",
	).String())
	if retention == "" {
		retention = strings.TrimSpace(firstExistingUsageNode(
			usageNode,
			"prompt_cache_retention",
			"prompt_cache_options.retention",
		).String())
	}
	options := firstExistingUsageNode(root, "response.prompt_cache_options", "prompt_cache_options")
	mode := cloneTrimmedString(options.Get("mode").String())
	ttl := cloneTrimmedString(options.Get("ttl").String())
	buckets := firstExistingUsageNode(usageNode, "input_tokens_details.cache_write_tokens_details", "prompt_tokens_details.cache_write_tokens_details")
	short := buckets.Get("ttl_30m_tokens")
	long := buckets.Get("ttl_12h_tokens")
	if retention == "" && mode == "" && ttl == "" && short.Type != gjson.Number && long.Type != gjson.Number {
		return
	}
	if detail.CacheDetails == nil {
		detail.CacheDetails = &usage.CacheDetails{}
	}
	detail.CacheDetails.Retention = strings.Clone(retention)
	detail.CacheDetails.Mode = mode
	detail.CacheDetails.TTL = ttl
	if short.Type == gjson.Number {
		detail.CacheCreation30mTokens = usage.Int64Ptr(short.Int())
		detail.CacheDetails.Ephemeral30mInputTokens = usage.Int64Ptr(short.Int())
	}
	if long.Type == gjson.Number {
		detail.CacheCreation12hTokens = usage.Int64Ptr(long.Int())
		detail.CacheDetails.Ephemeral12hInputTokens = usage.Int64Ptr(long.Int())
	}
}

func ParseCodexImageToolUsage(data []byte) (usage.Detail, bool) {
	return ParseCodexImageToolUsageWithOutputItems(nil, data, nil, nil)
}

func ParseCodexImageToolUsageWithOutputItems(requestBody, data []byte, itemsByIndex map[int64][]byte, fallback [][]byte) (usage.Detail, bool) {
	if len(data) == 0 {
		return usage.Detail{}, false
	}
	root := gjson.Parse(unsafeBytesToString(data))
	usageNode := root.Get("response.tool_usage.image_gen")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return usage.Detail{}, false
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	applyCodexImageToolOutputMetadata(&detail, requestBody, root.Get("response.output"), itemsByIndex, fallback)
	return detail, true
}

func applyCodexImageToolOutputMetadata(detail *usage.Detail, requestBody []byte, outputNode gjson.Result, itemsByIndex map[int64][]byte, fallback [][]byte) {
	if detail == nil {
		return
	}
	var items []gjson.Result
	if outputNode.Exists() && outputNode.IsArray() && len(outputNode.Array()) > 0 {
		items = outputNode.Array()
	} else {
		items = parseCollectedOutputItems(itemsByIndex, fallback)
	}
	var count int64
	var respMeta *usage.ImageDetails
	for _, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "image_generation_call" {
			continue
		}
		if status := strings.TrimSpace(item.Get("status").String()); strings.EqualFold(status, "failed") || strings.EqualFold(status, "in_progress") {
			continue
		}
		count++
		if respMeta == nil {
			meta := &usage.ImageDetails{
				Size:          cloneTrimmedString(item.Get("size").String()),
				Resolution:    cloneTrimmedString(item.Get("resolution").String()),
				AspectRatio:   cloneTrimmedString(item.Get("aspect_ratio").String()),
				Quality:       cloneTrimmedString(item.Get("quality").String()),
				Background:    cloneTrimmedString(item.Get("background").String()),
				OutputFormat:  cloneTrimmedString(item.Get("output_format").String()),
				MimeType:      cloneTrimmedString(firstExistingUsageNode(item, "mime_type", "mimeType").String()),
				InputFidelity: cloneTrimmedString(item.Get("input_fidelity").String()),
			}
			if !isEmptyImageDetails(meta) {
				respMeta = meta
			}
		}
	}
	if count > 0 {
		detail.ImageCount = usage.Int64Ptr(count)
	}
	detail.ImageDetails = respMeta
	detail.RequestedImageDetails = ParseRequestImageDetails(requestBody)
}

func ParseOpenAIUsage(data []byte) usage.Detail {
	if len(data) == 0 {
		return usage.Detail{}
	}
	var responseServiceTier string
	if bytes.Contains(data, openAIStreamServiceTierMarker) {
		responseServiceTier = extractResponseServiceTier(data)
	}
	root := gjson.Parse(unsafeBytesToString(data))
	usageNode := root.Get("usage")
	detail, _ := parseOpenAIStyleUsageWithCache(root, usageNode)
	detail.ResponseServiceTier = responseServiceTier
	if detail.UpstreamServiceTier == "" {
		detail.UpstreamServiceTier = responseServiceTier
	}
	applyOpenAIChatToolCallsUsage(&detail, root)
	if outputNode := root.Get("output"); outputNode.Exists() && outputNode.IsArray() {
		applyResponsesOutputItemsUsage(&detail, outputNode)
	}
	return detail
}

func applyOpenAIChatToolCallsUsage(detail *usage.Detail, root gjson.Result) {
	if detail == nil || !root.Exists() || !root.IsObject() {
		return
	}
	var requested int64
	for _, choice := range root.Get("choices").Array() {
		if tc := choice.Get("message.tool_calls"); tc.IsArray() {
			requested += int64(len(tc.Array()))
		} else if tc := choice.Get("delta.tool_calls"); tc.IsArray() {
			requested += int64(len(tc.Array()))
		}
	}
	if requested > 0 {
		if detail.ToolUsageDetails == nil {
			detail.ToolUsageDetails = &usage.ToolUsageDetails{}
		}
		if detail.ToolUsageDetails.ToolCallsRequested == nil {
			detail.ToolUsageDetails.ToolCallsRequested = usage.Int64Ptr(requested)
		}
	}
}

// parseOpenAIStyleUsageWithCache keeps structured-only usage and cache metadata
// consistent across Chat, Responses, and streaming entry points.
func parseOpenAIStyleUsageWithCache(root, usageNode gjson.Result) (usage.Detail, bool) {
	var detail usage.Detail
	hasTokens := hasOpenAIStyleUsageTokenFields(usageNode)
	if hasTokens {
		detail = parseOpenAIStyleUsageNode(usageNode)
	} else {
		populateOpenAIStyleStructuredFields(&detail, usageNode)
	}
	applyResponsesCacheRetention(&detail, root, usageNode)
	return detail, hasTokens || hasStructuredUsageDetail(detail)
}

func hasStructuredUsageDetail(d usage.Detail) bool {
	return d.CacheDetails != nil || d.ToolUsageDetails != nil || d.CostDetails != nil ||
		d.WebSearchRequests != nil || d.ImageCount != nil || d.ImageDetails != nil ||
		d.RequestedImageDetails != nil || d.MediaCounts != nil || d.Iterations != nil ||
		d.CacheCreation5mTokens != nil || d.CacheCreation30mTokens != nil ||
		d.CacheCreation1hTokens != nil || d.CacheCreation12hTokens != nil ||
		d.TextInputTokens != nil || d.ImageInputTokens != nil || d.AudioInputTokens != nil ||
		d.VideoInputTokens != nil || d.FileInputTokens != nil || d.CachedTextTokens != nil ||
		d.TextOutputTokens != nil || d.ImageOutputTokens != nil || d.AudioOutputTokens != nil ||
		d.AcceptedPredictionTokens != nil || d.RejectedPredictionTokens != nil ||
		d.Speed != "" || d.InferenceGeo != ""
}

func hasOpenAIStyleUsageTokenFields(usageNode gjson.Result) bool {
	if !usageNode.Exists() || !usageNode.IsObject() {
		return false
	}
	return usageNode.Get("total_tokens").Exists() || hasOpenAIStyleUsageBucketFields(usageNode)
}

func hasOpenAIStyleUsageBucketFields(usageNode gjson.Result) bool {
	return usageNode.Get("prompt_tokens").Exists() ||
		usageNode.Get("input_tokens").Exists() ||
		usageNode.Get("completion_tokens").Exists() ||
		usageNode.Get("output_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("completion_tokens_details.reasoning_tokens").Exists() ||
		usageNode.Get("output_tokens_details.reasoning_tokens").Exists() ||
		usageNode.Get("reasoning_tokens").Exists()
}

func parseOpenAIStyleUsageNode(usageNode gjson.Result) usage.Detail {
	if !usageNode.Exists() || !usageNode.IsObject() {
		return usage.Detail{}
	}
	var (
		detail          usage.Detail
		hasInput        bool
		hasOutput       bool
		hasBucketFields bool
		inputDetails    gjson.Result
		promptDetails   gjson.Result
		outputDetails   gjson.Result
		compDetails     gjson.Result
		flatReasoning   gjson.Result
	)
	usageNode.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "prompt_tokens":
			detail.InputTokens = val.Int()
			hasInput = true
			hasBucketFields = true
		case "input_tokens":
			if !hasInput {
				detail.InputTokens = val.Int()
				hasInput = true
			}
			hasBucketFields = true
		case "completion_tokens":
			detail.OutputTokens = val.Int()
			hasOutput = true
			hasBucketFields = true
		case "output_tokens":
			if !hasOutput {
				detail.OutputTokens = val.Int()
				hasOutput = true
			}
			hasBucketFields = true
		case "total_tokens":
			detail.TotalTokens = val.Int()
		case "reasoning_tokens":
			flatReasoning = val
			hasBucketFields = true
		case "prompt_tokens_details":
			promptDetails = val
		case "input_tokens_details":
			inputDetails = val
		case "completion_tokens_details":
			compDetails = val
		case "output_tokens_details":
			outputDetails = val
		}
		return true
	})

	var hasCached, hasCacheCreation, hasReasoning bool
	scanInDetails := func(node gjson.Result) {
		if !node.Exists() || !node.IsObject() {
			return
		}
		node.ForEach(func(k, v gjson.Result) bool {
			switch k.String() {
			case "cached_tokens":
				if !hasCached {
					detail.CachedTokens = v.Int()
					detail.CacheReadTokens = v.Int()
					hasCached = true
				}
				hasBucketFields = true
			case "cache_creation_tokens", "cache_write_tokens":
				if !hasCacheCreation {
					detail.CacheCreationTokens = v.Int()
					hasCacheCreation = true
				}
				hasBucketFields = true
			case "text_tokens":
				if v.Type == gjson.Number && detail.TextInputTokens == nil {
					detail.TextInputTokens = usage.Int64Ptr(v.Int())
				}
			case "image_tokens", "prompt_image_tokens":
				if v.Type == gjson.Number && detail.ImageInputTokens == nil {
					detail.ImageInputTokens = usage.Int64Ptr(v.Int())
				}
			case "audio_tokens":
				if v.Type == gjson.Number && detail.AudioInputTokens == nil {
					detail.AudioInputTokens = usage.Int64Ptr(v.Int())
				}
			case "video_tokens":
				if v.Type == gjson.Number && detail.VideoInputTokens == nil {
					detail.VideoInputTokens = usage.Int64Ptr(v.Int())
				}
			case "file_tokens":
				if v.Type == gjson.Number && detail.FileInputTokens == nil {
					detail.FileInputTokens = usage.Int64Ptr(v.Int())
				}
			case "cached_prompt_text_tokens":
				if v.Type == gjson.Number && detail.CachedTextTokens == nil {
					detail.CachedTextTokens = usage.Int64Ptr(v.Int())
				}
			case "cached_tokens_details":
				if detail.CachedTextTokens == nil && v.IsObject() {
					if tv := v.Get("text_tokens"); tv.Type == gjson.Number {
						detail.CachedTextTokens = usage.Int64Ptr(tv.Int())
					}
				}
			}
			return true
		})
	}
	// Match original precedence: prompt_tokens_details first for cached_tokens, input_tokens_details first for structured fields & cache_creation_tokens.
	if promptDetails.Exists() {
		if c := promptDetails.Get("cached_tokens"); c.Exists() {
			detail.CachedTokens = c.Int()
			detail.CacheReadTokens = c.Int()
			hasCached = true
			hasBucketFields = true
		}
	}
	scanInDetails(inputDetails)
	scanInDetails(promptDetails)

	scanOutDetails := func(node gjson.Result) {
		if !node.Exists() || !node.IsObject() {
			return
		}
		node.ForEach(func(k, v gjson.Result) bool {
			switch k.String() {
			case "reasoning_tokens":
				if !hasReasoning {
					detail.ReasoningTokens = v.Int()
					hasReasoning = true
				}
				hasBucketFields = true
			case "text_tokens":
				if v.Type == gjson.Number && detail.TextOutputTokens == nil {
					detail.TextOutputTokens = usage.Int64Ptr(v.Int())
				}
			case "image_tokens":
				if v.Type == gjson.Number && detail.ImageOutputTokens == nil {
					detail.ImageOutputTokens = usage.Int64Ptr(v.Int())
				}
			case "audio_tokens":
				if v.Type == gjson.Number && detail.AudioOutputTokens == nil {
					detail.AudioOutputTokens = usage.Int64Ptr(v.Int())
				}
			case "accepted_prediction_tokens":
				if v.Type == gjson.Number && detail.AcceptedPredictionTokens == nil {
					detail.AcceptedPredictionTokens = usage.Int64Ptr(v.Int())
				}
			case "rejected_prediction_tokens":
				if v.Type == gjson.Number && detail.RejectedPredictionTokens == nil {
					detail.RejectedPredictionTokens = usage.Int64Ptr(v.Int())
				}
			}
			return true
		})
	}
	if compDetails.Exists() {
		if r := compDetails.Get("reasoning_tokens"); r.Exists() {
			detail.ReasoningTokens = r.Int()
			hasReasoning = true
			hasBucketFields = true
		}
	}
	scanOutDetails(outputDetails)
	scanOutDetails(compDetails)
	if !hasReasoning && flatReasoning.Exists() {
		detail.ReasoningTokens = flatReasoning.Int()
		hasReasoning = true
	}

	if hasBucketFields {
		if hasInput && hasOutput {
			detail.TokenBreakdown = usage.NewSubsetTokenBreakdown(
				detail.InputTokens,
				detail.CacheReadTokens,
				detail.CacheCreationTokens,
				detail.OutputTokens,
				detail.ReasoningTokens,
				detail.TotalTokens,
			)
		} else {
			cacheReadTokens := detail.CacheReadTokens
			cacheCreationTokens := detail.CacheCreationTokens
			if !hasInput {
				cacheReadTokens = 0
				cacheCreationTokens = 0
			}
			reasoningTokens := detail.ReasoningTokens
			if !hasOutput {
				reasoningTokens = 0
			}
			detail.TokenBreakdown = usage.NewPartialSubsetTokenBreakdown(
				detail.InputTokens,
				cacheReadTokens,
				cacheCreationTokens,
				detail.OutputTokens,
				reasoningTokens,
				detail.TotalTokens,
			)
		}
	} else {
		detail.TokenBreakdown = usage.NewUnclassifiedTokenBreakdown(detail.TotalTokens)
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.TokenBreakdown.TotalTokens
	}
	populateOpenAIStyleStructuredFields(&detail, usageNode)
	return detail
}

func populateOpenAIStyleStructuredFields(detail *usage.Detail, usageNode gjson.Result) {
	if detail == nil || !usageNode.Exists() || !usageNode.IsObject() {
		return
	}
	var (
		toolDetails *usage.ToolUsageDetails
		costNode    gjson.Result
		ticksNode   gjson.Result
		byokNode    gjson.Result
		costDetails gjson.Result
		hasCost     bool
	)
	usageNode.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "service_tier":
			if detail.UpstreamServiceTier == "" && val.Type == gjson.String {
				detail.UpstreamServiceTier = cloneTrimmedString(val.String())
			}
		case "attribution":
			if detail.UpstreamServiceTier == "" && val.IsObject() {
				if t := val.Get("tier"); t.Type == gjson.String {
					detail.UpstreamServiceTier = cloneTrimmedString(t.String())
				}
			}
		case "server_side_tool_usage_details", "server_side_tool_usage", "server_tool_use_details", "server_tool_use":
			if parsed := parseOpenAIToolUsageDetailsNode(val); parsed != nil {
				toolDetails = mergeToolUsageDetails(toolDetails, parsed)
			}
		case "num_sources_used":
			if val.Type == gjson.Number {
				if toolDetails == nil {
					toolDetails = &usage.ToolUsageDetails{}
				}
				toolDetails.NumSourcesUsed = usage.Int64Ptr(val.Int())
			}
		case "num_server_side_tools_used":
			if val.Type == gjson.Number {
				if toolDetails == nil {
					toolDetails = &usage.ToolUsageDetails{}
				}
				toolDetails.NumServerSideToolsUsed = usage.Int64Ptr(val.Int())
			}
		case "cost":
			costNode = val
			hasCost = true
		case "cost_in_usd_ticks":
			ticksNode = val
			hasCost = true
		case "is_byok":
			byokNode = val
			hasCost = true
		case "cost_details":
			costDetails = val
			hasCost = true
		}
		return true
	})
	detail.ToolUsageDetails = toolDetails
	if toolDetails != nil {
		if toolDetails.WebSearchCalls != nil {
			detail.WebSearchRequests = usage.CloneInt64Ptr(toolDetails.WebSearchCalls)
		}
		if detail.ImageCount == nil && toolDetails.ImageGenerationCalls != nil && *toolDetails.ImageGenerationCalls > 0 {
			detail.ImageCount = usage.CloneInt64Ptr(toolDetails.ImageGenerationCalls)
		}
	}
	if hasCost {
		_ = costNode
		_ = ticksNode
		_ = byokNode
		_ = costDetails
		detail.CostDetails = parseClaudeCostNode(usageNode)
	}
}

func parseCollectedOutputItems(itemsByIndex map[int64][]byte, fallback [][]byte) []gjson.Result {
	if len(itemsByIndex) == 0 && len(fallback) == 0 {
		return nil
	}
	items := make([]gjson.Result, 0, len(itemsByIndex)+len(fallback))
	if len(itemsByIndex) > 0 {
		indexes := make([]int64, 0, len(itemsByIndex))
		for idx := range itemsByIndex {
			indexes = append(indexes, idx)
		}
		for i := 0; i < len(indexes); i++ {
			for j := i + 1; j < len(indexes); j++ {
				if indexes[j] < indexes[i] {
					indexes[i], indexes[j] = indexes[j], indexes[i]
				}
			}
		}
		for _, idx := range indexes {
			if raw := itemsByIndex[idx]; len(raw) > 0 && gjson.ValidBytes(raw) {
				items = append(items, gjson.ParseBytes(raw))
			}
		}
	}
	for _, raw := range fallback {
		if len(raw) > 0 && gjson.ValidBytes(raw) {
			items = append(items, gjson.ParseBytes(raw))
		}
	}
	return items
}

func applyResponsesCollectedOutputItemsUsage(detail *usage.Detail, itemsByIndex map[int64][]byte, fallback [][]byte) {
	if detail == nil {
		return
	}
	items := parseCollectedOutputItems(itemsByIndex, fallback)
	if len(items) == 0 {
		return
	}
	applyResponsesOutputItemSliceUsage(detail, items)
}

func applyResponsesOutputItemsUsage(detail *usage.Detail, outputNode gjson.Result) {
	if detail == nil || !outputNode.Exists() || !outputNode.IsArray() {
		return
	}
	applyResponsesOutputItemSliceUsage(detail, outputNode.Array())
}

func applyResponsesOutputItemSliceUsage(detail *usage.Detail, items []gjson.Result) {
	if detail == nil || len(items) == 0 {
		return
	}
	seenIDs := make(map[string]struct{})
	var (
		webSearchCalls      int64
		webSearchQueries    int64
		webSearchSources    int64
		imageSearchCalls    int64
		xSearchCalls        int64
		codeExecCalls       int64
		fileSearchCalls     int64
		documentSearchCalls int64
		mcpCalls            int64
		imageGenCalls       int64
		toolCallsRequested  int64
		firstImageMeta      *usage.ImageDetails
	)
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		if status := strings.TrimSpace(item.Get("status").String()); strings.EqualFold(status, "failed") || strings.EqualFold(status, "in_progress") {
			continue
		}
		if id := strings.TrimSpace(firstExistingUsageNode(item, "id", "call_id").String()); id != "" {
			if _, exists := seenIDs[id]; exists {
				continue
			}
			seenIDs[id] = struct{}{}
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "web_search_call":
			webSearchCalls++
			if strings.HasPrefix(strings.TrimSpace(item.Get("id").String()), "is_") {
				imageSearchCalls++
			}
			action := item.Get("action")
			if queries := action.Get("queries"); queries.IsArray() && len(queries.Array()) > 0 {
				webSearchQueries += int64(len(queries.Array()))
			} else if q := strings.TrimSpace(action.Get("query").String()); q != "" || strings.EqualFold(strings.TrimSpace(action.Get("type").String()), "search") {
				webSearchQueries++
			}
			if sources := action.Get("sources"); sources.IsArray() {
				webSearchSources += int64(len(sources.Array()))
			}
		case "file_search_call":
			fileSearchCalls++
		case "code_interpreter_call":
			codeExecCalls++
		case "mcp_call":
			mcpCalls++
		case "image_generation_call":
			imageGenCalls++
			if firstImageMeta == nil {
				meta := &usage.ImageDetails{
					Size:          cloneTrimmedString(item.Get("size").String()),
					Resolution:    cloneTrimmedString(item.Get("resolution").String()),
					AspectRatio:   cloneTrimmedString(item.Get("aspect_ratio").String()),
					Quality:       cloneTrimmedString(item.Get("quality").String()),
					Background:    cloneTrimmedString(item.Get("background").String()),
					OutputFormat:  cloneTrimmedString(item.Get("output_format").String()),
					MimeType:      cloneTrimmedString(firstExistingUsageNode(item, "mime_type", "mimeType").String()),
					InputFidelity: cloneTrimmedString(item.Get("input_fidelity").String()),
				}
				if !isEmptyImageDetails(meta) {
					firstImageMeta = meta
				}
			}
		case "function_call", "local_shell_call", "shell_call", "computer_call", "apply_patch_call":
			toolCallsRequested++
		case "custom_tool_call":
			callID := strings.TrimSpace(item.Get("call_id").String())
			switch {
			case strings.HasPrefix(callID, "xs_"):
				xSearchCalls++
			case strings.HasPrefix(callID, "as_"):
				documentSearchCalls++
			default:
				toolCallsRequested++
			}
		}
	}
	if webSearchCalls == 0 && xSearchCalls == 0 && codeExecCalls == 0 && fileSearchCalls == 0 && documentSearchCalls == 0 && mcpCalls == 0 && imageGenCalls == 0 && toolCallsRequested == 0 {
		return
	}
	if detail.ToolUsageDetails == nil {
		detail.ToolUsageDetails = &usage.ToolUsageDetails{}
	}
	if webSearchCalls > 0 && (detail.ToolUsageDetails.WebSearchCalls == nil || *detail.ToolUsageDetails.WebSearchCalls == 0) {
		detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(webSearchCalls)
		if detail.WebSearchRequests == nil || *detail.WebSearchRequests == 0 {
			detail.WebSearchRequests = usage.Int64Ptr(webSearchCalls)
		}
	} else if imageSearchCalls > 0 && detail.ToolUsageDetails.WebSearchCalls != nil && *detail.ToolUsageDetails.WebSearchCalls == 0 {
		// xAI Image Search (id prefix "is_") is billed at the standard Web Search rate
		// even when server_side_tool_usage_details.web_search_calls reports 0.
		detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(imageSearchCalls)
		detail.WebSearchRequests = usage.Int64Ptr(imageSearchCalls)
	}
	if imageSearchCalls > 0 {
		if detail.ToolUsageDetails.Extra == nil {
			detail.ToolUsageDetails.Extra = make(map[string]int64)
		}
		detail.ToolUsageDetails.Extra["image_search_calls"] = imageSearchCalls
	}
	if xSearchCalls > 0 && detail.ToolUsageDetails.XSearchCalls == nil {
		detail.ToolUsageDetails.XSearchCalls = usage.Int64Ptr(xSearchCalls)
	}
	if codeExecCalls > 0 && detail.ToolUsageDetails.CodeExecutionCalls == nil {
		detail.ToolUsageDetails.CodeExecutionCalls = usage.Int64Ptr(codeExecCalls)
	}
	if fileSearchCalls > 0 && detail.ToolUsageDetails.FileSearchCalls == nil {
		detail.ToolUsageDetails.FileSearchCalls = usage.Int64Ptr(fileSearchCalls)
	}
	if documentSearchCalls > 0 && detail.ToolUsageDetails.DocumentSearchCalls == nil {
		detail.ToolUsageDetails.DocumentSearchCalls = usage.Int64Ptr(documentSearchCalls)
	}
	if mcpCalls > 0 && detail.ToolUsageDetails.MCPCalls == nil {
		detail.ToolUsageDetails.MCPCalls = usage.Int64Ptr(mcpCalls)
	}
	if webSearchQueries > 0 && detail.ToolUsageDetails.WebSearchQueries == nil {
		detail.ToolUsageDetails.WebSearchQueries = usage.Int64Ptr(webSearchQueries)
	}
	if webSearchSources > 0 && detail.ToolUsageDetails.NumSourcesUsed == nil {
		detail.ToolUsageDetails.NumSourcesUsed = usage.Int64Ptr(webSearchSources)
	}
	if imageGenCalls > 0 {
		if detail.ToolUsageDetails.ImageGenerationCalls == nil {
			detail.ToolUsageDetails.ImageGenerationCalls = usage.Int64Ptr(imageGenCalls)
		}
		if detail.ImageCount == nil {
			detail.ImageCount = usage.Int64Ptr(imageGenCalls)
		}
		if detail.ImageDetails == nil && firstImageMeta != nil {
			detail.ImageDetails = firstImageMeta
		}
	}
	if toolCallsRequested > 0 && detail.ToolUsageDetails.ToolCallsRequested == nil {
		detail.ToolUsageDetails.ToolCallsRequested = usage.Int64Ptr(toolCallsRequested)
	}
}

func isEmptyImageDetails(d *usage.ImageDetails) bool {
	if d == nil {
		return true
	}
	return d.Size == "" && d.Resolution == "" && d.ImageSize == "" && d.AspectRatio == "" && d.Quality == "" && d.Background == "" && d.OutputFormat == "" && d.ResponseFormat == "" && d.MimeType == "" && d.InputFidelity == ""
}

// ParseRequestImageDetails extracts requested image/video parameters strictly from a request payload,
// preserving each upstream protocol's native field names.
func ParseRequestImageDetails(requestPayload []byte, contentType ...string) *usage.ImageDetails {
	if len(contentType) > 0 {
		mediaType, params, errParse := mime.ParseMediaType(contentType[0])
		if errParse == nil && mediaType == "multipart/form-data" {
			return parseMultipartImageDetails(requestPayload, params["boundary"])
		}
	}
	if len(requestPayload) == 0 || !gjson.ValidBytes(requestPayload) {
		return nil
	}
	root := gjson.ParseBytes(requestPayload)
	pick := func(paths ...string) string {
		for _, p := range paths {
			if v := root.Get(p); v.Type == gjson.String {
				if s := strings.TrimSpace(v.String()); s != "" {
					return s
				}
			}
		}
		return ""
	}
	out := &usage.ImageDetails{
		Size:           pick("size"),
		Resolution:     pick("resolution", "image_config.resolution"),
		ImageSize:      pick("image_size", "imageSize", "image_config.image_size", "image_config.imageSize", "generationConfig.imageConfig.imageSize", "generation_config.image_config.image_size", "request.generationConfig.imageConfig.imageSize", "request.generation_config.image_config.image_size", "parameters.sampleImageSize"),
		AspectRatio:    pick("aspect_ratio", "aspectRatio", "image_config.aspect_ratio", "image_config.aspectRatio", "generationConfig.imageConfig.aspectRatio", "generation_config.image_config.aspect_ratio", "request.generationConfig.imageConfig.aspectRatio", "request.generation_config.image_config.aspect_ratio", "parameters.aspectRatio"),
		Quality:        pick("quality"),
		Background:     pick("background"),
		OutputFormat:   pick("output_format"),
		ResponseFormat: pick("response_format"),
		MimeType:       pick("mime_type", "mimeType", "parameters.outputOptions.mimeType"),
		InputFidelity:  pick("input_fidelity"),
	}
	if isEmptyImageDetails(out) {
		for _, tool := range root.Get("tools").Array() {
			if strings.TrimSpace(tool.Get("type").String()) != "image_generation" {
				continue
			}
			out = &usage.ImageDetails{
				Size:          strings.TrimSpace(tool.Get("size").String()),
				Resolution:    strings.TrimSpace(tool.Get("resolution").String()),
				ImageSize:     strings.TrimSpace(firstExistingUsageNode(tool, "image_size", "imageSize").String()),
				AspectRatio:   strings.TrimSpace(firstExistingUsageNode(tool, "aspect_ratio", "aspectRatio").String()),
				Quality:       strings.TrimSpace(tool.Get("quality").String()),
				Background:    strings.TrimSpace(tool.Get("background").String()),
				OutputFormat:  strings.TrimSpace(tool.Get("output_format").String()),
				MimeType:      strings.TrimSpace(firstExistingUsageNode(tool, "mime_type", "mimeType").String()),
				InputFidelity: strings.TrimSpace(tool.Get("input_fidelity").String()),
			}
			break
		}
	}
	if isEmptyImageDetails(out) {
		return nil
	}
	return out
}

// parseMultipartImageDetails reads only small scalar attributes. File parts and
// unrelated fields are drained without allocating another copy or temporary files.
func parseMultipartImageDetails(payload []byte, boundary string) *usage.ImageDetails {
	if boundary == "" {
		return nil
	}
	out := &usage.ImageDetails{}
	fields := map[string]*string{
		"size": &out.Size, "resolution": &out.Resolution, "image_size": &out.ImageSize,
		"aspect_ratio": &out.AspectRatio, "quality": &out.Quality,
		"background": &out.Background, "output_format": &out.OutputFormat,
		"response_format": &out.ResponseFormat, "mime_type": &out.MimeType,
		"input_fidelity": &out.InputFidelity,
	}
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	for {
		part, errNext := reader.NextPart()
		if errNext == io.EOF {
			break
		}
		if errNext != nil {
			log.Debugf("usage: read multipart image attributes: %v", errNext)
			return nil
		}
		dst := fields[part.FormName()]
		var value []byte
		var errRead error
		const maxAttributeBytes = 4096
		if dst != nil && part.FileName() == "" {
			value, errRead = io.ReadAll(io.LimitReader(part, maxAttributeBytes+1))
		}
		errClose := part.Close()
		if errRead != nil || errClose != nil {
			log.Debugf("usage: read multipart image attribute: read=%v close=%v", errRead, errClose)
			return nil
		}
		if dst != nil && len(value) <= maxAttributeBytes && len(value) > 0 {
			*dst = strings.TrimSpace(string(value))
		}
	}
	if isEmptyImageDetails(out) {
		return nil
	}
	return out
}

// ParseOpenAIImageUsage extracts token usage, generated image count, and separate
// request vs upstream response image parameters from an OpenAI or xAI image request/response.
func ParseOpenAIImageUsage(requestPayload, responsePayload []byte, contentType ...string) usage.Detail {
	var detail usage.Detail
	detail.RequestedImageDetails = ParseRequestImageDetails(requestPayload, contentType...)
	if len(responsePayload) == 0 || !gjson.ValidBytes(responsePayload) {
		return detail
	}
	root := gjson.Parse(unsafeBytesToString(responsePayload))
	var (
		usageNode     gjson.Result
		serviceTier   string
		imageCount    int64
		imgDetails    usage.ImageDetails
		firstItemMeta usage.ImageDetails
	)
	root.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "usage":
			usageNode = val
		case "service_tier":
			if val.Type == gjson.String {
				serviceTier = cloneTrimmedString(val.String())
			}
		case "data":
			if val.IsArray() {
				arr := val.Array()
				imageCount = int64(len(arr))
				if len(arr) > 0 && arr[0].IsObject() {
					arr[0].ForEach(func(dk, dv gjson.Result) bool {
						if dv.Type != gjson.String {
							return true
						}
						switch dk.String() {
						case "b64_json", "url", "revised_prompt":
							return true
						case "size":
							firstItemMeta.Size = cloneTrimmedString(dv.String())
						case "resolution":
							firstItemMeta.Resolution = cloneTrimmedString(dv.String())
						case "image_size", "imageSize":
							if firstItemMeta.ImageSize == "" {
								firstItemMeta.ImageSize = cloneTrimmedString(dv.String())
							}
						case "aspect_ratio", "aspectRatio":
							if firstItemMeta.AspectRatio == "" {
								firstItemMeta.AspectRatio = cloneTrimmedString(dv.String())
							}
						case "quality":
							firstItemMeta.Quality = cloneTrimmedString(dv.String())
						case "background":
							firstItemMeta.Background = cloneTrimmedString(dv.String())
						case "output_format":
							firstItemMeta.OutputFormat = cloneTrimmedString(dv.String())
						case "mime_type", "mimeType":
							if firstItemMeta.MimeType == "" {
								firstItemMeta.MimeType = cloneTrimmedString(dv.String())
							}
						case "input_fidelity":
							firstItemMeta.InputFidelity = cloneTrimmedString(dv.String())
						}
						return true
					})
				}
			}
		case "size":
			if val.Type == gjson.String {
				imgDetails.Size = cloneTrimmedString(val.String())
			}
		case "resolution":
			if val.Type == gjson.String {
				imgDetails.Resolution = cloneTrimmedString(val.String())
			}
		case "image_size", "imageSize":
			if val.Type == gjson.String && imgDetails.ImageSize == "" {
				imgDetails.ImageSize = cloneTrimmedString(val.String())
			}
		case "aspect_ratio", "aspectRatio":
			if val.Type == gjson.String && imgDetails.AspectRatio == "" {
				imgDetails.AspectRatio = cloneTrimmedString(val.String())
			}
		case "quality":
			if val.Type == gjson.String {
				imgDetails.Quality = cloneTrimmedString(val.String())
			}
		case "background":
			if val.Type == gjson.String {
				imgDetails.Background = cloneTrimmedString(val.String())
			}
		case "output_format":
			if val.Type == gjson.String {
				imgDetails.OutputFormat = cloneTrimmedString(val.String())
			}
		case "mime_type", "mimeType":
			if val.Type == gjson.String && imgDetails.MimeType == "" {
				imgDetails.MimeType = cloneTrimmedString(val.String())
			}
		case "input_fidelity":
			if val.Type == gjson.String {
				imgDetails.InputFidelity = cloneTrimmedString(val.String())
			}
		}
		return true
	})
	if usageNode.Exists() {
		if hasOpenAIStyleUsageTokenFields(usageNode) {
			detail = parseOpenAIStyleUsageNode(usageNode)
		} else {
			populateOpenAIStyleStructuredFields(&detail, usageNode)
		}
		detail.RequestedImageDetails = ParseRequestImageDetails(requestPayload, contentType...)
	}
	if serviceTier != "" {
		detail.ResponseServiceTier = serviceTier
		if detail.UpstreamServiceTier == "" {
			detail.UpstreamServiceTier = serviceTier
		}
	}
	if imageCount > 0 {
		detail.ImageCount = usage.Int64Ptr(imageCount)
	}
	if imgDetails.Size == "" {
		imgDetails.Size = firstItemMeta.Size
	}
	if imgDetails.Resolution == "" {
		imgDetails.Resolution = firstItemMeta.Resolution
	}
	if imgDetails.ImageSize == "" {
		imgDetails.ImageSize = firstItemMeta.ImageSize
	}
	if imgDetails.AspectRatio == "" {
		imgDetails.AspectRatio = firstItemMeta.AspectRatio
	}
	if imgDetails.Quality == "" {
		imgDetails.Quality = firstItemMeta.Quality
	}
	if imgDetails.Background == "" {
		imgDetails.Background = firstItemMeta.Background
	}
	if imgDetails.OutputFormat == "" {
		imgDetails.OutputFormat = firstItemMeta.OutputFormat
	}
	if imgDetails.MimeType == "" {
		imgDetails.MimeType = firstItemMeta.MimeType
	}
	if imgDetails.InputFidelity == "" {
		imgDetails.InputFidelity = firstItemMeta.InputFidelity
	}
	if !isEmptyImageDetails(&imgDetails) {
		detail.ImageDetails = &imgDetails
	}
	return detail
}

// ParseXAIImageUsage extracts token usage, generated image count, and image parameters
// from an xAI image generation/edit request and response.
func ParseXAIImageUsage(requestPayload, responsePayload []byte) usage.Detail {
	return ParseOpenAIImageUsage(requestPayload, responsePayload)
}

// ParseXAIVideoUsage extracts token usage and video parameters from an xAI video response.
func ParseXAIVideoUsage(requestPayload, responsePayload []byte) usage.Detail {
	detail := ParseOpenAIUsage(responsePayload)
	root := gjson.ParseBytes(responsePayload)
	reqRoot := gjson.ParseBytes(requestPayload)
	// Only attach video media attributes when a completed video output or usage is present,
	// not on an initial async job creation response that only returns {"request_id":"..."}.
	hasCompletedVideo := root.Get("usage").Exists() || root.Get("video").Exists() || root.Get("data").Exists() || root.Get("url").Exists() || strings.EqualFold(strings.TrimSpace(root.Get("status").String()), "completed")
	if !hasCompletedVideo {
		return detail
	}
	pickRespString := func(paths ...string) string {
		for _, p := range paths {
			if v := root.Get(p); v.Type == gjson.String {
				if s := strings.TrimSpace(v.String()); s != "" {
					return s
				}
			}
		}
		return ""
	}
	imgDetails := &usage.ImageDetails{
		Resolution:  pickRespString("resolution", "video.resolution", "data.0.resolution"),
		AspectRatio: pickRespString("aspect_ratio", "video.aspect_ratio", "data.0.aspect_ratio"),
	}
	if !isEmptyImageDetails(imgDetails) {
		detail.ImageDetails = imgDetails
	}
	detail.RequestedImageDetails = ParseRequestImageDetails(requestPayload)
	if dur := firstExistingUsageNode(root, "duration", "video.duration"); dur.Type == gjson.Number && dur.Float() > 0 {
		detail.MediaCounts = &usage.MediaCounts{
			CompletionMedia: usage.Int64Ptr(1),
			AudioSeconds:    usage.Float64Ptr(dur.Float()),
		}
	} else if dur := reqRoot.Get("duration"); dur.Type == gjson.Number && dur.Float() > 0 {
		detail.MediaCounts = &usage.MediaCounts{
			CompletionMedia: usage.Int64Ptr(1),
			AudioSeconds:    usage.Float64Ptr(dur.Float()),
		}
	}
	return detail
}

func parseOpenAIToolUsageDetailsNode(node gjson.Result) *usage.ToolUsageDetails {
	if !node.Exists() || !node.IsObject() {
		return nil
	}
	out := &usage.ToolUsageDetails{}
	hasAny := false
	node.ForEach(func(key, value gjson.Result) bool {
		if value.Type != gjson.Number {
			return true
		}
		k := strings.TrimSpace(key.String())
		if k == "" {
			return true
		}
		v := value.Int()
		switch k {
		case "web_search_calls", "web_search_requests", "SERVER_SIDE_TOOL_WEB_SEARCH":
			out.WebSearchCalls = usage.Int64Ptr(v)
		case "web_search_queries":
			out.WebSearchQueries = usage.Int64Ptr(v)
		case "web_fetch_calls", "web_fetch_requests":
			out.WebFetchCalls = usage.Int64Ptr(v)
		case "num_search_results":
			out.NumSearchResults = usage.Int64Ptr(v)
		case "num_sources_used":
			out.NumSourcesUsed = usage.Int64Ptr(v)
		case "num_server_side_tools_used":
			out.NumServerSideToolsUsed = usage.Int64Ptr(v)
		case "tool_calls_requested":
			out.ToolCallsRequested = usage.Int64Ptr(v)
		case "tool_calls_executed":
			out.ToolCallsExecuted = usage.Int64Ptr(v)
		case "x_search_calls", "x_search_requests", "SERVER_SIDE_TOOL_X_SEARCH":
			out.XSearchCalls = usage.Int64Ptr(v)
		case "x_posts_fetched":
			out.XPostsFetched = usage.Int64Ptr(v)
		case "x_users_fetched":
			out.XUsersFetched = usage.Int64Ptr(v)
		case "code_interpreter_calls", "code_execution_calls", "code_execution_requests", "SERVER_SIDE_TOOL_CODE_EXECUTION":
			out.CodeExecutionCalls = usage.Int64Ptr(v)
		case "file_search_calls", "file_search_requests", "SERVER_SIDE_TOOL_COLLECTIONS_SEARCH":
			out.FileSearchCalls = usage.Int64Ptr(v)
		case "mcp_calls", "mcp_requests", "SERVER_SIDE_TOOL_MCP":
			out.MCPCalls = usage.Int64Ptr(v)
		case "document_search_calls", "document_search_requests", "attachment_search_calls", "attachment_search_requests", "SERVER_SIDE_TOOL_ATTACHMENT_SEARCH", "SERVER_SIDE_TOOL_DOCUMENT_SEARCH":
			out.DocumentSearchCalls = usage.Int64Ptr(v)
		case "image_generation_calls", "image_generation_requests":
			out.ImageGenerationCalls = usage.Int64Ptr(v)
		default:
			if out.Extra == nil {
				out.Extra = make(map[string]int64)
			}
			out.Extra[strings.Clone(k)] = v
		}
		hasAny = true
		return true
	})
	if !hasAny {
		return nil
	}
	return out
}

func ParseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	detail, ok := parseOpenAIStyleUsageWithCache(root, root.Get("usage"))
	detail.ResponseServiceTier = extractResponseServiceTierFromValidJSON(payload)
	if detail.UpstreamServiceTier == "" {
		detail.UpstreamServiceTier = detail.ResponseServiceTier
	}
	return detail, ok || detail.ResponseServiceTier != ""
}

func ParseClaudeUsage(data []byte) usage.Detail {
	root := gjson.ParseBytes(data)
	usageNode := root.Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	detail := parseClaudeUsageNode(usageNode)
	applyClaudeContentBlocksUsage(&detail, root.Get("content"))
	return detail
}

func ParseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !bytes.Contains(payload, openAIStreamUsageMarker) || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	usageNode := root.Get("usage")
	if !usageNode.Exists() {
		usageNode = root.Get("message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	return parseClaudeUsageNode(usageNode), true
}

func parseClaudeUsageNode(usageNode gjson.Result) usage.Detail {
	cacheReadTokens := usageNode.Get("cache_read_input_tokens").Int()
	cacheCreationTokens := usageNode.Get("cache_creation_input_tokens").Int()
	rawOutputTokens := usageNode.Get("output_tokens").Int()
	// Anthropic reports thinking as a subset of output_tokens. Prefer the official
	// nested field, then fall back to legacy aliases used by some gateways.
	reasoningNode := firstExistingUsageNode(
		usageNode,
		"output_tokens_details.thinking_tokens",
		"output_tokens_details.reasoning_tokens",
		"thinking_tokens",
	)
	reasoningTokens := reasoningNode.Int()
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	nonReasoningOutput := rawOutputTokens
	if reasoningTokens > 0 && reasoningTokens <= rawOutputTokens {
		nonReasoningOutput = rawOutputTokens - reasoningTokens
	} else if reasoningTokens > rawOutputTokens {
		// Keep Detail.OutputTokens authoritative for keeper subset checks and
		// avoid inventing extra non-reasoning output when the upstream payload
		// is inconsistent.
		nonReasoningOutput = 0
	}
	detail := usage.Detail{
		InputTokens:         usageNode.Get("input_tokens").Int(),
		OutputTokens:        rawOutputTokens,
		ReasoningTokens:     reasoningTokens,
		CachedTokens:        cacheReadTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	}
	// Anthropic reports the speed a response was served and billed at in
	// usage.speed ("fast" or "standard"): fast mode costs more, and a
	// fast request on a model without it runs, and is billed, at standard.
	// Carried as the response tier so usage sinks can price it.
	if speed := cloneTrimmedString(usageNode.Get("speed").String()); speed != "" {
		detail.ResponseServiceTier = speed
	}
	if detail.CachedTokens == 0 {
		detail.CachedTokens = detail.CacheCreationTokens
	}
	// raw output_tokens already includes thinking; cache fields are independent
	// from input_tokens in the Messages API.
	detail.TotalTokens = detail.InputTokens + rawOutputTokens + detail.CacheReadTokens + detail.CacheCreationTokens
	detail.TokenBreakdown = usage.NewIndependentTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		nonReasoningOutput,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	populateClaudeUsageStructuredFields(&detail, usageNode)
	return detail
}

func populateClaudeUsageStructuredFields(detail *usage.Detail, usageNode gjson.Result) {
	if detail == nil || !usageNode.Exists() || !usageNode.IsObject() {
		return
	}
	if v := usageNode.Get("service_tier"); v.Type == gjson.String {
		detail.UpstreamServiceTier = cloneTrimmedString(v.String())
	}
	if v := usageNode.Get("speed"); v.Type == gjson.String {
		detail.Speed = cloneTrimmedString(v.String())
	}
	if v := usageNode.Get("inference_geo"); v.Type == gjson.String {
		detail.InferenceGeo = cloneTrimmedString(v.String())
	}
	iterations := parseClaudeIterationsNode(usageNode.Get("iterations"))
	detail.Iterations = usage.IterationsPtr(iterations)
	cacheDetails := parseClaudeCacheCreationNode(usageNode.Get("cache_creation"))
	if cacheDetails == nil && len(iterations) > 0 &&
		(usageNode.Get("cache_creation_input_tokens").Type == gjson.Number || detail.CacheCreationTokens > 0) {
		cacheDetails = deriveClaudeCacheCreationFromIterations(iterations, detail.CacheCreationTokens)
	}
	applyCacheDetailsToUsageDetail(detail, cacheDetails)
	detail.ToolUsageDetails = parseClaudeServerToolUseNode(usageNode.Get("server_tool_use"))
	if detail.ToolUsageDetails != nil && detail.ToolUsageDetails.WebSearchCalls != nil {
		detail.WebSearchRequests = usage.CloneInt64Ptr(detail.ToolUsageDetails.WebSearchCalls)
	}
	detail.CostDetails = parseClaudeCostNode(usageNode)
}

func applyClaudeContentBlocksUsage(detail *usage.Detail, contentNode gjson.Result) {
	if detail == nil || !contentNode.Exists() || !contentNode.IsArray() {
		return
	}
	seenToolUseIDs := make(map[string]struct{})
	seenToolResultIDs := make(map[string]struct{})
	var (
		webSearchQueries  int64
		completedSearches int64
		searchResults     int64
		hasCompleted      bool
	)
	for _, block := range contentNode.Array() {
		if !block.IsObject() {
			continue
		}
		switch block.Get("type").String() {
		case "server_tool_use":
			if strings.TrimSpace(block.Get("name").String()) != "web_search" {
				continue
			}
			if id := strings.TrimSpace(block.Get("id").String()); id != "" {
				if _, ok := seenToolUseIDs[id]; ok {
					continue
				}
				seenToolUseIDs[id] = struct{}{}
			}
			webSearchQueries++
		case "web_search_tool_result":
			content := block.Get("content")
			if !content.Exists() || !content.IsArray() {
				continue
			}
			if id := strings.TrimSpace(block.Get("tool_use_id").String()); id != "" {
				if _, ok := seenToolResultIDs[id]; ok {
					continue
				}
				seenToolResultIDs[id] = struct{}{}
			}
			hasCompleted = true
			completedSearches++
			for _, item := range content.Array() {
				if item.Get("type").String() == "web_search_result" {
					searchResults++
				}
			}
		}
	}
	if webSearchQueries == 0 && !hasCompleted {
		return
	}
	if detail.ToolUsageDetails == nil {
		detail.ToolUsageDetails = &usage.ToolUsageDetails{}
	}
	if webSearchQueries > 0 {
		detail.ToolUsageDetails.WebSearchQueries = usage.Int64Ptr(webSearchQueries)
	}
	if hasCompleted {
		detail.ToolUsageDetails.NumSearchResults = usage.Int64Ptr(searchResults)
		if detail.ToolUsageDetails.WebSearchCalls == nil || (completedSearches > 0 && *detail.ToolUsageDetails.WebSearchCalls == 0) {
			detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(completedSearches)
		}
		if detail.WebSearchRequests == nil || (completedSearches > 0 && *detail.WebSearchRequests == 0) {
			detail.WebSearchRequests = usage.Int64Ptr(completedSearches)
		}
	}
}

func applyCacheDetailsToUsageDetail(detail *usage.Detail, cacheDetails *usage.CacheDetails) {
	if detail == nil {
		return
	}
	detail.CacheDetails = cacheDetails.Clone()
	if cacheDetails == nil {
		detail.CacheCreation5mTokens = nil
		detail.CacheCreation30mTokens = nil
		detail.CacheCreation1hTokens = nil
		detail.CacheCreation12hTokens = nil
		return
	}
	detail.CacheCreation5mTokens = usage.CloneInt64Ptr(cacheDetails.Ephemeral5mInputTokens)
	detail.CacheCreation30mTokens = usage.CloneInt64Ptr(cacheDetails.Ephemeral30mInputTokens)
	detail.CacheCreation1hTokens = usage.CloneInt64Ptr(cacheDetails.Ephemeral1hInputTokens)
	detail.CacheCreation12hTokens = usage.CloneInt64Ptr(cacheDetails.Ephemeral12hInputTokens)
}

func parseClaudeCacheCreationNode(node gjson.Result) *usage.CacheDetails {
	if !node.Exists() || !node.IsObject() {
		return nil
	}
	out := &usage.CacheDetails{}
	hasAny := false
	node.ForEach(func(key, value gjson.Result) bool {
		k := strings.TrimSpace(key.String())
		switch k {
		case "ttl":
			if value.Type == gjson.String {
				out.TTL = cloneTrimmedString(value.String())
				hasAny = out.TTL != "" || hasAny
			}
			return true
		case "mode":
			if value.Type == gjson.String {
				out.Mode = cloneTrimmedString(value.String())
				hasAny = out.Mode != "" || hasAny
			}
			return true
		case "retention":
			if value.Type == gjson.String {
				out.Retention = cloneTrimmedString(value.String())
				hasAny = out.Retention != "" || hasAny
			}
			return true
		}
		if value.Type != gjson.Number {
			return true
		}
		v := value.Int()
		if v < 0 {
			return true
		}
		ttlKey := ""
		switch {
		case k == "ephemeral_5m_input_tokens" || k == "5m":
			out.Ephemeral5mInputTokens = usage.Int64Ptr(v)
			ttlKey = "5m"
		case k == "ephemeral_30m_input_tokens" || k == "30m":
			out.Ephemeral30mInputTokens = usage.Int64Ptr(v)
			ttlKey = "30m"
		case k == "ephemeral_1h_input_tokens" || k == "1h":
			out.Ephemeral1hInputTokens = usage.Int64Ptr(v)
			ttlKey = "1h"
		case k == "ephemeral_12h_input_tokens" || k == "12h":
			out.Ephemeral12hInputTokens = usage.Int64Ptr(v)
			ttlKey = "12h"
		case strings.HasPrefix(k, "ephemeral_") && strings.HasSuffix(k, "_input_tokens"):
			ttlKey = strings.TrimSuffix(strings.TrimPrefix(k, "ephemeral_"), "_input_tokens")
		}
		if ttlKey != "" {
			if out.ByTTL == nil {
				out.ByTTL = make(map[string]int64)
			}
			out.ByTTL[strings.Clone(ttlKey)] = v
			hasAny = true
		}
		return true
	})
	if !hasAny {
		return nil
	}
	return out
}

func parseClaudeServerToolUseNode(node gjson.Result) *usage.ToolUsageDetails {
	if !node.Exists() || !node.IsObject() {
		return nil
	}
	out := &usage.ToolUsageDetails{}
	hasAny := false
	node.ForEach(func(key, value gjson.Result) bool {
		if value.Type != gjson.Number {
			return true
		}
		k := strings.TrimSpace(key.String())
		if k == "" {
			return true
		}
		v := value.Int()
		switch k {
		case "web_search_requests", "web_search_calls":
			out.WebSearchCalls = usage.Int64Ptr(v)
		case "web_fetch_requests", "web_fetch_calls":
			out.WebFetchCalls = usage.Int64Ptr(v)
		case "code_execution_requests", "code_execution_calls", "code_interpreter_calls":
			out.CodeExecutionCalls = usage.Int64Ptr(v)
		case "tool_calls_requested":
			out.ToolCallsRequested = usage.Int64Ptr(v)
		case "tool_calls_executed":
			out.ToolCallsExecuted = usage.Int64Ptr(v)
		case "file_search_requests", "file_search_calls":
			out.FileSearchCalls = usage.Int64Ptr(v)
		case "mcp_requests", "mcp_calls":
			out.MCPCalls = usage.Int64Ptr(v)
		default:
			if out.Extra == nil {
				out.Extra = make(map[string]int64)
			}
			out.Extra[strings.Clone(k)] = v
		}
		hasAny = true
		return true
	})
	if !hasAny {
		return nil
	}
	return out
}

func parseClaudeIterationsNode(node gjson.Result) []usage.IterationUsage {
	if !node.Exists() || !node.IsArray() {
		return nil
	}
	items := node.Array()
	if len(items) == 0 {
		return nil
	}
	out := make([]usage.IterationUsage, 0, len(items))
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		it := usage.IterationUsage{}
		if v := item.Get("type"); v.Type == gjson.String {
			it.Type = cloneTrimmedString(v.String())
		}
		if v := item.Get("model"); v.Type == gjson.String {
			it.Model = cloneTrimmedString(v.String())
		}
		if v := item.Get("input_tokens"); v.Type == gjson.Number {
			it.InputTokens = usage.Int64Ptr(v.Int())
		}
		if v := item.Get("output_tokens"); v.Type == gjson.Number {
			it.OutputTokens = usage.Int64Ptr(v.Int())
		}
		if v := firstExistingUsageNode(item, "output_tokens_details.thinking_tokens", "thinking_tokens"); v.Type == gjson.Number {
			val := v.Int()
			if val < 0 {
				val = 0
			}
			it.ThinkingTokens = usage.Int64Ptr(val)
			it.ReasoningTokens = usage.Int64Ptr(val)
		} else if v := item.Get("output_tokens_details.reasoning_tokens"); v.Type == gjson.Number {
			val := v.Int()
			if val < 0 {
				val = 0
			}
			it.ReasoningTokens = usage.Int64Ptr(val)
		}
		if v := item.Get("cache_read_input_tokens"); v.Type == gjson.Number {
			it.CacheReadInputTokens = usage.Int64Ptr(v.Int())
		}
		if v := item.Get("cache_creation_input_tokens"); v.Type == gjson.Number {
			it.CacheCreationInputTokens = usage.Int64Ptr(v.Int())
		}
		it.CacheCreation = parseClaudeCacheCreationNode(item.Get("cache_creation"))
		if it.CacheCreation != nil {
			if it.CacheCreation.Ephemeral5mInputTokens != nil {
				it.CacheCreation5mTokens = usage.Int64Ptr(*it.CacheCreation.Ephemeral5mInputTokens)
			}
			if it.CacheCreation.Ephemeral1hInputTokens != nil {
				it.CacheCreation1hTokens = usage.Int64Ptr(*it.CacheCreation.Ephemeral1hInputTokens)
			}
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func deriveClaudeCacheCreationFromIterations(iterations []usage.IterationUsage, totalCacheCreation int64) *usage.CacheDetails {
	if len(iterations) == 0 || totalCacheCreation < 0 {
		return nil
	}
	byTTL := make(map[string]int64)
	var has5m, has30m, has1h, has12h bool
	for _, it := range iterations {
		if it.Type != "" && it.Type != "message" {
			continue
		}
		if it.CacheCreation == nil {
			if it.CacheCreationInputTokens != nil && *it.CacheCreationInputTokens > 0 {
				return nil
			}
			continue
		}
		if it.CacheCreation.Ephemeral5mInputTokens != nil {
			has5m = true
		}
		if it.CacheCreation.Ephemeral30mInputTokens != nil {
			has30m = true
		}
		if it.CacheCreation.Ephemeral1hInputTokens != nil {
			has1h = true
		}
		if it.CacheCreation.Ephemeral12hInputTokens != nil {
			has12h = true
		}
		for ttl, tokens := range it.CacheCreation.ByTTL {
			sum, ok := safeUsageTokenSum(byTTL[ttl], tokens)
			if !ok {
				return nil
			}
			byTTL[ttl] = sum
		}
	}
	if len(byTTL) == 0 {
		return nil
	}
	out := &usage.CacheDetails{ByTTL: byTTL}
	sum, ok := out.SumTokens()
	if !ok || sum != totalCacheCreation {
		return nil
	}
	if has5m {
		out.Ephemeral5mInputTokens = usage.Int64Ptr(byTTL["5m"])
	}
	if has30m {
		out.Ephemeral30mInputTokens = usage.Int64Ptr(byTTL["30m"])
	}
	if has1h {
		out.Ephemeral1hInputTokens = usage.Int64Ptr(byTTL["1h"])
	}
	if has12h {
		out.Ephemeral12hInputTokens = usage.Int64Ptr(byTTL["12h"])
	}
	return out
}

func parseClaudeCostNode(usageNode gjson.Result) *usage.CostDetails {
	costNode := usageNode.Get("cost")
	byokNode := usageNode.Get("is_byok")
	detailsNode := usageNode.Get("cost_details")
	out := &usage.CostDetails{}
	hasAny := false
	if costNode.Type == gjson.Number {
		out.Cost = usage.Float64Ptr(costNode.Float())
		hasAny = true
	}
	if ticksNode := usageNode.Get("cost_in_usd_ticks"); ticksNode.Type == gjson.Number {
		out.CostInUSDTicks = usage.Int64Ptr(ticksNode.Int())
		hasAny = true
	}
	if byokNode.Type == gjson.True || byokNode.Type == gjson.False {
		out.IsBYOK = usage.BoolPtr(byokNode.Bool())
		hasAny = true
	}
	if detailsNode.Exists() && detailsNode.IsObject() {
		if v := detailsNode.Get("upstream_inference_cost"); v.Type == gjson.Number {
			out.UpstreamInferenceCost = usage.Float64Ptr(v.Float())
			hasAny = true
		}
		if v := detailsNode.Get("upstream_inference_prompt_cost"); v.Type == gjson.Number {
			out.UpstreamInferencePromptCost = usage.Float64Ptr(v.Float())
			hasAny = true
		}
		if v := detailsNode.Get("upstream_inference_completions_cost"); v.Type == gjson.Number {
			out.UpstreamInferenceCompletionsCost = usage.Float64Ptr(v.Float())
			hasAny = true
		}
	}
	if !hasAny {
		return nil
	}
	return out
}

func parseGeminiFamilyUsageDetail(node gjson.Result) usage.Detail {
	if !node.Exists() || !node.IsObject() {
		return usage.Detail{}
	}
	var (
		promptTokens     int64
		toolUseTokens    int64
		candidatesTokens int64
		thoughtsTokens   int64
		totalTokens      int64
		cachedTokens     int64
		serviceTier      string
		trafficType      string
		promptDetails    gjson.Result
		toolUseDetails   gjson.Result
		cacheDetails     gjson.Result
		candDetails      gjson.Result
	)
	node.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "promptTokenCount", "prompt_token_count":
			if promptTokens == 0 {
				promptTokens = val.Int()
			}
		case "toolUsePromptTokenCount", "tool_use_prompt_token_count":
			if toolUseTokens == 0 {
				toolUseTokens = val.Int()
			}
		case "candidatesTokenCount", "candidates_token_count":
			if candidatesTokens == 0 {
				candidatesTokens = val.Int()
			}
		case "thoughtsTokenCount", "thoughts_token_count":
			if thoughtsTokens == 0 {
				thoughtsTokens = val.Int()
			}
		case "totalTokenCount", "total_token_count":
			if totalTokens == 0 {
				totalTokens = val.Int()
			}
		case "cachedContentTokenCount", "cached_content_token_count":
			if cachedTokens == 0 {
				cachedTokens = val.Int()
			}
		case "serviceTier", "service_tier":
			if serviceTier == "" && val.Type == gjson.String {
				serviceTier = cloneTrimmedString(val.String())
			}
		case "trafficType", "traffic_type":
			if trafficType == "" && val.Type == gjson.String {
				trafficType = cloneTrimmedString(val.String())
			}
		case "promptTokensDetails", "prompt_tokens_details":
			if !promptDetails.Exists() {
				promptDetails = val
			}
		case "toolUsePromptTokensDetails", "tool_use_prompt_tokens_details":
			if !toolUseDetails.Exists() {
				toolUseDetails = val
			}
		case "cacheTokensDetails", "cache_tokens_details":
			if !cacheDetails.Exists() {
				cacheDetails = val
			}
		case "candidatesTokensDetails", "candidates_tokens_details":
			if !candDetails.Exists() {
				candDetails = val
			}
		}
		return true
	})
	inputTokens, okInput := safeUsageTokenSum(promptTokens, toolUseTokens)
	detail := usage.Detail{
		InputTokens:     inputTokens,
		OutputTokens:    candidatesTokens,
		ReasoningTokens: thoughtsTokens,
		TotalTokens:     totalTokens,
		CachedTokens:    cachedTokens,
		CacheReadTokens: cachedTokens,
	}
	if !okInput {
		detail.TokenBreakdown = invalidUsageTokenBreakdown(detail.TotalTokens)
		return detail
	}
	if detail.TotalTokens == 0 {
		var okTotal bool
		detail.TotalTokens, okTotal = safeUsageTokenSum(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens)
		if !okTotal {
			detail.TotalTokens = 0
			detail.TokenBreakdown = invalidUsageTokenBreakdown(0)
			return detail
		}
	}
	detail.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	if serviceTier != "" {
		detail.ResponseServiceTier = serviceTier
		if detail.UpstreamServiceTier == "" {
			detail.UpstreamServiceTier = serviceTier
		}
	}
	if trafficType != "" {
		detail.UpstreamServiceTier = trafficType
		if detail.ResponseServiceTier == "" {
			detail.ResponseServiceTier = trafficType
		}
	}
	if toolUseTokens > 0 {
		detail.ToolUsageDetails = &usage.ToolUsageDetails{
			Extra: map[string]int64{"tool_use_prompt_tokens": toolUseTokens},
		}
	}
	parseModalityList := func(arr gjson.Result, target func(modality string, count int64)) {
		if !arr.Exists() || !arr.IsArray() {
			return
		}
		arr.ForEach(func(_, entry gjson.Result) bool {
			mod := strings.ToUpper(strings.TrimSpace(entry.Get("modality").String()))
			if mod == "" {
				return true
			}
			cntNode := entry.Get("tokenCount")
			if !cntNode.Exists() {
				cntNode = entry.Get("token_count")
			}
			if cntNode.Type == gjson.Number {
				target(mod, cntNode.Int())
			}
			return true
		})
	}
	if promptDetails.Exists() || toolUseDetails.Exists() {
		var textIn, imgIn, audioIn, videoIn, docIn int64
		var hasTextIn, hasImgIn, hasAudioIn, hasVideoIn, hasDocIn bool
		addInputModality := func(mod string, cnt int64) {
			switch mod {
			case "TEXT":
				textIn += cnt
				hasTextIn = true
			case "IMAGE":
				imgIn += cnt
				hasImgIn = true
			case "AUDIO":
				audioIn += cnt
				hasAudioIn = true
			case "VIDEO":
				videoIn += cnt
				hasVideoIn = true
			case "DOCUMENT":
				docIn += cnt
				hasDocIn = true
			}
		}
		parseModalityList(promptDetails, addInputModality)
		parseModalityList(toolUseDetails, addInputModality)
		if hasTextIn {
			detail.TextInputTokens = usage.Int64Ptr(textIn)
		}
		if hasImgIn {
			detail.ImageInputTokens = usage.Int64Ptr(imgIn)
		}
		if hasAudioIn {
			detail.AudioInputTokens = usage.Int64Ptr(audioIn)
		}
		if hasVideoIn {
			detail.VideoInputTokens = usage.Int64Ptr(videoIn)
		}
		if hasDocIn {
			detail.FileInputTokens = usage.Int64Ptr(docIn)
		}
	}
	if cacheDetails.Exists() {
		var cachedText int64
		var hasCachedText bool
		parseModalityList(cacheDetails, func(mod string, cnt int64) {
			if mod == "TEXT" {
				cachedText += cnt
				hasCachedText = true
			}
		})
		if hasCachedText {
			detail.CachedTextTokens = usage.Int64Ptr(cachedText)
		}
	}
	if candDetails.Exists() {
		var textOut, imgOut, audioOut int64
		var hasTextOut, hasImgOut, hasAudioOut bool
		parseModalityList(candDetails, func(mod string, cnt int64) {
			switch mod {
			case "TEXT":
				textOut += cnt
				hasTextOut = true
			case "IMAGE":
				imgOut += cnt
				hasImgOut = true
			case "AUDIO":
				audioOut += cnt
				hasAudioOut = true
			}
		})
		if hasTextOut {
			detail.TextOutputTokens = usage.Int64Ptr(textOut)
		}
		if hasImgOut {
			detail.ImageOutputTokens = usage.Int64Ptr(imgOut)
		}
		if hasAudioOut {
			detail.AudioOutputTokens = usage.Int64Ptr(audioOut)
		}
	}
	return detail
}

func applyGeminiPayloadArtifacts(detail *usage.Detail, root gjson.Result) {
	if detail == nil || !root.Exists() || !root.IsObject() {
		return
	}
	candidates := root.Get("candidates")
	if !candidates.Exists() {
		candidates = root.Get("response.candidates")
	}
	var preds gjson.Result
	if strings.Contains(root.Raw, `"predictions"`) {
		preds = root.Get("predictions")
	}
	applyGeminiNodesArtifacts(detail, candidates, preds)
}

func applyGeminiNodesArtifacts(detail *usage.Detail, candidates, preds gjson.Result) {
	var callCounter geminiFunctionCallCounter
	applyGeminiNodesArtifactsWithCounter(detail, candidates, preds, &callCounter)
}

func applyGeminiNodesArtifactsWithCounter(detail *usage.Detail, candidates, preds gjson.Result, callCounter *geminiFunctionCallCounter) {
	if detail == nil {
		return
	}
	if callCounter == nil {
		callCounter = &geminiFunctionCallCounter{}
	}
	if candidates.Exists() && candidates.IsArray() {
		var (
			webSearchGrounded bool
			webSearchQueries  int64
			webChunks         int64
			fileChunks        int64
			mapsChunks        int64
			urlFetchCount     int64
			codeExecCount     int64
			fnCallCount       int64
			imageOutCount     int64
			firstImageMime    string
		)
		candidates.ForEach(func(candPos, cand gjson.Result) bool {
			if !cand.IsObject() {
				return true
			}
			candIdx := candPos.Int()
			if idxNode := cand.Get("index"); idxNode.Type == gjson.Number {
				candIdx = idxNode.Int()
			}
			cand.ForEach(func(ck, cv gjson.Result) bool {
				switch ck.String() {
				case "groundingMetadata", "grounding_metadata":
					if !cv.IsObject() {
						return true
					}
					cv.ForEach(func(gk, gv gjson.Result) bool {
						switch gk.String() {
						case "webSearchQueries", "web_search_queries":
							if gv.IsArray() {
								gv.ForEach(func(_, qItem gjson.Result) bool {
									if strings.TrimSpace(qItem.String()) != "" {
										webSearchQueries++
										webSearchGrounded = true
									}
									return true
								})
							}
						case "searchEntryPoint", "search_entry_point":
							webSearchGrounded = true
						case "groundingChunks", "grounding_chunks":
							if gv.IsArray() {
								gv.ForEach(func(_, ch gjson.Result) bool {
									switch {
									case ch.Get("web").Exists():
										webChunks++
										webSearchGrounded = true
									case ch.Get("retrievedContext").Exists() || ch.Get("retrieved_context").Exists():
										fileChunks++
									case ch.Get("maps").Exists():
										mapsChunks++
									}
									return true
								})
							}
						}
						return true
					})
				case "urlContextMetadata", "url_context_metadata":
					if cv.IsObject() {
						if urls := firstExistingUsageNode(cv, "urlMetadata", "url_metadata"); urls.IsArray() {
							urlFetchCount += int64(len(urls.Array()))
						}
					}
				case "content":
					if parts := cv.Get("parts"); parts.IsArray() {
						parts.ForEach(func(_, part gjson.Result) bool {
							if !part.IsObject() {
								return true
							}
							part.ForEach(func(pk, pv gjson.Result) bool {
								switch pk.String() {
								case "executableCode", "executable_code":
									codeExecCount++
								case "functionCall", "function_call":
									fnCallCount += callCounter.observePart(candIdx, pv)
								case "inlineData", "inline_data":
									if pv.IsObject() {
										mimeNode := pv.Get("mimeType")
										if !mimeNode.Exists() {
											mimeNode = pv.Get("mime_type")
										}
										mimeType := cloneTrimmedString(mimeNode.String())
										if len(mimeType) >= 6 && strings.EqualFold(mimeType[:6], "image/") {
											imageOutCount++
											if firstImageMime == "" {
												firstImageMime = mimeType
											}
										}
									}
								}
								return true
							})
							return true
						})
					}
				}
				return true
			})
			return true
		})
		if webSearchGrounded || webSearchQueries > 0 || webChunks > 0 || fileChunks > 0 || mapsChunks > 0 || urlFetchCount > 0 || codeExecCount > 0 || fnCallCount > 0 {
			if detail.ToolUsageDetails == nil {
				detail.ToolUsageDetails = &usage.ToolUsageDetails{}
			}
			if webSearchGrounded {
				calls := int64(1)
				if webSearchQueries > 1 {
					calls = webSearchQueries
				}
				detail.ToolUsageDetails.WebSearchCalls = usage.Int64Ptr(calls)
				detail.WebSearchRequests = usage.Int64Ptr(calls)
			}
			if webSearchQueries > 0 {
				detail.ToolUsageDetails.WebSearchQueries = usage.Int64Ptr(webSearchQueries)
			}
			if webChunks > 0 {
				detail.ToolUsageDetails.NumSearchResults = usage.Int64Ptr(webChunks)
				detail.ToolUsageDetails.NumSourcesUsed = usage.Int64Ptr(webChunks)
			}
			if fileChunks > 0 {
				detail.ToolUsageDetails.FileSearchCalls = usage.Int64Ptr(fileChunks)
			}
			if urlFetchCount > 0 {
				detail.ToolUsageDetails.WebFetchCalls = usage.Int64Ptr(urlFetchCount)
			}
			if codeExecCount > 0 {
				detail.ToolUsageDetails.CodeExecutionCalls = usage.Int64Ptr(codeExecCount)
			}
			if fnCallCount > 0 {
				detail.ToolUsageDetails.ToolCallsRequested = usage.Int64Ptr(fnCallCount)
			}
			if mapsChunks > 0 {
				if detail.ToolUsageDetails.Extra == nil {
					detail.ToolUsageDetails.Extra = make(map[string]int64)
				}
				detail.ToolUsageDetails.Extra["google_maps_chunks"] = mapsChunks
			}
		}
		if imageOutCount > 0 {
			detail.ImageCount = usage.Int64Ptr(imageOutCount)
			if firstImageMime != "" {
				if detail.ImageDetails == nil {
					detail.ImageDetails = &usage.ImageDetails{}
				}
				detail.ImageDetails.MimeType = firstImageMime
			}
		}
	}
	if preds.Exists() && preds.IsArray() {
		var count int64
		var firstMime string
		preds.ForEach(func(_, pred gjson.Result) bool {
			if !pred.IsObject() {
				return true
			}
			var hasImg bool
			var mime string
			pred.ForEach(func(pk, pv gjson.Result) bool {
				switch pk.String() {
				case "bytesBase64Encoded", "gcsUri":
					if pv.Type == gjson.String && len(pv.Raw) > 2 {
						hasImg = true
					}
				case "mimeType":
					if pv.Type == gjson.String {
						mime = cloneTrimmedString(pv.String())
					}
				}
				return true
			})
			if hasImg {
				count++
				if firstMime == "" {
					firstMime = mime
				}
			}
			return true
		})
		if count > 0 {
			detail.ImageCount = usage.Int64Ptr(count)
			if firstMime != "" {
				if detail.ImageDetails == nil {
					detail.ImageDetails = &usage.ImageDetails{}
				}
				detail.ImageDetails.MimeType = firstMime
			}
		}
	}
}

// ApplyGeminiRequestImageConfig populates detail.RequestedImageDetails from a Gemini or Vertex
// request payload without overwriting upstream-returned detail.ImageDetails.
func ApplyGeminiRequestImageConfig(detail *usage.Detail, requestPayload []byte) {
	if detail == nil || detail.ImageCount == nil || *detail.ImageCount <= 0 {
		return
	}
	detail.RequestedImageDetails = ParseRequestImageDetails(requestPayload)
}

func parseInteractionsUsageDetail(node gjson.Result) usage.Detail {
	cacheRead := firstExistingUsageNode(node, "cache_read_tokens", "cacheReadTokens")
	toolUseTokens := firstExistingUsageNode(node, "tool_use_tokens", "total_tool_use_tokens", "toolUseTokens", "totalToolUseTokens").Int()
	inputTokens, okInput := safeUsageTokenSum(
		firstExistingUsageNode(node, "input_tokens", "prompt_tokens", "total_input_tokens").Int(),
		toolUseTokens,
	)
	detail := usage.Detail{
		InputTokens:         inputTokens,
		OutputTokens:        firstExistingUsageNode(node, "output_tokens", "completion_tokens", "total_output_tokens").Int(),
		ReasoningTokens:     firstExistingUsageNode(node, "reasoning_tokens", "thoughtsTokenCount", "total_thought_tokens").Int(),
		TotalTokens:         firstExistingUsageNode(node, "total_tokens", "totalTokenCount").Int(),
		CachedTokens:        firstExistingUsageNode(node, "cached_tokens", "cachedContentTokenCount", "total_cached_tokens").Int(),
		CacheReadTokens:     cacheRead.Int(),
		CacheCreationTokens: firstExistingUsageNode(node, "cache_creation_tokens", "cacheCreationTokens", "cache_write_tokens", "cacheWriteTokens").Int(),
	}
	if !okInput {
		detail.TokenBreakdown = invalidUsageTokenBreakdown(detail.TotalTokens)
		return detail
	}
	if !cacheRead.Exists() && detail.CachedTokens > 0 {
		detail.CacheReadTokens = detail.CachedTokens
	}
	if detail.TotalTokens == 0 {
		var okTotal bool
		detail.TotalTokens, okTotal = safeUsageTokenSum(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens)
		if !okTotal {
			detail.TotalTokens = 0
			detail.TokenBreakdown = invalidUsageTokenBreakdown(0)
			return detail
		}
	}
	detail.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	return detail
}

func hasUsageDetail(detail usage.Detail) bool {
	return hasNonZeroTokenUsage(detail)
}

func ParseInteractionsUsage(data []byte) usage.Detail {
	root := gjson.ParseBytes(data)
	node := firstExistingUsageNode(root, "usage", "total_usage", "metadata.total_usage", "metadata.usage", "usageMetadata", "usage_metadata", "interaction.usage", "interaction.total_usage", "interaction.metadata.total_usage")
	if !node.Exists() {
		return usage.Detail{}
	}
	if node.Get("promptTokenCount").Exists() || node.Get("candidatesTokenCount").Exists() {
		detail := parseGeminiFamilyUsageDetail(node)
		detail.ResponseServiceTier = extractResponseServiceTier(data)
		return detail
	}
	detail := parseInteractionsUsageDetail(node)
	detail.ResponseServiceTier = extractResponseServiceTier(data)
	return detail
}

func extractResponseServiceTier(payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	return extractResponseServiceTierFromValidJSON(payload)
}

func extractResponseServiceTierFromValidJSON(payload []byte) string {
	for _, path := range []string{"response.service_tier", "service_tier", "interaction.service_tier"} {
		if tier := strings.TrimSpace(gjson.GetBytes(payload, path).String()); tier != "" {
			return tier
		}
	}
	return ""
}

func ParseInteractionsStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		payload = line
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	detail := ParseInteractionsUsage(payload)
	if !hasUsageDetail(detail) {
		return usage.Detail{}, false
	}
	return detail, true
}

func ParseGeminiUsage(data []byte) usage.Detail {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return usage.Detail{}
	}
	root := gjson.Parse(unsafeBytesToString(data))
	var (
		node           gjson.Result
		candidatesNode gjson.Result
		predsNode      gjson.Result
	)
	root.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "usageMetadata", "usage_metadata":
			if !node.Exists() {
				node = val
			}
		case "candidates":
			candidatesNode = val
		case "predictions":
			predsNode = val
		case "response":
			if val.IsObject() {
				val.ForEach(func(rk, rv gjson.Result) bool {
					switch rk.String() {
					case "usageMetadata", "usage_metadata":
						if !node.Exists() {
							node = rv
						}
					case "candidates":
						if !candidatesNode.Exists() {
							candidatesNode = rv
						}
					}
					return true
				})
			}
		}
		return true
	})
	var detail usage.Detail
	if node.Exists() {
		detail = parseGeminiFamilyUsageDetail(node)
	}
	if candidatesNode.Exists() || predsNode.Exists() {
		applyGeminiNodesArtifacts(&detail, candidatesNode, predsNode)
	}
	return detail
}

func ParseGeminiStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return usage.Detail{}, false
	}
	hasCamel := bytes.Contains(payload, geminiUsageMetaMarker)
	hasSnake := !hasCamel && bytes.Contains(payload, geminiUsageMetaSnakeMarker)
	if (!hasCamel && !hasSnake) || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	var node gjson.Result
	if hasCamel {
		node = root.Get("usageMetadata")
	} else {
		node = root.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	if !hasNonZeroTokenUsage(detail) {
		return usage.Detail{}, false
	}
	if hasGeminiCandidateArtifactMarker(payload) {
		applyGeminiPayloadArtifacts(&detail, root)
	}
	return detail, true
}

func firstExistingUsageNode(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		node := root.Get(path)
		if node.Exists() {
			return node
		}
	}
	return gjson.Result{}
}

func safeUsageTokenSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > int64(^uint64(0)>>1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func invalidUsageTokenBreakdown(total int64) usage.TokenBreakdown {
	if total < 0 {
		total = 0
	}
	return usage.TokenBreakdown{
		SchemaVersion:      usage.TokenAccountingSchemaVersion,
		Quality:            usage.TokenAccountingQualityInconsistent,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

func ParseAntigravityUsage(data []byte) usage.Detail {
	return ParseGeminiUsage(data)
}

func ParseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return usage.Detail{}, false
	}
	hasCamel := bytes.Contains(payload, geminiUsageMetaMarker)
	hasSnake := !hasCamel && bytes.Contains(payload, geminiUsageMetaSnakeMarker)
	if (!hasCamel && !hasSnake) || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	root := gjson.Parse(unsafeBytesToString(payload))
	node := root.Get("response.usageMetadata")
	if !node.Exists() && hasCamel {
		node = root.Get("usageMetadata")
	}
	if !node.Exists() && hasSnake {
		node = root.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	if hasGeminiCandidateArtifactMarker(payload) {
		applyGeminiPayloadArtifacts(&detail, root)
	}
	return detail, true
}

var stopChunkWithoutUsage sync.Map

func rememberStopWithoutUsage(traceID string) {
	stopChunkWithoutUsage.Store(traceID, struct{}{})
	time.AfterFunc(10*time.Minute, func() { stopChunkWithoutUsage.Delete(traceID) })
}

// FilterSSEUsageMetadata removes usageMetadata from SSE events that are not
// terminal (finishReason != "stop"). Stop chunks are left untouched. This
// function is shared between aistudio and antigravity executors.
func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	modified := false
	foundData := false
	for idx, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, []byte("data:"))
		if dataIdx < 0 {
			continue
		}
		rawJSON := bytes.TrimSpace(line[dataIdx+5:])
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			continue
		}
		if traceID != "" {
			if _, ok := stopChunkWithoutUsage.Load(traceID); ok && hasUsageMetadata(rawJSON) {
				stopChunkWithoutUsage.Delete(traceID)
				continue
			}
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			continue
		}
		rebuiltCap := dataIdx + 5
		if len(cleaned) > 0 {
			rebuiltCap += 1 + len(cleaned)
		}
		rebuilt := make([]byte, 0, rebuiltCap)
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, "data:"...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		lines[idx] = rebuilt
		modified = true
	}
	if !modified {
		if !foundData {
			// Handle payloads that are raw JSON without SSE data: prefix.
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripUsageMetadataFromJSON drops usageMetadata unless finishReason is present (terminal).
// It handles both formats:
// - Aistudio: candidates.0.finishReason
// - Antigravity: response.candidates.0.finishReason
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !bytes.Contains(jsonBytes, geminiUsageMetaMarker) || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	root := gjson.Parse(unsafeBytesToString(jsonBytes))
	var (
		finishReason string
		topUsage     gjson.Result
		respUsage    gjson.Result
		topUsageKey  gjson.Result
		respUsageKey gjson.Result
	)
	root.ForEach(func(key, val gjson.Result) bool {
		switch key.String() {
		case "usageMetadata":
			topUsage = val
			topUsageKey = key
		case "candidates":
			if finishReason == "" && val.IsArray() {
				finishReason = strings.TrimSpace(val.Get("0.finishReason").String())
			}
		case "response":
			if val.IsObject() {
				val.ForEach(func(rk, rv gjson.Result) bool {
					switch rk.String() {
					case "usageMetadata":
						respUsage = rv
						respUsageKey = rk
					case "candidates":
						if finishReason == "" && rv.IsArray() {
							finishReason = strings.TrimSpace(rv.Get("0.finishReason").String())
						}
					}
					return true
				})
			}
		}
		return true
	})

	if finishReason != "" || (!topUsage.Exists() && !respUsage.Exists()) {
		return rawJSON, false
	}

	// Fast single-allocation key rename when only one usageMetadata exists
	// (avoids 2 full-buffer sjson copies on multi-MB 4K image chunks).
	if topUsage.Exists() != respUsage.Exists() {
		key := topUsageKey
		if respUsage.Exists() {
			key = respUsageKey
		}
		if key.Raw == string(geminiUsageMetaMarker) && key.Index > 0 &&
			key.Index+len(key.Raw) <= len(jsonBytes) {
			keyPos := key.Index + 1 // Right after the selected key's opening quote.
			out := make([]byte, 0, len(jsonBytes)+3)
			out = append(out, jsonBytes[:keyPos]...)
			out = append(out, "cpaUsageMetadata"...)
			out = append(out, jsonBytes[keyPos+len("usageMetadata"):]...)
			return out, true
		}
	}

	cleaned := jsonBytes
	var changed bool
	if topUsage.Exists() {
		cleaned, _ = sjson.SetRawBytes(cleaned, "cpaUsageMetadata", []byte(topUsage.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}
	if respUsage.Exists() {
		cleaned, _ = sjson.SetRawBytes(cleaned, "response.cpaUsageMetadata", []byte(respUsage.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}
	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	if gjson.GetBytes(jsonBytes, "usageMetadata").Exists() {
		return true
	}
	if gjson.GetBytes(jsonBytes, "response.usageMetadata").Exists() {
		return true
	}
	return false
}

var finishReasonMarker = []byte(`"finishReason"`)

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !bytes.Contains(jsonBytes, finishReasonMarker) || bytes.Contains(jsonBytes, geminiUsageMetaMarker) || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !hasUsageMetadata(jsonBytes)
}

func JSONPayload(line []byte) []byte {
	return jsonPayload(line)
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}
