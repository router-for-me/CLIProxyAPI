package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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

type openAICompatResponsesRequest struct {
	ctx            context.Context
	secrets        []string
	baseModel      string
	responseFormat sdktranslator.Format
	to             sdktranslator.Format
	body           []byte
	trigger        bool
	reporter       *helps.UsageReporter
}

func (e *OpenAICompatExecutor) executeV1Responses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, scope string, secrets []string) (resp cliproxyexecutor.Response, err error) {
	prepared, httpResp, err := e.openV1Responses(ctx, auth, req, opts, scope, secrets, false)
	if err != nil {
		return resp, err
	}
	defer prepared.reporter.TrackFailure(prepared.ctx, &err)
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(prepared.ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(prepared.ctx, e.cfg, body)
	if prepared.trigger {
		var usage helps.StreamUsageBuffer
		completed, errCompleted := helps.CompletedV1ResponsesBody(body, &usage)
		if errCompleted != nil {
			usage.PublishFailure(prepared.ctx, prepared.reporter, errCompleted)
			helps.RecordAPIResponseError(prepared.ctx, e.cfg, errCompleted)
			return resp, errCompleted
		}
		prepared.reporter.ObserveResponseModel(completed)
		converted, errConvert := helps.ConvertResponsesCompactionResponse(completed, prepared.baseModel, scope, prepared.secrets)
		if errConvert != nil {
			usage.PublishFailure(prepared.ctx, prepared.reporter, errConvert)
			helps.RecordAPIResponseError(prepared.ctx, e.cfg, errConvert)
			return resp, errConvert
		}
		usage.Publish(prepared.ctx, prepared.reporter)
		prepared.reporter.EnsurePublished(prepared.ctx)
		return cliproxyexecutor.Response{Payload: helps.EnsureResponsesUsageDetails(converted), Headers: helps.ResponsesHeaders(httpResp.Header, false)}, nil
	}
	var usage helps.StreamUsageBuffer
	body, err = helps.CompletedV1ResponsesBody(body, &usage)
	if err != nil {
		usage.PublishFailure(prepared.ctx, prepared.reporter, err)
		helps.RecordAPIResponseError(prepared.ctx, e.cfg, err)
		return resp, err
	}
	prepared.reporter.ObserveResponseModel(body)
	var param any
	out := sdktranslator.TranslateNonStream(prepared.ctx, prepared.to, prepared.responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.body, body, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		errTranslate := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		usage.PublishFailure(prepared.ctx, prepared.reporter, errTranslate)
		return cliproxyexecutor.Response{}, errTranslate
	}
	usage.Publish(prepared.ctx, prepared.reporter)
	prepared.reporter.EnsurePublished(prepared.ctx)
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: helps.ResponsesHeaders(httpResp.Header, false)}, nil
}

func (e *OpenAICompatExecutor) executeV1ResponsesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, scope string, secrets []string) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, httpResp, err := e.openV1Responses(ctx, auth, req, opts, scope, secrets, true)
	if err != nil {
		return nil, err
	}
	defer prepared.reporter.TrackFailure(prepared.ctx, &err)
	if prepared.trigger {
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		body, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(prepared.ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(prepared.ctx, e.cfg, body)
		var usage helps.StreamUsageBuffer
		completed, errCompleted := helps.CompletedV1ResponsesBody(body, &usage)
		if errCompleted != nil {
			usage.PublishFailure(prepared.ctx, prepared.reporter, errCompleted)
			helps.RecordAPIResponseError(prepared.ctx, e.cfg, errCompleted)
			return nil, errCompleted
		}
		prepared.reporter.ObserveResponseModel(completed)
		converted, errConvert := helps.ConvertResponsesCompactionResponse(completed, prepared.baseModel, scope, prepared.secrets)
		if errConvert != nil {
			usage.PublishFailure(prepared.ctx, prepared.reporter, errConvert)
			helps.RecordAPIResponseError(prepared.ctx, e.cfg, errConvert)
			return nil, errConvert
		}
		usage.Publish(prepared.ctx, prepared.reporter)
		prepared.reporter.EnsurePublished(prepared.ctx)
		return helps.ResponsesCompactionStreamResult(prepared.ctx, cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}), nil
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go e.streamV1Responses(prepared, httpResp, out)
	return &cliproxyexecutor.StreamResult{Headers: helps.ResponsesHeaders(httpResp.Header, true), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) openV1Responses(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, scope string, secrets []string, downstreamStream bool) (prepared *openAICompatResponsesRequest, httpResp *http.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		return nil, nil, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FormatOpenAIResponse
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return nil, nil, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
	}
	isCompat := helps.APIKeyModelIsCompat(req)
	secretsForCapsules := append([]string(nil), secrets...)
	if strings.TrimSpace(apiKey) != "" {
		secretsForCapsules = append(secretsForCapsules, apiKey)
	}
	originalPayload, err = helps.ExpandResponsesCompactionCapsules(originalPayload, scope, secretsForCapsules)
	if err != nil {
		return nil, nil, err
	}
	requestPayload, err := helps.ExpandResponsesCompactionCapsules(req.Payload, scope, secretsForCapsules)
	if err != nil {
		return nil, nil, err
	}
	originalTranslated, body, updatesChanged := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, requestPayload, downstreamStream, isCompat)
	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return nil, nil, err
	}
	finalizePayload := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslated, req, opts)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	trigger := helps.HasResponsesCompactionTrigger(body)
	if trigger {
		for _, payload := range [][]byte{originalPayload, requestPayload, body} {
			if errValidate := helps.ValidateV1CompactionContext(ctx, payload); errValidate != nil {
				return nil, nil, errValidate
			}
		}
	}
	body = helps.SetBoolIfDifferent(body, "stream", downstreamStream || trigger)
	body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompat(ctx, "openai compat executor", body, isCompat)
	body, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, body)
	if err != nil {
		return nil, nil, err
	}
	if trigger {
		body = helps.PrepareV1CompactionPayload(body)
	}
	body = finalizePayload(body)
	if trigger {
		if errValidate := helps.ValidateV1CompactionContext(ctx, body); errValidate != nil {
			return nil, nil, errValidate
		}
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())
	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		return nil, nil, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	if downstreamStream || trigger {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
	} else {
		httpReq.Header.Set("Accept", "application/json")
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
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		upstreamError, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, upstreamError)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), upstreamError))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		return nil, nil, newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, upstreamError)
	}
	return &openAICompatResponsesRequest{
		ctx:            ctx,
		secrets:        secretsForCapsules,
		baseModel:      baseModel,
		responseFormat: responseFormat,
		to:             to,
		body:           body,
		trigger:        trigger,
		reporter:       reporter,
	}, httpResp, nil
}

func (e *OpenAICompatExecutor) streamV1Responses(prepared *openAICompatResponsesRequest, httpResp *http.Response, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	var usage helps.StreamUsageBuffer
	var failed bool
	defer func() {
		usage.Publish(prepared.ctx, prepared.reporter)
		prepared.reporter.EnsurePublished(prepared.ctx)
	}()
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	var frameLines [][]byte
	var dataLines [][]byte
	var eventName string
	var terminalSeen bool
	var responseUsage gjson.Result
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	publishError := func(err error) {
		helps.RecordAPIResponseError(prepared.ctx, e.cfg, err)
		usage.PublishFailure(prepared.ctx, prepared.reporter, err)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: err}:
		case <-prepared.ctx.Done():
		}
		failed = true
	}
	sendFrame := func(frame []byte) bool {
		if len(frame) == 0 {
			return true
		}
		if !bytes.HasSuffix(frame, []byte("\n\n")) {
			frame = append(frame, '\n', '\n')
		}
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
			return true
		case <-prepared.ctx.Done():
			return false
		}
	}
	processFrame := func() bool {
		frameData := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
		frame := bytes.Join(frameLines, []byte("\n"))
		frameLines = nil
		dataLines = nil
		frameType := eventName
		eventName = ""
		if len(frameData) == 0 {
			return sendFrame(frame)
		}
		if bytes.Equal(frameData, []byte("[DONE]")) {
			if !terminalSeen {
				publishError(helps.ResponsesCompactionError{Message: "upstream Responses stream ended before a terminal response"})
				return false
			}
			return sendFrame(frame)
		}
		if !json.Valid(frameData) {
			publishError(helps.ResponsesCompactionError{Message: "upstream Responses stream contained an incomplete event"})
			return false
		}
		eventType := gjson.GetBytes(frameData, "type").String()
		if eventType == "" {
			eventType = frameType
		}
		if terminalSeen && (eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed") {
			publishError(helps.ResponsesCompactionError{Message: "upstream Responses stream returned multiple terminal responses"})
			return false
		}
		prepared.reporter.ObserveResponseModel(frameData)
		usage.ObserveOpenAIStream(append([]byte("data: "), frameData...))
		usage.Observe(helps.ParseCodexUsage(frameData))
		currentUsage := gjson.GetBytes(frameData, "response.usage")
		if !currentUsage.IsObject() {
			currentUsage = gjson.GetBytes(frameData, "usage")
		}
		if currentUsage.IsObject() && !terminalSeen {
			responseUsage = currentUsage
		}
		if streamErr, isError := openAICompatStreamDataError(frameData, frameType); isError {
			publishError(streamErr)
			return false
		}
		if eventType == "response.output_item.done" {
			helps.CollectResponsesOutputItemDone(frameData, outputItemsByIndex, &outputItemsFallback)
		}
		if eventType == "response.completed" || eventType == "response.incomplete" {
			if eventType == "response.incomplete" && helps.IsCodexTerminalEmptyIncomplete(frameData, len(outputItemsByIndex)+len(outputItemsFallback), false) {
				publishError(helps.ResponsesCompactionError{Message: "upstream returned an empty incomplete Responses result"})
				return false
			}
			preparedData, errPrepare := helps.PrepareV1ResponsesTerminalEvent(frameData, eventType, outputItemsByIndex, outputItemsFallback)
			if errPrepare == nil {
				var response []byte
				response, errPrepare = helps.PreserveV1ResponsesUsage([]byte(gjson.GetBytes(preparedData, "response").Raw), responseUsage)
				if errPrepare == nil {
					preparedData, errPrepare = sjson.SetRawBytes(preparedData, "response", response)
				}
			}
			if errPrepare != nil {
				publishError(errPrepare)
				return false
			}
			frameData = preparedData
			terminalSeen = true
			frame = []byte("event: " + eventType + "\ndata: ")
			frame = append(frame, frameData...)
			frame = append(frame, '\n', '\n')
		}
		return sendFrame(frame)
	}
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		helps.AppendAPIResponseChunk(prepared.ctx, e.cfg, line)
		prepared.reporter.ObserveResponseModel(line)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if !processFrame() || failed {
				break
			}
		} else {
			frameLines = append(frameLines, line)
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				dataLines = append(dataLines, bytes.Clone(bytes.TrimSpace(trimmed[len("data:"):])))
			} else if bytes.HasPrefix(trimmed, []byte("event:")) {
				eventName = strings.TrimSpace(string(trimmed[len("event:"):]))
			} else if !bytes.HasPrefix(trimmed, []byte(":")) && !bytes.HasPrefix(trimmed, []byte("id:")) && !bytes.HasPrefix(trimmed, []byte("retry:")) {
				publishError(helps.ResponsesCompactionError{Message: "upstream Responses stream contained an invalid event line"})
				break
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil && !failed {
		publishError(errScan)
	}
	if !failed && len(frameLines) > 0 && !processFrame() {
		return
	}
	if !failed && !terminalSeen {
		publishError(helps.ResponsesCompactionError{Message: "upstream Responses stream closed before a terminal response"})
	}
}
