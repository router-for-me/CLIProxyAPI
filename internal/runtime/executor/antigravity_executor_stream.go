package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ExecuteStream performs a streaming request to the Antigravity API.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if helps.HasResponsesCompactionItem(req.Payload) {
		expanded, errExpand := helps.ExpandAntigravityCompactionCapsules(req.Payload)
		if errExpand != nil {
			return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
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
	if helps.HasResponsesCompactionTrigger(req.Payload) || helps.HasResponsesCompactionTrigger(opts.OriginalRequest) {
		return e.executeCompactionStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	ctx = context.WithValue(ctx, "alt", "")
	if !antigravityCoolingDisabled(auth, e.cfg) {
		if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
			return nil, homeKVUnavailableStatusErr(errCooldown)
		} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
			log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
			d := remaining
			return nil, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
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
		return nil, errValidate
	}
	req.Payload = originalPayload
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}

	modelInfo, _ := cliproxyauth.ResolvedModelInfo(req)
	translationReq := sdktranslator.RequestEnvelope{Format: from, Model: baseModel, Stream: true, ModelInfo: modelInfo}
	originalTranslated, translated := helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, translationReq, originalPayload, req.Payload)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, "antigravity", "request", originalTranslated, req, opts))
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	translated, _ = sjson.DeleteBytes(translated, "request.stream")
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
			return nil, err
		}
	}
	requestPayload = ensureAntigravityGeminiBoundaryUserContent(baseModel, requestPayload)
	httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, true, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
	if errReq != nil {
		err = errReq
		return nil, err
	}
	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "generate_stream").Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return nil, errDo
		}
		err = errDo
		return nil, err
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
				return nil, err
			}
			if errCtx := ctx.Err(); errCtx != nil {
				err = errCtx
				return nil, err
			}
			err = errRead
			return nil, err
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
						return nil, err
					}
					log.Debugf("antigravity executor: short quota cooldown (%s) for model %s recorded", *decision.retryAfter, baseModel)
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
		return nil, err
	}

	// Stream success
	if useCredits {
		clearAntigravityCreditsFailureState(auth)
	}
	replayAccumulator := newAntigravityReasoningReplayAccumulator(replayScope, requestPayload)
	out := make(chan cliproxyexecutor.StreamChunk)
	stopDeliverySupport := usage.SupportStreamDelivery(ctx)
	go func(resp *http.Response) {
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response line error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var streamUsage helps.StreamUsageBuffer
		var streamErr error
		var pendingJSON []byte
		var replayCommitted bool
		var terminalDelivered bool
		var param any
		commitReplay := func() {
			if !replayCommitted && replayAccumulator != nil && replayAccumulator.terminal && helps.ApplyPatchTranslationError(param) == nil {
				replayAccumulator.Commit(ctx)
				replayCommitted = true
			}
		}
		defer func() {
			defer stopDeliverySupport()
			if helps.ApplyPatchTranslationError(param) != nil {
				streamErr = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				stopDeliverySupport()
			}
			// Replay is settled before forwarding completion; accounting still waits
			// for the HTTP consumer, which may need EOF to acknowledge delivery.
			if terminalDelivered {
				commitReplay()
			}
			close(out)
			if streamErr == nil {
				streamErr = ctx.Err()
			}
			if deliveryErr, tracked := usage.WaitStreamDelivery(ctx); tracked {
				if streamErr == nil || errors.Is(streamErr, context.Canceled) {
					streamErr = deliveryErr
				}
			}
			if streamErr != nil {
				if terminalDelivered && errors.Is(streamErr, context.Canceled) {
					streamUsage.Publish(ctx, reporter)
					reporter.EnsurePublished(ctx)
					return
				}
				if !streamUsage.PublishFailure(ctx, reporter, streamErr) {
					reporter.PublishFailure(ctx, streamErr)
				}
				return
			}

			streamUsage.Publish(ctx, reporter)
			reporter.EnsurePublished(ctx)
		}()
		helps.InitializeApplyPatchStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, &param)
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if replayAccumulator != nil {
				replayAccumulator.ObserveSSELine(line)
			}

			// Capture accounting before the client-facing filter renames usage.
			streamUsage.Observe(helps.ParseAntigravityStreamUsage(line))

			// Filter usage metadata for all models
			// Only retain usage statistics in the terminal chunk
			line = helps.FilterSSEUsageMetadata(line)

			payload := helps.JSONPayload(line)
			if len(pendingJSON) > 0 {
				trimmedLine := bytes.TrimSpace(line)
				if bytes.HasPrefix(trimmedLine, []byte("data:")) {
					trimmedLine = bytes.TrimSpace(trimmedLine[len("data:"):])
				}
				if len(trimmedLine) > 0 {
					pendingJSON = append(pendingJSON, '\n')
					pendingJSON = append(pendingJSON, trimmedLine...)
				}
				if !gjson.ValidBytes(pendingJSON) {
					continue
				}
				payload = pendingJSON
				pendingJSON = nil
			} else if payload != nil && !gjson.ValidBytes(payload) {
				pendingJSON = append([]byte(nil), payload...)
				continue
			}
			if payload == nil {
				continue
			}
			if errorResult := gjson.GetBytes(payload, "error"); errorResult.Exists() {
				statusCode := int(errorResult.Get("code").Int())
				if statusCode < http.StatusBadRequest || statusCode > 599 {
					statusCode = http.StatusBadGateway
				}
				streamErr = newAntigravityStatusErr(statusCode, payload)
				stopDeliverySupport()
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}
			reporter.ObserveResponseModel(payload)

			payload = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, payload)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, bytes.Clone(payload), &param, claudeInputTokens)
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			var isTerminalChunk bool
			if finishReason := gjson.GetBytes(payload, "candidates.0.finishReason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
				isTerminalChunk = true
			} else if finishReason := gjson.GetBytes(payload, "response.candidates.0.finishReason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
				isTerminalChunk = true
			} else if replayAccumulator != nil && replayAccumulator.terminal {
				isTerminalChunk = true
			}
			if !isTerminalChunk {
				for _, chunk := range chunks {
					for _, cLine := range bytes.Split(chunk, []byte("\n")) {
						trimmed := bytes.TrimSpace(cLine)
						if bytes.Equal(trimmed, []byte("data: [DONE]")) || bytes.Equal(trimmed, []byte("[DONE]")) {
							isTerminalChunk = true
							break
						}
						cPayload := helps.JSONPayload(cLine)
						if len(cPayload) == 0 {
							continue
						}
						cType := gjson.GetBytes(cPayload, "type").String()
						if cType == "response.completed" || cType == "message_stop" {
							isTerminalChunk = true
							break
						}
						if finishReason := gjson.GetBytes(cPayload, "choices.0.finish_reason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
							isTerminalChunk = true
							break
						}
					}
					if isTerminalChunk {
						break
					}
				}
			}
			if replayAccumulator != nil && replayAccumulator.terminal && !replayCommitted && responseFormat == sdktranslator.FormatOpenAIResponse {
				// Wait for the translated completion, not the first finishReason:
				// split usage/signature frames may still extend the replay ledger.
				for _, chunk := range chunks {
					for _, line := range bytes.Split(chunk, []byte("\n")) {
						if gjson.GetBytes(helps.JSONPayload(line), "type").String() == "response.completed" {
							commitReplay()
						}
					}
				}
			}
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
			if isTerminalChunk {
				terminalDelivered = true
			}
			if helps.StopApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return
			}
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			streamErr = errScan
			if errors.Is(errScan, context.Canceled) && ctx.Err() != nil {
				if terminalDelivered {
					return
				}
				if deliveryErr, tracked := usage.WaitStreamDelivery(ctx); tracked && deliveryErr == nil {
					return
				}
			}
			stopDeliverySupport()
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else if ctx.Err() == nil {
			// Only a clean end of stream may produce a synthetic terminal event.
			// Translating [DONE] after a read error would report a truncated
			// stream as a successful completion.
			tail := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, []byte("[DONE]"), &param, claudeInputTokens)
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			// Commit upstream output before EOF-generated completion reaches interceptors.
			// Commit itself rejects partial streams without an upstream terminal.
			commitReplay()
			for i := range tail {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: tail[i]}:
				case <-ctx.Done():
					return
				}
			}
			terminalDelivered = true
		}
	}(httpResp)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *AntigravityExecutor) executeCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
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
		return nil, errSummary
	}

	summaryText, errExtract := helps.ExtractAntigravitySummaryText(summaryResp.Payload)
	if errExtract != nil {
		return nil, fmt.Errorf("extract summary: %w", errExtract)
	}
	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, baseModel)
	if errSeal != nil {
		return nil, fmt.Errorf("seal compaction capsule: %w", errSeal)
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

	chunks := helps.BuildAntigravityCompactionStreamChunks(baseModel, capsule, inputTokens, outputTokens, totalTokens)
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

	return &cliproxyexecutor.StreamResult{
		Headers: headers,
		Chunks:  out,
	}, nil
}
