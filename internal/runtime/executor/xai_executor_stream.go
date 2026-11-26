package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *XAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if xaiIsSpeechRequest(opts) {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /audio/speech"}
	}
	if xaiInputHasItemType(req.Payload, "compaction_trigger") {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequestDoc(ctx, req, opts, true)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	doc := prepared.ensureDoc()
	sanitizeXAIInputEncryptedContentForBaseURLDoc(doc, baseURL)
	helps.FinalizePayloadDoc(helps.WithPayloadFinalizer(ctx, prepared.finalizePayload), doc)
	prepared.body = doc.Bytes()
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	applyXAIChatHeaders(httpReq, auth, token, true, prepared.sessionID, opts.Headers)
	e.recordXAIRequest(ctx, auth, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return nil, xaiStatusErr(httpResp.StatusCode, data)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("xai executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var streamUsage helps.StreamUsageBuffer
		var terminalDelivered bool
		defer func() {
			if errCtx := ctx.Err(); errCtx != nil && !terminalDelivered {
				if !streamUsage.PublishFailure(ctx, reporter, errCtx) {
					reporter.PublishFailure(ctx, errCtx)
				}
				return
			}
			streamUsage.Publish(ctx, reporter)
		}()
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
		namespaceRestorer := newXAINamespaceRestorer(prepared.namespaceTools)
		var pendingEventLine []byte
		emitTranslatedLine := func(translatedLine []byte) (int, bool) {
			lines, errBridge := prepared.applyPatch.Stream(translatedLine)
			if errBridge != nil {
				streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			var chunks [][]byte
			for _, line := range lines {
				if bytes.HasPrefix(line, xaiDataTag) {
					eventData := bytes.TrimSpace(line[len(xaiDataTag):])
					switch ojson.GetBytesNoCopy(eventData, "type").String() {
					case "response.output_item.done":
						xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
					case "response.completed", "response.incomplete":
						// Reconstruct only after the bridge has restored dispatcher children.
						eventData = xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
						eventData = xaiNormalizeReasoningSummaryData(eventData)
						if ojson.GetBytesNoCopy(eventData, "type").String() == "response.completed" {
							// Only completed responses carry replayable terminal state.
							cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, eventData)
						}
						ending := line[len(bytes.TrimRight(line, "\r\n")):]
						line = append(append([]byte("data: "), eventData...), ending...)
					}
				}
				chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, line, &param, claudeInputTokens)...)
			}
			helps.RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, &streamUsage, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return 0, false
				}
			}
			if helps.StopApplyPatchStreamWithUsage(ctx, param, reporter, &streamUsage, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return 0, false
			}
			if errBridge != nil {
				errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				streamUsage.PublishFailure(ctx, reporter, errBridge)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errBridge}:
				case <-ctx.Done():
				}
				return 0, false
			}
			return len(chunks), true
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if bytes.HasPrefix(line, xaiEventTag) {
				if pendingEventLine != nil {
					if _, okEmit := emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")); !okEmit {
						return
					}
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}

			if bytes.HasPrefix(line, xaiDataTag) {
				eventDataList := xaiNormalizeReasoningSummaryDataEvents(bytes.TrimSpace(line[len(xaiDataTag):]))
				hasPendingEventLine := pendingEventLine != nil
				var pendingEventName string
				if hasPendingEventLine && bytes.HasPrefix(pendingEventLine, xaiEventTag) {
					pendingEventName = strings.TrimSpace(string(pendingEventLine[len(xaiEventTag):]))
				}
				for i, eventData := range eventDataList {
					if rawEventType := ojson.GetBytesNoCopy(eventData, "type").String(); rawEventType == "response.output_item.done" || rawEventType == "response.completed" || rawEventType == "response.incomplete" || rawEventType == "response.done" {
						streamUsage.ObserveResponsesStream(eventData)
					}
					if errTerminal, okFail := xaiTerminalFailureErr(pendingEventName, eventData); okFail {
						helps.RecordAPIResponseError(ctx, e.cfg, errTerminal)
						if !streamUsage.PublishFailure(ctx, reporter, errTerminal) {
							reporter.PublishFailure(ctx, errTerminal)
						}
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: errTerminal}:
						case <-ctx.Done():
						}
						return
					}
					prepared.applyPatch.RememberDispatcherEvent(eventData)
					eventData = namespaceRestorer.restore(eventData)
					if prepared.webSearchAlias != "" {
						eventData = restoreXAIClientWebSearchName(eventData, prepared.webSearchAlias)
					}
					eventData = responseFilter.apply(eventData)
					if len(eventData) == 0 {
						if hasPendingEventLine && i == 0 {
							pendingEventLine = nil
						}
						continue
					}
					reporter.ObserveResponseModel(eventData)
					normalizedEventName := ojson.GetBytesNoCopy(eventData, "type").String()
					if normalizedEventName == "response.done" && prepared.responseFormat != sdktranslator.FormatOpenAIResponse {
						normalizedEventName, eventData = normalizeMetaTerminalEventForTranslation(eventData, normalizedEventName)
					}

					if hasPendingEventLine {
						eventLine := []byte("event: " + normalizedEventName)
						if i == 0 {
							eventLine = xaiNormalizeReasoningSummaryEventLine(pendingEventLine, normalizedEventName)
							pendingEventLine = nil
						}
						if _, okEmit := emitTranslatedLine(eventLine); !okEmit {
							return
						}
					}
					emitted, okEmit := emitTranslatedLine(append([]byte("data: "), eventData...))
					if !okEmit {
						return
					}
					if emitted > 0 && (normalizedEventName == "response.completed" || normalizedEventName == "response.incomplete" || normalizedEventName == "response.done") {
						terminalDelivered = true
					}
				}
				continue
			}

			if pendingEventLine != nil {
				if _, okEmit := emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")); !okEmit {
					return
				}
				pendingEventLine = nil
			}
			if _, okEmit := emitTranslatedLine(bytes.Clone(line)); !okEmit {
				return
			}
		}
		if pendingEventLine != nil {
			if _, okEmit := emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")); !okEmit {
				return
			}
		}
		finishEvents, errFinish := prepared.applyPatch.FinishStream()
		if errFinish != nil {
			streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
		}
		for _, event := range finishEvents {
			for _, chunk := range helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, event, &param, claudeInputTokens) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errFinish != nil {
			errFinish = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			streamUsage.PublishFailure(ctx, reporter, errFinish)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errFinish}:
			case <-ctx.Done():
			}
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			if terminalDelivered && errors.Is(errScan, context.Canceled) {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			if !streamUsage.PublishFailure(ctx, reporter, errScan) {
				reporter.PublishFailure(ctx, errScan)
			}
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		if ctx.Err() != nil || terminalDelivered {
			return
		}
		truncErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before terminal event", streamTruncated: true}
		helps.RecordAPIResponseError(ctx, e.cfg, truncErr)
		if !streamUsage.PublishFailure(ctx, reporter, truncErr) {
			reporter.PublishFailure(ctx, truncErr)
		}
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: truncErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
