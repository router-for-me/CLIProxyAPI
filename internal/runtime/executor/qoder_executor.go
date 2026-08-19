package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// QoderExecutor executes requests against the Qoder API with COSY authentication
type QoderExecutor struct {
	cfg *config.Config
}

// NewQoderExecutor creates a new Qoder executor
func NewQoderExecutor(cfg *config.Config) *QoderExecutor {
	return &QoderExecutor{
		cfg: cfg,
	}
}

// Identifier returns the provider identifier
func (e *QoderExecutor) Identifier() string {
	return "qoder"
}

// ExecuteStream executes a streaming request against Qoder API
func (e *QoderExecutor) ExecuteStream(ctx context.Context, authRecord *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	// Get token storage from auth record
	storage, ok := authRecord.Storage.(*qoderauth.QoderTokenStorage)
	if !ok {
		return nil, fmt.Errorf("invalid auth storage type for qoder: %T", authRecord.Storage)
	}

	// Note: Qoder device tokens are long-lived (~30 days) and the upstream
	// /algo/api/v3/user/refresh_token endpoint returns 403 for them — see
	// QoderExecutor.Refresh's no-op rationale. We deliberately do not call
	// RefreshTokenIfNeeded per request: it would just produce a 403 in the
	// log on every chat call. Token expiry is handled by the user re-running
	// --qoder-login.

	// Translate non-openai formats to chat completions before extracting messages
	payload := req.Payload
	if opts.SourceFormat != "" && opts.SourceFormat != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, req.Model, payload, false)
	}

	// Parse request to get model and messages
	var chatReq map[string]interface{}
	if err := json.Unmarshal(payload, &chatReq); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	// Map model name — strip provider prefix so qoder/auto → auto
	model := req.Model
	if model == "" {
		model, _ = chatReq["model"].(string)
	}
	qoderModel, err := validateQoderModel(model, storage)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, qoderModel, authRecord)
	defer reporter.TrackFailure(ctx, &err)

	// Normalize messages: flatten Anthropic/OpenAI multipart content arrays
	// to plain strings (Qoder's chat endpoint expects content to be a string).
	// tool_calls / role:"tool" turns pass through verbatim — Qoder accepts
	// the canonical OpenAI structure and emits real tool_use events.
	messagesRaw, _ := chatReq["messages"].([]interface{})
	toolsRaw := chatReq["tools"]
	normalized, systemText := normalizeQoderMessages(messagesRaw)

	// Resolve the per-model server-side metadata (is_vl, is_reasoning,
	// max_input_tokens, ...). Failing here is a hard error — sending the
	// wrong block silently downgrades to a different model.
	modelConfig, err := buildQoderModelConfig(storage, qoderModel)
	if err != nil {
		return nil, err
	}

	isReasoning, _ := modelConfig["is_reasoning"].(bool)
	maxOutputTokens, _ := modelConfig["max_output_tokens"].(float64)

	// Last user message text — used by Qoder for the chat_context "current
	// turn" preview slot. The full conversation still goes through `messages`.
	lastUser := lastUserText(normalized)

	// Stable IDs derived from content so retries hit upstream caches.
	// session_id is stable per user+model (routing affinity).
	// chat_record_id is deterministic per payload (dedup/cache key).
	sessionID := stableHash("qoder-session", storage.UserID, qoderModel)
	recordID := stableChatRecordID(qoderModel, normalized, toolsRaw, int(maxOutputTokens))

	// Start with the model's maximum output tokens, then clamp to
	// any user-requested limit so callers can cap cost/latency/UI.
	maxTokens := 32768
	if maxOutputTokens > 0 {
		maxTokens = int(maxOutputTokens)
	}
	if userMax, ok := chatReq["max_tokens"].(float64); ok && userMax > 0 {
		if int(userMax) < maxTokens {
			maxTokens = int(userMax)
		}
	}
	if userMax, ok := chatReq["max_completion_tokens"].(float64); ok && userMax > 0 {
		if int(userMax) < maxTokens {
			maxTokens = int(userMax)
		}
	}

	reqBody := map[string]interface{}{
		"request_id":       uuid.New().String(),
		"request_set_id":   recordID,
		"chat_record_id":   recordID,
		"session_id":       sessionID,
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"code_language":    "",
		"chat_prompt":      "",
		"image_urls":       nil,
		"aliyun_user_type": "",
		"system":           systemText,
		"messages":         normalized,
		"tools":            []interface{}{},
		"parameters":       map[string]interface{}{"max_tokens": maxTokens},
		"chat_context": map[string]interface{}{
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]interface{}{
				"context": []interface{}{},
				"modelConfig": map[string]interface{}{
					"key":          qoderModel,
					"is_reasoning": isReasoning,
				},
				"originalContent": lastUser,
			},
			"features": []interface{}{},
			"text":     lastUser,
		},
		"model_config": modelConfig,
		"business": map[string]interface{}{
			"product":  "cli",
			"version":  "1.0.0",
			"type":     "agent",
			"stage":    "start",
			"id":       uuid.New().String(),
			"name":     truncate(lastUser, 30),
			"begin_at": time.Now().UnixMilli(),
		},
	}
	if toolsRaw != nil {
		reqBody["tools"] = toolsRaw
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	// Encode the body to bypass Alibaba Cloud WAF pattern matching.
	// The server decodes when &Encode=1 is present in the URL.
	encodedBytes := []byte(helps.QoderEncodeBody(bodyBytes))

	headers, err := qoderauth.BuildAuthHeaders(
		encodedBytes,
		qoderauth.QoderChatURLEncoded,
		qoderauth.CosyCredentials{
			UserID:    storage.UserID,
			AuthToken: storage.Token,
			Name:      storage.Name,
			Email:     storage.Email,
			MachineID: storage.MachineID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build COSY auth: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", qoderauth.QoderChatURLEncoded, bytes.NewReader(encodedBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	headers.Apply(httpReq)
	modelSource, _ := modelConfig["source"].(string)
	if modelSource == "" {
		modelSource = "system"
	}
	httpReq.Header.Set("X-Model-Key", qoderModel)
	httpReq.Header.Set("X-Model-Source", modelSource)
	// Disable automatic gzip — Accept-Encoding: gzip triggers signature
	// validation on the Qoder upstream and causes 403 Signature invalid.
	httpReq.Header.Set("Accept-Encoding", "identity")

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, authRecord, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		body, _ := io.ReadAll(httpResp.Body)
		allow := httpResp.Header.Get("Allow")
		server := httpResp.Header.Get("Server")
		bodyPreview := truncate(string(body), 500)
		log.WithFields(log.Fields{
			"url":            qoderauth.QoderChatURL,
			"server":         server,
			"content_type":   httpResp.Header.Get("Content-Type"),
			"x_request_id":   httpResp.Header.Get("X-Request-Id"),
			"x_eagleeye_id":  httpResp.Header.Get("Eagleeye-Traceid"),
			"x_oss_request":  httpResp.Header.Get("X-Oss-Request-Id"),
			"allow":          allow,
			"body_truncated": bodyPreview,
		}).Warnf("qoder: upstream %d allow=%q server=%q body=%q", httpResp.StatusCode, allow, server, bodyPreview)
		return nil, newQoderStatusError(httpResp.StatusCode, string(body))
	}

	// Create streaming channel
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = httpResp.Body.Close() }()

		// Shared across all TranslateStream calls in this stream — the
		// translator carries open-block / sequence state through it; a
		// per-chunk var would re-emit message_start on every delta.
		var streamParam any

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB max line

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			// Skip non-data lines
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}

			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimPrefix(data, []byte(" "))
			if bytes.Equal(data, []byte("[DONE]")) {
				emitDone(ctx, out, opts.SourceFormat, req.Model, opts.OriginalRequest, payload, &streamParam)
				reporter.EnsurePublished(ctx)
				return
			}

			// Parse Qoder response envelope
			var event map[string]interface{}
			if err := json.Unmarshal(data, &event); err != nil {
				continue
			}
			statusVal := 200
			if rawStatus, ok := event["statusCodeValue"]; ok {
				switch v := rawStatus.(type) {
				case float64:
					statusVal = int(v)
				case int:
					statusVal = v
				}
			}
			innerStr, _ := event["body"].(string)
			if statusVal != http.StatusOK {
				msg := innerStr
				if msg == "" {
					msg = fmt.Sprintf("upstream status %d", statusVal)
				}
				streamErr := newQoderStatusError(statusVal, msg)
				reporter.PublishFailure(ctx, streamErr)
				out <- cliproxyexecutor.StreamChunk{Err: streamErr}
				return
			}
			if innerStr == "" {
				continue
			}
			if innerStr == "[DONE]" {
				emitDone(ctx, out, opts.SourceFormat, req.Model, opts.OriginalRequest, payload, &streamParam)
				reporter.EnsurePublished(ctx)
				return
			}
			var inner map[string]interface{}
			if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
				continue
			}
			chunkBytes, err := buildOpenAIChunk(inner, model)
			if err != nil {
				continue
			}
			// Reconstruct an OpenAI-compatible SSE line ("data: {chunk}").
			// Qoder's upstream nests OpenAI chunks inside a
			// {statusCodeValue, body} envelope so unlike kimi/openai-compat/
			// codebuddy we can't forward the raw upstream line — we have to
			// rebuild the SSE frame here. The format matches what those
			// other executors feed into TranslateStream so the translators'
			// "expects data: prefix" assumption holds.
			ssePayload := append([]byte("data: "), chunkBytes...)
			if detail, ok := helps.ParseOpenAIStreamUsage(ssePayload); ok {
				reporter.Publish(ctx, detail)
			}

			// Always run through TranslateStream. When source==target
			// (OpenAI client) it strips the "data:" prefix and returns
			// raw JSON; the OpenAI handler then re-adds the SSE framing.
			// For cross-format clients (Anthropic/Gemini) it emits the
			// format-specific stream events (message_start /
			// content_block_delta / ...) directly as fully framed bytes
			// because those handlers write chunks verbatim.
			to := sdktranslator.FormatOpenAI
			from := opts.SourceFormat
			if from == "" {
				from = to
			}
			frames := sdktranslator.TranslateStream(ctx, to, from,
				req.Model, opts.OriginalRequest, payload, ssePayload, &streamParam)
			for _, frame := range frames {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
				case <-ctx.Done():
					return
				}
			}
		}
		// Scanner loop exited naturally (EOF). Emit a terminating
		// "data: [DONE]" / Anthropic message_stop frame so the client
		// closes the stream cleanly.
		emitDone(ctx, out, opts.SourceFormat, req.Model, opts.OriginalRequest, payload, &streamParam)
		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			streamErr := fmt.Errorf("scanner error: %w", err)
			reporter.PublishFailure(ctx, streamErr)
			out <- cliproxyexecutor.StreamChunk{Err: streamErr}
			return
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// lastUserText returns the text of the last user message in the (already
// normalized) message list, or empty when there isn't one. Qoder uses this
// for the chat_context "current turn" preview slot; the full conversation
// still travels through the messages array.
func lastUserText(messages []interface{}) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msgMap, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := msgMap["role"].(string); role != "user" {
			continue
		}
		if s, ok := msgMap["content"].(string); ok {
			return s
		}
		return extractContentGeneric(msgMap["content"])
	}
	return ""
}

// extractContentGeneric extracts text content from message content field
func extractContentGeneric(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if itemMap["type"] == "text" {
					if text, ok := itemMap["text"].(string); ok {
						parts = append(parts, text)
					}
					continue
				}
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content == nil {
			return ""
		}
		return fmt.Sprintf("%v", content)
	}
}

// normalizeQoderMessages clones each message and applies sanitizations
// required by Qoder's upstream:
//
//  1. Flatten content: Anthropic/OpenAI multipart content arrays
//     ([{type:"text",text:"..."}]) are collapsed to plain strings.
//
//  2. Remap system messages: Qoder rejects role="system" in the messages
//     array; system prompt content is collected and returned separately
//     so the caller can place it in the top-level "system" request field.
func normalizeQoderMessages(messages []interface{}) (normalized []interface{}, systemText string) {
	if len(messages) == 0 {
		return nil, ""
	}
	out := make([]interface{}, 0, len(messages))
	var systemParts []string
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		// Collect system messages — Qoder does not accept role="system"
		// in the messages array, so we remap them to the top-level
		// "system" request field.
		if role, _ := msgMap["role"].(string); role == "system" {
			if text := extractContentGeneric(msgMap["content"]); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		cloned := make(map[string]interface{}, len(msgMap))
		for k, v := range msgMap {
			cloned[k] = v
		}
		cloned["content"] = extractContentGeneric(msgMap["content"])
		out = append(out, cloned)
	}
	return out, strings.Join(systemParts, "\n\n")
}

func buildOpenAIChunk(inner map[string]interface{}, model string) ([]byte, error) {
	if inner == nil {
		return nil, fmt.Errorf("empty inner payload")
	}
	if _, ok := inner["model"]; !ok || inner["model"] == "" {
		inner["model"] = model
	}
	if choices, ok := inner["choices"].([]interface{}); ok {
		if len(choices) == 0 {
			if inner["finish_reason"] != nil || inner["stop"] != nil {
				inner["choices"] = []map[string]interface{}{{
					"index":         0,
					"delta":         map[string]interface{}{},
					"finish_reason": "stop",
				}}
			}
		}
	}
	return json.Marshal(inner)
}

// emitDone publishes the terminating SSE frame(s) for the stream. The
// upstream "[DONE]" sentinel is fed through TranslateStream so the
// client's SourceFormat dictates the actual wire bytes — "data: [DONE]\n\n"
// for OpenAI, "event: message_stop\ndata: {...}\n\n" for Anthropic, and
// the equivalent format-specific terminators for Gemini etc. This mirrors
// the pattern used by kimi_executor.
//
// param must be the same pointer the per-chunk TranslateStream calls used
// — the Anthropic translator (and others) need the carried state to know
// which content_block indices to close, the running token count, etc.
func emitDone(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk,
	sourceFormat sdktranslator.Format, reqModel string, originalReq, body []byte, param *any) {
	to := sdktranslator.FormatOpenAI
	from := sourceFormat
	if from == "" {
		from = to
	}
	frames := sdktranslator.TranslateStream(ctx, to, from,
		reqModel, originalReq, body, []byte("[DONE]"), param)
	for _, frame := range frames {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
		case <-ctx.Done():
			return
		}
	}
}

// qoderStatusError implements StatusError for Qoder API errors
type qoderStatusError struct {
	status  int
	message string
}

func newQoderStatusError(status int, message string) *qoderStatusError {
	return &qoderStatusError{status: status, message: message}
}

func (e *qoderStatusError) Error() string {
	return fmt.Sprintf("Qoder API error %d: %s", e.status, e.message)
}

func (e *qoderStatusError) StatusCode() int {
	return e.status
}

// CountTokens estimates token count for the request (placeholder implementation)
func (e *QoderExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Translate non-openai formats before extracting messages
	payload := req.Payload
	if opts.SourceFormat != "" && opts.SourceFormat != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, req.Model, payload, false)
	}

	// Simple estimation: 1 token ≈ 4 characters
	var chatReq map[string]interface{}
	if err := json.Unmarshal(payload, &chatReq); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	messagesRaw, _ := chatReq["messages"].([]interface{})
	totalChars := 0
	for _, msg := range messagesRaw {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			content := extractContentGeneric(msgMap["content"])
			totalChars += len(content)
		}
	}

	estimatedTokens := totalChars / 4
	if estimatedTokens < 1 {
		estimatedTokens = 1
	}

	response := map[string]interface{}{
		"usage": map[string]int{
			"prompt_tokens":     estimatedTokens,
			"completion_tokens": 0,
			"total_tokens":      estimatedTokens,
		},
	}

	responseBytes, _ := json.Marshal(response)
	return cliproxyexecutor.Response{
		Payload: responseBytes,
	}, nil
}

// Execute executes a non-streaming request against Qoder API
func (e *QoderExecutor) Execute(ctx context.Context, authRecord *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// We need ExecuteStream to:
	//   1. Translate the request payload from the client's SourceFormat
	//      (Anthropic/Gemini/etc) into OpenAI before sending to Qoder.
	//   2. Emit raw OpenAI chunks so we can accumulate choices[0].delta.
	//
	// (1) requires opts.SourceFormat to stay as the original; (2) requires
	// it to be OpenAI. Resolve by translating the payload up-front, then
	// passing FormatOpenAI for both directions to ExecuteStream.
	internalReq := req
	internalOpts := opts
	if opts.SourceFormat != "" && opts.SourceFormat != sdktranslator.FormatOpenAI {
		internalReq.Payload = sdktranslator.TranslateRequest(
			opts.SourceFormat, sdktranslator.FormatOpenAI,
			req.Model, req.Payload, false)
	}
	internalOpts.SourceFormat = sdktranslator.FormatOpenAI

	streamResult, err := e.ExecuteStream(ctx, authRecord, internalReq, internalOpts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	// Accumulate all chunks
	var content strings.Builder
	var finishReason string
	type pendingToolCall struct {
		ID        string
		Name      string
		Arguments string
	}
	pendingToolCalls := make(map[int]*pendingToolCall)

	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return cliproxyexecutor.Response{}, chunk.Err
		}

		// ExecuteStream was called with SourceFormat=FormatOpenAI so
		// TranslateStream strips the "data:" prefix and returns raw JSON.
		// Skip empty or [DONE] payloads.
		raw := chunk.Payload
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
			continue
		}

		var oiChunk map[string]interface{}
		if err := json.Unmarshal(raw, &oiChunk); err == nil {
			if choices, ok := oiChunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
							for _, call := range toolCalls {
								callMap, ok := call.(map[string]interface{})
								if !ok {
									continue
								}
								idx := 0
								if rawIdx, ok := callMap["index"].(float64); ok {
									idx = int(rawIdx)
								}
								entry := pendingToolCalls[idx]
								if entry == nil {
									entry = &pendingToolCall{}
									pendingToolCalls[idx] = entry
								}
								if id, ok := callMap["id"].(string); ok && id != "" {
									entry.ID = id
								}
								if fn, ok := callMap["function"].(map[string]interface{}); ok {
									if name, ok := fn["name"].(string); ok && name != "" {
										entry.Name = name
									}
									if args, ok := fn["arguments"].(string); ok && args != "" {
										entry.Arguments += args
									}
								}
							}
						}
						if contentStr, ok := delta["content"].(string); ok {
							content.WriteString(contentStr)
						}
					}
					if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
						finishReason = fr
					}
				}
			}
		}
	}

	var toolCalls []map[string]interface{}
	if finishReason == "tool_calls" && len(pendingToolCalls) > 0 {
		for i := 0; i < len(pendingToolCalls); i++ {
			entry, ok := pendingToolCalls[i]
			if !ok || entry == nil {
				continue
			}
			id := entry.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			args := entry.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   id,
				"type": "function",
				"function": map[string]interface{}{
					"name":      entry.Name,
					"arguments": args,
				},
			})
		}
	}

	// Build final response
	message := map[string]interface{}{
		"role":    "assistant",
		"content": content.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	response := map[string]interface{}{
		"id":      fmt.Sprintf("qoder-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}

	responseBytes, _ := json.Marshal(response)

	// Translate the Qoder OpenAI-format response back to the client's
	// expected SourceFormat. Reuse internalReq.Payload — that's already
	// the OpenAI-translated payload we computed above before calling
	// ExecuteStream, so we don't need to re-translate.
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, opts.SourceFormat, req.Model, opts.OriginalRequest, internalReq.Payload, responseBytes, &param)
	responseBytes = out

	return cliproxyexecutor.Response{
		Payload: responseBytes,
		Headers: streamResult.Headers,
	}, nil
}

// Refresh is a no-op for Qoder.
//
// Qoder's device-flow token (the "dt-..." string) is already long-lived
// (~30 days for the access token, ~360 days for the refresh token per
// the deviceToken/poll response). The upstream does not expose the
// classic OAuth refresh dance — every endpoint we've observed (cubk1's
// qoder2api, Veria, the official @qoder-ai/qodercli) either skips
// refresh entirely or routes through a different /jobToken exchange
// flow that requires personalToken (we don't have one).
//
// Hitting /algo/api/v3/user/refresh_token with our device token returns
// 403 "Forbidden" / errorCode=Forbidden — the endpoint is not for our
// flow. Mark the auth refreshed-now and keep going; if a real expiry
// happens the user re-runs --qoder-login.
func (e *QoderExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("qoder executor: auth is nil")
	}
	return auth, nil
}

// HttpRequest injects Qoder COSY authentication into the HTTP request and executes it
func (e *QoderExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage)
	if !ok {
		return nil, fmt.Errorf("invalid auth storage type for qoder")
	}

	// Read request body for COSY signing
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	headers, err := qoderauth.BuildAuthHeaders(
		bodyBytes,
		req.URL.String(),
		qoderauth.CosyCredentials{
			UserID:    storage.UserID,
			AuthToken: storage.Token,
			Name:      storage.Name,
			Email:     storage.Email,
			MachineID: storage.MachineID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build COSY auth: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	headers.Apply(req)

	req = req.WithContext(ctx)
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(req)
}

// buildQoderModelConfig returns the model_config block for a chat request,
// pulled from the cache populated by FetchQoderModels (which mirrors what
// /algo/api/v2/model/list publishes — per-model is_vl / is_reasoning /
// max_input_tokens / price_factor / strategies / ...). Returns an error
// when the cache has no entry for modelKey: that means we either never
// successfully fetched the model list for this auth, or the user asked
// for a model the server doesn't expose. Either way we should fail loudly
// rather than guess and silently get downgraded to a different model.
func buildQoderModelConfig(storage *qoderauth.QoderTokenStorage, modelKey string) (map[string]interface{}, error) {
	raw, ok := storage.GetModelConfig(modelKey)
	if !ok || len(raw) == 0 {
		keys := storage.ModelConfigKeys()
		if len(keys) == 0 {
			return nil, fmt.Errorf("qoder: model config cache is empty (model list not fetched yet); restart the service or check /algo/api/v2/model/list connectivity")
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("qoder: no model_config cached for %q; known models: %s", modelKey, strings.Join(keys, ", "))
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("qoder: cached model_config for %q is invalid JSON: %w", modelKey, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("qoder: cached model_config for %q decoded to nil", modelKey)
	}
	// The cache stores the model description; ensure the key matches what
	// we're sending (handles model alias rewrites in caller).
	cfg["key"] = modelKey
	return cfg, nil
}

// FetchQoderModels retrieves the live model list from Qoder's
// /algo/api/v2/model/list endpoint and converts it into ModelInfo entries.
// Falls back to the static registry if the auth lacks credentials, the request
// fails, or the response is malformed. Mirrors the FetchKiloModels /
// FetchCursorModels pattern used by other dynamic providers.
func FetchQoderModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage)
	if !ok || storage == nil || storage.Token == "" {
		log.Debug("qoder: no token, returning static models")
		return registry.GetQoderModels()
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	headers, err := qoderauth.BuildAuthHeaders(nil, qoderauth.QoderModelListURL, qoderauth.CosyCredentials{
		UserID:    storage.UserID,
		AuthToken: storage.Token,
		Name:      storage.Name,
		Email:     storage.Email,
		MachineID: storage.MachineID,
	})
	if err != nil {
		log.Warnf("qoder: build cosy headers for model list: %v", err)
		return registry.GetQoderModels()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderauth.QoderModelListURL, nil)
	if err != nil {
		log.Warnf("qoder: build model list request: %v", err)
		return registry.GetQoderModels()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	headers.Apply(req)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("qoder: model list fetch canceled: %v", err)
		} else {
			log.Warnf("qoder: model list fetch failed: %v", err)
		}
		return registry.GetQoderModels()
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("qoder: read model list response: %v", err)
		return registry.GetQoderModels()
	}
	if resp.StatusCode != http.StatusOK {
		log.Warnf("qoder: model list returned %d: %s", resp.StatusCode, truncate(string(body), 300))
		return registry.GetQoderModels()
	}

	chat := gjson.GetBytes(body, "chat")
	if !chat.Exists() || !chat.IsArray() {
		log.Warnf("qoder: model list response missing 'chat' array")
		return registry.GetQoderModels()
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, 16)
	configs := make(map[string]json.RawMessage, 16)
	chat.ForEach(func(_, entry gjson.Result) bool {
		key := entry.Get("key").String()
		if key == "" {
			return true
		}
		if !entry.Get("enable").Bool() {
			return true
		}
		display := entry.Get("display_name").String()
		if display == "" {
			display = key
		}
		ctxLen := int(entry.Get("max_input_tokens").Int())
		isVL := entry.Get("is_vl").Bool()

		// Cache the raw upstream JSON for this model so ExecuteStream can
		// forward the exact model_config the server published (per-model
		// is_vl / is_reasoning / max_input_tokens / price_factor / ...).
		configs[key] = json.RawMessage(entry.Raw)

		mi := &registry.ModelInfo{
			ID:            "qoder/" + key,
			Object:        "model",
			Created:       now,
			OwnedBy:       "qoder",
			Type:          "qoder",
			DisplayName:   display,
			Description:   fmt.Sprintf("%s via Qoder", display),
			ContextLength: ctxLen,
		}
		if isVL {
			mi.SupportedInputModalities = []string{"TEXT", "IMAGE"}
		}
		// Parse thinking_config from upstream. Qoder returns per-model
		// effort levels (e.g. dmodel has only high/max, ultimate has
		// low/medium/high/max/xhigh) and a disabled key to indicate
		// whether reasoning can be turned off. Models without
		// thinking_config but with is_reasoning=true still get a
		// basic Thinking marker (no predefined levels).
		if tc := entry.Get("thinking_config"); tc.Exists() {
			ts := &registry.ThinkingSupport{}
			if tc.Get("disabled").Exists() {
				ts.ZeroAllowed = true
			}
			efforts := tc.Get("enabled.efforts")
			if efforts.Exists() && efforts.IsObject() {
				levels := make([]string, 0, 5)
				efforts.ForEach(func(key, _ gjson.Result) bool {
					levels = append(levels, key.String())
					return true
				})
				ts.Levels = levels
			}
			mi.Thinking = ts
		} else if entry.Get("is_reasoning").Bool() {
			mi.Thinking = &registry.ThinkingSupport{}
		}
		models = append(models, mi)
		return true
	})

	if len(models) == 0 {
		log.Warn("qoder: model list returned no enabled models, falling back to static")
		return registry.GetQoderModels()
	}

	storage.SetModelConfigs(configs)

	log.Infof("qoder: fetched %d models from /algo/api/v2/model/list", len(models))

	// Fetch usage alongside models so the management UI has fresh credit data.
	// Use context.Background() so the goroutine outlives the caller's context.
	go FetchQoderUsage(context.Background(), auth, cfg)

	return models
}

// stableHash returns a deterministic hex identifier from the given inputs.
func stableHash(prefix string, inputs ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, in := range inputs {
		h.Write([]byte{0})
		h.Write([]byte(in))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// stableChatRecordID produces a deterministic chat_record_id from the
// request payload so retries with identical content hit upstream caches.
func stableChatRecordID(model string, messages []interface{}, toolsRaw interface{}, maxTokens int) string {
	h := sha256.New()
	h.Write([]byte("qoder-record"))
	h.Write([]byte{0})
	h.Write([]byte(model))
	for _, msg := range messages {
		m, _ := msg.(map[string]interface{})
		if m == nil {
			continue
		}
		if role, _ := m["role"].(string); role != "" {
			h.Write([]byte{0})
			h.Write([]byte(role))
		}
		if content, _ := m["content"].(string); content != "" {
			h.Write([]byte{0})
			h.Write([]byte(content))
		}
	}
	if toolsRaw != nil {
		toolsJSON, _ := json.Marshal(toolsRaw)
		h.Write([]byte{0})
		h.Write(toolsJSON)
	}
	h.Write([]byte{0})
	h.Write([]byte(fmt.Sprintf("mt=%d", maxTokens)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// FetchQoderUsage fetches the current quota usage from /api/v2/quota/usage
// and caches the result in storage.UsageInfo. It is called opportunistically
// alongside FetchQoderModels so the management UI can display credit balance
// without a separate round-trip.
func FetchQoderUsage(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) *qoderauth.QoderUsageInfo {
	storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage)
	if !ok || storage == nil || storage.Token == "" {
		return nil
	}

	const usageURL = "https://openapi.qoder.sh/api/v2/quota/usage"
	log.Debugf("qoder: fetching usage for user %s (token len=%d)", storage.UserID, len(storage.Token))
	req, err := http.NewRequest(http.MethodGet, usageURL, nil)
	if err != nil {
		log.Debugf("qoder: build usage request: %v", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+storage.Token)
	req.Header.Set("Accept", "application/json")

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Debugf("qoder: usage fetch failed: %v", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Debugf("qoder: usage fetch returned %d", resp.StatusCode)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("qoder: read usage response: %v", err)
		return nil
	}

	var info qoderauth.QoderUsageInfo
	if err := json.Unmarshal(body, &info); err != nil {
		log.Debugf("qoder: parse usage response: %v", err)
		return nil
	}

	storage.SetUsageInfo(&info)
	log.Debugf("qoder: usage fetched — %.0f/%.0f %s used (%.1f%%)",
		info.UserQuota.Used, info.UserQuota.Total, info.UserQuota.Unit,
		info.TotalUsagePercentage*100)
	return &info
}

func validateQoderModel(rawModel string, storage *qoderauth.QoderTokenStorage) (string, error) {
	qoderModel := strings.TrimPrefix(rawModel, "qoder/")
	if mapped, ok := qoderauth.ModelMap[qoderModel]; ok {
		return mapped, nil
	}
	if storage != nil {
		if _, cached := storage.GetModelConfig(qoderModel); cached {
			return qoderModel, nil
		}
	}
	return "", fmt.Errorf("unsupported qoder model: %q", qoderModel)
}
