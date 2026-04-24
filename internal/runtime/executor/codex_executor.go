package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
)

// codexUserAgent is the default User-Agent string used when no explicit
// client-, config-, or auth-file- provided value is available. It is built
// dynamically at startup by misc.BuildCodexUserAgent so the proxy emits a
// plausible fingerprint for the actual host OS/arch/terminal rather than a
// hard-coded Linux string.
var codexUserAgent = misc.CodexCLIUserAgent

const codexOriginator = misc.CodexCLIOriginator

var dataTag = []byte("data:")

type codexStreamFunctionCallState struct {
	ItemID      string
	CallID      string
	Name        string
	Arguments   string
	OutputIndex int64
}

type codexStreamCompletionState struct {
	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte
	functionCallsByItem map[string]*codexStreamFunctionCallState
}

type codexCompletedStreamEvent struct {
	data           []byte
	recoveredCount int
}

func newCodexStreamCompletionState() *codexStreamCompletionState {
	return &codexStreamCompletionState{
		outputItemsByIndex:  make(map[int64][]byte),
		functionCallsByItem: make(map[string]*codexStreamFunctionCallState),
	}
}

func (s *codexStreamCompletionState) functionCallByItem(itemID string, outputIndex int64) *codexStreamFunctionCallState {
	if s == nil {
		return nil
	}
	itemID = strings.TrimSpace(itemID)
	if itemID != "" {
		if state, ok := s.functionCallsByItem[itemID]; ok && state != nil {
			return state
		}
	}
	if outputIndex < 0 {
		return nil
	}
	for _, state := range s.functionCallsByItem {
		if state != nil && state.OutputIndex == outputIndex {
			return state
		}
	}
	return nil
}

func codexEventData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, dataTag) {
		return nil, false
	}
	return bytes.TrimSpace(line[len(dataTag):]), true
}

func codexSSEDataLine(data []byte) []byte {
	line := make([]byte, 0, len(dataTag)+1+len(data))
	line = append(line, dataTag...)
	line = append(line, ' ')
	line = append(line, data...)
	return line
}

func codexEventType(eventData []byte) string {
	if len(eventData) == 0 {
		return ""
	}
	return gjson.GetBytes(eventData, "type").String()
}

func (s *codexStreamCompletionState) recordEvent(eventData []byte) {
	s.recordEventWithType(codexEventType(eventData), eventData)
}

func (s *codexStreamCompletionState) recordEventWithType(eventType string, eventData []byte) {
	if s == nil || len(eventData) == 0 {
		return
	}

	switch eventType {
	case "response.output_item.done":
		itemResult := gjson.GetBytes(eventData, "item")
		if !itemResult.Exists() || itemResult.Type != gjson.JSON {
			return
		}
		itemBytes := []byte(itemResult.Raw)
		outputIndexResult := gjson.GetBytes(eventData, "output_index")
		if outputIndexResult.Exists() {
			s.outputItemsByIndex[outputIndexResult.Int()] = itemBytes
			return
		}
		s.outputItemsFallback = append(s.outputItemsFallback, itemBytes)
	case "response.output_item.added":
		item := gjson.GetBytes(eventData, "item")
		if !item.Exists() || item.Get("type").String() != "function_call" {
			return
		}
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			return
		}
		state := s.functionCallByItem(itemID, gjson.GetBytes(eventData, "output_index").Int())
		if state == nil {
			state = &codexStreamFunctionCallState{
				ItemID:      itemID,
				OutputIndex: gjson.GetBytes(eventData, "output_index").Int(),
			}
			s.functionCallsByItem[itemID] = state
		}
		if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
			state.CallID = callID
		}
		if name := strings.TrimSpace(item.Get("name").String()); name != "" {
			state.Name = name
		}
	case "response.function_call_arguments.delta":
		itemID := strings.TrimSpace(gjson.GetBytes(eventData, "item_id").String())
		outputIndex := gjson.GetBytes(eventData, "output_index").Int()
		state := s.functionCallByItem(itemID, outputIndex)
		if state == nil {
			return
		}
		if delta := gjson.GetBytes(eventData, "delta").String(); delta != "" {
			state.Arguments += delta
		}
	case "response.function_call_arguments.done":
		itemID := strings.TrimSpace(gjson.GetBytes(eventData, "item_id").String())
		outputIndex := gjson.GetBytes(eventData, "output_index").Int()
		state := s.functionCallByItem(itemID, outputIndex)
		if state == nil {
			return
		}
		if arguments := gjson.GetBytes(eventData, "arguments").String(); arguments != "" {
			state.Arguments = arguments
		}
	}
}

func (s *codexStreamCompletionState) processEventData(eventData []byte, patchCompleted bool) (codexCompletedStreamEvent, bool) {
	return s.processEventDataWithType(codexEventType(eventData), eventData, patchCompleted)
}

func (s *codexStreamCompletionState) processEventDataWithType(eventType string, eventData []byte, patchCompleted bool) (codexCompletedStreamEvent, bool) {
	if s == nil || len(eventData) == 0 {
		return codexCompletedStreamEvent{}, false
	}

	s.recordEventWithType(eventType, eventData)
	if eventType != "response.completed" {
		return codexCompletedStreamEvent{}, false
	}

	completed := codexCompletedStreamEvent{data: eventData}
	if patchCompleted {
		if patched, recoveredCount := s.patchCompletedOutputIfEmpty(eventData); recoveredCount > 0 {
			completed.data = patched
			completed.recoveredCount = recoveredCount
		}
	}
	return completed, true
}

func (s *codexStreamCompletionState) patchCompletedOutputIfEmpty(completedData []byte) ([]byte, int) {
	if s == nil || len(completedData) == 0 {
		return completedData, 0
	}

	outputResult := gjson.GetBytes(completedData, "response.output")
	if outputResult.Exists() && outputResult.IsArray() && len(outputResult.Array()) > 0 {
		return completedData, 0
	}

	type recoveredItem struct {
		outputIndex int64
		raw         []byte
	}

	recovered := make([]recoveredItem, 0, len(s.outputItemsByIndex)+len(s.outputItemsFallback)+len(s.functionCallsByItem))
	seenCallIDs := make(map[string]struct{}, len(s.functionCallsByItem))
	seenItemIDs := make(map[string]struct{}, len(s.functionCallsByItem))

	indexes := make([]int64, 0, len(s.outputItemsByIndex))
	for idx := range s.outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for _, idx := range indexes {
		raw := s.outputItemsByIndex[idx]
		recovered = append(recovered, recoveredItem{outputIndex: idx, raw: raw})
		if callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()); callID != "" {
			seenCallIDs[callID] = struct{}{}
		}
		if itemID := strings.TrimSpace(gjson.GetBytes(raw, "id").String()); itemID != "" {
			seenItemIDs[itemID] = struct{}{}
		}
	}
	for _, raw := range s.outputItemsFallback {
		recovered = append(recovered, recoveredItem{outputIndex: int64(len(indexes) + len(recovered)), raw: raw})
		if callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()); callID != "" {
			seenCallIDs[callID] = struct{}{}
		}
		if itemID := strings.TrimSpace(gjson.GetBytes(raw, "id").String()); itemID != "" {
			seenItemIDs[itemID] = struct{}{}
		}
	}

	if len(s.functionCallsByItem) > 0 {
		keys := make([]string, 0, len(s.functionCallsByItem))
		for key := range s.functionCallsByItem {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left := s.functionCallsByItem[keys[i]]
			right := s.functionCallsByItem[keys[j]]
			if left == nil || right == nil {
				return keys[i] < keys[j]
			}
			if left.OutputIndex != right.OutputIndex {
				return left.OutputIndex < right.OutputIndex
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			state := s.functionCallsByItem[key]
			if state == nil || strings.TrimSpace(state.CallID) == "" {
				continue
			}
			if _, ok := seenCallIDs[state.CallID]; ok {
				continue
			}
			if _, ok := seenItemIDs[state.ItemID]; ok {
				continue
			}

			args := state.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			itemID := state.ItemID
			if strings.TrimSpace(itemID) == "" {
				itemID = fmt.Sprintf("fc_%s", state.CallID)
			}

			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", itemID)
			item, _ = sjson.SetBytes(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", state.CallID)
			item, _ = sjson.SetBytes(item, "name", state.Name)
			recovered = append(recovered, recoveredItem{outputIndex: state.OutputIndex, raw: item})
			seenCallIDs[state.CallID] = struct{}{}
			seenItemIDs[itemID] = struct{}{}
		}
	}

	if len(recovered) == 0 {
		return completedData, 0
	}

	sort.SliceStable(recovered, func(i, j int) bool {
		return recovered[i].outputIndex < recovered[j].outputIndex
	})

	patched := completedData
	outputArray := []byte("[]")
	if len(recovered) > 0 {
		var buf bytes.Buffer
		totalLen := 2
		for _, item := range recovered {
			totalLen += len(item.raw)
		}
		if len(recovered) > 1 {
			totalLen += len(recovered) - 1
		}
		buf.Grow(totalLen)
		buf.WriteByte('[')
		for i, item := range recovered {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(item.raw)
		}
		buf.WriteByte(']')
		outputArray = buf.Bytes()
	}
	patched, _ = sjson.SetRawBytes(patched, "response.output", outputArray)
	return patched, len(recovered)
}

// CodexExecutor executes Codex requests and reuses per-proxy auth services for refresh flows.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg            *config.Config
	codexAuthCache sync.Map
	responseDedupe helps.InFlightGroup[codexNonStreamHTTPResult]
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

type codexPreparedHTTPCall struct {
	url        string
	prepared   codexPreparedRequest
	requestLog helps.UpstreamRequestLog
}

func (e *CodexExecutor) prepareCodexHTTPCall(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	from sdktranslator.Format,
	url string,
	req cliproxyexecutor.Request,
	body []byte,
	token string,
	stream bool,
) (codexPreparedHTTPCall, error) {
	prepared, err := e.prepareCodexRequest(ctx, from, url, req, body)
	if err != nil {
		return codexPreparedHTTPCall{}, err
	}
	applyCodexHeaders(prepared.httpReq, auth, token, stream, e.cfg)
	if err := maybeEnableCodexRequestCompression(prepared.httpReq, auth); err != nil {
		return codexPreparedHTTPCall{}, fmt.Errorf("codex executor: request compression failed: %w", err)
	}
	return codexPreparedHTTPCall{
		url:      url,
		prepared: prepared,
		requestLog: codexUpstreamRequestLog(
			url,
			http.MethodPost,
			prepared.httpReq.Header,
			prepared.body,
			e.Identifier(),
			auth,
		),
	}, nil
}

func codexUpstreamRequestLog(
	url string,
	method string,
	headers http.Header,
	body []byte,
	provider string,
	auth *cliproxyauth.Auth,
) helps.UpstreamRequestLog {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	return helps.UpstreamRequestLog{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body = helps.EditJSONBytes(body,
		helps.SetJSONEdit("model", baseModel),
		helps.SetJSONEdit("stream", true),
		helps.DeleteJSONEdit("previous_response_id"),
		helps.DeleteJSONEdit("prompt_cache_retention"),
		helps.DeleteJSONEdit("safety_identifier"),
		helps.DeleteJSONEdit("stream_options"),
	)
	body = normalizeCodexInstructions(body)
	body = ensureImageGenerationTool(body, baseModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, url, req, body, apiKey, true)
	if err != nil {
		return resp, err
	}
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)
	result, usageOwner, err := e.fetchCodexResponsesAggregate(ctx, auth, call.url, call.prepared)
	if err != nil {
		return resp, err
	}
	captureCodexSessionHeaders(codexSessionKey(auth, call.prepared.promptCacheID), call.prepared.promptCacheID, result.headers)
	if result.statusCode < 200 || result.statusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", result.statusCode, helps.SummarizeErrorBody(result.headers.Get("Content-Type"), result.body))
		err = newCodexStatusErr(result.statusCode, result.body)
		return resp, err
	}
	if len(result.completedData) > 0 {
		if usageOwner {
			if detail, ok := helps.ParseCodexUsage(result.completedData); ok {
				reporter.Publish(ctx, detail)
			}
			reporter.EnsurePublished(ctx)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, result.completedData, &param)
		resp = cliproxyexecutor.Response{Payload: out, Headers: result.headers.Clone()}
		return resp, nil
	}
	err = statusErr{code: 408, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body = helps.EditJSONBytes(body,
		helps.SetJSONEdit("model", baseModel),
		helps.DeleteJSONEdit("stream"),
	)
	body = normalizeCodexInstructions(body)
	body = ensureImageGenerationTool(body, baseModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, url, req, body, apiKey, false)
	if err != nil {
		return resp, err
	}
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)
	result, usageOwner, err := e.fetchCodexNonStreamResponse(ctx, auth, call.url, call.prepared)
	if err != nil {
		return resp, err
	}
	captureCodexSessionHeaders(codexSessionKey(auth, call.prepared.promptCacheID), call.prepared.promptCacheID, result.headers)
	if result.statusCode < 200 || result.statusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", result.statusCode, helps.SummarizeErrorBody(result.headers.Get("Content-Type"), result.body))
		err = newCodexStatusErr(result.statusCode, result.body)
		return resp, err
	}
	data := result.body
	if usageOwner {
		reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
		reporter.EnsurePublished(ctx)
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: result.headers.Clone()}
	return resp, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body = helps.EditJSONBytes(body,
		helps.DeleteJSONEdit("previous_response_id"),
		helps.DeleteJSONEdit("prompt_cache_retention"),
		helps.DeleteJSONEdit("safety_identifier"),
		helps.DeleteJSONEdit("stream_options"),
		helps.SetJSONEdit("model", baseModel),
	)
	body = normalizeCodexInstructions(body)
	body = ensureImageGenerationTool(body, baseModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, url, req, body, apiKey, true)
	if err != nil {
		return nil, err
	}
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(call.prepared.httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header)
	captureCodexSessionHeaders(codexSessionKey(auth, call.prepared.promptCacheID), call.prepared.promptCacheID, httpResp.Header)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, helps.StreamChunkBufferSize)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		var param any
		streamState := newCodexStreamCompletionState()
		terminalFailure := false
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		errRead := helps.ReadStreamLines(httpResp.Body, func(line []byte) error {
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if eventData, ok := codexEventData(line); ok {
				eventType := codexEventType(eventData)
				switch eventType {
				case "response.incomplete":
					// Mirror codex-rs: treat response.incomplete as a terminal
					// failure for telemetry purposes, but keep forwarding the
					// event to the downstream client so SDKs relying on it for
					// signalling (rate limits, safety stops, etc.) still work.
					reason := gjson.GetBytes(eventData, "response.incomplete_details.reason").String()
					if reason == "" {
						reason = "unknown"
					}
					log.Warnf("codex stream terminated with response.incomplete: reason=%s", reason)
					terminalFailure = true
				case "response.failed":
					message := gjson.GetBytes(eventData, "response.error.message").String()
					if message == "" {
						message = "response.failed"
					}
					log.Warnf("codex stream terminated with response.failed: %s", message)
					terminalFailure = true
				}
				if completed, isCompleted := streamState.processEventDataWithType(eventType, eventData, true); isCompleted {
					if detail, ok := helps.ParseCodexUsage(completed.data); ok {
						reporter.Publish(ctx, detail)
					}
					if completed.recoveredCount > 0 {
						log.Warnf(
							"codex stream completed with empty response.output; recovered_items=%d cached_done_items=%d cached_function_calls=%d",
							completed.recoveredCount,
							len(streamState.outputItemsByIndex)+len(streamState.outputItemsFallback),
							len(streamState.functionCallsByItem),
						)
						line = codexSSEDataLine(completed.data)
					}
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, line, &param)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					return ctx.Err()
				}
			}
			return nil
		})
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			reporter.PublishFailure(ctx)
			_ = send(cliproxyexecutor.StreamChunk{Err: errRead})
		} else if terminalFailure {
			reporter.PublishFailure(ctx)
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body = helps.EditJSONBytes(body,
		helps.SetJSONEdit("model", baseModel),
		helps.DeleteJSONEdit("previous_response_id"),
		helps.DeleteJSONEdit("prompt_cache_retention"),
		helps.DeleteJSONEdit("safety_identifier"),
		helps.DeleteJSONEdit("stream_options"),
		helps.SetJSONEdit("stream", false),
	)
	body = normalizeCodexInstructions(body)

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := e.codexAuthService(auth)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) codexAuthService(auth *cliproxyauth.Auth) *codexauth.CodexAuth {
	proxyURL := e.codexAuthProxyURL(auth)
	if cached, ok := e.codexAuthCache.Load(proxyURL); ok {
		if svc, okSvc := cached.(*codexauth.CodexAuth); okSvc {
			return svc
		}
	}

	svc := codexauth.NewCodexAuthWithProxyURL(e.cfg, proxyURL)
	actual, _ := e.codexAuthCache.LoadOrStore(proxyURL, svc)
	if cached, ok := actual.(*codexauth.CodexAuth); ok {
		return cached
	}
	return svc
}

func (e *CodexExecutor) codexAuthProxyURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if e.cfg == nil {
		return ""
	}
	return strings.TrimSpace(e.cfg.ProxyURL)
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	// Replay any sticky session/turn state we previously captured. The
	// Session_id header was already set by prepareCodexRequest to the stable
	// prompt-cache id when available, so we can use it to rebuild the session
	// cache key here.
	if promptCacheID := strings.TrimSpace(r.Header.Get("Session_id")); promptCacheID != "" {
		_ = injectCodexSessionHeaders(r.Header, codexSessionKey(auth, promptCacheID))
	}

	if ginHeaders.Get("X-Codex-Beta-Features") != "" {
		r.Header.Set("X-Codex-Beta-Features", ginHeaders.Get("X-Codex-Beta-Features"))
	}
	misc.EnsureHeader(r.Header, ginHeaders, "Version", "")
	codexEnsureTurnMetadataHeader(r.Header, ginHeaders)
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-State", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-OpenAI-Subagent", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Traceparent", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Tracestate", "")
	identity := codexResolvedIdentity(r.Header, ginHeaders, auth, cfg)
	r.Header.Set("User-Agent", identity.userAgent)
	codexEnsureSessionHeaders(r.Header, ginHeaders, auth)

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	if originator := firstNonEmptyHeaderValue(r.Header, ginHeaders, "Originator"); originator != "" {
		r.Header.Set("Originator", originator)
	} else if !codexIsAPIKeyAuth(auth) {
		r.Header.Set("Originator", codexOriginatorFor(cfg))
	}
	if residency := strings.TrimSpace(ginHeaders.Get(misc.CodexResidencyHeader)); residency != "" {
		r.Header.Set(misc.CodexResidencyHeader, residency)
	} else if !codexIsAPIKeyAuth(auth) {
		if residency := codexResidencyFor(cfg); residency != "" && strings.TrimSpace(r.Header.Get(misc.CodexResidencyHeader)) == "" {
			r.Header.Set(misc.CodexResidencyHeader, residency)
		}
	}
	if !codexIsAPIKeyAuth(auth) {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				if trimmed := strings.TrimSpace(accountID); trimmed != "" {
					r.Header.Set("Chatgpt-Account-Id", trimmed)
				}
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
	if cfgUserAgent := codexConfiguredUserAgent(cfg, auth); cfgUserAgent != "" {
		r.Header.Set("User-Agent", cfgUserAgent)
	}
}

// codexOriginatorFor resolves the originator value for the given config,
// honouring config > env > built-in default.
func codexOriginatorFor(cfg *config.Config) string {
	configured := ""
	if cfg != nil {
		configured = cfg.CodexHeaderDefaults.Originator
	}
	return misc.ResolveCodexOriginator(configured)
}

// codexResidencyFor resolves the residency header value; empty means "do not
// send" (matches codex-rs behaviour).
func codexResidencyFor(cfg *config.Config) string {
	configured := ""
	if cfg != nil {
		configured = cfg.CodexHeaderDefaults.Residency
	}
	return misc.ResolveCodexResidency(configured)
}

func codexAuthUserAgent(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if ua := strings.TrimSpace(auth.Attributes["header:User-Agent"]); ua != "" {
			return ua
		}
		if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
			return ua
		}
		if ua := strings.TrimSpace(auth.Attributes["user-agent"]); ua != "" {
			return ua
		}
	}
	if auth.Metadata == nil {
		return ""
	}
	if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
		return strings.TrimSpace(ua)
	}
	if ua, ok := auth.Metadata["user-agent"].(string); ok && strings.TrimSpace(ua) != "" {
		return strings.TrimSpace(ua)
	}
	return ""
}

func newCodexStatusErr(statusCode int, body []byte) statusErr {
	errCode := statusCode
	if isCodexModelCapacityError(body) {
		errCode = http.StatusTooManyRequests
	}
	err := statusErr{code: errCode, msg: string(body)}
	if retryAfter := parseCodexRetryAfter(errCode, body, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err
}

func normalizeCodexInstructions(body []byte) []byte {
	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null {
		body, _ = helps.SetJSONBytes(body, "instructions", "")
	}
	return body
}

var imageGenToolJSON = []byte(`{"type":"image_generation","output_format":"png"}`)
var imageGenToolArrayJSON = []byte(`[{"type":"image_generation","output_format":"png"}]`)

func ensureImageGenerationTool(body []byte, baseModel string) []byte {
	if strings.HasSuffix(baseModel, "spark") {
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tools", imageGenToolArrayJSON)
		return body
	}
	for _, t := range tools.Array() {
		if t.Get("type").String() == "image_generation" {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tools.-1", imageGenToolJSON)
	return body
}

func isCodexModelCapacityError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(errorBody, "error.type").String()) != "usage_limit_reached" {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	if resetsInSeconds := gjson.GetBytes(errorBody, "error.resets_in_seconds").Int(); resetsInSeconds > 0 {
		retryAfter := time.Duration(resetsInSeconds) * time.Second
		return &retryAfter
	}
	return nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
