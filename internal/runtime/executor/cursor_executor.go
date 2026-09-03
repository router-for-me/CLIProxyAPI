package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	cursorwire "github.com/router-for-me/CLIProxyAPI/v7/internal/cursor"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

// CursorExecutor talks to Cursor's AIServer over Connect-RPC.
//
// Requests arrive in whatever format the client used, are translated to the
// canonical OpenAI shape, then encoded as Cursor protobuf; the Cursor stream is
// re-emitted as OpenAI SSE and translated back into the caller's format.
type CursorExecutor struct {
	cfg *config.Config
}

// NewCursorExecutor creates a new Cursor executor.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	return &CursorExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *CursorExecutor) Identifier() string { return "cursor" }

// RequestToFormat reports the upstream request format used after auth selection.
func (e *CursorExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest injects Cursor credentials into the outgoing HTTP request.
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	cursorwire.ApplyHeaders(req, cursorCreds(auth), true)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Cursor credentials into the request and executes it.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor executor: request is nil")
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

// buildUpstreamRequest translates the incoming payload to the canonical OpenAI
// shape and encodes it as a Cursor Connect-RPC body.
func (e *CursorExecutor) buildUpstreamRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (translated, native []byte, err error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(originalPayloadSource), stream)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream)

	upstreamModel := normalizeCursorUpstreamModel(baseModel)
	if body, err = sjson.SetBytes(body, "model", upstreamModel); err != nil {
		return nil, nil, fmt.Errorf("cursor executor: failed to set model in payload: %w", err)
	}
	if body, err = helps.ApplyThinkingWithSourcePayload(body, req.Payload, originalPayloadSource, req.Model, from.String(), to.String(), e.Identifier()); err != nil {
		return nil, nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	native, err = cursorwire.GenerateChatBody(cursorMessagesFromOpenAI(body), upstreamModel)
	if err != nil {
		return nil, nil, err
	}
	return body, native, nil
}

// doUpstream sends the native Cursor body and returns the live response.
func (e *CursorExecutor) doUpstream(ctx context.Context, auth *cliproxyauth.Auth, native []byte, reporter *helps.UsageReporter) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorwire.ChatEndpoint, bytes.NewReader(native))
	if err != nil {
		return nil, err
	}
	if err = e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       cursorwire.ChatEndpoint,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	if reporter != nil {
		httpClient = reporter.TrackHTTPClient(httpClient)
	}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := cursorwire.DecodeMaybeGzip(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("cursor executor: close response body error: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}
	return httpResp, nil
}

// Execute performs a non-streaming chat completion request to Cursor.
//
// Cursor has no unary chat endpoint, so the stream is consumed fully and
// collapsed into a single OpenAI chat.completion document.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	to := sdktranslator.FromString("openai")
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, native, errBuild := e.buildUpstreamRequest(ctx, req, opts, false)
	if errBuild != nil {
		return resp, errBuild
	}

	httpResp, errResp := e.doUpstream(ctx, auth, native, reporter)
	if errResp != nil {
		return resp, errResp
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("cursor executor: close response body error: %v", errClose)
		}
	}()

	var text, reasoning strings.Builder
	if err = cursorwire.ReadFrames(httpResp.Body, func(thinkingText, content string) {
		reasoning.WriteString(thinkingText)
		text.WriteString(content)
	}); err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, wrapCursorStreamError(err)
	}

	data := buildOpenAICompletion(req.Model, reasoning.String(), text.String(), cursorEstimateUsage(baseModel, body, reasoning.String()+text.String()))
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream performs a streaming chat completion request to Cursor.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, native, errBuild := e.buildUpstreamRequest(ctx, req, opts, true)
	if errBuild != nil {
		return nil, errBuild
	}

	httpResp, errResp := e.doUpstream(ctx, auth, native, reporter)
	if errResp != nil {
		return nil, errResp
	}

	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	originalPayload = bytes.Clone(originalPayload)

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("cursor executor: close response body error: %v", errClose)
			}
		}()

		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		var produced strings.Builder

		emit := func(line []byte) bool {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, line, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}

		cancelled := false
		errRead := cursorwire.ReadFrames(httpResp.Body, func(thinkingText, content string) {
			if cancelled || (thinkingText == "" && content == "") {
				return
			}
			produced.WriteString(thinkingText)
			produced.WriteString(content)
			if !emit(buildOpenAIStreamChunk(req.Model, thinkingText, content, nil)) {
				cancelled = true
			}
		})
		if cancelled {
			return
		}
		if errRead != nil {
			errRead = wrapCursorStreamError(errRead)
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			reporter.PublishFailure(ctx, errRead)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
			case <-ctx.Done():
			}
			return
		}

		usageDetail := cursorEstimateUsage(baseModel, body, produced.String())
		reporter.Publish(ctx, helps.ParseOpenAIUsage(buildOpenAICompletion(req.Model, "", "", usageDetail)))
		if !emit(buildOpenAIStreamChunk(req.Model, "", "", usageDetail)) {
			return
		}
		_ = emit([]byte("[DONE]"))
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens estimates the token count for Cursor requests locally: the Cursor
// API exposes no token counting endpoint.
func (e *CursorExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)
	enc, err := helps.TokenizerForModel(normalizeCursorUpstreamModel(baseModel))
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: token counting failed: %w", err)
	}
	usageJSON := helps.BuildOpenAIUsageJSON(count)
	return cliproxyexecutor.Response{Payload: sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)}, nil
}

// Refresh is a no-op: Cursor issues a long-lived token and no refresh token, so
// a rejected credential has to be replaced by logging in again.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("cursor executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

// wrapCursorStreamError maps a Cursor end-of-stream error onto a status error so
// the caller reports a meaningful HTTP status instead of a generic failure.
func wrapCursorStreamError(err error) error {
	var streamErr *cursorwire.StreamError
	if errors.As(err, &streamErr) {
		return statusErr{code: streamErr.HTTPStatus(), msg: streamErr.Error()}
	}
	return err
}

// cursorMessagesFromOpenAI flattens an OpenAI chat payload into Cursor turns.
func cursorMessagesFromOpenAI(body []byte) []cursorwire.Message {
	messages := gjson.ParseBytes(body).Get("messages")
	if !messages.IsArray() {
		return nil
	}
	items := messages.Array()
	out := make([]cursorwire.Message, 0, len(items))
	for _, msg := range items {
		role := strings.TrimSpace(msg.Get("role").String())
		if role == "" {
			role = "user"
		}
		text := flattenOpenAIContent(msg.Get("content"))
		if text == "" {
			continue
		}
		out = append(out, cursorwire.Message{Role: role, Content: text})
	}
	return out
}

// flattenOpenAIContent renders an OpenAI content value as plain text. Cursor's
// unified chat schema carries text only, so non-text parts are dropped.
func flattenOpenAIContent(content gjson.Result) string {
	switch {
	case !content.Exists() || content.Type == gjson.Null:
		return ""
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			if item.Type == gjson.String {
				if s := item.String(); s != "" {
					parts = append(parts, s)
				}
				continue
			}
			if text := item.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// buildOpenAIStreamChunk renders one OpenAI chat.completion.chunk SSE line.
// An empty delta with usage marks the final chunk.
func buildOpenAIStreamChunk(model, reasoning, text string, usage []byte) []byte {
	payload := []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{}}]}`)
	payload, _ = sjson.SetBytes(payload, "model", model)
	if reasoning != "" {
		payload, _ = sjson.SetBytes(payload, "choices.0.delta.reasoning_content", reasoning)
	}
	if text != "" {
		payload, _ = sjson.SetBytes(payload, "choices.0.delta.role", "assistant")
		payload, _ = sjson.SetBytes(payload, "choices.0.delta.content", text)
	}
	if usage != nil {
		payload, _ = sjson.SetRawBytes(payload, "usage", usage)
		payload, _ = sjson.SetBytes(payload, "choices.0.finish_reason", "stop")
	}
	return append([]byte("data: "), payload...)
}

// buildOpenAICompletion renders a complete OpenAI chat.completion document.
func buildOpenAICompletion(model, reasoning, text string, usage []byte) []byte {
	payload := []byte(`{"object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":""}}]}`)
	payload, _ = sjson.SetBytes(payload, "model", model)
	payload, _ = sjson.SetBytes(payload, "choices.0.message.content", text)
	if reasoning != "" {
		payload, _ = sjson.SetBytes(payload, "choices.0.message.reasoning_content", reasoning)
	}
	if usage != nil {
		payload, _ = sjson.SetRawBytes(payload, "usage", usage)
	}
	return payload
}

// cursorEstimateUsage counts prompt and completion tokens locally, since Cursor
// reports no usage of its own.
func cursorEstimateUsage(model string, requestBody []byte, completion string) []byte {
	enc, err := helps.TokenizerForModel(normalizeCursorUpstreamModel(model))
	if err != nil {
		return nil
	}
	promptTokens, err := helps.CountOpenAIChatTokens(enc, requestBody)
	if err != nil {
		return nil
	}
	ids, _, err := enc.Encode(completion)
	if err != nil {
		return nil
	}
	completionTokens := int64(len(ids))
	usage := []byte(`{}`)
	usage, _ = sjson.SetBytes(usage, "prompt_tokens", promptTokens)
	usage, _ = sjson.SetBytes(usage, "completion_tokens", completionTokens)
	usage, _ = sjson.SetBytes(usage, "total_tokens", promptTokens+completionTokens)
	return usage
}

// normalizeCursorUpstreamModel strips the CLIProxyAPI "cursor-" prefix and any
// thinking suffix, so the API receives IDs such as "claude-4-sonnet".
func normalizeCursorUpstreamModel(model string) string {
	base := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if len(base) > len("cursor-") && strings.EqualFold(base[:len("cursor-")], "cursor-") {
		base = base[len("cursor-"):]
	}
	return base
}

// cursorCreds extracts the Cursor access token from auth.
func cursorCreds(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		for _, key := range []string{"access_token", "cookie"} {
			if v, ok := a.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"access_token", "cookie", "api_key"} {
			if v := strings.TrimSpace(a.Attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}
