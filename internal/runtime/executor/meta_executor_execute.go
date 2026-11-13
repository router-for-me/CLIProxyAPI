package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tiktoken-go/tokenizer"
)

type metaPreparedRequest struct {
	applyPatch      *helps.ApplyPatchResponsesState
	baseModel       string
	effectiveModel  string
	from            sdktranslator.Format
	responseFormat  sdktranslator.Format
	to              sdktranslator.Format
	originalPayload []byte
	body            []byte
}

func (e *MetaExecutor) prepareResponsesRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*metaPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	isCompat := helps.APIKeyModelIsCompat(req)
	targetModel := resolveMetaSignatureTargetModel(baseModel, req, opts)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, targetModel, originalPayload, stream, isCompat)
	origDoc := ojson.NewDoc(originalTranslated)
	origChanged := helps.SetStringIfDifferentDoc(origDoc, "model", baseModel)
	if syncMetaPromptCacheRetentionDoc(origDoc, originalPayload) {
		origChanged = true
	}
	if origChanged {
		originalTranslated = origDoc.Bytes()
	}
	body, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, from, to, targetModel, bytes.Clone(req.Payload), stream, isCompat)
	if err != nil {
		return nil, err
	}

	doc := ojson.NewDoc(body)
	if errThinking := helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier()); errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", stream)
	_ = doc.Delete("generate")
	syncMetaPromptCacheRetentionDoc(doc, req.Payload)
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	_ = doc.Delete("client_metadata")
	applyPatch := helps.NewApplyPatchResponsesState(from, originalPayload, originalTranslated)
	if errNormalizePatch := helps.NormalizeApplyPatchResponsesRequestDoc(doc, originalPayload); errNormalizePatch != nil {
		return nil, errNormalizePatch
	}
	normalizeCodexInstructionsDoc(doc)
	baseURL, _ := metaCreds(auth)
	sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURLDoc(ctx, "meta executor", doc, baseURL)
	helps.SanitizeMetaWebSearchToolsDoc(doc)
	helps.NormalizeCodexToolIntegerTypesDoc(doc, opts.Headers)
	helps.EnsureMetaMuseToolNamespaceDoc(doc)

	helps.ApplyPayloadConfigWithRequestDoc(e.cfg, baseModel, e.Identifier(), from.String(), "", doc, originalTranslated, requestedModel, requestPath, opts.Headers)
	effectiveModel := strings.TrimSpace(doc.Get("model").String())
	if effectiveModel == "" {
		effectiveModel = baseModel
	}
	body = doc.Bytes()
	return &metaPreparedRequest{
		applyPatch:      applyPatch,
		baseModel:       baseModel,
		effectiveModel:  effectiveModel,
		from:            from,
		responseFormat:  responseFormat,
		to:              to,
		originalPayload: originalPayload,
		body:            body,
	}, nil
}

// syncMetaPromptCacheRetentionDoc preserves an explicit non-empty string
// prompt_cache_retention supplied by the caller without injecting a default,
// and removes null, blank, or non-string values before user payload rules run.
func syncMetaPromptCacheRetentionDoc(doc *ojson.Doc, source []byte) bool {
	if doc == nil {
		return false
	}
	if retention := extractMetaPromptCacheRetention(source); retention != "" {
		return helps.SetStringIfDifferentDoc(doc, "prompt_cache_retention", retention)
	}
	if !doc.Get("prompt_cache_retention").Exists() {
		return false
	}
	return doc.Delete("prompt_cache_retention") == nil
}

func extractMetaPromptCacheRetention(source []byte) string {
	if len(source) == 0 {
		return ""
	}
	val := ojson.GetBytesNoCopy(source, "prompt_cache_retention")
	if val.Type != ojson.String {
		return ""
	}
	return strings.TrimSpace(val.String())
}

func (e *MetaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, "meta"); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeMetaCompaction(ctx, auth, req, opts)
	}

	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return resp, errAuth
	}

	prepared, errPrepare := e.prepareResponsesRequest(ctx, enriched, req, opts, true)
	if errPrepare != nil {
		return resp, errPrepare
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, enriched)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	baseURL, token := metaCreds(enriched)
	if strings.TrimSpace(baseURL) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing provider baseURL"}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if errRequest != nil {
		return resp, errRequest
	}
	applyMetaAPIHeaders(httpReq, enriched, token, true, opts.Headers, prepared.effectiveModel)
	e.recordMetaRequest(ctx, enriched, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, errRead := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	reporter.ObserveResponseModel(data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, wrapMetaUpstreamError(httpResp.StatusCode, data)
	}

	var upstreamUsage helps.StreamUsageBuffer
	out, errCompleted := e.translateMetaCompleted(ctx, req, prepared, data, &upstreamUsage)
	if errCompleted != nil {
		upstreamUsage.PublishFailure(ctx, reporter, errCompleted)
		return resp, errCompleted
	}
	if len(out.sourceEvent) > 0 {
		reporter.ObserveResponseModel(out.sourceEvent)
	}
	if detail, ok := helps.ParseCodexUsage(out.sourceEvent); ok {
		reporter.Publish(ctx, detail)
	} else {
		reporter.EnsurePublished(ctx)
	}
	payload := out.payload
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		payload = helps.EnsureResponsesUsageDetails(payload)
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: httpResp.Header.Clone()}, nil
}

type metaCompletedTranslation struct {
	payload     []byte
	sourceEvent []byte
}

func (e *MetaExecutor) translateMetaCompleted(ctx context.Context, req cliproxyexecutor.Request, prepared *metaPreparedRequest, data []byte, upstreamUsage *helps.StreamUsageBuffer) (metaCompletedTranslation, error) {
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	var filteringSubscriptionUsage bool
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmedLine := bytes.TrimSpace(line)
		if len(trimmedLine) == 0 {
			filteringSubscriptionUsage = false
			continue
		}
		if bytes.HasPrefix(trimmedLine, metaEventTag) {
			eventName := bytes.TrimSpace(trimmedLine[len(metaEventTag):])
			filteringSubscriptionUsage = bytes.Equal(eventName, []byte(metaSubscriptionUsageEventType))
			continue
		}
		if !bytes.HasPrefix(trimmedLine, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
		eventType := ojson.GetBytesNoCopy(eventData, "type").String()
		if isMetaSubscriptionUsageEvent(eventType, eventData) {
			filteringSubscriptionUsage = false
			continue
		}
		if filteringSubscriptionUsage {
			if eventType == "" {
				if ojson.ValidBytes(eventData) {
					filteringSubscriptionUsage = false
				}
				continue
			}
			filteringSubscriptionUsage = false
		}
		upstreamUsage.ObserveResponsesStream(eventData)
		if errEvent := metaStreamEventError(eventData); errEvent != nil {
			return metaCompletedTranslation{}, errEvent
		}
		events, errBridge := prepared.applyPatch.Transform(eventData)
		if errBridge != nil {
			errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			return metaCompletedTranslation{}, errBridge
		}
		for _, eventData := range events {
			eventType := ojson.GetBytesNoCopy(eventData, "type").String()
			switch eventType {
			case "response.output_item.done":
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.completed", "response.incomplete":
				completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				upstreamUsage.ObserveResponsesStream(completedData)
				var param any
				out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
				if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
					return metaCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				}
				return metaCompletedTranslation{payload: out, sourceEvent: completedData}, nil
			}
		}
	}

	if completedData, ok := metaAsCompletedEvent(data); ok {
		if detail, okUsage := helps.ParseCodexUsage(completedData); okUsage {
			upstreamUsage.Observe(detail, true)
		}
		completedData = patchCodexCompletedOutput(completedData, outputItemsByIndex, outputItemsFallback)
		var errBridge error
		completedData, errBridge = prepared.applyPatch.Bridge.TransformNonStream(completedData)
		if errBridge != nil {
			errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			return metaCompletedTranslation{}, errBridge
		}
		var param any
		out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
		if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
			return metaCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		return metaCompletedTranslation{payload: out, sourceEvent: completedData}, nil
	}

	if errFinish := prepared.applyPatch.Finish(); errFinish != nil {
		return metaCompletedTranslation{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	return metaCompletedTranslation{}, statusErr{code: http.StatusRequestTimeout, msg: "meta stream error: stream disconnected before response.completed or response.incomplete"}
}

func (e *MetaExecutor) executeMetaCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(helps.ResponsesCompactionSourcePayload(req, opts), baseModel)
	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  summaryPayload,
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return resp, errSummary
	}
	respPayload, errBuild := helps.BuildResponsesCompactionResponseFromSummary(baseModel, summaryResp.Payload)
	if errBuild != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: errBuild.Error()}
	}
	return cliproxyexecutor.Response{
		Payload: respPayload,
		Headers: summaryResp.Headers,
	}, nil
}

func (e *MetaExecutor) executeMetaCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryResp, errSummary := e.executeMetaCompaction(ctx, auth, req, opts)
	if errSummary != nil {
		return nil, errSummary
	}
	chunks, errChunks := helps.BuildResponsesCompactionStreamChunksFromResponse(baseModel, summaryResp.Payload)
	if errChunks != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: errChunks.Error()}
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)

	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func (e *MetaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, "meta"); errExpand != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return cliproxyexecutor.Response{}, errAuth
	}
	prepared, errPrepare := e.prepareResponsesRequest(ctx, enriched, req, opts, false)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	enc, errEnc := tokenizer.Get(tokenizer.O200kBase)
	if errEnc != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: tokenizer init failed: %w", errEnc)
	}
	count, errCount := countCodexInputTokens(enc, prepared.body)
	if errCount != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: token counting failed: %w", errCount)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, prepared.to, prepared.responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func (e *MetaExecutor) recordMetaRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

// applyMetaClientIdentity stamps the headers Muse Code 1.4.14 sends on
// /v1/responses. Tool-bearing subscription requests also need the canonical
// tool namespace. Session ids are a fresh UUID-7 per request so
// proxy traffic does not reuse one Muse session across unrelated clients.
func applyMetaClientIdentity(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", metaUserAgent())
	req.Header.Set("X-Client-Id", metaClientSurface)
	if strings.TrimSpace(req.Header.Get("X-Tbh-Session-Id")) != "" {
		return
	}
	sessionID := uuid.Must(uuid.NewV7()).String()
	req.Header.Set("X-Tbh-Session-Id", sessionID)
	req.Header.Set("X-Meta-Ai-Gateway-Session-Id", sessionID)
	req.Header.Set("X-Tbh-Thread-Id", sessionID)
	req.Header.Set("X-Tbh-Root-Session-Id", sessionID)
	req.Header.Set("X-Tbh-Agent-Role", "main")
}

func isMetaContributorModel(model string) bool {
	base := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	return strings.HasSuffix(strings.ToLower(base), "-contributor")
}

func applyMetaAPIHeaders(req *http.Request, auth *cliproxyauth.Auth, token string, stream bool, clientHeaders http.Header, model ...string) {
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Del("Authorization")
	}
	applyMetaClientIdentity(req)
	for _, m := range model {
		if isMetaContributorModel(m) {
			req.Header.Set("X-Tbh-Contributor-Mode", "1")
			break
		}
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)
}

const (
	metaNotFoundCooldown           = 5 * time.Minute
	metaSubscriptionUsageEventType = "response.subscription_usage"
)

var metaEventTag = []byte("event:")

func isMetaSubscriptionUsageEvent(eventType string, eventData []byte) bool {
	if eventType == metaSubscriptionUsageEventType {
		return true
	}
	if eventType == "" && len(eventData) > 0 && ojson.GetBytesNoCopy(eventData, "subscription").IsObject() {
		return true
	}
	return false
}

func wrapMetaUpstreamError(statusCode int, body []byte) error {
	se := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if retryAfter := parseMetaRetryAfter(statusCode, body, time.Now()); retryAfter != nil {
			se.retryAfter = retryAfter
		}
		if isMetaSubscriptionQuota(statusCode, body) {
			return metaRateLimitError{statusErr: se, credentialScoped: true}
		}
	}
	if statusCode == http.StatusNotFound {
		if retryAfter := parseMetaRetryAfter(statusCode, body, time.Now()); retryAfter != nil {
			se.retryAfter = retryAfter
		} else {
			retry := metaNotFoundCooldown
			se.retryAfter = &retry
		}
	}
	return se
}

func metaStreamEventError(eventData []byte) error {
	eventType := ojson.GetBytesNoCopy(eventData, "type").String()
	if eventType != "error" && eventType != "response.failed" {
		return nil
	}
	statusCode := http.StatusBadGateway
	if code := int(ojson.GetBytesNoCopy(eventData, "error.code").Int()); code >= 400 && code <= 599 {
		statusCode = code
	}
	return wrapMetaUpstreamError(statusCode, eventData)
}

func metaAsCompletedEvent(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if !ojson.ValidBytes(trimmed) {
		return nil, false
	}
	root := util.ParseGJSONBytesNoCopy(trimmed)
	switch root.Get("type").String() {
	case "response.completed", "response.incomplete":
		return trimmed, true
	}
	if root.Get("object").String() == "response" || root.Get("output").Exists() {
		wrapped, errSet := ojson.SetRawBytes([]byte(`{"type":"response.completed"}`), "response", trimmed)
		if errSet != nil {
			return nil, false
		}
		return wrapped, true
	}
	return nil, false
}
