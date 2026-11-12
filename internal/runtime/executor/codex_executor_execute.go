package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type codexCompactionSummaryContextKey struct{}

type codexCompactionSummaryState struct {
	baseModel string
}

func withCodexCompactionSummary(ctx context.Context, baseModel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexCompactionSummaryContextKey{}, &codexCompactionSummaryState{baseModel: baseModel})
}

func codexCompactionSummaryStateFromContext(ctx context.Context) *codexCompactionSummaryState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(codexCompactionSummaryContextKey{}).(*codexCompactionSummaryState)
	return state
}

func codexCompactionSummaryFromContext(ctx context.Context) bool {
	return codexCompactionSummaryStateFromContext(ctx) != nil
}

func rejectSyntheticCompactionPreviousResponseID(ctx context.Context, payload []byte) error {
	prevID := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "previous_response_id").String())
	if !helps.RecognizedResponsesCompactionResponseID(prevID) {
		return nil
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) {
		return &cliproxyexecutor.UpstreamWebsocketTurnError{
			FullContextRequired: true,
			EventID:             strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(payload, "event_id").String())),
		}
	}
	return statusErr{
		code: http.StatusBadRequest,
		msg:  "previous_response_id references a local compaction response; resend full conversation input without previous_response_id",
	}
}

func validateCodexLocalCompactionSource(ctx context.Context, payload []byte) error {
	prevID := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "previous_response_id").String())
	reqType := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "type").String())
	if prevID != "" || reqType == "response.append" {
		if cliproxyexecutor.DownstreamWebsocket(ctx) {
			return &cliproxyexecutor.UpstreamWebsocketTurnError{
				FullContextRequired: true,
				EventID:             strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(payload, "event_id").String())),
			}
		}
		return statusErr{
			code: http.StatusBadRequest,
			msg:  "local summary compaction requires full conversation input without previous_response_id",
		}
	}
	if !helps.HasResponsesCompactionConversationInput(payload) {
		return statusErr{
			code: http.StatusBadRequest,
			msg:  "local summary compaction requires non-empty conversation input",
		}
	}
	return nil
}

func (e *CodexExecutor) expandCodexResponsesCompaction(auth *cliproxyauth.Auth, req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options, baseModel string) (bool, error) {
	if req == nil || opts == nil {
		return false, nil
	}
	isCompat := e.resolveCodexModelIsCompat(auth, *req, baseModel)
	if isCompat && opts.SourceFormat == sdktranslator.FormatOpenAIResponse && opts.Alt != "responses/compact" {
		return true, helps.ExpandResponsesCompactionCapsules(req, opts, e.Identifier())
	}
	return isCompat, helps.ExpandRecognizedResponsesCompactionCapsules(req, opts)
}

func (e *CodexExecutor) shouldCompactCompatResponsesTrigger(isCompat bool, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return isCompat && opts.SourceFormat == sdktranslator.FormatOpenAIResponse && opts.Alt != "responses/compact" && helps.HasResponsesCompactionTriggerRequest(req, opts)
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if isCodexOpenAIImageRequest(opts) {
		if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
			return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
		}
		return e.executeOpenAIImage(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	isCompat, errExpand := e.expandCodexResponsesCompaction(auth, &req, &opts, baseModel)
	if errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if !codexCompactionSummaryFromContext(ctx) && e.shouldCompactCompatResponsesTrigger(isCompat, req, opts) {
		return e.executeCodexResponsesCompaction(ctx, auth, req, opts)
	}
	if errPrev := rejectSyntheticCompactionPreviousResponseID(ctx, req.Payload); errPrev != nil {
		return resp, errPrev
	}

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return resp, errSIWC
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, false, isCompat)
	if err != nil {
		return resp, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	doc := ojson.NewDoc(body)
	normalizeCodexOutputBudgetDoc(doc)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return resp, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts, auth))
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", true)
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("generate")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
	hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
	isCompactionSummary := codexCompactionSummaryFromContext(ctx)
	if !isCompactionSummary {
		ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	}
	if !useSIWC {
		normalizeCodexInstructionsDoc(doc, helps.IsNativeCodexRequest(req.Payload, opts))
		if !isCompactionSummary && (e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff) {
			checkModel := req.Model
			if strings.TrimSpace(checkModel) == "" {
				checkModel = baseModel
			}
			ensureImageGenerationToolDoc(doc, checkModel, auth, opts.Headers)
			if !codexAuthUsesAPIKey(auth) {
				normalizeCodexImageGenerationToolsDoc(doc)
			}
		}
	} else {
		applyCodexSIWCRequestBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
	helps.NormalizeCodexToolSchemasDoc(doc)
	optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuthDoc(doc, ctx, opts.Headers, e.cfg, auth, isCompat)
	replayScope, _, errReplay := applyCodexReasoningReplayCacheRequiredDoc(ctx, from, req, opts, doc)
	if errReplay != nil {
		return resp, errReplay
	}
	sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex executor", doc, isCompat, targetModel, baseURL)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, upstreamBody, err := e.cacheHelperDoc(ctx, from, url, req, doc, auth, opts.Headers)
	if err != nil {
		return resp, err
	}
	body = upstreamBody
	upstreamModel := strings.TrimSpace(doc.Get("model").String())
	if upstreamModel != "" && upstreamModel != baseModel {
		reporter.SetUpstreamModel(upstreamModel)
	}
	reporter.SetTranslatedReasoningEffort(upstreamBody, to.String())
	prepareHeaders := func(request *http.Request) {
		applyCodexHeaders(request, auth, apiKey, true, e.cfg, opts.Headers)
		applyCodexRoutingHint(ctx, request.Header, auth, upstreamModel, upstreamBody, opts.Headers)
		applyModelHeaderOverrides(request.Header, upstreamModel)
		alignCodexContentEncoding(request)
	}
	prepareHeaders(httpReq)
	if errValidate := helps.ValidateCodexHostedImageGenerationDoc(doc, httpReq.Header, e.cfg, !useSIWC && !codexAuthUsesAPIKey(auth)); errValidate != nil {
		return resp, errValidate
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, nil)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, errRead := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		}
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, b); errClearReplay != nil {
			return resp, errClearReplay
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
		return resp, err
	}
	data, errRead := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	events := helps.NewCodexSSEScanner(ctx, bytes.NewReader(data), len(data)+1)
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	sawOutputDelta := false
	for events.Scan() {
		line := events.Bytes()
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		eventData := bytes.TrimSpace(line[5:])
		eventData = helps.RestoreCodexMultiAgentV2Response(eventData, optimizeMultiAgentV2)
		reporter.ObserveResponseModel(eventData)
		eventType := ojson.GetBytesNoCopy(eventData, "type").String()

		if helps.HasMeaningfulCodexOutputDelta(eventData) {
			sawOutputDelta = true
		}

		if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(eventData, e.modelLevelCooling()); ok {
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
				if detail, hasDetail := helps.ParseCodexUsageWithOutputItems(eventData, outputItemsByIndex, outputItemsFallback); hasDetail {
					reporter.PublishFailureWithDetail(ctx, detail, errClearReplay)
				}
				return resp, errClearReplay
			}
			err = streamErr
			if detail, hasDetail := helps.ParseCodexUsageWithOutputItems(eventData, outputItemsByIndex, outputItemsFallback); hasDetail {
				reporter.PublishFailureWithDetail(ctx, detail, err)
			}
			return resp, err
		}

		if eventType == "response.output_item.done" {
			itemResult := ojson.GetBytesNoCopy(eventData, "item")
			if !itemResult.Exists() || itemResult.Type != ojson.JSON {
				continue
			}
			outputIndexResult := ojson.GetBytesNoCopy(eventData, "output_index")
			if outputIndexResult.Exists() {
				outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
			} else {
				outputItemsFallback = append(outputItemsFallback, []byte(itemResult.Raw))
			}
			continue
		}

		if eventType != "response.completed" && eventType != "response.incomplete" {
			continue
		}

		if helps.IsCodexTerminalEmptyIncomplete(eventData, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
			err = newCodexEmptyIncompleteStreamError()
			if detail, hasDetail := helps.ParseCodexUsageWithOutputItems(eventData, outputItemsByIndex, outputItemsFallback); hasDetail {
				reporter.PublishFailureWithDetail(ctx, detail, err)
			}
			return resp, err
		}

		completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
		if eventType == "response.completed" {
			cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, completedData, &param)
		if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
			return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		detail, hasDetail := helps.ParseCodexUsageWithOutputItems(completedData, outputItemsByIndex, outputItemsFallback)
		if responseFormat == sdktranslator.FormatOpenAIResponse {
			out = helps.EnsureResponsesUsageDetails(out)
		}
		if summaryState := codexCompactionSummaryStateFromContext(ctx); summaryState != nil {
			if eventType == "response.incomplete" {
				err = statusErr{code: http.StatusBadGateway, msg: "compaction summary response status is \"incomplete\", want \"completed\""}
				if hasDetail {
					reporter.PublishFailureWithDetail(ctx, detail, err)
				} else {
					reporter.PublishFailure(ctx, err)
				}
				return cliproxyexecutor.Response{}, err
			}
			compactedPayload, errBuild := helps.BuildResponsesCompactionResponseFromSummary(summaryState.baseModel, out)
			if errBuild != nil {
				err = statusErr{code: http.StatusBadGateway, msg: errBuild.Error()}
				if hasDetail {
					reporter.PublishFailureWithDetail(ctx, detail, err)
				} else {
					reporter.PublishFailure(ctx, err)
				}
				return cliproxyexecutor.Response{}, err
			}
			if hasDetail {
				reporter.Publish(ctx, detail)
			} else {
				reporter.EnsurePublished(ctx)
			}
			publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, completedData, outputItemsByIndex, outputItemsFallback)
			resp = cliproxyexecutor.Response{Payload: compactedPayload, Headers: httpResp.Header.Clone()}
			return resp, nil
		}
		if hasDetail {
			reporter.Publish(ctx, detail)
		}
		publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, completedData, outputItemsByIndex, outputItemsFallback)
		resp = cliproxyexecutor.Response{Payload: out, Incomplete: eventType == "response.incomplete", Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	if errRead != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errCtx)
			err = errCtx
			return resp, err
		}
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
	}
	err = newCodexIncompleteStreamError()
	return resp, err
}

func (e *CodexExecutor) executeCodexResponsesCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	sourcePayload := helps.ResponsesCompactionSourcePayload(req, opts)
	if errValidate := validateCodexLocalCompactionSource(ctx, sourcePayload); errValidate != nil {
		return resp, errValidate
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(sourcePayload, baseModel)
	summaryReq := req
	summaryReq.Payload = summaryPayload
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	return e.Execute(withCodexCompactionSummary(ctx, baseModel), auth, summaryReq, summaryOpts)
}

func (e *CodexExecutor) executeCodexResponsesCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkLocalWebsocketTurn(ctx)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryResp, errSummary := e.executeCodexResponsesCompaction(ctx, auth, req, opts)
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

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return resp, errSIWC
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := e.resolveCodexModelIsCompat(auth, req, baseModel)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, false, isCompat)
	if err != nil {
		return resp, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)

	doc := ojson.NewDoc(body)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return resp, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts))
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	_ = doc.Delete("stream")
	if !codexUseSIWCForRequest(ctx, auth, opts.Headers) {
		normalizeCodexInstructionsDoc(doc, helps.IsNativeCodexRequest(req.Payload, opts))
	} else {
		instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
		hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
		applyCodexSIWCCompactBodyRulesDoc(doc, hasExplicitInstructions, cliproxyexecutor.Options{KeepTopLevelEffort: thinking.HasThinkingSuffix(req.Model)})
	}
	ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex executor", doc, isCompat, resolveResponsesSignatureTargetModel(baseModel, req, opts), baseURL)
	normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
	helps.NormalizeCodexToolSchemasDoc(doc)
	optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuthDoc(doc, ctx, opts.Headers, e.cfg, auth, isCompat)

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	httpReq, upstreamBody, err := e.cacheHelperDoc(ctx, from, url, req, doc, auth, opts.Headers)
	if err != nil {
		return resp, err
	}
	body = upstreamBody
	reporter.SetTranslatedReasoningEffort(upstreamBody, to.String())
	prepareHeaders := func(request *http.Request) {
		applyCodexHeaders(request, auth, apiKey, false, e.cfg, opts.Headers)
		applyCodexRoutingHint(ctx, request.Header, auth, baseModel, upstreamBody, opts.Headers)
		applyModelHeaderOverrides(request.Header, baseModel)
		alignCodexContentEncoding(request)
	}
	prepareHeaders(httpReq)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, nil)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, errRead := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	upstreamData := helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, upstreamData, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(upstreamData))
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}
