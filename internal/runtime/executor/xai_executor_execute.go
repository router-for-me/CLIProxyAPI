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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *XAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if endpointPath := xaiImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}
	if xaiIsVideoRequest(opts) {
		return e.executeVideos(ctx, auth, req, opts)
	}
	if xaiIsSpeechRequest(opts) {
		return e.executeSpeech(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequestDoc(ctx, req, opts, true)
	if err != nil {
		return resp, err
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
		return resp, err
	}
	applyXAIChatHeaders(httpReq, auth, token, true, prepared.sessionID, opts.Headers)
	e.recordXAIRequest(ctx, auth, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return resp, errRead
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, xaiStatusErr(httpResp.StatusCode, data)
	}

	data, err := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
	namespaceRestorer := newXAINamespaceRestorer(prepared.namespaceTools)
	var upstreamUsage helps.StreamUsageBuffer
	events := helps.NewCodexSSEScanner(ctx, bytes.NewReader(data), len(data)+1)
	sawSSEData := false
	var pendingEventName string
	var pendingData []byte
	hasPendingData := false

	dispatchSSEEvent := func(eventName string, rawEventData []byte) (cliproxyexecutor.Response, bool, error) {
		rawEventData = bytes.TrimSpace(rawEventData)
		if len(rawEventData) == 0 || bytes.Equal(rawEventData, []byte("[DONE]")) {
			return cliproxyexecutor.Response{}, false, nil
		}
		sawSSEData = true
		if !ojson.ValidBytes(rawEventData) {
			if errTerminal, ok := xaiTerminalFailureErr(eventName, rawEventData); ok {
				upstreamUsage.PublishFailure(ctx, reporter, errTerminal)
				return cliproxyexecutor.Response{}, true, errTerminal
			}
			return cliproxyexecutor.Response{}, false, nil
		}
		eventData := xaiNormalizeReasoningSummaryData(rawEventData)
		upstreamUsage.ObserveResponsesStream(eventData)
		reporter.ObserveResponseModel(eventData)
		if errTerminal, ok := xaiTerminalFailureErr(eventName, eventData); ok {
			upstreamUsage.PublishFailure(ctx, reporter, errTerminal)
			return cliproxyexecutor.Response{}, true, errTerminal
		}
		prepared.applyPatch.RememberDispatcherEvent(eventData)
		eventData = namespaceRestorer.restore(eventData)
		if prepared.webSearchAlias != "" {
			eventData = restoreXAIClientWebSearchName(eventData, prepared.webSearchAlias)
		}
		eventData = responseFilter.apply(eventData)
		if len(eventData) == 0 {
			return cliproxyexecutor.Response{}, false, nil
		}
		transformedEvents, errBridge := prepared.applyPatch.Transform(eventData)
		if errBridge != nil {
			errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			upstreamUsage.PublishFailure(ctx, reporter, errBridge)
			return cliproxyexecutor.Response{}, true, errBridge
		}
		for _, eventData := range transformedEvents {
			reporter.ObserveResponseModel(eventData)
			eventType := ojson.GetBytesNoCopy(eventData, "type").String()
			switch eventType {
			case "response.output_item.done":
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.completed", "response.incomplete":
				completedData := xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				completedData = xaiNormalizeReasoningSummaryData(completedData)
				if eventType == "response.completed" {
					// A truncated turn carries no replayable terminal state, so only a
					// completed response may refresh the reasoning replay cache.
					cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, completedData)
				}
				var param any
				out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
				if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
					errTranslation := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
					upstreamUsage.PublishFailure(ctx, reporter, errTranslation)
					return cliproxyexecutor.Response{}, true, errTranslation
				}
				if !upstreamUsage.Publish(ctx, reporter) {
					if detail, ok := helps.ParseCodexUsageWithOutputItems(completedData, outputItemsByIndex, outputItemsFallback); ok {
						reporter.Publish(ctx, detail)
					}
				}
				if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
					out = helps.EnsureResponsesUsageDetails(out)
				}
				return cliproxyexecutor.Response{
					Payload:    out,
					Incomplete: eventType == "response.incomplete",
					Headers:    httpResp.Header.Clone(),
				}, true, nil
			}
		}
		return cliproxyexecutor.Response{}, false, nil
	}

	flushPendingEvent := func() (cliproxyexecutor.Response, bool, error) {
		if !hasPendingData {
			pendingEventName = ""
			pendingData = nil
			return cliproxyexecutor.Response{}, false, nil
		}
		eventName := pendingEventName
		eventPayload := pendingData
		pendingEventName = ""
		pendingData = nil
		hasPendingData = false
		return dispatchSSEEvent(eventName, eventPayload)
	}

	for events.Scan() {
		line := bytes.TrimRight(events.Bytes(), "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			if outResp, done, errDispatch := flushPendingEvent(); done {
				return outResp, errDispatch
			}
			continue
		}
		if bytes.HasPrefix(line, xaiEventTag) {
			nextEventName := strings.TrimSpace(string(line[len(xaiEventTag):]))
			if hasPendingData && pendingEventName != "" {
				if outResp, done, errDispatch := flushPendingEvent(); done {
					return outResp, errDispatch
				}
			}
			pendingEventName = nextEventName
			continue
		}
		if !bytes.HasPrefix(line, xaiDataTag) {
			continue
		}
		rawEventData := bytes.TrimSpace(line[len(xaiDataTag):])
		if hasPendingData {
			if ojson.ValidBytes(pendingData) || bytes.Equal(pendingData, []byte("[DONE]")) {
				if outResp, done, errDispatch := flushPendingEvent(); done {
					return outResp, errDispatch
				}
			} else {
				pendingData = append(append(pendingData, '\n'), rawEventData...)
				continue
			}
		}
		pendingData = bytes.Clone(rawEventData)
		hasPendingData = true
	}
	if outResp, done, errDispatch := flushPendingEvent(); done {
		return outResp, errDispatch
	}

	if !sawSSEData {
		if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
			if errTerminal, ok := xaiTerminalFailureErr("", trimmed); ok {
				upstreamUsage.PublishFailure(ctx, reporter, errTerminal)
				return resp, errTerminal
			}
		}
	}
	if prepared.applyPatch.HasPatchActivity() {
		if errFinish := prepared.applyPatch.Finish(); errFinish != nil {
			errFinish = xaiRequestScopedStreamError{statusErr: statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}}
			upstreamUsage.PublishFailure(ctx, reporter, errFinish)
			return resp, errFinish
		}
	}
	if !sawSSEData {
		errEmpty := newXAIEmptyStreamError()
		upstreamUsage.PublishFailure(ctx, reporter, errEmpty)
		return resp, errEmpty
	}
	errIncomplete := newXAIIncompleteStreamError()
	upstreamUsage.PublishFailure(ctx, reporter, errIncomplete)
	return resp, errIncomplete
}

func (e *XAIExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, data, headers, reporter, errCompactRequest := e.executeCompactRequest(ctx, auth, req, opts)
	if errCompactRequest != nil {
		return resp, errCompactRequest
	}

	defer reporter.TrackFailure(ctx, &err)
	upstreamUsage := helps.ParseOpenAIUsage(data)
	converted, errBridge := prepared.applyPatch.Bridge.TransformNonStream(data)
	if errBridge != nil {
		errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		reporter.PublishFailureWithDetail(ctx, upstreamUsage, errBridge)
		return resp, errBridge
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, converted, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		errTranslation := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		reporter.PublishFailureWithDetail(ctx, upstreamUsage, errTranslation)
		return cliproxyexecutor.Response{}, errTranslation
	}
	reporter.Publish(ctx, upstreamUsage)
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: headers}, nil
}

func (e *XAIExecutor) executeCompactRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*xaiPreparedRequest, []byte, http.Header, *helps.UsageReporter, error) {
	token, _ := xaiCreds(auth)
	// Compact must not use xaiChatBaseURL: CLI chat-proxy returns 404 for
	// /responses/compact and a 404 cools down the whole xAI auth pool.
	baseURL := xaiCompactBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequestToDoc(ctx, req, opts, false, sdktranslator.FormatOpenAIResponse)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	doc := prepared.ensureDoc()
	_ = doc.Delete("stream")
	_ = doc.Delete("tools")
	// Compact deletes tools after prepareResponsesRequestTo, which can now keep
	// image_generation and rewrite its forced choice to "required" on grok-4.6+.
	// Drop the leftover selection so compact does not send tool_choice without tools.
	normalizeXAIToolChoiceForToolsDoc(doc)
	for _, field := range []string{"max_output_tokens", "temperature", "top_p", "top_k", "stop"} {
		_ = doc.Delete(field)
	}
	xaiRemoveInputItemsByTypeDoc(doc, "compaction_trigger")
	if previousResponseID := strings.TrimSpace(ojson.GetBytesNoCopy(req.Payload, "previous_response_id").String()); previousResponseID != "" {
		_ = doc.Set("previous_response_id", previousResponseID)
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	requestURL := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	sanitizeXAIInputEncryptedContentForBaseURLDoc(doc, baseURL)
	helps.FinalizePayloadDoc(helps.WithPayloadFinalizer(ctx, prepared.finalizePayload), doc)
	prepared.body = doc.Bytes()
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Official API / custom compact endpoints use standard API headers, not CLI
	// chat-proxy identity headers (which applyXAIChatHeaders may still attach for OAuth chat).
	applyXAIHeaders(httpReq, auth, token, false, prepared.sessionID, opts.Headers)
	e.recordXAIRequest(ctx, auth, requestURL, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, nil, nil, nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, nil, nil, nil, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = xaiStatusErr(httpResp.StatusCode, data)
		return nil, nil, nil, nil, err
	}

	reporter.ObserveResponseModel(data)
	clearXAIReasoningReplayAfterCompaction(ctx, prepared.replayScope)
	return prepared, data, httpResp.Header.Clone(), reporter, nil
}

func (e *XAIExecutor) executeCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	prepared, data, headers, reporter, errCompactRequest := e.executeCompactRequest(ctx, auth, req, opts)
	if errCompactRequest != nil {
		return nil, errCompactRequest
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	headers = headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")

	chunks := xaiBuildCompactionTriggerStreamChunks(prepared, data)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func xaiInputHasItemType(body []byte, itemType string) bool {
	input := ojson.GetBytesNoCopy(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			return true
		}
	}
	return false
}

func xaiRemoveInputItemsByType(body []byte, itemType string) []byte {
	doc := ojson.NewDoc(body)
	if !xaiRemoveInputItemsByTypeDoc(doc, itemType) {
		return body
	}
	return doc.Bytes()
}

func xaiRemoveInputItemsByTypeDoc(doc *ojson.Doc, itemType string) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	kept := 0
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			continue
		}
		if kept > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(item.Raw)
		kept++
	}
	buf.WriteByte(']')

	return doc.SetRawBytes("input", buf.Bytes()) == nil
}

func xaiBuildCompactionTriggerStreamChunks(prepared *xaiPreparedRequest, compactData []byte) [][]byte {
	responseID := xaiCompactionResponseID(compactData)
	now := time.Now().Unix()
	createdAt := ojson.GetBytesNoCopy(compactData, "created_at").Int()
	if createdAt == 0 {
		createdAt = now
	}
	completedAt := ojson.GetBytesNoCopy(compactData, "completed_at").Int()
	if completedAt == 0 {
		completedAt = now
	}

	item := xaiCompactionOutputItem(compactData, responseID)
	output := make([]byte, 0, len(item)+2)
	output = append(output, '[')
	output = append(output, item...)
	output = append(output, ']')

	createdResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "in_progress")
	inProgressResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "in_progress")
	completedResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "completed")
	requestModelName := ""
	if prepared != nil {
		requestModelName = ojson.GetBytesNoCopy(prepared.originalPayload, "model").String()
		if requestModelName == "" {
			requestModelName = prepared.baseModel
		}
	}
	if requestModelName == "" {
		requestModelName = ojson.GetBytesNoCopy(compactData, "model").String()
	}
	if requestModelName != "" {
		createdResponse, _ = ojson.SetBytes(createdResponse, "model", requestModelName)
		inProgressResponse, _ = ojson.SetBytes(inProgressResponse, "model", requestModelName)
	}
	completedDoc := ojson.NewDoc(completedResponse)
	_ = completedDoc.Set("completed_at", completedAt)
	_ = completedDoc.SetRawBytes("output", output)
	if usage := ojson.GetBytesNoCopy(compactData, "usage"); usage.Exists() {
		_ = completedDoc.SetRawBytes("usage", []byte(usage.Raw))
	}
	completedResponse = completedDoc.Bytes()

	createdPayload := []byte(`{"type":"response.created","sequence_number":0}`)
	createdPayload, _ = ojson.SetRawBytes(createdPayload, "response", createdResponse)
	inProgressPayload := []byte(`{"type":"response.in_progress","sequence_number":1}`)
	inProgressPayload, _ = ojson.SetRawBytes(inProgressPayload, "response", inProgressResponse)
	addedPayload := []byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	addedPayload, _ = ojson.SetRawBytes(addedPayload, "item", item)
	keepalivePayload := []byte(`{"type":"keepalive","sequence_number":3}`)
	donePayload := []byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0}`)
	donePayload, _ = ojson.SetRawBytes(donePayload, "item", item)
	completedPayload := []byte(`{"type":"response.completed","sequence_number":5}`)
	completedPayload, _ = ojson.SetRawBytes(completedPayload, "response", completedResponse)
	completedPayload = helps.EnsureResponsesUsageDetails(completedPayload)

	return [][]byte{
		xaiBuildSSEFrame("response.created", createdPayload),
		xaiBuildSSEFrame("response.in_progress", inProgressPayload),
		xaiBuildSSEFrame("response.output_item.added", addedPayload),
		xaiBuildSSEFrame("keepalive", keepalivePayload),
		xaiBuildSSEFrame("response.output_item.done", donePayload),
		xaiBuildSSEFrame("response.completed", completedPayload),
	}
}

func xaiBuildCompactionBaseResponse(prepared *xaiPreparedRequest, compactData []byte, responseID string, createdAt int64, status string) []byte {
	respDoc := ojson.NewDoc([]byte(`{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null,"incomplete_details":null,"output":[]}`))
	_ = respDoc.Set("id", responseID)
	_ = respDoc.Set("created_at", createdAt)
	_ = respDoc.Set("status", status)
	if model := ojson.GetBytesNoCopy(compactData, "model").String(); model != "" {
		_ = respDoc.Set("model", model)
	} else if prepared != nil && prepared.baseModel != "" {
		_ = respDoc.Set("model", prepared.baseModel)
	}

	if prepared == nil {
		return respDoc.Bytes()
	}
	for _, field := range []string{
		"instructions",
		"max_output_tokens",
		"max_tool_calls",
		"parallel_tool_calls",
		"previous_response_id",
		"prompt_cache_key",
		"reasoning",
		"text",
		"tool_choice",
		"tools",
		"top_logprobs",
		"top_p",
		"truncation",
		"user",
		"metadata",
	} {
		if value := ojson.GetBytesNoCopy(prepared.body, field); value.Exists() {
			_ = respDoc.SetRawBytes(field, []byte(value.Raw))
		}
	}
	return respDoc.Bytes()
}

func xaiCompactionOutputItem(compactData []byte, responseID string) []byte {
	itemResult := ojson.GetBytesNoCopy(compactData, "output.0")
	item := []byte(`{"type":"compaction"}`)
	if itemResult.Exists() && itemResult.Type == ojson.JSON {
		item = []byte(itemResult.Raw)
	}
	itemDoc := ojson.NewDoc(item)
	changed := false
	if !itemDoc.Get("type").Exists() {
		if itemDoc.Set("type", "compaction") == nil {
			changed = true
		}
	}
	if !itemDoc.Get("id").Exists() {
		if itemDoc.Set("id", xaiCompactionItemID(responseID)) == nil {
			changed = true
		}
	}
	if !changed {
		return item
	}
	return itemDoc.Bytes()
}

func xaiCompactionResponseID(compactData []byte) string {
	if responseID := strings.TrimSpace(ojson.GetBytesNoCopy(compactData, "id").String()); responseID != "" {
		if strings.HasPrefix(responseID, "resp_") {
			return responseID
		}
		return "resp_" + strings.TrimPrefix(responseID, "cmp_")
	}
	return fmt.Sprintf("resp_xai_compaction_%d", time.Now().UnixNano())
}

func xaiCompactionItemID(responseID string) string {
	if suffix := strings.TrimPrefix(responseID, "resp_"); suffix != "" && suffix != responseID {
		return "cmp_" + suffix
	}
	return "cmp_" + responseID
}

func xaiBuildSSEFrame(eventName string, data []byte) []byte {
	out := make([]byte, 0, len(eventName)+len(data)+16)
	out = append(out, "event: "...)
	out = append(out, eventName...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, data...)
	out = append(out, '\n', '\n')
	return out
}
