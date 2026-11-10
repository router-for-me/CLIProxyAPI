package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		if errExpand := helps.ExpandRecognizedResponsesCompactionCapsules(&req, &opts); errExpand != nil {
			return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
		}
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	isCompat, errExpand := e.expandCodexResponsesCompaction(auth, &req, &opts, baseModel)
	if errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if e.shouldCompactCompatResponsesTrigger(isCompat, req, opts) {
		return e.executeCodexResponsesCompactionStream(ctx, auth, req, opts)
	}
	if errPrev := rejectSyntheticCompactionPreviousResponseID(ctx, req.Payload); errPrev != nil {
		return nil, errPrev
	}

	apiKey, baseURL := codexCredsForRequest(ctx, auth, opts.Headers)
	if errSIWC := ensureCodexSIWCCredentialsForRequest(ctx, auth, apiKey, opts.Headers); errSIWC != nil {
		return nil, errSIWC
	}
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	preserveNativeOutput := helps.IsNativeCodexRequest(req.Payload, opts)
	isGrokClient := grokbuild.IsGrokClientContext(ctx, opts.Headers)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, body, updatesChanged, err := translateCodexRequestPairWithUpdateIntentContext(ctx, from, to, targetModel, originalPayload, req.Payload, true, isCompat)
	if err != nil {
		return nil, err
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)
	doc := ojson.NewDoc(body)
	normalizeCodexOutputBudgetDoc(doc)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return nil, err
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts, auth))
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("generate")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	reasoningSummaryDelivery := doc.Get("stream_options.reasoning_summary_delivery")
	hasReasoningSummaryDelivery := reasoningSummaryDelivery.Exists()
	var reasoningSummaryDeliveryVal any
	if hasReasoningSummaryDelivery {
		reasoningSummaryDeliveryVal = reasoningSummaryDelivery.Value()
	}
	_ = doc.Delete("stream_options")
	if hasReasoningSummaryDelivery {
		_ = doc.Set("stream_options.reasoning_summary_delivery", reasoningSummaryDeliveryVal)
	}
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	useSIWC := codexUseSIWCForRequest(ctx, auth, opts.Headers)
	instructionsResult := ojson.GetBytesNoCopy(req.Payload, "instructions")
	hasExplicitInstructions := instructionsResult.Exists() && strings.TrimSpace(instructionsResult.String()) != ""
	ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
	if !useSIWC {
		normalizeCodexInstructionsDoc(doc, preserveNativeOutput)
		if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
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
		return nil, errReplay
	}
	sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex executor", doc, isCompat, targetModel, baseURL)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, upstreamBody, err := e.cacheHelperDoc(ctx, from, url, req, doc, auth, opts.Headers)
	if err != nil {
		return nil, err
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
		return nil, errValidate
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
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	buffering := e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering
	var bootstrapTimeout time.Duration
	var bootstrapStart time.Time
	if buffering {
		bootstrapTimeout = e.cfg.Codex.StreamBootstrapTimeoutDuration()
	}
	// Start the bootstrap clock at response headers, before decoding may read body bytes.
	responseReady := func(response *http.Response) {
		if buffering && response.StatusCode >= 200 && response.StatusCode < 300 {
			bootstrapStart = nowCodexBootstrap()
		}
	}
	httpResp, err := doCodexRequest(httpClient, httpReq, upstreamBody, prepareHeaders, responseReady)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErrWithHeadersAndCooling(httpResp.StatusCode, httpResp.Header, data, e.modelLevelCooling())
		return nil, err
	}

	bufferedFrames := 0
	bufferedBytes := 0
	budgetExhausted := false
	exhaustedReason := ""

	scanner := helps.NewCodexSSEScanner(ctx, httpResp.Body, 52_428_800) // 50MB
	scanner.SetOnLine(func(rawLine []byte) {
		helps.AppendAPIResponseChunk(ctx, e.cfg, rawLine)
	})
	if buffering {
		scanner.SetBudgetChecker(func(line []byte, heldLines, heldBytes int) bool {
			timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
			if bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout {
				budgetExhausted = true
				exhaustedReason = "time budget"
				return true
			}
			if bufferedFrames+heldLines >= codexBootstrapMaxBufferedFrames {
				budgetExhausted = true
				exhaustedReason = "frame budget"
				return true
			}
			if bufferedBytes+heldBytes >= codexBootstrapMaxBufferedBytes {
				budgetExhausted = true
				exhaustedReason = "byte budget"
				return true
			}
			return false
		})
	}
	claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
	var param any
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte

	var bufferedChunks [][]byte
	var initialChunks [][]byte
	streamStarted := false
	immediateTerminal := false
	immediateIncomplete := false
	// bootstrapTerminalErr holds a non-overload terminal failure seen while buffering. It is
	// delivered as an in-stream chunk after the buffered handshake so downstream behaviour stays
	// identical to the unbuffered path instead of silently turning into a credential failover.
	var bootstrapTerminalErr error

	closeBootstrapBody := func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}

	sawOutputDelta := false
	if buffering {
		for scanner.Scan() {
			line := scanner.Bytes()
			if budgetExhausted {
				helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap %s exhausted after %d lines / %d bytes / %v, releasing stream without overload probing", exhaustedReason, bufferedFrames, bufferedBytes, nowCodexBootstrap().Sub(bootstrapStart))
				initialChunks = nil
				streamStarted = true
				break
			}
			var translatedLine []byte
			isHandshake := false
			terminalSuccess := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); ok {
				translatedLine = transformed
				isHandshake = true
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				observeCodexTokenEvent(reporter, data)
				translatedLine = append([]byte("data: "), data...)
				eventType := ojson.GetBytesNoCopy(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					closeBootstrapBody()
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						return nil, errClearReplay
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					if isCodexOverloadBootstrapFailure(terminalBody) {
						timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
						timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
						if !timeoutReached {
							// Transient capacity rejection smuggled into an HTTP 200 stream. Fail the
							// attempt before the downstream headers are committed so the conductor can
							// transparently retry on another credential, and report the status the
							// upstream refused to put on the wire.
							helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d buffered lines, failing over", bufferedFrames)
							return nil, newCodexBootstrapOverloadErr(terminalBody)
						}
						helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d lines / %v, time budget exhausted; delivering in-stream", bufferedFrames, timeSinceStart)
					}
					bootstrapTerminalErr = streamErr
					break
				}
				if helps.HasMeaningfulCodexOutputDelta(data) {
					sawOutputDelta = true
				}
				if helps.IsCodexTerminalEmptyIncomplete(data, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
					closeBootstrapBody()
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					// Not an overload rejection, so it keeps its in-stream delivery: flush what is
					// held and hand the error downstream, matching the websocket executor and the
					// contract that only overload and rate-limit rejections fail the attempt over.
					// The conductor commits a stream once it has seen a payload, so this only takes
					// effect for downstream formats that render the held frames into a chunk.
					bootstrapTerminalErr = streamErr
					break
				}
				if isCodexBootstrapBufferableEvent(eventType, data) {
					isHandshake = true
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete", "response.done":
					terminalSuccess = true
					immediateIncomplete = eventType == "response.incomplete"
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsageWithOutputItems(data, outputItemsByIndex, outputItemsFallback); ok {
						reporter.Publish(ctx, detail)
					} else {
						reporter.EnsurePublished(ctx)
					}
					publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, data, outputItemsByIndex, outputItemsFallback)
					if !preserveNativeOutput {
						data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					}
					if eventType == "response.completed" || eventType == "response.done" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			} else {
				// Lines that are not data: frames - SSE comments, event:, id:, retry: and the blank
				// separator - carry no event to check against the allow-list, so they are held with
				// the frame they belong to. They spend the same budgets.
				// Non-data lines also need owned storage before translation can retain them.
				translatedLine = bytes.Clone(line)
				isHandshake = true
			}

			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			if isHandshake && !terminalSuccess {
				rawLineCount := scanner.RawLines()
				frameBytes := scanner.RawBytes()
				for i := range chunks {
					frameBytes += len(chunks[i])
				}
				timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
				timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
				totalFrames := bufferedFrames + scanner.HeldLines() + rawLineCount
				totalBytes := bufferedBytes + scanner.HeldBytes() + frameBytes
				if !timeoutReached && totalFrames <= codexBootstrapMaxBufferedFrames && totalBytes <= codexBootstrapMaxBufferedBytes {
					bufferedFrames += rawLineCount
					bufferedBytes += frameBytes
					bufferedChunks = append(bufferedChunks, chunks...)
					continue
				}
				exhausted := "frame budget"
				if timeoutReached {
					exhausted = "time budget"
				} else if totalFrames > codexBootstrapMaxBufferedFrames {
					exhausted = "frame budget"
				} else {
					exhausted = "byte budget"
				}
				helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap %s exhausted after %d lines / %d bytes / %v, releasing stream without overload probing", exhausted, bufferedFrames, bufferedBytes, timeSinceStart)
			}

			initialChunks = chunks
			streamStarted = true
			if terminalSuccess {
				immediateTerminal = true
			}
			break
		}
		scanner.SetBudgetChecker(nil)

		if !streamStarted && bootstrapTerminalErr == nil {
			closeBootstrapBody()
			if errScan := scanner.Err(); errScan != nil {
				// A cancelled downstream request must not be recorded as an upstream failure or
				// penalise the credential; mirror the unbuffered goroutine's guard.
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				return nil, errScan
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if len(bufferedChunks) == 0 && len(initialChunks) == 0 {
				emptyErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before first payload"}
				helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
				reporter.PublishFailure(ctx, emptyErr)
				closedCh := make(chan cliproxyexecutor.StreamChunk)
				close(closedCh)
				return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: closedCh}, nil
			}
			streamErr := newCodexIncompleteStreamError()
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			return nil, streamErr
		}
	}

	chanCapacity := len(bufferedChunks) + len(initialChunks)
	if bootstrapTerminalErr != nil || (immediateIncomplete && len(initialChunks) == 0) {
		chanCapacity++
	}
	out := make(chan cliproxyexecutor.StreamChunk, chanCapacity)
	for _, chunk := range bufferedChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	for _, chunk := range initialChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk, Incomplete: immediateIncomplete}
	}
	if immediateIncomplete && len(initialChunks) == 0 {
		out <- cliproxyexecutor.StreamChunk{Incomplete: true}
	}
	if bootstrapTerminalErr != nil {
		// Buffered handshake payloads are flushed first so the conductor observes a committed
		// stream and delivers this failure in-stream, exactly as the unbuffered path would.
		out <- cliproxyexecutor.StreamChunk{Err: bootstrapTerminalErr}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}
	if immediateTerminal {
		closeBootstrapBody()
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}

	emittedCount := 0
	for _, chunk := range bufferedChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	for _, chunk := range initialChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		for scanner.Scan() {
			line := scanner.Bytes()
			var translatedLine []byte
			terminalSuccess := false
			isIncompleteEvent := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); ok {
				translatedLine = transformed
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				observeCodexTokenEvent(reporter, data)
				translatedLine = append([]byte("data: "), data...)
				eventType := ojson.GetBytesNoCopy(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: errClearReplay}:
						case <-ctx.Done():
						}
						return
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				if helps.HasMeaningfulCodexOutputDelta(data) {
					sawOutputDelta = true
				}
				if helps.IsCodexTerminalEmptyIncomplete(data, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete", "response.done":
					terminalSuccess = true
					isIncompleteEvent = eventType == "response.incomplete"
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsageWithOutputItems(data, outputItemsByIndex, outputItemsFallback); ok {
						reporter.Publish(ctx, detail)
					} else {
						reporter.EnsurePublished(ctx)
					}
					publishCodexImageToolUsageWithOutputItems(ctx, reporter, body, data, outputItemsByIndex, outputItemsFallback)
					if !preserveNativeOutput {
						data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					}
					if eventType == "response.completed" || eventType == "response.done" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			} else {
				translatedLine = bytes.Clone(line)
			}

			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i], Incomplete: isIncompleteEvent}:
					if len(chunks[i]) > 0 {
						emittedCount++
					}
				case <-ctx.Done():
					return
				}
			}
			if terminalSuccess {
				if isIncompleteEvent && len(chunks) == 0 {
					select {
					case out <- cliproxyexecutor.StreamChunk{Incomplete: true}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			if ctx.Err() != nil {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		}
		if emittedCount == 0 {
			emptyErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before first payload"}
			helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
			reporter.PublishFailure(ctx, emptyErr)
			return
		}
		streamErr := newCodexIncompleteStreamError()
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
