package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CursorExecutor proxies requests to Cursor's private AgentService protocol.
type CursorExecutor struct{ cfg *config.Config }

func NewCursorExecutor(cfg *config.Config) *CursorExecutor { return &CursorExecutor{cfg: cfg} }

func (e *CursorExecutor) Identifier() string { return cursorauth.Provider }

func (e *CursorExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	translated, modelID, errPrepare := e.prepareCursorRequest(ctx, auth, req, opts, false)
	if errPrepare != nil {
		return resp, errPrepare
	}
	run, errRun := helps.BuildCursorRunPayload(translated, modelID)
	if errRun != nil {
		return resp, cursorRequestError(errRun)
	}
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	stream, errStream := helps.OpenCursorStream(ctx, client, cursorAccessToken(auth), run)
	if errStream != nil {
		return resp, errStream
	}

	content := strings.Builder{}
	reasoning := strings.Builder{}
	toolCalls := make([]map[string]any, 0)
	usage := map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	for event := range stream.Events {
		if event.Err != nil {
			return resp, event.Err
		}
		content.WriteString(event.Text)
		reasoning.WriteString(event.Reasoning)
		if event.ToolCallID != "" {
			toolCalls = append(toolCalls, map[string]any{
				"id":       event.ToolCallID,
				"type":     "function",
				"function": map[string]any{"name": event.ToolName, "arguments": event.ToolArguments},
			})
		}
		if event.Usage {
			usage["prompt_tokens"] = event.PromptTokens
			usage["completion_tokens"] = event.CompletionTokens
			usage["total_tokens"] = event.TotalTokens
		}
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if content.Len() == 0 {
			message["content"] = nil
		}
		finishReason = "tool_calls"
	}
	openAIResponse := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(run.ConversationID, "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   usage,
	}
	body, errJSON := json.Marshal(openAIResponse)
	if errJSON != nil {
		return resp, fmt.Errorf("cursor executor: encode response: %w", errJSON)
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: stream.Headers}, nil
}

func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	translated, modelID, errPrepare := e.prepareCursorRequest(ctx, auth, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	run, errRun := helps.BuildCursorRunPayload(translated, modelID)
	if errRun != nil {
		return nil, cursorRequestError(errRun)
	}
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	reporter.StartResponseTTFT()
	stream, errStream := helps.OpenCursorStream(ctx, client, cursorAccessToken(auth), run)
	if errStream != nil {
		return nil, errStream
	}

	first, ok := <-stream.Events
	if !ok {
		return nil, &helps.CursorStatusError{Status: http.StatusBadGateway, Message: "cursor stream: upstream closed before producing a response"}
	}
	if first.Err != nil {
		return nil, first.Err
	}
	reporter.MarkFirstResponseByte()

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
		var param any
		toolIndex := 0
		finished := false
		sentRole := false
		process := func(event helps.CursorStreamEvent) bool {
			if event.Err != nil {
				reporter.PublishFailure(ctx, event.Err)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: event.Err}:
				case <-ctx.Done():
				}
				return false
			}
			if event.Usage {
				usageBody := cursorOpenAIStreamChunk(run.ConversationID, modelID, nil, nil, map[string]int{
					"prompt_tokens":     event.PromptTokens,
					"completion_tokens": event.CompletionTokens,
					"total_tokens":      event.TotalTokens,
				})
				reporter.Publish(ctx, helps.ParseOpenAIUsage(usageBody))
				return sendCursorTranslatedStream(ctx, out, responseFormat, req.Model, opts.OriginalRequest, translated, usageBody, &param)
			}
			if event.Text != "" || event.Reasoning != "" || event.ToolCallID != "" {
				delta := make(map[string]any)
				if !sentRole {
					delta["role"] = "assistant"
					sentRole = true
				}
				if event.Text != "" {
					delta["content"] = event.Text
				}
				if event.Reasoning != "" {
					delta["reasoning_content"] = event.Reasoning
				}
				if event.ToolCallID != "" {
					delta["tool_calls"] = []any{map[string]any{
						"index":    toolIndex,
						"id":       event.ToolCallID,
						"type":     "function",
						"function": map[string]any{"name": event.ToolName, "arguments": event.ToolArguments},
					}}
					toolIndex++
				}
				body := cursorOpenAIStreamChunk(run.ConversationID, modelID, delta, nil, nil)
				return sendCursorTranslatedStream(ctx, out, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
			}
			if event.Done && !finished {
				finished = true
				finish := "stop"
				if toolIndex > 0 {
					finish = "tool_calls"
				}
				body := cursorOpenAIStreamChunk(run.ConversationID, modelID, map[string]any{}, &finish, nil)
				if !sendCursorTranslatedStream(ctx, out, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param) {
					return false
				}
				return sendCursorRawStream(ctx, out, sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param))
			}
			return true
		}
		if !process(first) {
			return
		}
		for event := range stream.Events {
			if !process(event) {
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: stream.Headers, Chunks: out}, nil
}

func (e *CursorExecutor) prepareCursorRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, string, error) {
	if auth == nil || cursorAccessToken(auth) == "" {
		return nil, "", &helps.CursorStatusError{Status: http.StatusUnauthorized, Message: "cursor executor: missing OAuth access token"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream)
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	var errThinking error
	translated, errThinking = helps.ApplyThinkingWithSourcePayload(translated, req.Payload, originalPayload, req.Model, from.String(), "cursor", e.Identifier())
	if errThinking != nil {
		return nil, "", errThinking
	}
	effort := strings.TrimSpace(gjson.GetBytes(translated, "cursor.reasoning_effort").String())
	if updated, errDelete := sjson.DeleteBytes(translated, "cursor"); errDelete == nil {
		translated = updated
	}
	modelID, errModel := resolveCursorModel(auth, baseModel, effort)
	if errModel != nil {
		return nil, "", errModel
	}
	updated, errSet := sjson.SetBytes(translated, "model", modelID)
	if errSet != nil {
		return nil, "", fmt.Errorf("cursor executor: set model: %w", errSet)
	}
	return updated, modelID, nil
}

func (e *CursorExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, baseModel, req.Payload, false)
	encoder, errEncoder := helps.TokenizerForModel(baseModel)
	if errEncoder != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: tokenizer init: %w", errEncoder)
	}
	count, errCount := helps.CountOpenAIChatTokens(encoder, translated)
	if errCount != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: count tokens: %w", errCount)
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	usageJSON := helps.BuildOpenAIUsageJSON(count)
	return cliproxyexecutor.Response{Payload: sdktranslator.TranslateTokenCount(ctx, sdktranslator.FormatOpenAI, responseFormat, count, usageJSON)}, nil
}

func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, errHome := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, errHome
	}
	if auth == nil {
		return nil, fmt.Errorf("cursor executor: auth is nil")
	}
	refreshToken := cursorMetadataString(auth, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}
	client := cursorauth.NewClient(e.cfg, auth.ProxyURL)
	tokens, errRefresh := client.Refresh(ctx, refreshToken)
	if errRefresh != nil {
		return nil, errRefresh
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = cursorauth.Provider
	auth.Metadata["auth_kind"] = cursorauth.OAuthKind
	auth.Metadata["access_token"] = tokens.AccessToken
	auth.Metadata["refresh_token"] = tokens.RefreshToken
	auth.Metadata["expired"] = tokens.ExpiresAt.UTC().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if models, errModels := client.DiscoverModels(ctx, tokens.AccessToken); errModels != nil {
		log.WithError(errModels).Warn("cursor executor: model refresh failed; keeping cached catalog")
	} else {
		auth.Metadata[cursorauth.ModelCacheKey] = models
	}
	if storage, ok := auth.Storage.(*cursorauth.TokenStorage); ok && storage != nil {
		storage.AccessToken = tokens.AccessToken
		storage.RefreshToken = tokens.RefreshToken
		storage.Expired = tokens.ExpiresAt.UTC().Format(time.RFC3339)
		storage.LastRefresh = time.Now().UTC().Format(time.RFC3339)
		if models, okModels := decodeCursorModels(auth.Metadata[cursorauth.ModelCacheKey]); okModels {
			storage.Models = models
		}
	}
	return auth, nil
}

func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("cursor executor: request is nil")
	}
	request = request.WithContext(ctx)
	if token := cursorAccessToken(auth); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

func cursorAccessToken(auth *cliproxyauth.Auth) string {
	return cursorMetadataString(auth, "access_token")
}

func cursorMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func resolveCursorModel(auth *cliproxyauth.Auth, requested, effort string) (string, error) {
	requested = strings.TrimSpace(requested)
	models, okModels := decodeCursorModels(auth.Metadata[cursorauth.ModelCacheKey])
	if !okModels || len(models) == 0 {
		return "", &helps.CursorStatusError{Status: http.StatusServiceUnavailable, Message: "cursor executor: cached model catalog is unavailable"}
	}
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[model.ID] = struct{}{}
	}
	if effort == "" || effort == "auto" {
		if _, ok := available[requested]; ok {
			return requested, nil
		}
		return "", &helps.CursorStatusError{Status: http.StatusBadRequest, Message: fmt.Sprintf("cursor executor: model %q is not available for this account", requested)}
	}
	root := trimCursorEffortSuffix(requested)
	candidate := root
	if effort != "none" {
		candidate = root + "-" + effort
	}
	if _, ok := available[candidate]; ok {
		return candidate, nil
	}
	if strings.HasSuffix(requested, "-"+effort) {
		if _, ok := available[requested]; ok {
			return requested, nil
		}
	}
	return "", &helps.CursorStatusError{Status: http.StatusBadRequest, Message: fmt.Sprintf("cursor executor: reasoning effort %q has no discovered model variant for %q", effort, requested)}
}

func trimCursorEffortSuffix(model string) string {
	for _, suffix := range []string{"-minimal", "-low", "-medium", "-high", "-xhigh", "-max"} {
		if strings.HasSuffix(strings.ToLower(model), suffix) {
			return model[:len(model)-len(suffix)]
		}
	}
	return model
}

func decodeCursorModels(value any) ([]cursorauth.ModelDetails, bool) {
	if models, ok := value.([]cursorauth.ModelDetails); ok {
		return models, len(models) > 0
	}
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	var models []cursorauth.ModelDetails
	if errJSON := json.Unmarshal(raw, &models); errJSON != nil {
		return nil, false
	}
	return models, len(models) > 0
}

func cursorRequestError(err error) error {
	if err == nil {
		return nil
	}
	return &helps.CursorStatusError{Status: http.StatusBadRequest, Message: err.Error()}
}

func cursorOpenAIStreamChunk(conversationID, modelID string, delta map[string]any, finishReason *string, usage map[string]int) []byte {
	response := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(conversationID, "-", ""),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
	}
	if delta != nil {
		response["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}}
	} else {
		response["choices"] = []any{}
	}
	if usage != nil {
		response["usage"] = usage
	}
	body, _ := json.Marshal(response)
	return body
}

func sendCursorTranslatedStream(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, responseFormat sdktranslator.Format, model string, original, translated, body []byte, param *any) bool {
	line := append([]byte("data: "), body...)
	return sendCursorRawStream(ctx, out, sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, responseFormat, model, original, translated, line, param))
}

func sendCursorRawStream(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunks [][]byte) bool {
	for _, chunk := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
		case <-ctx.Done():
			return false
		}
	}
	return true
}
