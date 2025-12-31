package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	iflowauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	iflowDefaultEndpoint = "/chat/completions"
	iflowUserAgent       = "iFlow-Cli"
)

// IFlowExecutor executes OpenAI-compatible chat completions against the iFlow API using API keys derived from OAuth.
type IFlowExecutor struct {
	cfg *config.Config
}

// NewIFlowExecutor constructs a new executor instance.
func NewIFlowExecutor(cfg *config.Config) *IFlowExecutor { return &IFlowExecutor{cfg: cfg} }

// Identifier returns the provider key.
func (e *IFlowExecutor) Identifier() string { return "iflow" }

// PrepareRequest implements ProviderExecutor but requires no preprocessing.
func (e *IFlowExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

// Execute performs a non-streaming chat completion request.
func (e *IFlowExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = fmt.Errorf("iflow executor: missing api key")
		return resp, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning_effort", false)
	body, _ = sjson.SetBytes(body, "model", req.Model)
	body = NormalizeThinkingConfig(body, req.Model, false)
	if errValidate := ValidateThinkingConfig(body, req.Model); errValidate != nil {
		return resp, errValidate
	}
	body = applyIFlowThinkingConfig(body)
	body = preserveReasoningContentInMessages(body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	endpoint := strings.TrimSuffix(baseURL, "/") + iflowDefaultEndpoint

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyIFlowHeaders(httpReq, apiKey, false)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("iflow executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("iflow request error: status %d body %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))
	// Ensure usage is recorded even if upstream omits usage metadata.
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request.
func (e *IFlowExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = fmt.Errorf("iflow executor: missing api key")
		return nil, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning_effort", false)
	body, _ = sjson.SetBytes(body, "model", req.Model)
	body = NormalizeThinkingConfig(body, req.Model, false)
	if errValidate := ValidateThinkingConfig(body, req.Model); errValidate != nil {
		return nil, errValidate
	}
	body = applyIFlowThinkingConfig(body)
	body = preserveReasoningContentInMessages(body)
	// Ensure tools array exists to avoid provider quirks similar to Qwen's behaviour.
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() && len(toolsResult.Array()) == 0 {
		body = ensureToolsArray(body)
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)

	endpoint := strings.TrimSuffix(baseURL, "/") + iflowDefaultEndpoint

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyIFlowHeaders(httpReq, apiKey, true)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("iflow executor: close response body error: %v", errClose)
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("iflow streaming error: status %d body %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("iflow executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
		// Guarantee a usage record exists even if the stream never emitted usage data.
		reporter.ensurePublished(ctx)
	}()

	return stream, nil
}

func (e *IFlowExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	enc, err := tokenizerForModel(req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("iflow executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("iflow executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

// Refresh refreshes OAuth tokens or cookie-based API keys and updates the stored API key.
func (e *IFlowExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("iflow executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("iflow executor: auth is nil")
	}

	// Check if this is cookie-based authentication
	var cookie string
	var email string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["cookie"].(string); ok {
			cookie = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["email"].(string); ok {
			email = strings.TrimSpace(v)
		}
	}

	// If cookie is present, use cookie-based refresh
	if cookie != "" && email != "" {
		return e.refreshCookieBased(ctx, auth, cookie, email)
	}

	// Otherwise, use OAuth-based refresh
	return e.refreshOAuthBased(ctx, auth)
}

// refreshCookieBased refreshes API key using browser cookie
func (e *IFlowExecutor) refreshCookieBased(ctx context.Context, auth *cliproxyauth.Auth, cookie, email string) (*cliproxyauth.Auth, error) {
	log.Debugf("iflow executor: checking refresh need for cookie-based API key for user: %s", email)

	// Get current expiry time from metadata
	var currentExpire string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["expired"].(string); ok {
			currentExpire = strings.TrimSpace(v)
		}
	}

	// Check if refresh is needed
	needsRefresh, _, err := iflowauth.ShouldRefreshAPIKey(currentExpire)
	if err != nil {
		log.Warnf("iflow executor: failed to check refresh need: %v", err)
		// If we can't check, continue with refresh anyway as a safety measure
	} else if !needsRefresh {
		log.Debugf("iflow executor: no refresh needed for user: %s", email)
		return auth, nil
	}

	log.Infof("iflow executor: refreshing cookie-based API key for user: %s", email)

	svc := iflowauth.NewIFlowAuth(e.cfg)
	keyData, err := svc.RefreshAPIKey(ctx, cookie, email)
	if err != nil {
		log.Errorf("iflow executor: cookie-based API key refresh failed: %v", err)
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["api_key"] = keyData.APIKey
	auth.Metadata["expired"] = keyData.ExpireTime
	auth.Metadata["type"] = "iflow"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	auth.Metadata["cookie"] = cookie
	auth.Metadata["email"] = email

	log.Infof("iflow executor: cookie-based API key refreshed successfully, new expiry: %s", keyData.ExpireTime)

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["api_key"] = keyData.APIKey

	return auth, nil
}

// refreshOAuthBased refreshes tokens using OAuth refresh token
func (e *IFlowExecutor) refreshOAuthBased(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	refreshToken := ""
	oldAccessToken := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["access_token"].(string); ok {
			oldAccessToken = strings.TrimSpace(v)
		}
	}
	if refreshToken == "" {
		return auth, nil
	}

	// Log the old access token (masked) before refresh
	if oldAccessToken != "" {
		log.Debugf("iflow executor: refreshing access token, old: %s", util.HideAPIKey(oldAccessToken))
	}

	svc := iflowauth.NewIFlowAuth(e.cfg)
	tokenData, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		log.Errorf("iflow executor: token refresh failed: %v", err)
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.APIKey != "" {
		auth.Metadata["api_key"] = tokenData.APIKey
	}
	auth.Metadata["expired"] = tokenData.Expire
	auth.Metadata["type"] = "iflow"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	// Log the new access token (masked) after successful refresh
	log.Debugf("iflow executor: token refresh successful, new: %s", util.HideAPIKey(tokenData.AccessToken))

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if tokenData.APIKey != "" {
		auth.Attributes["api_key"] = tokenData.APIKey
	}

	return auth, nil
}

func applyIFlowHeaders(r *http.Request, apiKey string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiKey)
	r.Header.Set("User-Agent", iflowUserAgent)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

func iflowCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["api_key"]); v != "" {
			apiKey = v
		}
		if v := strings.TrimSpace(a.Attributes["base_url"]); v != "" {
			baseURL = v
		}
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["api_key"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	if baseURL == "" && a.Metadata != nil {
		if v, ok := a.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	return apiKey, baseURL
}

func ensureToolsArray(body []byte) []byte {
	placeholder := `[{"type":"function","function":{"name":"noop","description":"Placeholder tool to stabilise streaming","parameters":{"type":"object"}}}]`
	updated, err := sjson.SetRawBytes(body, "tools", []byte(placeholder))
	if err != nil {
		return body
	}
	return updated
}

// preserveReasoningContentInMessages ensures reasoning_content from assistant messages in the
// conversation history is preserved when sending to iFlow models that support thinking.
// This is critical for multi-turn conversations where the model needs to see its previous
// reasoning to maintain coherent thought chains across tool calls and conversation turns.
//
// For GLM-4.7 and MiniMax-M2.1, the full assistant response (including reasoning) must be
// appended back into message history before the next call.
func preserveReasoningContentInMessages(body []byte) []byte {
	model := strings.ToLower(gjson.GetBytes(body, "model").String())

	// Only apply to models that support thinking with history preservation
	needsPreservation := strings.HasPrefix(model, "glm-4.7") ||
		strings.HasPrefix(model, "glm-4-7") ||
		strings.HasPrefix(model, "minimax-m2.1") ||
		strings.HasPrefix(model, "minimax-m2-1")

	if !needsPreservation {
		return body
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	// Check if any assistant message already has reasoning_content preserved
	hasReasoningContent := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role == "assistant" {
			rc := msg.Get("reasoning_content")
			if rc.Exists() && rc.String() != "" {
				hasReasoningContent = true
				return false // stop iteration
			}
		}
		return true
	})

	// If reasoning content is already present, the messages are properly formatted
	// No need to modify - the client has correctly preserved reasoning in history
	if hasReasoningContent {
		log.Debugf("iflow executor: reasoning_content found in message history for %s", model)
	}

	return body
}

// applyIFlowThinkingConfig converts normalized reasoning_effort to model-specific thinking configurations.
// This should be called after NormalizeThinkingConfig has processed the payload.
//
// Model-specific handling:
//   - GLM-4.7: Uses extra_body={"thinking": {"type": "enabled"}, "clear_thinking": false}
//   - MiniMax-M2.1: Uses reasoning_split=true for OpenAI-style reasoning separation
//   - Other iFlow models: Uses chat_template_kwargs.enable_thinking (boolean)
func applyIFlowThinkingConfig(body []byte) []byte {
	effort := gjson.GetBytes(body, "reasoning_effort")
	model := strings.ToLower(gjson.GetBytes(body, "model").String())

	// Check if thinking should be enabled
	val := ""
	if effort.Exists() {
		val = strings.ToLower(strings.TrimSpace(effort.String()))
	}
	enableThinking := effort.Exists() && val != "none" && val != ""

	// Remove reasoning_effort as we'll convert to model-specific format
	if effort.Exists() {
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
	}

	// GLM-4.7: Use extra_body with thinking config and clear_thinking: false
	if strings.HasPrefix(model, "glm-4.7") || strings.HasPrefix(model, "glm-4-7") {
		if enableThinking {
			body, _ = sjson.SetBytes(body, "extra_body.thinking.type", "enabled")
			body, _ = sjson.SetBytes(body, "extra_body.clear_thinking", false)
		}
		return body
	}

	// MiniMax-M2.1: Use reasoning_split=true for interleaved thinking
	if strings.HasPrefix(model, "minimax-m2.1") || strings.HasPrefix(model, "minimax-m2-1") {
		if enableThinking {
			body, _ = sjson.SetBytes(body, "reasoning_split", true)
		}
		return body
	}

	// Other iFlow models (including GLM-4.6): Use chat_template_kwargs.enable_thinking
	if effort.Exists() {
		body, _ = sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", enableThinking)
	}

	return body
}
