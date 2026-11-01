package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *MetaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, "meta"); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeMetaCompactionStream(ctx, auth, req, opts)
	}

	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return nil, errAuth
	}

	prepared, errPrepare := e.prepareResponsesRequest(ctx, enriched, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	prepared.replay.RecordRequest(ctx, prepared.baseModel, enriched.ID)

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, enriched)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	baseURL, token := metaCreds(enriched)
	if strings.TrimSpace(baseURL) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing provider baseURL"}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var httpResp *http.Response
	for {
		httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
		if errRequest != nil {
			return nil, errRequest
		}
		applyMetaAPIHeaders(httpReq, enriched, token, true, opts.Headers)
		e.recordMetaRequest(ctx, enriched, url, httpReq.Header.Clone(), prepared.body)

		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
		httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
		var errDo error
		httpResp, errDo = httpClient.Do(httpReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			return nil, errDo
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			if prepared.canRetryWithoutReplay() && metaStreamBootstrapRetryWithoutReplay(ctx, prepared, httpResp) {
				continue
			}
			break
		}
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		if !prepared.retryWithoutReplay(ctx, httpResp.StatusCode, data) {
			return nil, wrapMetaUpstreamError(httpResp.StatusCode, data)
		}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("meta executor: close response body error: %v", errClose)
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
		var terminalDelivered bool
		emitTranslatedLine := func(translatedLine []byte) bool {
			lines, errBridge := prepared.applyPatch.Stream(translatedLine)
			if errBridge != nil {
				streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			var chunks [][]byte
			for _, line := range lines {
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
		var pendingEventLine []byte
		var filteringSubscriptionUsage bool
		var skipFilteredBlankLine bool
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmedLine := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmedLine, metaEventTag) {
				eventName := bytes.TrimSpace(trimmedLine[len(metaEventTag):])
				if bytes.Equal(eventName, []byte(metaSubscriptionUsageEventType)) {
					if pendingEventLine != nil {
						if !emitTranslatedLine(pendingEventLine) {
							return
						}
						pendingEventLine = nil
					}
					filteringSubscriptionUsage = true
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}
			if !bytes.HasPrefix(trimmedLine, dataTag) {
				if len(trimmedLine) == 0 {
					filteringSubscriptionUsage = false
					if skipFilteredBlankLine {
						skipFilteredBlankLine = false
						continue
					}
				}
				if filteringSubscriptionUsage {
					continue
				}
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
					pendingEventLine = nil
				}
				if !emitTranslatedLine(bytes.Clone(line)) {
					return
				}
				continue
			}
			eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
			eventType := gjson.GetBytes(eventData, "type").String()
			if isMetaSubscriptionUsageEvent(eventType, eventData) {
				pendingEventLine = nil
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = true
				continue
			}
			if filteringSubscriptionUsage {
				if eventType == "" {
					pendingEventLine = nil
					if gjson.ValidBytes(eventData) {
						filteringSubscriptionUsage = false
					}
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
			}
			skipFilteredBlankLine = false
			// Observe upstream content before translation or failure publication.
			helps.ObserveResponsesTokenEvent(reporter, eventData)
			if errEvent := metaStreamEventError(eventData); errEvent != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errEvent)
				reporter.PublishFailure(ctx, errEvent)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errEvent}:
				case <-ctx.Done():
				}
				return
			}
			eventData = prepared.replay.TagResponseEvent(eventData)
			switch eventType {
			case "response.output_item.done":
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.completed", "response.incomplete":
				if detail, ok := helps.ParseCodexUsageWithOutputItems(eventData, outputItemsByIndex, outputItemsFallback); ok {
					streamUsage.Observe(detail, true)
				}
				eventData = patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			}
			if pendingEventLine != nil {
				if !emitTranslatedLine(pendingEventLine) {
					return
				}
				pendingEventLine = nil
			}
			if !emitTranslatedLine(append([]byte("data: "), eventData...)) {
				return
			}
			if eventType == "response.completed" || eventType == "response.incomplete" {
				terminalDelivered = true
			}
		}
		if pendingEventLine != nil {
			if !emitTranslatedLine(pendingEventLine) {
				return
			}
			pendingEventLine = nil
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
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// metaRewoundStreamBody replays bytes already read from an upstream stream body
// back to the streaming scanner, so a bootstrap peek consumes nothing.
type metaRewoundStreamBody struct {
	io.Reader
	io.Closer
}

// metaStreamBootstrapRetryWithoutReplay inspects the first SSE data event of a
// 200 response before any chunk reaches the client. When Meta rejected the
// replayed envelopes as an error event instead of an HTTP error, the request
// is retried once without reasoning, exactly like the non-streaming path.
// Anything already read is rewound onto the response body for the streaming
// scanner. It reports whether the caller should resend the request.
func metaStreamBootstrapRetryWithoutReplay(ctx context.Context, prepared *metaPreparedRequest, httpResp *http.Response) bool {
	reader := bufio.NewReader(httpResp.Body)
	var peeked bytes.Buffer
	retry := false
	for {
		line, errRead := reader.ReadBytes('\n')
		peeked.Write(line)
		if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, dataTag) {
			eventData := bytes.TrimSpace(trimmed[len(dataTag):])
			if isMetaSubscriptionUsageEvent(gjson.GetBytes(eventData, "type").String(), eventData) {
				if errRead != nil {
					break
				}
				continue
			}
			var upstreamErr statusErr
			if errEvent := metaStreamEventError(eventData); errEvent != nil &&
				errors.As(errEvent, &upstreamErr) &&
				prepared.retryWithoutReplay(ctx, upstreamErr.code, []byte(upstreamErr.msg)) {
				retry = true
			}
			break
		}
		if errRead != nil {
			break
		}
	}
	if retry {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
		return true
	}
	httpResp.Body = &metaRewoundStreamBody{Reader: io.MultiReader(bytes.NewReader(peeked.Bytes()), reader), Closer: httpResp.Body}
	return false
}
