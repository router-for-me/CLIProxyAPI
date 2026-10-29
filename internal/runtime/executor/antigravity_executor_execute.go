package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Execute performs a non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if helps.HasResponsesCompactionItem(req.Payload) {
		expanded, errExpand := helps.ExpandAntigravityCompactionCapsules(req.Payload)
		if errExpand != nil {
			return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
		}
		req.Payload = expanded
		if len(opts.OriginalRequest) > 0 {
			expandedOrig, errOrig := helps.ExpandAntigravityCompactionCapsules(opts.OriginalRequest)
			if errOrig == nil {
				opts.OriginalRequest = expandedOrig
			} else {
				opts.OriginalRequest = expanded
			}
		}
	}
	if opts.Alt == "responses/compact" || helps.HasResponsesCompactionTrigger(req.Payload) || helps.HasResponsesCompactionTrigger(opts.OriginalRequest) {
		return e.executeCompaction(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if !antigravityCoolingDisabled(auth, e.cfg) {
		if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
			return resp, homeKVUnavailableStatusErr(errCooldown)
		} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
			log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
			d := remaining
			return resp, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
		}
	}

	isClaude := strings.Contains(strings.ToLower(baseModel), "claude")
	if isClaude || strings.Contains(baseModel, "gemini-3-pro") || strings.Contains(baseModel, "gemini-3.1-flash-image") {
		return e.executeClaudeNonStream(ctx, auth, req, opts)
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalPayload, errValidate := validateAntigravityRequestSignatures(ctx, baseModel, from, originalPayload)
	if errValidate != nil {
		return resp, errValidate
	}
	req.Payload = originalPayload
	// Observe a translation refusal before the token refresh or any upstream call.
	modelInfo, _ := cliproxyauth.ResolvedModelInfo(req)
	translationReq := sdktranslator.RequestEnvelope{Format: from, Model: baseModel, ModelInfo: modelInfo}
	originalTranslated, translated, errTranslate := helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, translationReq, originalPayload, req.Payload)
	if errTranslate != nil {
		return resp, errTranslate
	}
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, "antigravity", "request", originalTranslated, req, opts)
	ctx = helps.WithPayloadFinalizer(ctx, func(body []byte) []byte {
		out := finalize(body)
		reporter.SetTranslatedRequestMetadata(out, to.String())
		return out
	})
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURL := resolveAntigravityRequestBaseURL(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	// Credential retry rounds are owned by the conductor. Perform one upstream
	// request per credential so request-retry is not consumed twice.
	requestPayload := translated
	if useCredits {
		if cp := injectEnabledCreditTypes(translated); len(cp) > 0 {
			requestPayload = cp
			helps.MarkCreditsUsed(ctx)
		}
	}
	replayScope := antigravityReasoningReplayScope{}
	if antigravityUsesReasoningReplayCache(baseModel) {
		var errReplay error
		requestPayload, replayScope, errReplay = prepareAntigravityGeminiReasoningReplayPayload(ctx, baseModel, req, opts, requestPayload)
		if errReplay != nil {
			err = errReplay
			return resp, err
		}
	}
	requestPayload = ensureAntigravityGeminiBoundaryUserContent(baseModel, requestPayload)

	httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, false, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
	if errReq != nil {
		err = errReq
		return resp, err
	}

	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "generate").Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return resp, errDo
		}
		err = errDo
		return resp, err
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("antigravity executor: close response body error: %v", errClose)
	}
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)

	if httpResp.StatusCode == http.StatusTooManyRequests {
		decision := decideAntigravity429(bodyBytes)
		switch decision.kind {
		case antigravity429DecisionShortCooldownSwitchAuth:
			closeAntigravityAuthIdleTransports(auth)
			if decision.retryAfter != nil && *decision.retryAfter > 0 && !antigravityCoolingDisabled(auth, e.cfg) {
				if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
					err = homeKVUnavailableStatusErr(errMarkCooldown)
					return resp, err
				}
				log.Debugf("antigravity executor: short quota cooldown (%s) for model %s, recorded cooldown", *decision.retryAfter, baseModel)
			}
		case antigravity429DecisionFullQuotaExhausted:
			closeAntigravityAuthIdleTransports(auth)
			if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) && !antigravityCoolingDisabled(auth, e.cfg) {
				markAntigravityCreditsPermanentlyDisabled(auth)
			}
			// No credits logic - just fall through to error return below
		}
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
		if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
			// Report the upstream failure rather than the cleanup failure.
			logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
		}
		err = newAntigravityStatusErr(httpResp.StatusCode, bodyBytes)
		return resp, err
	}

	// Success
	if useCredits {
		clearAntigravityCreditsFailureState(auth)
	}
	cacheAntigravityReasoningReplayFromResponse(ctx, replayScope, requestPayload, bodyBytes)
	bodyBytes = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, bodyBytes)
	reporter.ObserveResponseModel(bodyBytes)
	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, bodyBytes, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(converted) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	antiDetail := helps.ParseAntigravityUsage(bodyBytes)
	helps.ApplyGeminiRequestImageConfig(&antiDetail, requestPayload)
	reporter.Publish(ctx, antiDetail)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		converted = helps.EnsureResponsesUsageDetails(converted)
	}
	resp = cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}
	reporter.EnsurePublished(ctx)
	return resp, nil
}

func (e *AntigravityExecutor) executeCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	payload := req.Payload
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(payload, baseModel)

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

	summaryText, errExtract := helps.ExtractAntigravitySummaryText(summaryResp.Payload)
	if errExtract != nil {
		return resp, fmt.Errorf("extract summary: %w", errExtract)
	}
	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, baseModel)
	if errSeal != nil {
		return resp, fmt.Errorf("seal compaction capsule: %w", errSeal)
	}

	inputTokens := int(gjson.GetBytes(summaryResp.Payload, "usage.input_tokens").Int())
	outputTokens := int(gjson.GetBytes(summaryResp.Payload, "usage.output_tokens").Int())
	totalTokens := int(gjson.GetBytes(summaryResp.Payload, "usage.total_tokens").Int())
	if totalTokens == 0 && inputTokens == 0 {
		usage := helps.ParseOpenAIUsage(summaryResp.Payload)
		inputTokens = int(usage.InputTokens)
		outputTokens = int(usage.OutputTokens)
		totalTokens = int(usage.TotalTokens)
	}

	respBytes := helps.BuildAntigravityCompactionResponse(baseModel, capsule, inputTokens, outputTokens, totalTokens)
	return cliproxyexecutor.Response{
		Payload: respBytes,
		Headers: summaryResp.Headers,
	}, nil
}

// executeClaudeNonStream performs a claude non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) executeClaudeNonStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if !antigravityCoolingDisabled(auth, e.cfg) {
		if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
			return resp, homeKVUnavailableStatusErr(errCooldown)
		} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
			log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
			d := remaining
			return resp, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
		}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalPayload, errValidate := validateAntigravityRequestSignatures(ctx, baseModel, from, originalPayload)
	if errValidate != nil {
		return resp, errValidate
	}
	req.Payload = originalPayload
	// Observe a translation refusal before the token refresh or any upstream call.
	modelInfo, _ := cliproxyauth.ResolvedModelInfo(req)
	translationReq := sdktranslator.RequestEnvelope{Format: from, Model: baseModel, Stream: true, ModelInfo: modelInfo}
	originalTranslated, translated, errTranslate := helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, translationReq, originalPayload, req.Payload)
	if errTranslate != nil {
		return resp, errTranslate
	}
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, "antigravity", "request", originalTranslated, req, opts)
	ctx = helps.WithPayloadFinalizer(ctx, func(body []byte) []byte {
		out := finalize(body)
		reporter.SetTranslatedRequestMetadata(out, to.String())
		return out
	})
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURL := resolveAntigravityRequestBaseURL(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	// Credential retry rounds are owned by the conductor. Perform one upstream
	// request per credential so request-retry is not consumed twice.
	requestPayload := translated
	if useCredits {
		if cp := injectEnabledCreditTypes(translated); len(cp) > 0 {
			requestPayload = cp
			helps.MarkCreditsUsed(ctx)
		}
	}
	replayScope := antigravityReasoningReplayScope{}
	if antigravityUsesReasoningReplayCache(baseModel) {
		var errReplay error
		requestPayload, replayScope, errReplay = prepareAntigravityGeminiReasoningReplayPayload(ctx, baseModel, req, opts, requestPayload)
		if errReplay != nil {
			err = errReplay
			return resp, err
		}
	}
	requestPayload = ensureAntigravityGeminiBoundaryUserContent(baseModel, requestPayload)
	httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, true, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
	if errReq != nil {
		err = errReq
		return resp, err
	}

	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "generate").Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return resp, errDo
		}
		err = errDo
		return resp, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
				err = errRead
				return resp, err
			}
			if errCtx := ctx.Err(); errCtx != nil {
				err = errCtx
				return resp, err
			}
			err = errRead
			return resp, err
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
		if httpResp.StatusCode == http.StatusTooManyRequests {
			decision := decideAntigravity429(bodyBytes)

			switch decision.kind {
			case antigravity429DecisionShortCooldownSwitchAuth:
				closeAntigravityAuthIdleTransports(auth)
				if decision.retryAfter != nil && *decision.retryAfter > 0 && !antigravityCoolingDisabled(auth, e.cfg) {
					if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
						err = homeKVUnavailableStatusErr(errMarkCooldown)
						return resp, err
					}
					log.Debugf("antigravity executor: short quota cooldown (%s) for model %s, recorded cooldown", *decision.retryAfter, baseModel)
				}
			case antigravity429DecisionFullQuotaExhausted:
				closeAntigravityAuthIdleTransports(auth)
				if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) && !antigravityCoolingDisabled(auth, e.cfg) {
					markAntigravityCreditsPermanentlyDisabled(auth)
				}
				// No credits logic - just fall through to error return below
			}
		}

		if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
			// Report the upstream failure rather than the cleanup failure.
			logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
		}
		err = newAntigravityStatusErr(httpResp.StatusCode, bodyBytes)
		return resp, err
	}

	// Stream success
	if useCredits {
		clearAntigravityCreditsFailureState(auth)
	}
	replayAccumulator := newAntigravityReasoningReplayAccumulator(replayScope, requestPayload)
	var rawPayloads [][]byte
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		if replayAccumulator != nil {
			replayAccumulator.ObserveSSELine(line)
		}
		payload := helps.JSONPayload(line)
		if payload == nil {
			continue
		}
		reporter.ObserveResponseModel(payload)
		rawPayloads = append(rawPayloads, bytes.Clone(payload))
	}
	scanErr := scanner.Err()
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("antigravity executor: close response body error: %v", errClose)
	}
	if scanErr != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, scanErr)
		reporter.PublishFailure(ctx, scanErr)
		return resp, scanErr
	}
	if replayAccumulator != nil {
		replayAccumulator.Commit(ctx)
	}
	resp = cliproxyexecutor.Response{Payload: e.convertStreamChunksToNonStream(rawPayloads)}

	resp.Payload = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, resp.Payload)
	reporter.ObserveResponseModel(resp.Payload)
	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, resp.Payload, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(converted) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	antiDetail := helps.ParseAntigravityUsage(resp.Payload)
	helps.ApplyGeminiRequestImageConfig(&antiDetail, requestPayload)
	reporter.Publish(ctx, antiDetail)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		converted = helps.EnsureResponsesUsageDetails(converted)
	}
	resp = cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}
	reporter.EnsurePublished(ctx)

	return resp, nil
}

const antigravityPartsPlaceholder = `"__cpa_parts_placeholder__"`

func (e *AntigravityExecutor) convertStreamToNonStream(stream []byte) []byte {
	var chunks [][]byte
	remaining := stream
	for len(remaining) > 0 {
		var line []byte
		if nl := bytes.IndexByte(remaining, '\n'); nl >= 0 {
			line = remaining[:nl]
			remaining = remaining[nl+1:]
		} else {
			line = remaining
			remaining = nil
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			chunks = append(chunks, trimmed)
		}
	}
	return e.convertStreamChunksToNonStream(chunks)
}

func (e *AntigravityExecutor) convertStreamChunksToNonStream(chunks [][]byte) []byte {
	var (
		responseNodeRaw    string
		responsePartsRaw   string
		responsePartsIndex int
		traceID            string
		finishReason       string
		modelVersion       string
		responseID         string
		role               string
		usageRaw           string
		parts              []string
		pendingKind        string
		pendingText        strings.Builder
		pendingThoughtSig  string
	)

	flushPending := func() {
		if pendingKind == "" {
			return
		}
		text := pendingText.String()
		switch pendingKind {
		case "text":
			if strings.TrimSpace(text) == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			encoded, _ := json.Marshal(map[string]any{"text": text})
			parts = append(parts, string(encoded))
		case "thought":
			if strings.TrimSpace(text) == "" && pendingThoughtSig == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			part := map[string]any{"thought": true, "text": text}
			if pendingThoughtSig != "" {
				part["thoughtSignature"] = pendingThoughtSig
			}
			encoded, _ := json.Marshal(part)
			parts = append(parts, string(encoded))
		}
		pendingKind = ""
		pendingText.Reset()
		pendingThoughtSig = ""
	}

	normalizeRawPart := func(partResult gjson.Result, hasSnakeSig, hasSnakeInline bool, sig string, inlineRaw string) string {
		if !hasSnakeSig && !hasSnakeInline {
			return partResult.Raw
		}
		raw := []byte(partResult.Raw)
		if hasSnakeSig {
			if sig != "" {
				raw, _ = sjson.SetBytes(raw, "thoughtSignature", sig)
			}
			raw, _ = sjson.DeleteBytes(raw, "thought_signature")
		}
		if hasSnakeInline {
			if inlineRaw != "" {
				raw, _ = sjson.SetRawBytes(raw, "inlineData", []byte(inlineRaw))
			}
			raw, _ = sjson.DeleteBytes(raw, "inline_data")
		}
		return string(raw)
	}

	for _, line := range chunks {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
			continue
		}

		root := util.GetGJSONBytesNoCopy(trimmed, "@this")
		responseNode := root.Get("response")
		if !responseNode.Exists() {
			if root.Get("candidates").Exists() {
				responseNode = root
			} else {
				continue
			}
		}
		responseNodeRaw = responseNode.Raw
		responsePartsRaw = ""
		responsePartsIndex = -1

		if traceResult := root.Get("traceId"); traceResult.Exists() && traceResult.String() != "" {
			traceID = traceResult.String()
		}

		var partsResult gjson.Result
		responseNode.ForEach(func(rk, rv gjson.Result) bool {
			switch rk.String() {
			case "modelVersion":
				if rv.String() != "" {
					modelVersion = rv.String()
				}
			case "responseId":
				if rv.String() != "" {
					responseID = rv.String()
				}
			case "usageMetadata", "cpaUsageMetadata":
				if rk.String() == "usageMetadata" || usageRaw == "" {
					usageRaw = rv.Raw
				}
			case "candidates":
				if rv.IsArray() {
					cand0 := rv.Get("0")
					if cand0.IsObject() {
						cand0.ForEach(func(ck, cv gjson.Result) bool {
							switch ck.String() {
							case "finishReason":
								if cv.String() != "" {
									finishReason = cv.String()
								}
							case "content":
								if cv.IsObject() {
									cv.ForEach(func(ctk, ctv gjson.Result) bool {
										switch ctk.String() {
										case "role":
											role = ctv.String()
										case "parts":
											partsResult = ctv
											responsePartsRaw = ctv.Raw
											responsePartsIndex = ctv.Index - responseNode.Index
										}
										return true
									})
								}
							}
							return true
						})
					}
				}
			}
			return true
		})
		if usageRaw == "" {
			if usageMetadataResult := root.Get("usageMetadata"); usageMetadataResult.Exists() {
				usageRaw = usageMetadataResult.Raw
			}
		}

		if partsResult.IsArray() {
			partsResult.ForEach(func(_, part gjson.Result) bool {
				var (
					hasFunctionCall bool
					hasInlineData   bool
					hasSnakeInline  bool
					hasSnakeSig     bool
					hasText         bool
					sig             string
					inlineRaw       string
					text            string
					thought         bool
				)
				part.ForEach(func(pk, pv gjson.Result) bool {
					switch pk.String() {
					case "functionCall":
						hasFunctionCall = true
					case "inlineData":
						hasInlineData = true
					case "inline_data":
						hasInlineData = true
						hasSnakeInline = true
						inlineRaw = pv.Raw
					case "thoughtSignature":
						sig = pv.String()
					case "thought_signature":
						hasSnakeSig = true
						if sig == "" {
							sig = pv.String()
						}
					case "text":
						hasText = true
						text = pv.String()
					case "thought":
						thought = pv.Bool()
					}
					return true
				})

				if hasFunctionCall || hasInlineData {
					flushPending()
					parts = append(parts, normalizeRawPart(part, hasSnakeSig, hasSnakeInline, sig, inlineRaw))
					return true
				}

				if thought || hasText {
					kind := "text"
					if thought {
						kind = "thought"
					}
					if pendingKind != "" && pendingKind != kind {
						flushPending()
					}
					pendingKind = kind
					pendingText.WriteString(text)
					if kind == "thought" && sig != "" {
						pendingThoughtSig = sig
					}
					return true
				}

				flushPending()
				parts = append(parts, normalizeRawPart(part, hasSnakeSig, hasSnakeInline, sig, inlineRaw))
				return true
			})
		}
	}
	flushPending()

	var templateBytes []byte
	if responseNodeRaw == "" {
		templateBytes = []byte(`{"candidates":[{"content":{"role":"model","parts":` + antigravityPartsPlaceholder + `}}]}`)
	} else if responsePartsRaw != "" {
		if idx := responsePartsIndex; idx >= 0 && idx+len(responsePartsRaw) <= len(responseNodeRaw) {
			templateBytes = make([]byte, 0, len(responseNodeRaw)-len(responsePartsRaw)+len(antigravityPartsPlaceholder))
			templateBytes = append(templateBytes, responseNodeRaw[:idx]...)
			templateBytes = append(templateBytes, antigravityPartsPlaceholder...)
			templateBytes = append(templateBytes, responseNodeRaw[idx+len(responsePartsRaw):]...)
		} else {
			templateBytes, _ = sjson.SetRawBytes([]byte(responseNodeRaw), "candidates.0.content.parts", []byte(antigravityPartsPlaceholder))
		}
	} else {
		templateBytes, _ = sjson.SetRawBytes([]byte(responseNodeRaw), "candidates.0.content.parts", []byte(antigravityPartsPlaceholder))
	}

	if role != "" {
		templateBytes, _ = sjson.SetBytes(templateBytes, "candidates.0.content.role", role)
	}
	if finishReason != "" {
		templateBytes, _ = sjson.SetBytes(templateBytes, "candidates.0.finishReason", finishReason)
	}
	if modelVersion != "" {
		templateBytes, _ = sjson.SetBytes(templateBytes, "modelVersion", modelVersion)
	}
	if responseID != "" {
		templateBytes, _ = sjson.SetBytes(templateBytes, "responseId", responseID)
	}
	if usageRaw != "" {
		templateBytes, _ = sjson.SetRawBytes(templateBytes, "usageMetadata", []byte(usageRaw))
	} else if !gjson.GetBytes(templateBytes, "usageMetadata").Exists() {
		templateBytes, _ = sjson.SetRawBytes(templateBytes, "usageMetadata", []byte(`{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}`))
	}
	templateBytes, _ = sjson.DeleteBytes(templateBytes, "cpaUsageMetadata")

	partsTotalLen := 2
	for i, p := range parts {
		if i > 0 {
			partsTotalLen++
		}
		partsTotalLen += len(p)
	}

	traceJSON, _ := json.Marshal(traceID)
	placeholder := gjson.GetBytes(templateBytes, "candidates.0.content.parts")
	placeholderIdx := placeholder.Index
	if placeholder.Raw != antigravityPartsPlaceholder || placeholderIdx <= 0 {
		partsJSON := make([]byte, 0, partsTotalLen)
		partsJSON = append(partsJSON, '[')
		for i, p := range parts {
			if i > 0 {
				partsJSON = append(partsJSON, ',')
			}
			partsJSON = append(partsJSON, p...)
		}
		partsJSON = append(partsJSON, ']')
		templateBytes, _ = sjson.SetRawBytes(templateBytes, "candidates.0.content.parts", partsJSON)
		output := make([]byte, 0, len(templateBytes)+len(traceJSON)+28)
		output = append(output, `{"response":`...)
		output = append(output, templateBytes...)
		output = append(output, `,"traceId":`...)
		output = append(output, traceJSON...)
		output = append(output, '}')
		return output
	}

	output := make([]byte, 0, len(templateBytes)-len(antigravityPartsPlaceholder)+partsTotalLen+len(traceJSON)+28)
	output = append(output, `{"response":`...)
	output = append(output, templateBytes[:placeholderIdx]...)
	output = append(output, '[')
	for i, p := range parts {
		if i > 0 {
			output = append(output, ',')
		}
		output = append(output, p...)
	}
	output = append(output, ']')
	output = append(output, templateBytes[placeholderIdx+len(antigravityPartsPlaceholder):]...)
	output = append(output, `,"traceId":`...)
	output = append(output, traceJSON...)
	output = append(output, '}')
	return output
}
