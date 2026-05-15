package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/sjson"
)

// DeepSeekProxyExecutor executes DeepSeek web models directly against chat.deepseek.com.
type DeepSeekProxyExecutor struct {
	cfg     *config.Config
	regular *http.Client
	stream  *http.Client
}

func NewDeepSeekProxyExecutor(cfg *config.Config) *DeepSeekProxyExecutor {
	return &DeepSeekProxyExecutor{
		cfg:     cfg,
		regular: newDeepSeekHTTPClient(60 * time.Second),
		stream:  newDeepSeekHTTPClient(0),
	}
}

func (e *DeepSeekProxyExecutor) Identifier() string { return "deepseek" }

func (e *DeepSeekProxyExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, err := e.prepareOpenAIRequest(baseModel, req, opts, false)
	if err != nil {
		return resp, err
	}
	request, err := prepareDeepSeekRequest(translated, baseModel)
	if err != nil {
		return resp, err
	}
	request.Stream = false
	dsAuth, err := e.resolveDeepSeekAuth(ctx, auth)
	if err != nil {
		return resp, err
	}
	sessionID, upstream, err := e.openCompletion(ctx, auth, dsAuth, request, translated)
	if err != nil {
		return resp, err
	}
	defer closeDeepSeekBody(upstream.Body)
	defer e.deleteSession(context.WithoutCancel(ctx), dsAuth.Token, sessionID)

	result, err := e.collectDeepSeekResponse(ctx, auth, dsAuth, upstream, sessionID, request)
	if err != nil {
		return resp, err
	}
	body := buildOpenAINonStreamResponse(request.RequestedModel, result.Content, result.Reasoning, result.ToolCalls)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: upstream.Header.Clone()}, nil
}

func (e *DeepSeekProxyExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, err := e.prepareOpenAIRequest(baseModel, req, opts, true)
	if err != nil {
		return nil, err
	}
	request, err := prepareDeepSeekRequest(translated, baseModel)
	if err != nil {
		return nil, err
	}
	request.Stream = true
	dsAuth, err := e.resolveDeepSeekAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	sessionID, upstream, err := e.openCompletion(ctx, auth, dsAuth, request, translated)
	if err != nil {
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer e.deleteSession(context.WithoutCancel(ctx), dsAuth.Token, sessionID)
		defer reporter.EnsurePublished(ctx)
		var param any
		emit := func(line []byte) bool {
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, line, &param)
			for i := range chunks {
				select {
				case <-ctx.Done():
					return false
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				}
			}
			return true
		}
		if errStream := e.streamDeepSeekAsOpenAI(ctx, auth, dsAuth, upstream, sessionID, request, emit); errStream != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errStream)
			reporter.PublishFailure(ctx)
			select {
			case <-ctx.Done():
			case out <- cliproxyexecutor.StreamChunk{Err: errStream}:
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: upstream.Header.Clone(), Chunks: out}, nil
}

func (e *DeepSeekProxyExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return (&OpenAICompatExecutor{provider: e.Identifier(), cfg: e.cfg}).CountTokens(ctx, auth, req, opts)
}

func (e *DeepSeekProxyExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "deepseek auth is missing"}
	}
	clone := auth.Clone()
	token, err := e.login(ctx, clone)
	if err != nil {
		return nil, err
	}
	setDeepSeekAuthToken(clone, token)
	return clone, nil
}

func (e *DeepSeekProxyExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("deepseek executor: request is nil")
	}
	dsAuth, err := e.resolveDeepSeekAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	httpReq := req.WithContext(ctx)
	for key, value := range e.authHeaders(dsAuth.Token) {
		httpReq.Header.Set(key, value)
	}
	return e.regular.Do(httpReq)
}

func (e *DeepSeekProxyExecutor) prepareOpenAIRequest(baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	translated, err := sjson.SetBytes(translated, "model", baseModel)
	if err != nil {
		return nil, fmt.Errorf("deepseek executor: failed to set model: %w", err)
	}
	translated, _ = sjson.SetBytes(translated, "stream", stream)
	if stream {
		translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	}
	return thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
}

func (e *DeepSeekProxyExecutor) streamDeepSeekAsOpenAI(ctx context.Context, auth *cliproxyauth.Auth, dsAuth *deepSeekAuth, initial *http.Response, sessionID string, request deepSeekRequest, emit func([]byte) bool) error {
	completionID := "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	created := time.Now().Unix()
	state := deepSeekContinueState{SessionID: sessionID}
	current := initial
	sentRole := false
	sawToolCall := false
	toolIndex := 0
	contentParser := newDeepSeekToolCallParserWithHold(request.ToolMode)
	reasoningParser := newDeepSeekToolCallParser()
	emitParsed := func(seg deepSeekSegment) bool {
		if seg.Kind == "tool_call" && seg.ToolCall != nil {
			chunk := buildOpenAIToolCallStreamChunk(completionID, request.RequestedModel, created, *seg.ToolCall, toolIndex, !sentRole)
			sentRole = true
			sawToolCall = true
			toolIndex++
			return emit(chunk)
		}
		if seg.Text == "" {
			return true
		}
		chunk := buildOpenAIStreamChunk(completionID, request.RequestedModel, created, seg, !sentRole)
		sentRole = true
		return emit(chunk)
	}
	for round := 0; ; round++ {
		err := scanDeepSeekSSE(ctx, current.Body, request.Thinking, &state, func(line []byte) {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		}, func(seg deepSeekSegment) bool {
			if seg.Text == "" {
				return true
			}
			parser := contentParser
			if seg.Kind == "reasoning" {
				parser = reasoningParser
			}
			for _, parsed := range parser.PushKind(seg.Text, seg.Kind) {
				if !emitParsed(parsed) {
					return false
				}
			}
			return true
		})
		_ = current.Body.Close()
		if err != nil {
			return err
		}
		if !state.shouldContinue() || round >= deepSeekMaxContinueRounds {
			break
		}
		next, err := e.callContinue(ctx, auth, dsAuth, sessionID, state.ResponseMessageID)
		if err != nil {
			return err
		}
		current = next
		state.prepareNext()
	}
	for _, parsed := range reasoningParser.Finish() {
		if !emitParsed(parsed) {
			return ctx.Err()
		}
	}
	for _, parsed := range contentParser.Finish() {
		if !emitParsed(parsed) {
			return ctx.Err()
		}
	}
	finishReason := "stop"
	if sawToolCall {
		finishReason = "tool_calls"
	}
	if !emit(buildOpenAIFinishChunk(completionID, request.RequestedModel, created, finishReason)) {
		return ctx.Err()
	}
	if !emit([]byte("data: [DONE]\n\n")) {
		return ctx.Err()
	}
	return nil
}

func buildOpenAINonStreamResponse(model, content, reasoning string, toolCalls []deepSeekToolCall) []byte {
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = openAIToolCallsFromDeepSeek(toolCalls)
		finishReason = "tool_calls"
	}
	body := map[string]any{
		"id":      "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
	b, _ := json.Marshal(body)
	return b
}

func buildOpenAIStreamChunk(id, model string, created int64, segment deepSeekSegment, includeRole bool) []byte {
	delta := map[string]any{}
	if includeRole {
		delta["role"] = "assistant"
	}
	if segment.Kind == "reasoning" {
		delta["reasoning_content"] = segment.Text
	} else {
		delta["content"] = segment.Text
	}
	body := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	}
	b, _ := json.Marshal(body)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

func buildOpenAIToolCallStreamChunk(id, model string, created int64, toolCall deepSeekToolCall, index int, includeRole bool) []byte {
	delta := map[string]any{
		"tool_calls": []any{map[string]any{
			"index": index,
			"id":    toolCall.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      toolCall.Name,
				"arguments": toolCall.Arguments,
			},
		}},
	}
	if includeRole {
		delta["role"] = "assistant"
	}
	body := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	}
	b, _ := json.Marshal(body)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

func buildOpenAIFinishChunk(id, model string, created int64, finishReason string) []byte {
	if finishReason == "" {
		finishReason = "stop"
	}
	body := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
	b, _ := json.Marshal(body)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

func openAIToolCallsFromDeepSeek(toolCalls []deepSeekToolCall) []any {
	out := make([]any, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		out = append(out, map[string]any{
			"id":   toolCall.ID,
			"type": "function",
			"function": map[string]any{
				"name":      toolCall.Name,
				"arguments": toolCall.Arguments,
			},
		})
	}
	return out
}

func bytesHasDataPrefix(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:"))
}
