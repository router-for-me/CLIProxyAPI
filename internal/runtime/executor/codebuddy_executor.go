package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	codebuddyauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// CodeBuddyExecutor is a stateless executor for the Tencent CodeBuddy
// (WorkBuddy) copilot service. The upstream speaks the OpenAI chat
// completions protocol at /v2/chat/completions and authenticates with an
// OAuth access token obtained through the state-based plugin auth flow.
type CodeBuddyExecutor struct {
	cfg *config.Config
}

// NewCodeBuddyExecutor creates a new CodeBuddy executor.
func NewCodeBuddyExecutor(cfg *config.Config) *CodeBuddyExecutor {
	return &CodeBuddyExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *CodeBuddyExecutor) Identifier() string { return "codebuddy-cn" }

// RequestToFormat reports the upstream request format: OpenAI chat completions.
func (e *CodeBuddyExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// codebuddyCreds extracts the access token from an auth record. OAuth tokens
// live in metadata; a manually provisioned token may appear in attributes.
func codebuddyCreds(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	if a.Attributes != nil {
		if v := a.Attributes["access_token"]; v != "" {
			return v
		}
		if v := a.Attributes["api_key"]; v != "" {
			return v
		}
	}
	return ""
}

// codebuddyUID returns the account user id used for the X-User-Id header.
func codebuddyUID(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["uid"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if a.Attributes != nil {
		return strings.TrimSpace(a.Attributes["uid"])
	}
	return ""
}

// codebuddyBaseURL resolves the upstream base URL, allowing a per-auth override.
func codebuddyBaseURL(a *cliproxyauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if base := strings.TrimSpace(a.Attributes["base_url"]); base != "" {
			return base
		}
	}
	return codebuddyauth.DefaultBaseURL
}

// isCodeBuddySSEHeartbeat reports whether the SSE line is an upstream
// keep-alive comment (": heartbeat" or "data: : heartbeat") whose payload is
// not valid JSON and must not be forwarded to downstream clients.
func isCodeBuddySSEHeartbeat(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte(":")) {
		return true
	}
	payload, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return false
	}
	return bytes.HasPrefix(bytes.TrimSpace(payload), []byte(":"))
}

// normalizeCodeBuddyUpstreamModel returns the upstream model ID by stripping
// the CLIProxyAPI "codebuddy-" prefix and any Claude Code "[1m]" context
// suffix while preserving a trailing thinking suffix (e.g. "(1024)").
func normalizeCodeBuddyUpstreamModel(model string) string {
	model = strings.TrimSpace(model)
	parsed := thinking.ParseSuffix(model)
	base := strings.TrimSpace(parsed.ModelName)
	if len(base) >= 4 && strings.HasSuffix(strings.ToLower(base), "[1m]") {
		base = base[:len(base)-len("[1m]")]
	}
	prefix := "codebuddy-"
	if len(base) > len(prefix) && strings.HasPrefix(strings.ToLower(base), prefix) {
		base = base[len(prefix):]
	}
	if parsed.HasSuffix {
		return base + "(" + parsed.RawSuffix + ")"
	}
	return base
}

// PrepareRequest injects CodeBuddy credentials into the outgoing HTTP request.
func (e *CodeBuddyExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if token := codebuddyCreds(auth); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects CodeBuddy credentials into the request and executes it.
func (e *CodeBuddyExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codebuddy executor: request is nil")
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

// buildCodeBuddyChatRequest translates the inbound payload into an OpenAI chat
// completions body and prepares the upstream HTTP request with the headers the
// CodeBuddy endpoint requires.
func (e *CodeBuddyExecutor) buildCodeBuddyChatRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*http.Request, []byte, error) {
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(originalPayloadSource), stream)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), stream)

	body, errSet := sjson.SetBytes(body, "model", normalizeCodeBuddyUpstreamModel(baseModel))
	if errSet != nil {
		return nil, nil, fmt.Errorf("codebuddy executor: failed to set model in payload: %w", errSet)
	}

	body, err := helps.ApplyThinkingWithSourcePayload(body, req.Payload, originalPayloadSource, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, nil, err
	}

	if stream {
		body, errSet = sjson.SetBytes(body, "stream_options.include_usage", true)
		if errSet != nil {
			return nil, nil, fmt.Errorf("codebuddy executor: failed to set stream_options in payload: %w", errSet)
		}
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	// The upstream content filter rejects vendor identity phrases in system
	// prompts; rewrite them before sending.
	if sanitized, changed, sanitizeErr := codebuddyauth.SanitizeForContentFilter(body, nil); sanitizeErr != nil {
		log.WithError(sanitizeErr).Warn("codebuddy executor: system prompt sanitize failed, sending original payload")
	} else if changed {
		body = sanitized
	}

	url := codebuddyauth.BuildChatCompletionsURL(codebuddyBaseURL(auth))
	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNew != nil {
		return nil, nil, errNew
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if token := codebuddyCreds(auth); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	codebuddyauth.ApplyChatHeaders(httpReq, codebuddyUID(auth))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	return httpReq, body, nil
}

// Execute performs a non-streaming chat completion request to CodeBuddy.
func (e *CodeBuddyExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, body, err := e.buildCodeBuddyChatRequest(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       httpReq.URL.String(),
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
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))

	to := sdktranslator.FormatOpenAI
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to CodeBuddy.
func (e *CodeBuddyExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, body, err := e.buildCodeBuddyChatRequest(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       httpReq.URL.String(),
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
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	to := sdktranslator.FormatOpenAI
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codebuddy executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576) // 1MB
		claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, to, responseFormat, bytes.Clone(opts.OriginalRequest))
		var param any
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		for scanner.Scan() {
			line := scanner.Bytes()
			// The upstream emits keep-alive comment lines (": heartbeat") that
			// are not JSON; strict downstream SSE parsers reject them, so drop
			// these lines before translating.
			if isCodeBuddySSEHeartbeat(line) {
				continue
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		doneChunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param, claudeInputTokens)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
			case <-ctx.Done():
				return
			}
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

// CountTokens estimates the token count locally; the upstream has no count endpoint.
func (e *CodeBuddyExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FormatOpenAI
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false)

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codebuddy executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codebuddy executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh rotates the OAuth tokens and re-syncs the account's available models.
func (e *CodeBuddyExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codebuddy executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("codebuddy executor: auth is nil")
	}
	refreshToken := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = strings.TrimSpace(v)
		}
	}
	if refreshToken == "" {
		// Nothing to refresh.
		return auth, nil
	}

	client := codebuddyauth.NewClient(e.cfg, auth.ProxyURL)
	token, err := client.RefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		auth.Metadata["refresh_token"] = token.RefreshToken
	}
	if token.TokenType != "" {
		auth.Metadata["token_type"] = token.TokenType
	}
	if token.Domain != "" {
		auth.Metadata["domain"] = token.Domain
	}
	if expiresAt := token.ExpiresAt(); expiresAt > 0 {
		auth.Metadata["expired"] = time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
	}
	auth.Metadata["type"] = "codebuddy-cn"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	// Re-sync the account's available models; keep the previous list on failure.
	uid := ""
	if v, ok := auth.Metadata["uid"].(string); ok {
		uid = strings.TrimSpace(v)
	}
	if configData, cfgErr := client.FetchConfig(ctx, token.AccessToken, uid); cfgErr != nil {
		log.WithError(cfgErr).Debugf("codebuddy executor: model config refresh failed for %s", auth.ID)
	} else if models, parseErr := codebuddyauth.ParseModels(configData); parseErr != nil {
		log.WithError(parseErr).Debugf("codebuddy executor: model config parse failed for %s", auth.ID)
	} else if len(models) > 0 {
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		auth.Metadata["enabled_models"] = ids
		if raw, marshalErr := json.Marshal(models); marshalErr == nil {
			auth.Metadata["models_meta"] = string(raw)
		}
	}
	return auth, nil
}
