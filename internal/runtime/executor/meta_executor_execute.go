package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

type metaPreparedRequest struct {
	baseModel       string
	from            sdktranslator.Format
	responseFormat  sdktranslator.Format
	to              sdktranslator.Format
	originalPayload []byte
	body            []byte
}

func (e *MetaExecutor) prepareResponsesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*metaPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, stream, isCompat)
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream, isCompat)

	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, e.Identifier(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = helps.SetBoolIfDifferent(body, "stream", stream)
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.DeleteBytes(body, "client_metadata")
	body = normalizeCodexInstructions(body)
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "meta executor", body)

	return &metaPreparedRequest{
		baseModel:       baseModel,
		from:            from,
		responseFormat:  responseFormat,
		to:              to,
		originalPayload: originalPayload,
		body:            body,
	}, nil
}

func (e *MetaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}

	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return resp, errAuth
	}

	prepared, errPrepare := e.prepareResponsesRequest(ctx, req, opts, true)
	if errPrepare != nil {
		return resp, errPrepare
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, enriched)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	baseURL, token := metaCreds(enriched)
	if strings.TrimSpace(baseURL) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing provider baseURL"}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if errRequest != nil {
		return resp, errRequest
	}
	applyMetaAPIHeaders(httpReq, enriched, token, true, opts.Headers)
	e.recordMetaRequest(ctx, enriched, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.ObserveResponseModel(data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, wrapMetaUpstreamError(httpResp.StatusCode, data)
	}

	out, errCompleted := e.translateMetaCompleted(ctx, req, prepared, data)
	if errCompleted != nil {
		return resp, errCompleted
	}
	if len(out.sourceEvent) > 0 {
		reporter.ObserveResponseModel(out.sourceEvent)
	}
	if detail, ok := helps.ParseCodexUsage(out.sourceEvent); ok {
		reporter.Publish(ctx, detail)
	} else {
		reporter.EnsurePublished(ctx)
	}
	payload := out.payload
	if prepared.responseFormat == sdktranslator.FormatOpenAIResponse {
		payload = helps.EnsureResponsesUsageDetails(payload)
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: httpResp.Header.Clone()}, nil
}

type metaCompletedTranslation struct {
	payload     []byte
	sourceEvent []byte
}

func (e *MetaExecutor) translateMetaCompleted(ctx context.Context, req cliproxyexecutor.Request, prepared *metaPreparedRequest, data []byte) (metaCompletedTranslation, error) {
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(dataTag):])
		if errEvent := metaStreamEventError(eventData); errEvent != nil {
			return metaCompletedTranslation{}, errEvent
		}
		eventType := gjson.GetBytes(eventData, "type").String()
		switch eventType {
		case "response.output_item.done":
			xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed", "response.incomplete":
			completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			var param any
			out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
			return metaCompletedTranslation{payload: out, sourceEvent: completedData}, nil
		}
	}

	if completedData, ok := metaAsCompletedEvent(data); ok {
		completedData = patchCodexCompletedOutput(completedData, outputItemsByIndex, outputItemsFallback)
		var param any
		out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
		return metaCompletedTranslation{payload: out, sourceEvent: completedData}, nil
	}

	return metaCompletedTranslation{}, statusErr{code: http.StatusRequestTimeout, msg: "meta stream error: stream disconnected before response.completed or response.incomplete"}
}

func (e *MetaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if _, errAuth := e.ensureAuth(ctx, auth); errAuth != nil {
		return cliproxyexecutor.Response{}, errAuth
	}
	prepared, errPrepare := e.prepareResponsesRequest(ctx, req, opts, false)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	enc, errEnc := tokenizer.Get(tokenizer.O200kBase)
	if errEnc != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: tokenizer init failed: %w", errEnc)
	}
	count, errCount := countCodexInputTokens(enc, prepared.body)
	if errCount != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: token counting failed: %w", errCount)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, prepared.to, prepared.responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func (e *MetaExecutor) recordMetaRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func applyMetaAPIHeaders(req *http.Request, auth *cliproxyauth.Auth, token string, stream bool, clientHeaders http.Header) {
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Del("Authorization")
	}
	req.Header.Set("User-Agent", metaUserAgent)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)
}

func wrapMetaUpstreamError(statusCode int, body []byte) error {
	se := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if retryAfter := parseMetaRetryAfter(statusCode, body, time.Now()); retryAfter != nil {
			se.retryAfter = retryAfter
		}
		if isMetaSubscriptionQuota(statusCode, body) {
			return metaRateLimitError{statusErr: se, credentialScoped: true}
		}
	}
	return se
}

func metaStreamEventError(eventData []byte) error {
	eventType := gjson.GetBytes(eventData, "type").String()
	if eventType != "error" && eventType != "response.failed" {
		return nil
	}
	statusCode := http.StatusBadGateway
	if code := int(gjson.GetBytes(eventData, "error.code").Int()); code >= 400 && code <= 599 {
		statusCode = code
	}
	return wrapMetaUpstreamError(statusCode, eventData)
}

func metaAsCompletedEvent(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if !gjson.ValidBytes(trimmed) {
		return nil, false
	}
	root := gjson.ParseBytes(trimmed)
	switch root.Get("type").String() {
	case "response.completed", "response.incomplete":
		return trimmed, true
	}
	if root.Get("object").String() == "response" || root.Get("output").Exists() {
		wrapped, errSet := sjson.SetRawBytes([]byte(`{"type":"response.completed"}`), "response", trimmed)
		if errSet != nil {
			return nil, false
		}
		return wrapped, true
	}
	return nil, false
}
