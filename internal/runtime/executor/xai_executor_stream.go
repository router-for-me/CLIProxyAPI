package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
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
		defer streamUsage.Publish(ctx, reporter)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
		namespaceRestorer := newXAINamespaceRestorer(prepared.namespaceTools)
		var pendingEventLine []byte
		emitTranslatedLine := func(translatedLine []byte) bool {
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
					case "response.completed", "response.incomplete", "response.done":
						// response.done is a legitimate terminal on xAI Responses SSE.
						// Promote only a clean success; incomplete stays incomplete;
						// failed, cancelled, or error-bearing events stay response.done.
						terminalType := xaiDoneTerminalType(eventData)
						if terminalType != "response.done" {
							eventData = xaiSetTerminalEventType(eventData, terminalType)
							// Reconstruct only after the bridge has restored dispatcher children.
							eventData = xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
							eventData = xaiNormalizeReasoningSummaryData(eventData)
							if terminalType == "response.completed" {
								// Only completed responses carry replayable terminal state.
								cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, eventData)
							}
							ending := line[len(bytes.TrimRight(line, "\r\n")):]
							line = append(append([]byte("data: "), eventData...), ending...)
						}
					}
				}
				chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, line, &param, claudeInputTokens)...)
			}
			helps.RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, &streamUsage, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			if helps.StopApplyPatchStreamWithUsage(ctx, param, reporter, &streamUsage, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return false
			}
			if errBridge != nil {
				errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				streamUsage.PublishFailure(ctx, reporter, errBridge)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errBridge}:
				case <-ctx.Done():
				}
				return false
			}
			return true
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if bytes.HasPrefix(line, xaiEventTag) {
				if pendingEventLine != nil && !emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")) {
					return
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}

			if bytes.HasPrefix(line, xaiDataTag) {
				eventDataList := xaiNormalizeReasoningSummaryDataEvents(bytes.TrimSpace(line[len(xaiDataTag):]))
				hasPendingEventLine := pendingEventLine != nil
				for i, eventData := range eventDataList {
					// A failed, cancelled, or error-bearing response.done is not a
					// usage source. Clean done events are classified as completed
					// or incomplete before they are observed.
					if terminalType := xaiDoneTerminalType(eventData); terminalType == "response.output_item.done" || terminalType == "response.completed" || terminalType == "response.incomplete" {
						streamUsage.ObserveResponsesStream(eventData)
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
					// Keep the event: line aligned with the data-line rewrite in
					// emitTranslatedLine. Failures stay response.done.
					normalizedEventName := xaiDoneTerminalType(eventData)

					if hasPendingEventLine {
						eventLine := []byte("event: " + normalizedEventName)
						if i == 0 {
							eventLine = xaiNormalizeReasoningSummaryEventLine(pendingEventLine, normalizedEventName)
							pendingEventLine = nil
						}
						if !emitTranslatedLine(eventLine) {
							return
						}
					}
					if !emitTranslatedLine(append([]byte("data: "), eventData...)) {
						return
					}
				}
				continue
			}

			if pendingEventLine != nil {
				if !emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")) {
					return
				}
				pendingEventLine = nil
			}
			if !emitTranslatedLine(bytes.Clone(line)) {
				return
			}
		}
		if pendingEventLine != nil {
			emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, ""))
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
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// xaiDoneTerminalType resolves the downstream terminal type for a response.done
// event. Only a clean success (no error, and status empty or completed) is
// promoted to response.completed. Incomplete stays response.incomplete. Failed,
// cancelled, or error-bearing payloads keep response.done so nothing downstream
// can report a fabricated success or replay a failed turn.
func xaiDoneTerminalType(eventData []byte) string {
	eventType := ojson.GetBytesNoCopy(eventData, "type").String()
	if eventType != "response.done" {
		return eventType
	}
	if xaiTerminalHasError(eventData) {
		return "response.done"
	}
	switch strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(eventData, "response.status").String())) {
	case "", "completed":
		return "response.completed"
	case "incomplete":
		return "response.incomplete"
	default:
		return "response.done"
	}
}

// xaiTerminalHasError reports whether a terminal event carries a non-empty
// error. An explicit JSON null, empty object, or empty array is not an error.
func xaiTerminalHasError(eventData []byte) bool {
	for _, path := range []string{"response.error", "error"} {
		node := ojson.GetBytesNoCopy(eventData, path)
		if !node.Exists() || node.Type == ojson.Null {
			continue
		}
		switch node.Type {
		case ojson.JSON:
			if node.IsObject() && len(node.Map()) > 0 {
				return true
			}
			if node.IsArray() && len(node.Array()) > 0 {
				return true
			}
		case ojson.String:
			if strings.TrimSpace(node.String()) != "" {
				return true
			}
		default:
			if strings.TrimSpace(node.Raw) != "" {
				return true
			}
		}
	}
	return false
}

func xaiSetTerminalEventType(eventData []byte, eventType string) []byte {
	if eventType == "" || ojson.GetBytesNoCopy(eventData, "type").String() == eventType {
		return eventData
	}
	updated, err := ojson.SetBytes(eventData, "type", eventType)
	if err != nil || len(updated) == 0 {
		return eventData
	}
	return updated
}

// xaiTerminalFailurePayload unwraps a failure response.done without rewriting
// its status or error. Callers must not treat this payload as a success.
func xaiTerminalFailurePayload(eventData []byte) []byte {
	response := ojson.GetBytes(eventData, "response")
	if response.Exists() && response.IsObject() {
		return []byte(response.Raw)
	}
	return append([]byte(nil), eventData...)
}
