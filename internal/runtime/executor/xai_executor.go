package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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

var (
	xaiDataTag  = []byte("data:")
	xaiEventTag = []byte("event:")
)

const (
	xaiImageHandlerType         = "openai-image"
	xaiVideoHandlerType         = "openai-video"
	xaiCustomToolType           = "custom"
	xaiCustomToolCallType       = "custom_tool_call"
	xaiCustomToolCallOutputType = "custom_tool_call_output"
	xaiFunctionToolType         = "function"
	xaiFunctionCallType         = "function_call"
	xaiFunctionCallOutputType   = "function_call_output"
	xaiAdditionalToolsType      = "additional_tools"
	xaiItemReferenceType        = "item_reference"
	xaiImageGenerationToolType  = "image_generation"
	xaiNamespaceToolType        = "namespace"
	xaiToolSearchType           = "tool_search"
	xaiXSearchToolType          = "x_search"
	xaiWebSearchCallType        = "web_search_call"
	xaiXSearchCallType          = "x_search_call"
	xaiWebSearchToolType        = "web_search"
	// Codex Desktop injects codex_app.automation_update with a large oneOf+$ref
	// schema. xAI's free/build Responses path accepts the HTTP request but never
	// emits SSE when that schema is present, so Desktop hangs on "thinking".
	xaiCodexAppNamespaceName    = "codex_app"
	xaiAutomationUpdateToolName = "automation_update"
	// Freeform schema for Codex/OpenAI custom tools (e.g. exec grammar tools).
	// Keeps the tool callable on xAI while preserving a single string input.
	xaiCustomToolFunctionParameters = `{"type":"object","properties":{"input":{"type":"string","description":"Freeform tool input (source/code/text)"}},"required":["input"],"additionalProperties":true}`
	// Permissive placeholder schema: keeps the tool callable without the hang.
	xaiSafeFunctionParameters   = `{"type":"object","properties":{},"additionalProperties":true}`
	xaiImagesGenerationsPath    = "/images/generations"
	xaiImagesEditsPath          = "/images/edits"
	xaiDefaultImageEndpointPath = xaiImagesGenerationsPath
	xaiVideosGenerationsPath    = "/videos/generations"
	xaiVideosEditsPath          = "/videos/edits"
	xaiVideosExtensionsPath     = "/videos/extensions"
	xaiVideosPath               = "/videos"
	xaiIdempotencyKeyMetaKey    = "idempotency_key"
	xaiComposerModelPrefix      = "grok-composer-"
	xaiTokenAuthHeader          = "X-XAI-Token-Auth"
	xaiTokenAuthValue           = "xai-grok-cli"
	xaiClientVersionHeader      = "x-grok-client-version"
	// Keep in sync with the current Grok CLI client version that chat-proxy expects.
	xaiClientVersionValue = "0.2.93"
	// xaiUsingAPIAttr enables the official API path for non-media HTTP chat.
	xaiUsingAPIAttr = "using_api"
)

// XAIExecutor is a stateless executor for xAI Grok's Responses API.
type XAIExecutor struct {
	cfg *config.Config
}

// NewXAIExecutor creates a new xAI executor.
func NewXAIExecutor(cfg *config.Config) *XAIExecutor {
	return &XAIExecutor{cfg: cfg}
}

// Identifier returns the provider identifier.
func (e *XAIExecutor) Identifier() string {
	return "xai"
}

// PrepareRequest injects xAI credentials into the outgoing HTTP request.
func (e *XAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _ := xaiCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects xAI credentials into the request and executes it.
func (e *XAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("xai executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *XAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if endpointPath := xaiImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, endpointPath)
	}
	if xaiIsVideoRequest(opts) {
		return e.executeVideos(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)

	prepared, err := e.prepareResponsesRequest(ctx, req, opts, true)
	if err != nil {
		return resp, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return resp, err
	}
	applyXAIChatHeaders(httpReq, auth, token, true, prepared.sessionID)
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
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return resp, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, xaiStatusErr(httpResp.StatusCode, data)
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, xaiDataTag) {
			continue
		}
		eventData := xaiNormalizeReasoningSummaryData(bytes.TrimSpace(line[len(xaiDataTag):]))
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			if xaiShouldHideInjectedSearchResults(e.cfg) {
				filtered := filterXAIInjectedServerToolPayload(eventData)
				if len(filtered) == 0 {
					continue
				}
				eventData = filtered
			}
			eventData = remapXAICustomToolCallsInPayload(eventData, prepared.customToolNames)
			xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed":
			if detail, ok := helps.ParseCodexUsage(eventData); ok {
				reporter.Publish(ctx, detail)
			}
			completedData := xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			completedData = xaiNormalizeReasoningSummaryData(completedData)
			if xaiShouldHideInjectedSearchResults(e.cfg) {
				completedData = filterXAIInjectedServerToolPayload(completedData)
			}
			// Only remap tools that were originally custom (e.g. Codex exec).
			completedData = remapXAICustomToolCallsInPayload(completedData, prepared.customToolNames)
			cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, completedData)
			var param any
			out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
			return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
		}
	}

	return resp, statusErr{code: http.StatusRequestTimeout, msg: "xai stream error: stream disconnected before response.completed"}
}

func (e *XAIExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, data, headers, errCompact := e.executeCompactRequest(ctx, auth, req, opts)
	if errCompact != nil {
		return resp, errCompact
	}

	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, data, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: headers}, nil
}

func (e *XAIExecutor) executeCompactRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*xaiPreparedRequest, []byte, http.Header, error) {
	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)

	prepared, err := e.prepareResponsesRequestTo(ctx, req, opts, false, sdktranslator.FormatOpenAIResponse)
	if err != nil {
		return nil, nil, nil, err
	}
	prepared.body, _ = sjson.DeleteBytes(prepared.body, "stream")
	prepared.body, _ = sjson.DeleteBytes(prepared.body, "tools")
	prepared.body = xaiRemoveInputItemsByType(prepared.body, "compaction_trigger")

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())

	requestURL := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, nil, nil, err
	}
	applyXAIChatHeaders(httpReq, auth, token, false, prepared.sessionID)
	e.recordXAIRequest(ctx, auth, requestURL, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, nil, nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, nil, nil, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = xaiStatusErr(httpResp.StatusCode, data)
		return nil, nil, nil, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)
	return prepared, data, httpResp.Header.Clone(), nil
}

func (e *XAIExecutor) executeCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	prepared, data, headers, err := e.executeCompactRequest(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}

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
	input := gjson.GetBytes(body, "input")
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
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
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

	updated, err := sjson.SetRawBytes(body, "input", buf.Bytes())
	if err != nil {
		return body
	}
	return updated
}

func xaiBuildCompactionTriggerStreamChunks(prepared *xaiPreparedRequest, compactData []byte) [][]byte {
	responseID := xaiCompactionResponseID(compactData)
	now := time.Now().Unix()
	createdAt := gjson.GetBytes(compactData, "created_at").Int()
	if createdAt == 0 {
		createdAt = now
	}
	completedAt := gjson.GetBytes(compactData, "completed_at").Int()
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
	completedResponse, _ = sjson.SetBytes(completedResponse, "completed_at", completedAt)
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "output", output)
	if usage := gjson.GetBytes(compactData, "usage"); usage.Exists() {
		completedResponse, _ = sjson.SetRawBytes(completedResponse, "usage", []byte(usage.Raw))
	}

	createdPayload := []byte(`{"type":"response.created","sequence_number":0}`)
	createdPayload, _ = sjson.SetRawBytes(createdPayload, "response", createdResponse)
	inProgressPayload := []byte(`{"type":"response.in_progress","sequence_number":1}`)
	inProgressPayload, _ = sjson.SetRawBytes(inProgressPayload, "response", inProgressResponse)
	addedPayload := []byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	addedPayload, _ = sjson.SetRawBytes(addedPayload, "item", item)
	keepalivePayload := []byte(`{"type":"keepalive","sequence_number":3}`)
	donePayload := []byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0}`)
	donePayload, _ = sjson.SetRawBytes(donePayload, "item", item)
	completedPayload := []byte(`{"type":"response.completed","sequence_number":5}`)
	completedPayload, _ = sjson.SetRawBytes(completedPayload, "response", completedResponse)

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
	response := []byte(`{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null,"incomplete_details":null,"output":[]}`)
	response, _ = sjson.SetBytes(response, "id", responseID)
	response, _ = sjson.SetBytes(response, "created_at", createdAt)
	response, _ = sjson.SetBytes(response, "status", status)
	if model := gjson.GetBytes(compactData, "model").String(); model != "" {
		response, _ = sjson.SetBytes(response, "model", model)
	} else if prepared != nil && prepared.baseModel != "" {
		response, _ = sjson.SetBytes(response, "model", prepared.baseModel)
	}

	if prepared == nil {
		return response
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
		if value := gjson.GetBytes(prepared.body, field); value.Exists() {
			response, _ = sjson.SetRawBytes(response, field, []byte(value.Raw))
		}
	}
	return response
}

func xaiCompactionOutputItem(compactData []byte, responseID string) []byte {
	itemResult := gjson.GetBytes(compactData, "output.0")
	item := []byte(`{"type":"compaction"}`)
	if itemResult.Exists() && itemResult.Type == gjson.JSON {
		item = []byte(itemResult.Raw)
	}
	if !gjson.GetBytes(item, "type").Exists() {
		item, _ = sjson.SetBytes(item, "type", "compaction")
	}
	if !gjson.GetBytes(item, "id").Exists() {
		item, _ = sjson.SetBytes(item, "id", xaiCompactionItemID(responseID))
	}
	return item
}

func xaiCompactionResponseID(compactData []byte) string {
	if responseID := strings.TrimSpace(gjson.GetBytes(compactData, "id").String()); responseID != "" {
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

func (e *XAIExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := xaiCreds(auth)
	if baseURL == "" {
		baseURL = xaiauth.DefaultAPIBaseURL
	}
	if endpointPath == "" {
		endpointPath = xaiDefaultImageEndpointPath
	}

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Payload))
	if err != nil {
		return resp, err
	}
	applyXAIHeaders(httpReq, auth, token, false, "")
	e.recordXAIRequest(ctx, auth, url, httpReq.Header.Clone(), req.Payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
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

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, xaiStatusErr(httpResp.StatusCode, data)
	}

	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

func (e *XAIExecutor) executeVideos(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := xaiCreds(auth)
	if baseURL == "" {
		baseURL = xaiauth.DefaultAPIBaseURL
	}

	method := http.MethodPost
	endpointPath := xaiVideosGenerationsPath
	var body io.Reader = bytes.NewReader(req.Payload)

	switch path := xaiVideoEndpointPath(opts); path {
	case xaiVideosGenerationsPath, xaiVideosEditsPath, xaiVideosExtensionsPath:
		endpointPath = path
	default:
		if requestID := strings.TrimSpace(gjson.GetBytes(req.Payload, "request_id").String()); requestID != "" {
			method = http.MethodGet
			endpointPath = xaiVideosPath + "/" + url.PathEscape(requestID)
			body = nil
		}
	}
	requestURL := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return resp, err
	}
	applyXAIHeaders(httpReq, auth, token, false, "")
	if method == http.MethodPost {
		key := xaiMetadataString(opts.Metadata, xaiIdempotencyKeyMetaKey)
		if key == "" && opts.Headers != nil {
			key = strings.TrimSpace(opts.Headers.Get("x-idempotency-key"))
		}
		if key != "" {
			httpReq.Header.Set("x-idempotency-key", key)
		}
	}
	e.recordXAIRequest(ctx, auth, requestURL, httpReq.Header.Clone(), req.Payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
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

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, xaiStatusErr(httpResp.StatusCode, data)
	}

	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

func (e *XAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if xaiInputHasItemType(req.Payload, "compaction_trigger") {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)

	prepared, err := e.prepareResponsesRequest(ctx, req, opts, true)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	applyXAIChatHeaders(httpReq, auth, token, true, prepared.sessionID)
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
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		// Track output indexes dropped because they belonged to injected
		// search tools, so remaining stream events can be renumbered densely.
		droppedOutputIndexes := make(map[int64]struct{})
		// output_index values whose item was remapped to custom_tool_call, so
		// subsequent function_call_arguments.* events can become
		// custom_tool_call_input.*.
		customOutputIndexes := make(map[int64]struct{})
		var pendingEventLine []byte
		emitTranslatedLine := func(translatedLine []byte) bool {
			chunks := sdktranslator.TranslateStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, translatedLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
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
					normalizedEventName := gjson.GetBytes(eventData, "type").String()
					switch normalizedEventName {
					case "response.output_item.done":
						if xaiShouldHideInjectedSearchResults(e.cfg) {
							filtered := filterXAIInjectedServerToolPayload(eventData)
							if len(filtered) == 0 {
								xaiRecordDroppedOutputIndex(eventData, droppedOutputIndexes)
								pendingEventLine = nil
								continue
							}
							eventData = filtered
						}
						eventData = xaiCompactOutputIndex(eventData, droppedOutputIndexes)
						eventData = remapXAICustomToolCallsInPayload(eventData, prepared.customToolNames)
						xaiTrackCustomOutputIndex(eventData, customOutputIndexes)
						xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
					case "response.completed":
						if detail, ok := helps.ParseCodexUsage(eventData); ok {
							reporter.Publish(ctx, detail)
						}
						eventData = xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
						eventData = xaiNormalizeReasoningSummaryData(eventData)
						if xaiShouldHideInjectedSearchResults(e.cfg) {
							eventData = filterXAIInjectedServerToolPayload(eventData)
						}
						eventData = remapXAICustomToolCallsInPayload(eventData, prepared.customToolNames)
						cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, eventData)
						normalizedEventName = gjson.GetBytes(eventData, "type").String()
					default:
						// Drop SSE for injected server tools so clients never see them.
						if xaiShouldHideInjectedSearchResults(e.cfg) {
							if xaiIsInjectedServerToolEvent(eventData) {
								xaiRecordDroppedOutputIndex(eventData, droppedOutputIndexes)
								pendingEventLine = nil
								continue
							}
							// Residual part/delta events for an already-dropped inject item
							// (no type marker, only output_index) must also disappear.
							if xaiOutputIndexIsDropped(eventData, droppedOutputIndexes) {
								pendingEventLine = nil
								continue
							}
						}
						// Renumber after earlier injected items were stripped.
						eventData = xaiCompactOutputIndex(eventData, droppedOutputIndexes)
						// Remap item-bearing events (e.g. output_item.added) for custom tools only.
						eventData = remapXAICustomToolCallsInPayload(eventData, prepared.customToolNames)
						xaiTrackCustomOutputIndex(eventData, customOutputIndexes)
						// custom item + function_call_arguments.* → custom_tool_call_input.done;
						// partial argument deltas are dropped (nil).
						eventData = translateXAICustomToolCallInputEvents(eventData, customOutputIndexes)
						if eventData == nil {
							pendingEventLine = nil
							continue
						}
						normalizedEventName = gjson.GetBytes(eventData, "type").String()
					}

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

// CountTokens estimates token count for xAI Responses requests.
func (e *XAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, err := e.prepareResponsesRequest(ctx, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("xai executor: tokenizer init failed: %w", err)
	}
	count, err := enc.Count(string(prepared.body))
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("xai executor: token counting failed: %w", err)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, prepared.to, prepared.responseFormat, int64(count), []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

// Refresh refreshes xAI OAuth credentials using the stored refresh token.
func (e *XAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("xai executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "xai executor: auth is nil"}
	}
	refreshToken := xaiMetadataString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}
	tokenEndpoint := xaiMetadataString(auth.Metadata, "token_endpoint")
	svc := xaiauth.NewXAIAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokens(ctx, refreshToken, tokenEndpoint)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = "xai"
	auth.Metadata["auth_kind"] = "oauth"
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.IDToken != "" {
		auth.Metadata["id_token"] = td.IDToken
	}
	if td.TokenType != "" {
		auth.Metadata["token_type"] = td.TokenType
	}
	if td.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = td.ExpiresIn
	}
	if td.Expire != "" {
		auth.Metadata["expired"] = td.Expire
	}
	if td.Email != "" {
		auth.Metadata["email"] = td.Email
	}
	if td.Subject != "" {
		auth.Metadata["sub"] = td.Subject
	}
	if tokenEndpoint != "" {
		auth.Metadata["token_endpoint"] = tokenEndpoint
	}
	if xaiMetadataString(auth.Metadata, "base_url") == "" {
		auth.Metadata["base_url"] = xaiauth.DefaultAPIBaseURL
	}
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["auth_kind"] = "oauth"
	if strings.TrimSpace(auth.Attributes["base_url"]) == "" {
		auth.Attributes["base_url"] = xaiauth.DefaultAPIBaseURL
	}
	return auth, nil
}

type xaiPreparedRequest struct {
	baseModel       string
	from            sdktranslator.Format
	responseFormat  sdktranslator.Format
	to              sdktranslator.Format
	originalPayload []byte
	body            []byte
	sessionID       string
	replayScope     xaiReasoningReplayScope
	// Names of tools that were originally type=custom and got promoted to
	// function for xAI. Response function_call items with these names are
	// remapped back to custom_tool_call for Codex Desktop.
	customToolNames map[string]struct{}
}

func (e *XAIExecutor) prepareResponsesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestTo(ctx, req, opts, stream, sdktranslator.FormatCodex)
}

func (e *XAIExecutor) prepareResponsesRequestTo(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestToWithPreviousResponseToolOutputs(ctx, req, opts, stream, to, false)
}

func (e *XAIExecutor) prepareResponsesRequestKeepingPreviousResponseToolOutputs(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestToWithPreviousResponseToolOutputs(ctx, req, opts, stream, sdktranslator.FormatCodex, true)
}

func (e *XAIExecutor) prepareResponsesRequestToWithPreviousResponseToolOutputs(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format, keepPreviousResponseToolOutputs bool) (*xaiPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)

	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), e.Identifier(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", stream)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	// Capture original custom tool names before rewriting tools for xAI.
	customToolNames := collectXAIOriginalCustomToolNames(body)
	// Promote/drop Codex/OpenAI-only input items before tool filtering so
	// additional_tools can feed top-level tools and keep function calling.
	body = normalizeXAIInputItems(body)
	body = normalizeXAITools(body)
	body = injectXAIBuildCacheTools(e.cfg, body)
	body = normalizeXAIToolChoiceForTools(body)
	var replayScope xaiReasoningReplayScope
	body, replayScope, err = applyXAIReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	// After replay may re-insert function_call items for matching outputs,
	// drop any remaining tool outputs whose call is still missing. WebSocket
	// follow-ups keep previous_response_id, so their matching calls remain in
	// upstream history and those outputs must be preserved.
	keepPreviousResponseOutputs := keepPreviousResponseToolOutputs && strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String()) != ""
	if !keepPreviousResponseOutputs {
		body = pruneXAIOrphanToolOutputs(body)
	}
	body = normalizeXAIInputReasoningItems(body)
	body = sanitizeXAIInputEncryptedContent(body)
	body = normalizeCodexInstructions(body)
	body = sanitizeXAIResponsesBody(body, baseModel)

	sessionID, errSession := xaiResolveComposerSessionID(ctx, req, opts, baseModel)
	if errSession != nil {
		return nil, errSession
	}
	if sessionID != "" {
		body, _ = sjson.SetBytes(body, "prompt_cache_key", sessionID)
	}

	return &xaiPreparedRequest{
		baseModel:       baseModel,
		from:            from,
		responseFormat:  responseFormat,
		to:              to,
		originalPayload: originalPayload,
		body:            body,
		sessionID:       sessionID,
		replayScope:     replayScope,
		customToolNames: customToolNames,
	}, nil
}

func (e *XAIExecutor) recordXAIRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
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

func xaiCreds(auth *cliproxyauth.Auth) (token, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if auth.Metadata != nil {
		if token == "" {
			token = xaiMetadataString(auth.Metadata, "access_token")
		}
		if baseURL == "" {
			baseURL = xaiMetadataString(auth.Metadata, "base_url")
		}
	}
	return token, baseURL
}

// xaiUsingAPI reports whether this xAI auth should use the official API path
// for non-media HTTP chat. OAuth defaults to false to use Grok Build.
func xaiUsingAPI(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return true
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes[xaiUsingAPIAttr]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) > 0 {
		raw, ok := auth.Metadata[xaiUsingAPIAttr]
		if ok && raw != nil {
			switch v := raw.(type) {
			case bool:
				return v
			case string:
				parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
				if errParse == nil {
					return parsed
				}
			default:
			}
		}
	}
	if raw := strings.TrimSpace(auth.Attributes["auth_kind"]); raw != "" {
		return !strings.EqualFold(raw, "oauth")
	}
	return !strings.EqualFold(xaiMetadataString(auth.Metadata, "auth_kind"), "oauth")
}

// xaiChatBaseURL returns the base URL for non-image/video xAI HTTP chat requests.
// When auth using_api is true, the official API base URL logic is used. When it
// is false (including its OAuth default), empty or official default base_url is
// rewritten to the CLI chat-proxy endpoint; an explicit non-default base_url is
// still honored.
// Websocket transport intentionally does not use this helper: cli-chat-proxy only
// accepts HTTP POST and returns 405 for websocket upgrades.
func xaiChatBaseURL(auth *cliproxyauth.Auth) string {
	_, baseURL := xaiCreds(auth)
	if xaiUsingAPI(auth) {
		if baseURL == "" {
			return xaiauth.DefaultAPIBaseURL
		}
		return baseURL
	}
	if baseURL != "" && !xaiIsDefaultAPIBaseURL(baseURL) {
		return baseURL
	}
	return xaiauth.CLIChatProxyBaseURL
}

func xaiNormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func xaiIsDefaultAPIBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.DefaultAPIBaseURL)
}

func xaiIsCLIChatProxyBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.CLIChatProxyBaseURL)
}

func applyXAIHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	applyXAICustomHeaders(r, auth)
}

func applyXAIDefaultHeaders(r *http.Request, token string, stream bool, sessionID string) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")
	if sessionID != "" {
		r.Header.Set("x-grok-conv-id", sessionID)
	}
}

func applyXAICustomHeaders(r *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

// applyXAIChatHeaders applies standard xAI headers for non-image/video chat
// requests. When using_api is true, this matches the standard
// applyXAIHeaders behavior. CLI chat-proxy identity headers are only attached
// when using_api is false and the resolved chat base URL is the official CLI
// chat-proxy endpoint.
func applyXAIChatHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	if xaiUsingAPI(auth) {
		applyXAIHeaders(r, auth, token, stream, sessionID)
		return
	}
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	if xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		r.Header.Set(xaiTokenAuthHeader, xaiTokenAuthValue)
		r.Header.Set(xaiClientVersionHeader, xaiClientVersionValue)
	}
	applyXAICustomHeaders(r, auth)
}

func xaiResolveComposerSessionID(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string) (string, error) {
	if sessionID := xaiExecutionSessionID(req, opts); sessionID != "" {
		return sessionID, nil
	}
	if !xaiRequiresIsolatedConversation(baseModel) {
		return "", nil
	}
	cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, req.Model, req.Payload, opts.Headers)
	if errCache != nil {
		return "", errCache
	}
	if ok {
		return cached.ID, nil
	}
	return uuid.NewString(), nil
}

func xaiExecutionSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if value := xaiMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if value := xaiMetadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
		return strings.TrimSpace(promptCacheKey.String())
	}
	return ""
}

func xaiRequiresIsolatedConversation(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), xaiComposerModelPrefix)
}

func xaiImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != xaiImageHandlerType {
		return ""
	}

	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/images/edits") {
		return xaiImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return xaiImagesGenerationsPath
	}
	return xaiDefaultImageEndpointPath
}

func xaiIsVideoRequest(opts cliproxyexecutor.Options) bool {
	return opts.SourceFormat.String() == xaiVideoHandlerType
}

func xaiVideoEndpointPath(opts cliproxyexecutor.Options) string {
	if !xaiIsVideoRequest(opts) {
		return ""
	}
	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/videos/edits") {
		return xaiVideosEditsPath
	}
	if strings.HasSuffix(path, "/videos/extensions") {
		return xaiVideosExtensionsPath
	}
	if strings.HasSuffix(path, "/videos/generations") {
		return xaiVideosGenerationsPath
	}
	return ""
}

func xaiMetadataString(meta map[string]any, key string) string {
	if len(meta) == 0 || key == "" {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func sanitizeXAIResponsesBody(body []byte, model string) []byte {
	body = removeXAIEncryptedReasoningInclude(body)
	if !xaiSupportsReasoningEffort(model) {
		if gjson.GetBytes(body, "reasoning.effort").Exists() {
			log.Debugf("xai: stripping reasoning.effort for model %s (no thinking levels in model registry)", model)
		}
		body, _ = sjson.DeleteBytes(body, "reasoning.effort")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
	}
	return body
}

// normalizeXAIInputItems rewrites OpenAI/Codex-only Responses input items so
// xAI's ModelInput untagged enum can deserialize the body without dropping
// callable tools.
//
//   - additional_tools: promote nested tools to top-level tools, drop the item
//   - item_reference: drop (xAI has no variant; previous_response_id is stripped).
//     Paired orphan tool outputs are pruned later by pruneXAIOrphanToolOutputs
//     after reasoning-replay may reinsert the referenced calls.
//   - custom_tool_call(_output): map to function_call(_output)
//   - function_call_output.output arrays: flatten to string
func normalizeXAIInputItems(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	inputArray := input.Array()
	changed := false
	items := make([]json.RawMessage, 0, len(inputArray))
	promotedTools := make([]json.RawMessage, 0)

	for _, item := range inputArray {
		itemType := strings.TrimSpace(item.Get("type").String())
		switch itemType {
		case xaiAdditionalToolsType:
			changed = true
			if tools := item.Get("tools"); tools.IsArray() {
				for _, tool := range tools.Array() {
					if !tool.Exists() {
						continue
					}
					promotedTools = append(promotedTools, json.RawMessage(tool.Raw))
				}
			}
			continue
		case xaiItemReferenceType:
			changed = true
			log.Debugf("xai: dropping unsupported input item_reference id=%s", strings.TrimSpace(item.Get("id").String()))
			continue
		case xaiCustomToolCallType:
			raw, ok := normalizeXAICustomToolCallItem(item)
			if !ok {
				return body
			}
			items = append(items, json.RawMessage(raw))
			changed = true
		case xaiCustomToolCallOutputType:
			raw, ok := normalizeXAIFunctionCallOutputItem(item, true)
			if !ok {
				return body
			}
			items = append(items, json.RawMessage(raw))
			changed = true
		case xaiFunctionCallOutputType:
			raw, ok := normalizeXAIFunctionCallOutputItem(item, false)
			if !ok {
				return body
			}
			if string(raw) != item.Raw {
				changed = true
			}
			items = append(items, json.RawMessage(raw))
		default:
			items = append(items, json.RawMessage(item.Raw))
		}
	}

	if len(promotedTools) > 0 {
		merged := []byte(`[]`)
		if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
			for _, tool := range tools.Array() {
				updated, errSet := sjson.SetRawBytes(merged, "-1", []byte(tool.Raw))
				if errSet != nil {
					return body
				}
				merged = updated
			}
		}
		for _, tool := range promotedTools {
			updated, errSet := sjson.SetRawBytes(merged, "-1", tool)
			if errSet != nil {
				return body
			}
			merged = updated
		}
		updated, errSet := sjson.SetRawBytes(body, "tools", merged)
		if errSet != nil {
			return body
		}
		body = updated
		changed = true
	}

	if !changed {
		return body
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

func normalizeXAICustomToolCallItem(item gjson.Result) ([]byte, bool) {
	raw := []byte(item.Raw)
	updated, errSet := sjson.SetBytes(raw, "type", xaiFunctionCallType)
	if errSet != nil {
		return nil, false
	}
	raw = updated

	if item.Get("input").Exists() {
		arguments := xaiCustomToolCallArguments(item.Get("input"))
		updated, errSet = sjson.SetBytes(raw, "arguments", arguments)
		if errSet != nil {
			return nil, false
		}
		raw = updated
		updated, errDel := sjson.DeleteBytes(raw, "input")
		if errDel != nil {
			return nil, false
		}
		raw = updated
	} else if !item.Get("arguments").Exists() {
		updated, errSet = sjson.SetBytes(raw, "arguments", "{}")
		if errSet != nil {
			return nil, false
		}
		raw = updated
	}
	return raw, true
}

func normalizeXAIFunctionCallOutputItem(item gjson.Result, forceFunctionType bool) ([]byte, bool) {
	raw := []byte(item.Raw)
	changed := false

	if forceFunctionType {
		updated, errSet := sjson.SetBytes(raw, "type", xaiFunctionCallOutputType)
		if errSet != nil {
			return nil, false
		}
		raw = updated
		changed = true
	}

	output := item.Get("output")
	if output.Exists() && output.IsArray() {
		text := xaiFlattenFunctionCallOutput(output)
		updated, errSet := sjson.SetBytes(raw, "output", text)
		if errSet != nil {
			return nil, false
		}
		raw = updated
		changed = true
	}

	if !changed {
		return []byte(item.Raw), true
	}
	return raw, true
}

func xaiFlattenFunctionCallOutput(output gjson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == gjson.String {
		return output.String()
	}
	if !output.IsArray() {
		if output.Raw != "" {
			return output.Raw
		}
		return ""
	}

	var b strings.Builder
	for _, part := range output.Array() {
		if part.Type == gjson.String {
			b.WriteString(part.String())
			continue
		}
		if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
			b.WriteString(text.String())
			continue
		}
		if part.Raw != "" {
			b.WriteString(part.Raw)
		}
	}
	return b.String()
}

func xaiCustomToolCallArguments(input gjson.Result) string {
	if !input.Exists() {
		return "{}"
	}

	// Freeform custom tools are promoted to a function schema with a required
	// string field "input". Always wrap string payloads under that key — even
	// when the string happens to be JSON — so replayed history matches the
	// promoted contract (e.g. input `{"cmd":"pwd"}` -> {"input":"{\"cmd\":\"pwd\"}"}).
	if input.Type == gjson.String {
		if encoded, errMarshal := json.Marshal(input.String()); errMarshal == nil {
			return `{"input":` + string(encoded) + `}`
		}
		return "{}"
	}

	if input.IsObject() {
		// Already shaped as {"input": ...} or a structured arguments object.
		return input.Raw
	}
	if input.Raw != "" {
		return `{"input":` + input.Raw + `}`
	}
	return "{}"
}

// injectXAIBuildCacheTools ensures native server tools web_search + x_search
// are present when cfg.XAI.InjectBuildSearchTools is enabled.
//
// Free OAuth requests without native web_search/x_search land on
// grok-4.5-build-free (cached_tokens=0); with them they route to the
// cache-capable grok-4.5 tier.
//
// xAI rejects Duplicate tool names across the tools list. Client
// function/custom tools named "web_search"/"x_search" MUST be dropped when
// natives are injected, otherwise upstream returns 400 invalid-argument.
// Cache + real search still coexist via hide-injected-search-results=false
// (default): natives stay on the wire for routing AND search results are
// returned to the client as web_search_call / x_search_call.
func injectXAIBuildCacheTools(cfg *config.Config, body []byte) []byte {
	if cfg == nil || !cfg.XAI.InjectBuildSearchTools || len(body) == 0 {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	hasWeb := false
	hasX := false
	changed := false
	filtered := []byte(`[]`)
	if tools.Exists() && tools.IsArray() {
		for _, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			toolName := strings.TrimSpace(tool.Get("name").String())
			// Keep a single native type tool; drop client function/custom tools
			// that collide on the reserved server-tool names (xAI name uniqueness).
			switch {
			case toolType == xaiWebSearchToolType:
				if hasWeb {
					changed = true
					continue
				}
				hasWeb = true
			case toolType == xaiXSearchToolType:
				if hasX {
					changed = true
					continue
				}
				hasX = true
			case strings.EqualFold(toolName, xaiWebSearchToolType) || strings.EqualFold(toolName, xaiXSearchToolType):
				// e.g. {"type":"function","name":"web_search"} duplicates native
				// inject and triggers invalid-argument 400.
				changed = true
				continue
			}
			updated, errSet := sjson.SetRawBytes(filtered, "-1", []byte(tool.Raw))
			if errSet != nil {
				return body
			}
			filtered = updated
		}
	} else {
		// No tools array yet — we will create one with just the natives.
		changed = true
	}

	needWeb := !hasWeb
	needX := !hasX
	if !needWeb && !needX && !changed {
		return body
	}

	// Prepend missing native search tools so client tools keep relative order.
	outTools := filtered
	if needWeb || needX {
		prefix := make([]byte, 0, 64)
		prefix = append(prefix, '[')
		first := true
		if needWeb {
			prefix = append(prefix, `{"type":"web_search"}`...)
			first = false
			changed = true
		}
		if needX {
			if !first {
				prefix = append(prefix, ',')
			}
			prefix = append(prefix, `{"type":"x_search"}`...)
			changed = true
		}
		rest := outTools
		if len(rest) >= 2 && rest[0] == '[' {
			rest = rest[1:]
			if len(rest) == 1 && rest[0] == ']' {
				prefix = append(prefix, ']')
			} else {
				prefix = append(prefix, ',')
				prefix = append(prefix, rest...)
			}
		} else {
			prefix = append(prefix, ']')
		}
		outTools = prefix
	}

	if !changed {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "tools", outTools)
	if errSet != nil {
		return body
	}
	log.Debugf("xai: injected build cache search tools web_search=%v x_search=%v stripped_client_collisions=%v", needWeb, needX, changed && (needWeb || needX || true))
	return updated
}

// xaiShouldHideInjectedSearchResults is true only when cache-tool injection is
// enabled AND the operator opted into hiding server-search results from clients.
// Default is false: inject for cache routing while still exposing native search.
func xaiShouldHideInjectedSearchResults(cfg *config.Config) bool {
	return cfg != nil && cfg.XAI.InjectBuildSearchTools && cfg.XAI.HideInjectedSearchResults
}

func xaiIsInjectedServerToolType(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case xaiWebSearchCallType, xaiXSearchCallType, xaiWebSearchToolType, xaiXSearchToolType:
		return true
	default:
		return false
	}
}

func xaiIsInjectedServerToolEvent(eventData []byte) bool {
	if len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return false
	}
	eventType := gjson.GetBytes(eventData, "type").String()
	if strings.Contains(eventType, "web_search") || strings.Contains(eventType, "x_search") {
		return true
	}
	if itemType := gjson.GetBytes(eventData, "item.type").String(); xaiIsInjectedServerToolType(itemType) {
		return true
	}
	return false
}

// filterXAIInjectedServerToolPayload drops web_search/x_search items from
// response payloads. Callers must only invoke this when
// xaiShouldHideInjectedSearchResults(cfg) is true (inject + hide both on).
func filterXAIInjectedServerToolPayload(data []byte) []byte {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data
	}
	if itemType := gjson.GetBytes(data, "item.type").String(); xaiIsInjectedServerToolType(itemType) {
		eventType := gjson.GetBytes(data, "type").String()
		if strings.HasPrefix(eventType, "response.output_item.") {
			return nil
		}
	}
	out := data
	for _, path := range []string{"response.output", "output"} {
		arr := gjson.GetBytes(out, path)
		if !arr.Exists() || !arr.IsArray() {
			continue
		}
		filtered := make([]byte, 0, len(arr.Raw))
		filtered = append(filtered, '[')
		first := true
		changed := false
		for _, item := range arr.Array() {
			if xaiIsInjectedServerToolType(item.Get("type").String()) {
				changed = true
				continue
			}
			if !first {
				filtered = append(filtered, ',')
			}
			first = false
			filtered = append(filtered, item.Raw...)
		}
		if !changed {
			continue
		}
		filtered = append(filtered, ']')
		updated, errSet := sjson.SetRawBytes(out, path, filtered)
		if errSet != nil {
			continue
		}
		out = updated
	}
	return out
}

// xaiRecordDroppedOutputIndex records an output_index removed from the client
// stream (injected search tools) so later events can be renumbered densely.
func xaiRecordDroppedOutputIndex(eventData []byte, dropped map[int64]struct{}) {
	if dropped == nil || len(eventData) == 0 {
		return
	}
	idx := gjson.GetBytes(eventData, "output_index")
	if idx.Exists() {
		dropped[idx.Int()] = struct{}{}
	}
}

// xaiCompactOutputIndex rewrites output_index by subtracting how many dropped
// indexes are strictly below it. No-op when nothing was dropped or the field
// is absent. Keeps stream assemblers aligned with the filtered completed output.
func xaiCompactOutputIndex(eventData []byte, dropped map[int64]struct{}) []byte {
	if len(dropped) == 0 || len(eventData) == 0 {
		return eventData
	}
	idx := gjson.GetBytes(eventData, "output_index")
	if !idx.Exists() {
		return eventData
	}
	orig := idx.Int()
	var delta int64
	for d := range dropped {
		if d < orig {
			delta++
		}
	}
	if delta == 0 {
		return eventData
	}
	updated, errSet := sjson.SetBytes(eventData, "output_index", orig-delta)
	if errSet != nil {
		return eventData
	}
	return updated
}

// pruneXAIOrphanToolOutputs drops function_call_output / custom_tool_call_output
// items whose call_id has no matching function_call / custom_tool_call in the
// same input array. Used after item_reference stripping and reasoning-replay
// insertion so xAI never sees outputs without a call (422).
func pruneXAIOrphanToolOutputs(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	callIDs := make(map[string]struct{})
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case xaiFunctionCallType, xaiCustomToolCallType:
			if id := strings.TrimSpace(item.Get("call_id").String()); id != "" {
				callIDs[id] = struct{}{}
			}
		}
	}

	changed := false
	kept := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == xaiFunctionCallOutputType || itemType == xaiCustomToolCallOutputType {
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				changed = true
				log.Debugf("xai: dropping tool output without call_id")
				continue
			}
			if _, ok := callIDs[callID]; !ok {
				changed = true
				log.Debugf("xai: dropping orphan tool output call_id=%s (no matching call after item_reference/replay)", callID)
				continue
			}
		}
		kept = append(kept, json.RawMessage(item.Raw))
	}
	if !changed {
		return body
	}
	rawInput, errMarshal := json.Marshal(kept)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

// xaiOutputIndexIsDropped reports whether eventData's output_index was recorded
// as dropped (injected search tool). Used to suppress residual part/delta SSE
// that still points at a removed index.
func xaiOutputIndexIsDropped(eventData []byte, dropped map[int64]struct{}) bool {
	if len(dropped) == 0 || len(eventData) == 0 {
		return false
	}
	idx := gjson.GetBytes(eventData, "output_index")
	if !idx.Exists() {
		return false
	}
	_, ok := dropped[idx.Int()]
	return ok
}

// xaiTrackCustomOutputIndex records output_index when the event's item is a
// custom_tool_call (after remap). Indexes are the compacted client-facing ones.
func xaiTrackCustomOutputIndex(eventData []byte, custom map[int64]struct{}) {
	if custom == nil || len(eventData) == 0 {
		return
	}
	if strings.TrimSpace(gjson.GetBytes(eventData, "item.type").String()) != xaiCustomToolCallType {
		return
	}
	idx := gjson.GetBytes(eventData, "output_index")
	if idx.Exists() {
		custom[idx.Int()] = struct{}{}
	}
}

// translateXAICustomToolCallInputEvents rewrites function_call_arguments.* stream
// events into custom_tool_call_input.* when the matching output_index belongs to
// a remapped custom tool. OpenAI/Codex custom-tool clients expect input events,
// not function-argument events, once the item type is custom_tool_call.
//
// xAI streams promoted-function arguments as JSON fragments for
// {"input":"..."}. Emitting those fragments as custom_tool_call_input.delta
// would corrupt freeform input (clients would see the wrapper JSON). Deltas
// are therefore suppressed (returns nil); only arguments.done is translated
// into custom_tool_call_input.done with the unwrapped freeform string.
// Callers must treat a nil return as "drop this SSE event".
func translateXAICustomToolCallInputEvents(eventData []byte, customIndexes map[int64]struct{}) []byte {
	if len(customIndexes) == 0 || len(eventData) == 0 {
		return eventData
	}
	eventType := strings.TrimSpace(gjson.GetBytes(eventData, "type").String())
	idx := gjson.GetBytes(eventData, "output_index")
	if !idx.Exists() {
		return eventData
	}
	if _, ok := customIndexes[idx.Int()]; !ok {
		return eventData
	}

	switch eventType {
	case "response.function_call_arguments.delta":
		// Suppress partial JSON argument fragments for custom tools.
		return nil
	case "response.function_call_arguments.done":
		// fall through
	default:
		return eventData
	}

	out := eventData
	updated, errSet := sjson.SetBytes(out, "type", "response.custom_tool_call_input.done")
	if errSet != nil {
		return eventData
	}
	out = updated
	args := gjson.GetBytes(out, "arguments")
	if args.Exists() {
		inputText := xaiFunctionArgumentsToCustomInput(args.String())
		updated, errSet = sjson.SetBytes(out, "input", inputText)
		if errSet != nil {
			return eventData
		}
		out = updated
		updated, errDel := sjson.DeleteBytes(out, "arguments")
		if errDel != nil {
			return eventData
		}
		out = updated
	}
	return out
}

// collectXAIOriginalCustomToolNames records tools that were type=custom before
// normalizeXAITools rewrites them to function. Used only for response remapping.
func collectXAIOriginalCustomToolNames(body []byte) map[string]struct{} {
	names := make(map[string]struct{})
	var walk func(tool gjson.Result)
	walk = func(tool gjson.Result) {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case xaiCustomToolType:
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				names[name] = struct{}{}
			}
		case xaiNamespaceToolType:
			if nested := tool.Get("tools"); nested.IsArray() {
				for _, child := range nested.Array() {
					walk(child)
				}
			}
		}
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		for _, tool := range tools.Array() {
			walk(tool)
		}
	}
	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		for _, item := range input.Array() {
			switch strings.TrimSpace(item.Get("type").String()) {
			case xaiAdditionalToolsType:
				if tools := item.Get("tools"); tools.IsArray() {
					for _, tool := range tools.Array() {
						walk(tool)
					}
				}
			case xaiCustomToolCallType:
				// History may reference custom tools not re-declared in tools[].
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					names[name] = struct{}{}
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// remapXAICustomToolCallsInPayload rewrites function_call items that correspond
// to originally-custom tools back into custom_tool_call for Codex Desktop.
// No-op when customNames is empty; never touches ordinary function tools.
func remapXAICustomToolCallsInPayload(data []byte, customNames map[string]struct{}) []byte {
	if len(customNames) == 0 || len(data) == 0 {
		return data
	}
	out := data
	out = remapXAIFunctionCallObjectAt(out, "item", customNames)
	if arr := gjson.GetBytes(out, "response.output"); arr.IsArray() {
		for i := range arr.Array() {
			out = remapXAIFunctionCallObjectAt(out, fmt.Sprintf("response.output.%d", i), customNames)
		}
	}
	if arr := gjson.GetBytes(out, "output"); arr.IsArray() {
		for i := range arr.Array() {
			out = remapXAIFunctionCallObjectAt(out, fmt.Sprintf("output.%d", i), customNames)
		}
	}
	if gjson.GetBytes(out, "type").String() == xaiFunctionCallType {
		out = remapXAIFunctionCallObjectAt(out, "", customNames)
	}
	return out
}

func remapXAIFunctionCallObjectAt(data []byte, path string, customNames map[string]struct{}) []byte {
	obj := gjson.GetBytes(data, path)
	if path == "" {
		obj = gjson.ParseBytes(data)
	}
	if !obj.Exists() || !obj.IsObject() {
		return data
	}
	if strings.TrimSpace(obj.Get("type").String()) != xaiFunctionCallType {
		return data
	}
	name := strings.TrimSpace(obj.Get("name").String())
	if name == "" {
		return data
	}
	if _, ok := customNames[name]; !ok {
		return data
	}

	prefix := path
	if prefix != "" {
		prefix += "."
	}
	updated, errSet := sjson.SetBytes(data, prefix+"type", xaiCustomToolCallType)
	if errSet != nil {
		return data
	}
	data = updated

	inputText := xaiFunctionArgumentsToCustomInput(obj.Get("arguments").String())
	updated, errSet = sjson.SetBytes(data, prefix+"input", inputText)
	if errSet != nil {
		return data
	}
	data = updated
	if obj.Get("arguments").Exists() {
		updated, errDel := sjson.DeleteBytes(data, prefix+"arguments")
		if errDel != nil {
			return data
		}
		data = updated
	}
	return data
}

func xaiFunctionArgumentsToCustomInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || arguments == "{}" || arguments == "null" {
		return ""
	}
	if !gjson.Valid(arguments) {
		return arguments
	}
	parsed := gjson.Parse(arguments)
	if parsed.IsObject() {
		if input := parsed.Get("input"); input.Exists() {
			if input.Type == gjson.String {
				return input.String()
			}
			return input.Raw
		}
	}
	// Fall back to the raw arguments string so freeform source is not lost.
	return arguments
}

func normalizeXAITools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body
	}

	changed := false
	filtered := []byte(`[]`)
	for _, tool := range tools.Array() {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			namespaceName := tool.Get("name").String()
			if namespaceTools := tool.Get("tools"); namespaceTools.IsArray() {
				for _, nestedTool := range namespaceTools.Array() {
					nestedRaw, nestedChanged, ok := normalizeXAITool(nestedTool, namespaceName)
					if !ok {
						return body
					}
					changed = changed || nestedChanged
					if len(nestedRaw) == 0 {
						continue
					}
					updated, errSet := sjson.SetRawBytes(filtered, "-1", nestedRaw)
					if errSet != nil {
						return body
					}
					filtered = updated
				}
			}
			continue
		}
		raw, toolChanged, ok := normalizeXAITool(tool, "")
		if !ok {
			return body
		}
		changed = changed || toolChanged
		if len(raw) == 0 {
			continue
		}
		updated, errSet := sjson.SetRawBytes(filtered, "-1", raw)
		if errSet != nil {
			return body
		}
		filtered = updated
	}
	if !changed {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "tools", filtered)
	if errSet != nil {
		return body
	}
	return updated
}

// normalizeXAIToolChoiceForTools drops tool_choice and parallel_tool_calls
// when tools are absent or empty (including after normalizeXAITools filtering).
// xAI rejects payloads that include tool_choice without any tools defined.
// Existence checks avoid unnecessary sjson parse/copy passes.
func normalizeXAIToolChoiceForTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if hasTools {
		return body
	}
	if tools.Exists() {
		body, _ = sjson.DeleteBytes(body, "tools")
	}
	if gjson.GetBytes(body, "tool_choice").Exists() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
	}
	if gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		body, _ = sjson.DeleteBytes(body, "parallel_tool_calls")
	}
	return body
}

func normalizeXAITool(tool gjson.Result, namespaceName string) ([]byte, bool, bool) {
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType || toolType == xaiImageGenerationToolType {
		return nil, true, true
	}
	raw := []byte(tool.Raw)
	wasCustom := toolType == xaiCustomToolType
	if wasCustom {
		if tool.Get("name").String() == "apply_patch" {
			return nil, true, true
		}
		updatedTool, errSet := sjson.SetBytes(raw, "type", xaiFunctionToolType)
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		toolType = xaiFunctionToolType
		changed = true
		// Codex custom tools may carry grammar "format" instead of JSON schema.
		// xAI function tools only accept parameters; drop the OpenAI-only field.
		if gjson.GetBytes(raw, "format").Exists() {
			updatedTool, errDel := sjson.DeleteBytes(raw, "format")
			if errDel != nil {
				return nil, false, false
			}
			raw = updatedTool
		}
		// Freeform string input keeps exec/apply-style tools usable after conversion.
		// Do not replace an already-present properties schema.
		if !gjson.GetBytes(raw, "parameters.properties").Exists() {
			updatedTool, errSet = sjson.SetRawBytes(raw, "parameters", []byte(xaiCustomToolFunctionParameters))
			if errSet != nil {
				return nil, false, false
			}
			raw = updatedTool
			// freeform schema is not strict-compatible in the OpenAI sense
			if strict := gjson.GetBytes(raw, "strict"); strict.Exists() && strict.Bool() {
				updatedTool, errSet = sjson.SetBytes(raw, "strict", false)
				if errSet != nil {
					return nil, false, false
				}
				raw = updatedTool
			}
		}
	}
	if toolType == xaiWebSearchToolType && tool.Get("external_web_access").Exists() {
		updatedTool, errDel := sjson.DeleteBytes(raw, "external_web_access")
		if errDel != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	if toolType == xaiFunctionToolType && !wasCustom && !gjson.GetBytes(raw, "parameters").Exists() {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(`{"type":"object","properties":{}}`))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	// Codex Desktop's codex_app.automation_update schema hangs xAI free/build
	// streaming. Limit the workaround to that exact namespaced tool so unrelated
	// tools keep their parameter contracts.
	if toolType == xaiFunctionToolType && xaiFunctionParametersNeedSimplification(tool, namespaceName) {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(xaiSafeFunctionParameters))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		if strict := tool.Get("strict"); strict.Exists() && strict.Bool() {
			updatedTool, errSet = sjson.SetBytes(raw, "strict", false)
			if errSet != nil {
				return nil, false, false
			}
			raw = updatedTool
		}
		changed = true
		log.Debugf("xai: simplified parameters for tool %s.%s to avoid upstream hang", namespaceName, tool.Get("name").String())
	}
	return raw, changed, true
}

// xaiFunctionParametersNeedSimplification reports whether a function tool is
// the Codex Desktop automation tool known to hang xAI Responses streaming.
func xaiFunctionParametersNeedSimplification(tool gjson.Result, namespaceName string) bool {
	return strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), xaiFunctionToolType) &&
		strings.EqualFold(strings.TrimSpace(namespaceName), xaiCodexAppNamespaceName) &&
		strings.EqualFold(strings.TrimSpace(tool.Get("name").String()), xaiAutomationUpdateToolName)
}

func sanitizeXAIInputEncryptedContent(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	changed := false
	dropCount := 0
	firstReason := ""
	firstItemType := ""
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType != "reasoning" && itemType != "compaction" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if !encryptedContent.Exists() {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		reason := ""
		switch encryptedContent.Type {
		case gjson.String:
			if _, err := signature.InspectGrokEncryptedContent(encryptedContent.String()); err != nil {
				reason = err.Error()
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}

		if itemType == "compaction" {
			changed = true
			dropCount++
			if firstReason == "" {
				firstReason = reason
				firstItemType = itemType
			}
			continue
		}

		next, err := sjson.DeleteBytes([]byte(item.Raw), "encrypted_content")
		if err != nil {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(next))
		changed = true
		dropCount++
		if firstReason == "" {
			firstReason = reason
			firstItemType = itemType
		}
	}
	if !changed {
		return body
	}
	rawInput, err := json.Marshal(items)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "input", rawInput)
	if err != nil {
		return body
	}
	if dropCount > 0 {
		log.WithFields(log.Fields{
			"component":       "xai_encrypted_content_sanitizer",
			"dropped":         dropCount,
			"first_item_type": firstItemType,
			"first_reason":    firstReason,
		}).Debug("xai executor: removed invalid encrypted_content before upstream")
	}
	return mergeAdjacentXAIInputReasoningSummaries(updated)
}

func normalizeXAIInputReasoningItems(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	updated := body
	for i, item := range input.Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		contentPath := fmt.Sprintf("input.%d.content", i)
		if content := gjson.GetBytes(updated, contentPath); content.Exists() && content.Type == gjson.Null {
			updatedBody, errDel := sjson.DeleteBytes(updated, contentPath)
			if errDel != nil {
				return body
			}
			updated = updatedBody
		}
		encryptedContentPath := fmt.Sprintf("input.%d.encrypted_content", i)
		if encryptedContent := gjson.GetBytes(updated, encryptedContentPath); encryptedContent.Exists() && encryptedContent.Type == gjson.Null {
			updatedBody, errDel := sjson.DeleteBytes(updated, encryptedContentPath)
			if errDel != nil {
				return body
			}
			updated = updatedBody
		}
	}
	return mergeAdjacentXAIInputReasoningSummaries(updated)
}

func mergeAdjacentXAIInputReasoningSummaries(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	changed := false
	items := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		if len(items) > 0 && canMergeXAIReasoningSummary(items[len(items)-1], item) {
			merged, ok := appendXAIReasoningSummary(items[len(items)-1], item.Get("summary").Array())
			if ok {
				items[len(items)-1] = json.RawMessage(merged)
				changed = true
				continue
			}
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return body
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

func canMergeXAIReasoningSummary(previous json.RawMessage, current gjson.Result) bool {
	previousItem := gjson.ParseBytes(previous)
	if previousItem.Get("type").String() != "reasoning" || current.Get("type").String() != "reasoning" {
		return false
	}
	if !previousItem.Get("summary").IsArray() || !current.Get("summary").IsArray() {
		return false
	}
	if len(current.Get("summary").Array()) == 0 {
		return false
	}
	for name := range current.Map() {
		if name != "type" && name != "summary" {
			return false
		}
	}
	return true
}

func appendXAIReasoningSummary(previous json.RawMessage, currentSummary []gjson.Result) ([]byte, bool) {
	updated := []byte(previous)
	summary := gjson.GetBytes(updated, "summary")
	if !summary.IsArray() {
		return previous, false
	}
	nextIndex := len(summary.Array())
	for i, item := range currentSummary {
		updatedItem, errSet := sjson.SetRawBytes(updated, fmt.Sprintf("summary.%d", nextIndex+i), []byte(item.Raw))
		if errSet != nil {
			return previous, false
		}
		updated = updatedItem
	}
	return updated, true
}

func removeXAIEncryptedReasoningInclude(body []byte) []byte {
	include := gjson.GetBytes(body, "include")
	if !include.Exists() || !include.IsArray() {
		return body
	}
	kept := make([]string, 0, len(include.Array()))
	for _, item := range include.Array() {
		value := strings.TrimSpace(item.String())
		if value == "" || value == "reasoning.encrypted_content" {
			continue
		}
		kept = append(kept, value)
	}
	body, _ = sjson.SetBytes(body, "include", kept)
	return body
}

// xaiSupportsReasoningEffort reports whether the model accepts Responses API
// reasoning.effort. Capability comes from model registry thinking metadata
// (static models.json and dynamic registrations), not a hard-coded name allowlist.
func xaiSupportsReasoningEffort(model string) bool {
	name := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return false
	}
	info := registry.LookupModelInfo(name, "xai")
	if info == nil || info.Thinking == nil {
		return false
	}
	return len(info.Thinking.Levels) > 0
}

func xaiNormalizeReasoningSummaryEventLine(line []byte, eventName string) []byte {
	if eventName == "" && bytes.HasPrefix(line, xaiEventTag) {
		eventName = strings.TrimSpace(string(line[len(xaiEventTag):]))
	}
	eventName = xaiNormalizeReasoningSummaryEventName(eventName)
	if eventName == "" {
		return bytes.Clone(line)
	}
	return []byte("event: " + eventName)
}

func xaiNormalizeReasoningSummaryEventName(eventName string) string {
	switch eventName {
	case "response.reasoning_text.delta":
		return "response.reasoning_summary_text.delta"
	case "response.reasoning_text.done":
		return "response.reasoning_summary_part.done"
	default:
		return eventName
	}
}

func xaiNormalizeReasoningSummaryData(eventData []byte) []byte {
	if len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return eventData
	}

	normalized := eventData
	switch gjson.GetBytes(normalized, "type").String() {
	case "response.reasoning_text.delta":
		normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_text.delta")
		normalized = xaiNormalizeReasoningSummaryIndex(normalized)
	case "response.reasoning_text.done":
		normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.done")
		normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
		if text := gjson.GetBytes(normalized, "text"); text.Exists() {
			normalized, _ = sjson.SetBytes(normalized, "part.text", text.String())
		}
		normalized, _ = sjson.DeleteBytes(normalized, "text")
		normalized = xaiNormalizeReasoningSummaryIndex(normalized)
	case "response.content_part.added":
		if gjson.GetBytes(normalized, "part.type").String() == "reasoning_text" {
			normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.added")
			normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
			normalized = xaiNormalizeReasoningSummaryIndex(normalized)
		}
	case "response.content_part.done":
		if gjson.GetBytes(normalized, "part.type").String() == "reasoning_text" {
			normalized, _ = sjson.SetBytes(normalized, "type", "response.reasoning_summary_part.done")
			normalized, _ = sjson.SetBytes(normalized, "part.type", "summary_text")
			normalized = xaiNormalizeReasoningSummaryIndex(normalized)
		}
	}

	if item := gjson.GetBytes(normalized, "item"); item.Exists() && item.Type == gjson.JSON {
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			normalized, _ = sjson.SetRawBytes(normalized, "item", updatedItem)
		}
	}
	if output := gjson.GetBytes(normalized, "response.output"); output.IsArray() {
		updatedOutput, changed := xaiNormalizeReasoningOutputItems(output.Array())
		if changed {
			normalized, _ = sjson.SetRawBytes(normalized, "response.output", updatedOutput)
		}
	}

	return normalized
}

func xaiNormalizeReasoningSummaryDataEvents(eventData []byte) [][]byte {
	if len(eventData) == 0 || !gjson.ValidBytes(eventData) {
		return [][]byte{eventData}
	}
	if gjson.GetBytes(eventData, "type").String() != "response.reasoning_text.done" {
		return [][]byte{xaiNormalizeReasoningSummaryData(eventData)}
	}

	textDone, _ := sjson.SetBytes(eventData, "type", "response.reasoning_summary_text.done")
	textDone = xaiNormalizeReasoningSummaryIndex(textDone)
	partDone := xaiNormalizeReasoningSummaryData(eventData)
	return [][]byte{textDone, partDone}
}

func xaiNormalizeReasoningSummaryIndex(eventData []byte) []byte {
	contentIndex := gjson.GetBytes(eventData, "content_index")
	if contentIndex.Exists() && contentIndex.Raw != "" && !gjson.GetBytes(eventData, "summary_index").Exists() {
		eventData, _ = sjson.SetRawBytes(eventData, "summary_index", []byte(contentIndex.Raw))
	}
	eventData, _ = sjson.DeleteBytes(eventData, "content_index")
	return eventData
}

func xaiNormalizeReasoningOutputItems(items []gjson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			changed = true
		}
		buf.Write(updatedItem)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiNormalizeReasoningOutputItem(item []byte) []byte {
	if !gjson.ValidBytes(item) || gjson.GetBytes(item, "type").String() != "reasoning" {
		return item
	}

	normalized := item
	if summary := gjson.GetBytes(normalized, "summary"); summary.IsArray() {
		updatedSummary, changed := xaiNormalizeReasoningSummaryItems(summary.Array())
		if changed {
			normalized, _ = sjson.SetRawBytes(normalized, "summary", updatedSummary)
		}
	}

	content := gjson.GetBytes(normalized, "content")
	if !content.IsArray() {
		return normalized
	}

	summaryItems := make([]gjson.Result, 0, len(content.Array()))
	for _, part := range content.Array() {
		if part.Get("type").String() == "reasoning_text" {
			summaryItems = append(summaryItems, part)
		}
	}
	if len(summaryItems) == 0 {
		return normalized
	}

	updatedSummary, _ := xaiNormalizeReasoningSummaryItems(summaryItems)
	normalized, _ = sjson.SetRawBytes(normalized, "summary", updatedSummary)
	normalized, _ = sjson.DeleteBytes(normalized, "content")
	return normalized
}

func xaiNormalizeReasoningSummaryItems(items []gjson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		itemRaw := []byte(item.Raw)
		if item.Get("type").String() == "reasoning_text" {
			var errSet error
			itemRaw, errSet = sjson.SetBytes(itemRaw, "type", "summary_text")
			if errSet == nil {
				changed = true
			}
		}
		buf.Write(itemRaw)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiCollectOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func xaiPatchCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	outputResult := gjson.GetBytes(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	outputArray := []byte("[]")
	var buf bytes.Buffer
	buf.WriteByte('[')
	wrote := false
	for _, idx := range indexes {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(outputItemsByIndex[idx])
		wrote = true
	}
	for _, item := range outputItemsFallback {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(item)
		wrote = true
	}
	buf.WriteByte(']')
	if wrote {
		outputArray = buf.Bytes()
	}

	patched, _ := sjson.SetRawBytes(eventData, "response.output", outputArray)
	return patched
}

// xaiFreeUsageExhaustedCooldown is the free-tier rolling window advertised by
// cli-chat-proxy ("Usage resets over a rolling 24-hour window").
const xaiFreeUsageExhaustedCooldown = 24 * time.Hour

// xaiStatusErr wraps upstream error bodies so free-tier exhaustion
// (subscription:free-usage-exhausted) carries a 24h RetryAfter hint for
// auth cooldown / account rotation. Generic 429s stay without an explicit
// retry hint so conductor backoff still applies.
func xaiStatusErr(code int, body []byte) statusErr {
	err := statusErr{code: code, msg: string(body)}
	if code != http.StatusTooManyRequests || len(body) == 0 {
		return err
	}
	codeStr := strings.ToLower(gjson.GetBytes(body, "code").String())
	msg := strings.ToLower(gjson.GetBytes(body, "error").String())
	if msg == "" {
		msg = strings.ToLower(string(body))
	}
	if strings.Contains(codeStr, "free-usage-exhausted") ||
		strings.Contains(msg, "free-usage-exhausted") ||
		strings.Contains(msg, "included free usage") {
		d := xaiFreeUsageExhaustedCooldown
		err.retryAfter = &d
	}
	return err
}
