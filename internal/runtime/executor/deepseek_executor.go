package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/client"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/promptcompat"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/sse"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolcall"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolstream"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// DeepSeekExecutor drives the DeepSeek web chat client (chat.deepseek.com)
// reverse-engineered transport. Unlike the OpenAI-compatible executors it does
// not call a public API: it impersonates Chrome at the TLS + HTTP/2 layer,
// solves a proof-of-work challenge, and speaks the web client's SSE protocol.
//
// The executor receives requests in the source client format, translates them
// to OpenAI chat-completions JSON (the working intermediate), runs that through
// promptcompat to build the DeepSeek wire payload, and emits OpenAI-format
// responses/chunks that the conductor's translator layer converts to the
// client's target format.
type DeepSeekExecutor struct {
	cfg *config.Config

	// client is lazily initialised and reused across requests.
	client atomic.Pointer[client.Client]
}

// NewDeepSeekExecutor creates a new DeepSeek executor.
func NewDeepSeekExecutor(cfg *config.Config) *DeepSeekExecutor {
	return &DeepSeekExecutor{cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *DeepSeekExecutor) Identifier() string { return constant.DeepSeek }

// sharedClient returns the cached DeepSeek client, creating it on first use.
func (e *DeepSeekExecutor) sharedClient() *client.Client {
	if c := e.client.Load(); c != nil {
		return c
	}
	c := client.NewClient()
	if e.client.CompareAndSwap(nil, c) {
		return c
	}
	return e.client.Load()
}

// deepSeekCreds extracts the user token and locale from auth attributes.
// It reads from auth.Attributes first (config-based auth via deepseek-api-key)
// and falls back to auth.Metadata (file-based auth via JSON file in auth-dir).
// Supported token keys in both maps: "token", "access_token", "api_key".
func deepSeekCreds(auth *cliproxyauth.Auth) (token, locale, proxyURL string) {
	if auth == nil {
		return "", "zh_CN", ""
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["token"])
		if token == "" {
			token = strings.TrimSpace(auth.Attributes["access_token"])
		}
		if token == "" {
			token = strings.TrimSpace(auth.Attributes["api_key"])
		}
		locale = strings.TrimSpace(auth.Attributes["locale"])
	}
	// Fall back to Metadata (file-based auth: JSON files loaded by FileTokenStore
	// put raw JSON fields into auth.Metadata as map[string]any).
	if token == "" && auth.Metadata != nil {
		token = stringFromAny(auth.Metadata["token"])
		if token == "" {
			token = stringFromAny(auth.Metadata["access_token"])
		}
		if token == "" {
			token = stringFromAny(auth.Metadata["api_key"])
		}
	}
	if locale == "" && auth.Metadata != nil {
		locale = stringFromAny(auth.Metadata["locale"])
	}
	if locale == "" {
		locale = "zh_CN"
	}
	proxyURL = strings.TrimSpace(auth.ProxyURL)
	return token, locale, proxyURL
}

// stringFromAny safely extracts a trimmed string from an any value.
func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// deepSeekConfigReader adapts *config.Config into the interface that
// promptcompat.NormalizeOpenAIChatRequest expects. CLIP does not carry
// DeepSeek-specific alias overrides today, so the alias map is empty and
// the built-in default alias table is used.
type deepSeekConfigReader struct{}

func (deepSeekConfigReader) ModelAliases() map[string]string { return nil }
func (deepSeekConfigReader) AutoRouteVisionEnabled() bool     { return false }

// Execute handles non-streaming chat completion against the DeepSeek web client.
func (e *DeepSeekExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	token, locale, proxyURL := deepSeekCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, fmt.Errorf("deepseek executor: missing user token in auth attributes")
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Translate the inbound payload to OpenAI chat-completions JSON (the
	// working intermediate format that promptcompat understands).
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)

	stdReq, err := e.buildStandardRequest(body)
	if err != nil {
		return resp, err
	}

	cl := e.sharedClient()
	ctx = transport.WithAccountID(ctx, auth.ID)

	sessionID, err := cl.CreateSession(ctx, token, locale, proxyURL)
	if err != nil {
		return resp, mapDeepSeekClientError(err)
	}
	powResp, err := cl.GetPow(ctx, token, locale, proxyURL)
	if err != nil {
		return resp, mapDeepSeekClientError(err)
	}
	payload := stdReq.CompletionPayload(sessionID)

	httpResp, err := cl.CallCompletion(ctx, token, locale, proxyURL, payload, powResp)
	if err != nil {
		return resp, mapDeepSeekClientError(err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	collectResult := sse.CollectStream(httpResp, stdReq.Thinking, true)

	if collectResult.ContentFilter {
		return resp, statusErr{code: http.StatusOK, msg: "deepseek: content filter triggered"}
	}
	if collectResult.UpstreamError != "" {
		return resp, statusErr{code: http.StatusOK, msg: collectResult.UpstreamError}
	}

	openAIResp := buildOpenAIChatCompletion(req.Model, collectResult.Text, collectResult.Thinking, stdReq.Thinking)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(openAIResp))

	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, body, openAIResp, nil)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream handles streaming chat completion against the DeepSeek web client.
func (e *DeepSeekExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	token, locale, proxyURL := deepSeekCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("deepseek executor: missing user token in auth attributes")
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), true)

	stdReq, err := e.buildStandardRequest(body)
	if err != nil {
		return nil, err
	}

	cl := e.sharedClient()
	ctx = transport.WithAccountID(ctx, auth.ID)

	sessionID, err := cl.CreateSession(ctx, token, locale, proxyURL)
	if err != nil {
		return nil, mapDeepSeekClientError(err)
	}
	powResp, err := cl.GetPow(ctx, token, locale, proxyURL)
	if err != nil {
		return nil, mapDeepSeekClientError(err)
	}
	payload := stdReq.CompletionPayload(sessionID)

	httpResp, err := cl.CallCompletion(ctx, token, locale, proxyURL, payload, powResp)
	if err != nil {
		return nil, mapDeepSeekClientError(err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = httpResp.Body.Close() }()

		e.streamDeepSeekSSE(ctx, out, httpResp, stdReq, to, responseFormat, req.Model, originalPayload, body, reporter)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// streamDeepSeekSSE consumes the DeepSeek SSE response, converts each content
// event to an OpenAI chat-completion chunk, and emits translated StreamChunks.
func (e *DeepSeekExecutor) streamDeepSeekSSE(
	ctx context.Context,
	out chan<- cliproxyexecutor.StreamChunk,
	httpResp *http.Response,
	stdReq promptcompat.StandardRequest,
	upstreamFormat, responseFormat sdktranslator.Format,
	model string,
	originalRequest, translatedRequest []byte,
	reporter *helps.UsageReporter,
) {
	completionID := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()

	initialType := "text"
	if stdReq.Thinking {
		initialType = "thinking"
	}

	lineCh, errCh := sse.StartParsedLinePump(ctx, httpResp.Body, stdReq.Thinking, initialType)
	claudeInputTokens := helps.NewClaudeInputTokenState(upstreamFormat, upstreamFormat, responseFormat, originalRequest)

	var toolSieve toolstream.State
	toolNames := stdReq.ToolNames
	bufferToolContent := len(toolNames) > 0

	var firstChunkSent bool
	var streamUsage helps.StreamUsageBuffer
	defer streamUsage.Publish(ctx, reporter)

	emitChunk := func(delta map[string]any) {
		if len(delta) == 0 {
			return
		}
		if !firstChunkSent {
			delta["role"] = "assistant"
			firstChunkSent = true
		}
		chunk := buildOpenAIStreamChunk(completionID, created, model, delta, nil)
		chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, upstreamFormat, responseFormat, model, originalRequest, translatedRequest, chunk, nil, claudeInputTokens)
		for i := range chunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
			case <-ctx.Done():
				return
			}
		}
	}

	emitToolCalls := func(events []toolstream.Event) {
		for _, evt := range events {
			if len(evt.ToolCalls) > 0 {
				emitChunk(map[string]any{"tool_calls": formatToolCallsForOpenAI(evt.ToolCalls)})
			}
			if evt.Content != "" {
				emitChunk(map[string]any{"content": evt.Content})
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lineCh:
			if !ok {
				// Stream ended; flush tool sieve and emit finish.
				if bufferToolContent {
					emitToolCalls(toolstream.Flush(&toolSieve, toolNames))
				}
				finishChunk := buildOpenAIStreamFinishChunk(completionID, created, model, "stop")
				chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, upstreamFormat, responseFormat, model, originalRequest, translatedRequest, finishChunk, nil, claudeInputTokens)
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				doneChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, upstreamFormat, responseFormat, model, originalRequest, translatedRequest, []byte("data: [DONE]\n\n"), nil, claudeInputTokens)
				for i := range doneChunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				return
			}
			if !line.Parsed {
				continue
			}
			if line.Stop {
				if line.ErrorMessage != "" {
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("%s", line.ErrorMessage)}:
					case <-ctx.Done():
					}
					return
				}
				// Flush remaining tool content before finishing.
				if bufferToolContent {
					emitToolCalls(toolstream.Flush(&toolSieve, toolNames))
				}
				finishReason := "stop"
				if line.ContentFilter {
					finishReason = "content_filter"
				}
				finishChunk := buildOpenAIStreamFinishChunk(completionID, created, model, finishReason)
				chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, upstreamFormat, responseFormat, model, originalRequest, translatedRequest, finishChunk, nil, claudeInputTokens)
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			// Process content parts.
			for _, part := range line.Parts {
				if part.Type == "thinking" {
					if stdReq.Thinking {
						emitChunk(map[string]any{"reasoning_content": part.Text})
					}
					continue
				}
				if !bufferToolContent {
					emitChunk(map[string]any{"content": part.Text})
				} else {
					events := toolstream.ProcessChunk(&toolSieve, part.Text, toolNames)
					emitToolCalls(events)
				}
			}
		case streamErr, ok := <-errCh:
			if ok && streamErr != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
			}
			return
		}
	}
}

// buildStandardRequest parses the OpenAI chat-completions JSON body and
// normalises it into a DeepSeek StandardRequest via promptcompat.
func (e *DeepSeekExecutor) buildStandardRequest(body []byte) (promptcompat.StandardRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return promptcompat.StandardRequest{}, fmt.Errorf("deepseek executor: failed to parse openai payload: %w", err)
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(deepSeekConfigReader{}, raw, "")
	if err != nil {
		return promptcompat.StandardRequest{}, fmt.Errorf("deepseek executor: %w", err)
	}
	return stdReq, nil
}

// CountTokens estimates token count for DeepSeek requests.
// DeepSeek does not expose a token-counting endpoint; this returns a rough
// character-based estimate derived from the translated prompt.
func (e *DeepSeekExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)

	stdReq, err := e.buildStandardRequest(body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	promptText := stdReq.FinalPrompt
	// Conservative estimate: ~4 chars per token.
	count := int64(len(promptText) / 4)
	if count < 1 {
		count = 1
	}
	respJSON := []byte(fmt.Sprintf(`{"count":%d}`, count))
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	out := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, respJSON)
	return cliproxyexecutor.Response{Payload: out}, nil
}

// Refresh is a no-op for DeepSeek web accounts: the userToken login state is
// long-lived and refreshed lazily by the client layer when a 401 is observed.
func (e *DeepSeekExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest injects DeepSeek credentials into the supplied HTTP request and
// executes it. Used by the auth conductor for auxiliary upstream calls.
func (e *DeepSeekExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("deepseek executor: request is nil")
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	token, _, _ := deepSeekCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(req)
}

// mapDeepSeekClientError converts a *client.RequestFailure into a statusErr
// so the conductor can make auth-state decisions (401 triggers refresh, etc.).
func mapDeepSeekClientError(err error) error {
	if err == nil {
		return nil
	}
	var failure *client.RequestFailure
	if !errors.As(err, &failure) || failure == nil {
		return err
	}
	switch failure.Kind {
	case client.FailureManagedUnauthorized, client.FailureDirectUnauthorized:
		return statusErr{code: http.StatusUnauthorized, msg: failure.Message}
	case client.FailureCaptchaRequired:
		return statusErr{code: http.StatusForbidden, msg: failure.Message}
	case client.FailureMuted:
		return statusErr{code: http.StatusForbidden, msg: failure.Message}
	default:
		return statusErr{code: http.StatusBadGateway, msg: failure.Message}
	}
}

// buildOpenAIChatCompletion constructs a non-streaming OpenAI chat completion
// response JSON from the collected DeepSeek text and thinking content.
func buildOpenAIChatCompletion(model, text, thinking string, thinkingEnabled bool) []byte {
	message := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	if thinkingEnabled && strings.TrimSpace(thinking) != "" {
		message["reasoning_content"] = thinking
	}
	resp := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// buildOpenAIStreamChunk constructs a streaming OpenAI chat completion chunk.
func buildOpenAIStreamChunk(completionID string, created int64, model string, delta map[string]any, usage map[string]any) []byte {
	chunk := map[string]any{
		"id":      completionID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	b, _ := json.Marshal(chunk)
	return append([]byte("data: "), append(b, '\n', '\n')...)
}

// buildOpenAIStreamFinishChunk constructs the final streaming chunk with a
// finish_reason and empty delta.
func buildOpenAIStreamFinishChunk(completionID string, created int64, model, finishReason string) []byte {
	chunk := map[string]any{
		"id":      completionID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			},
		},
	}
	b, _ := json.Marshal(chunk)
	return append([]byte("data: "), append(b, '\n', '\n')...)
}

// formatToolCallsForOpenAI converts parsed tool calls into the OpenAI
// tool_calls array format used in streaming deltas.
func formatToolCallsForOpenAI(calls []toolcall.ParsedToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		args := ""
		if call.Input != nil {
			if b, err := json.Marshal(call.Input); err == nil {
				args = string(b)
			}
		}
		out = append(out, map[string]any{
			"index": i,
			"id":    "call_" + uuid.NewString(),
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": args,
			},
		})
	}
	return out
}
