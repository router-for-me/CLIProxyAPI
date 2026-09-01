package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *OpenAICompatExecutor) executeResponsesCompactionTrigger(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, errPrepare := e.prepareResponsesCompactionTrigger(ctx, auth, req, opts, false)
	if errPrepare != nil {
		return resp, errPrepare
	}
	defer prepared.reporter.TrackFailure(ctx, &err)

	httpResp, errDo := e.doOpenAICompatRequest(ctx, auth, prepared, opts, false)
	if errDo != nil {
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	eventType := remoteCompactionV2EventType(body)
	body = aggregateResponsesCompactionPayload(body)
	if errCompaction := validateRemoteCompactionV2Response(body, eventType); errCompaction != nil {
		return resp, errCompaction
	}
	prepared.reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	prepared.reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, opts.OriginalRequest, prepared.body, body, &param)
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *OpenAICompatExecutor) executeResponsesCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, errPrepare := e.prepareResponsesCompactionTrigger(ctx, auth, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	defer prepared.reporter.TrackFailure(ctx, &err)

	httpResp, errDo := e.doOpenAICompatRequest(ctx, auth, prepared, opts, true)
	if errDo != nil {
		return nil, errDo
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		sawCompleted := false

		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			translatedLine := line
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				eventType := gjson.GetBytes(data, "type").String()
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete":
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					if errCompaction := validateRemoteCompactionV2Response(data, eventType); errCompaction != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errCompaction)
						prepared.reporter.PublishFailure(ctx, errCompaction)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: errCompaction}:
						case <-ctx.Done():
						}
						return
					}
					if eventType == "response.completed" {
						if detail, ok := helps.ParseCodexUsage(data); ok {
							prepared.reporter.Publish(ctx, detail)
						} else {
							prepared.reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
						}
						sawCompleted = true
					}
					translatedLine = append([]byte("data: "), data...)
				default:
					if isRemoteCompactionV2TerminalEvent(eventType) {
						streamErr := remoteCompactionV2TerminalError(eventType)
						helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
						prepared.reporter.PublishFailure(ctx, streamErr)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
						case <-ctx.Done():
						}
						return
					}
				}
			}

			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, opts.OriginalRequest, prepared.body, translatedLine, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
			if sawCompleted {
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			if ctx.Err() != nil {
				return
			}
			streamErr := newRemoteCompactionV2ProtocolError(http.StatusBadGateway, "remote compaction v2 stream read failed: "+errScan.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			prepared.reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := newRemoteCompactionV2ProtocolError(http.StatusBadGateway, "upstream stream closed before response.completed")
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		prepared.reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

type openAICompatResponsesRequest struct {
	from            sdktranslator.Format
	to              sdktranslator.Format
	responseFormat  sdktranslator.Format
	baseModel       string
	originalPayload []byte
	body            []byte
	url             string
	apiKey          string
	reporter        *helps.UsageReporter
}

func (e *OpenAICompatExecutor) prepareResponsesCompactionTrigger(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*openAICompatResponsesRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		var err error = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		reporter.TrackFailure(ctx, &err)
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayloadSource, stream, isCompat)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, stream, isCompat)
	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if !stream {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	} else {
		translated = helps.SetBoolIfDifferent(translated, "stream", true)
	}
	translated = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "openai compat executor", translated)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	return &openAICompatResponsesRequest{
		from:            from,
		to:              to,
		responseFormat:  responseFormat,
		baseModel:       baseModel,
		originalPayload: originalPayloadSource,
		body:            translated,
		url:             strings.TrimSuffix(baseURL, "/") + "/responses",
		apiKey:          apiKey,
		reporter:        reporter,
	}, nil
}

func (e *OpenAICompatExecutor) doOpenAICompatRequest(ctx context.Context, auth *cliproxyauth.Auth, prepared *openAICompatResponsesRequest, opts cliproxyexecutor.Options, stream bool) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if prepared.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+prepared.apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       prepared.url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      prepared.body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = prepared.reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	return httpResp, nil
}

func aggregateResponsesCompactionPayload(body []byte) []byte {
	if !bytes.Contains(body, dataTag) {
		return body
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	lines := bytes.Split(body, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[5:])
		eventType := gjson.GetBytes(eventData, "type").String()
		if eventType == "response.output_item.done" {
			collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			continue
		}
		if eventType != "response.completed" && eventType != "response.incomplete" {
			continue
		}
		completed := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
		response := gjson.GetBytes(completed, "response")
		if response.Exists() && response.Type == gjson.JSON {
			return []byte(response.Raw)
		}
		return completed
	}
	return body
}
