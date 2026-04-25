package executor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	iflowauth "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/iflow"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
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

type iflowProviderError struct {
	Code        string
	Message     string
	Refreshable bool
}

func (e *iflowProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("iflow executor: upstream error status=%s: %s", e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("iflow executor: upstream error: %s", e.Message)
	}
	return "iflow executor: upstream error"
}

func (e *iflowProviderError) StatusCode() int {
	if e == nil {
		return http.StatusBadGateway
	}
	switch e.Code {
	case "401", "403", "439":
		return http.StatusUnauthorized
	case "429":
		return http.StatusTooManyRequests
	case "500", "502", "503", "504":
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}

func detectIFlowProviderError(rawJSON []byte) *iflowProviderError {
	root := gjson.ParseBytes(rawJSON)
	status := strings.TrimSpace(root.Get("status").String())
	if status == "" || status == "0" || status == "200" {
		return nil
	}

	if root.Get("choices").Exists() || root.Get("object").String() == "chat.completion" {
		return nil
	}

	msg := strings.TrimSpace(root.Get("msg").String())
	if msg == "" {
		msg = strings.TrimSpace(root.Get("message").String())
	}
	if msg == "" && root.Get("body").Exists() && !root.Get("body").IsObject() && !root.Get("body").IsArray() {
		msg = strings.TrimSpace(root.Get("body").String())
	}
	if msg == "" {
		msg = "unknown provider error"
	}

	lowerMsg := strings.ToLower(msg)
	return &iflowProviderError{
		Code:        status,
		Message:     msg,
		Refreshable: status == "439" || strings.Contains(lowerMsg, "expired"),
	}
}

// PrepareRequest injects iFlow credentials into the outgoing HTTP request.
func (e *IFlowExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := iflowCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return nil
}

// HttpRequest injects iFlow credentials into the request and executes it.
func (e *IFlowExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("iflow executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat completion request.
func (e *IFlowExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	return e.execute(ctx, auth, req, opts, true)
}

func (e *IFlowExecutor) execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, allowRefresh bool) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "iflow executor: missing api key"}
		return resp, err
	}
	baseURL = resolveOAuthBaseURLWithOverride(e.cfg, e.Identifier(), iflowauth.DefaultAPIBaseURL, baseURL)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	body, _ = sjson.SetBytes(body, "model", baseModel)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "iflow", e.Identifier())
	if err != nil {
		return resp, err
	}

	body = preserveReasoningContentInMessages(body)
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

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

	httpResp, err := ExecuteHTTPRequest(ctx, e.cfg, auth, httpReq, "iflow executor")
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("iflow executor: close response body error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))
	// Ensure usage is recorded even if upstream omits usage metadata.
	reporter.ensurePublished(ctx)

	if providerErr := detectIFlowProviderError(data); providerErr != nil {
		recordAPIResponseError(ctx, e.cfg, providerErr)
		if allowRefresh && providerErr.Refreshable {
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				return resp, refreshErr
			}
			if refreshedAuth != nil {
				return e.execute(ctx, refreshedAuth, req, opts, false)
			}
		}
		return resp, providerErr
	}

	var param any
	// Note: TranslateNonStream uses req.Model (original with suffix) to preserve
	// the original model name in the response for client compatibility.
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request.
func (e *IFlowExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "iflow executor: missing api key"}
		return nil, err
	}
	baseURL = resolveOAuthBaseURLWithOverride(e.cfg, e.Identifier(), iflowauth.DefaultAPIBaseURL, baseURL)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	body, _ = sjson.SetBytes(body, "model", baseModel)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "iflow", e.Identifier())
	if err != nil {
		return nil, err
	}

	body = preserveReasoningContentInMessages(body)
	// Ensure tools array exists to avoid provider quirks similar to Qwen's behaviour.
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() && len(toolsResult.Array()) == 0 {
		body = ensureToolsArray(body)
	}
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

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

	httpResp, err := ExecuteHTTPRequestForStreaming(ctx, e.cfg, auth, httpReq, "iflow executor")
	if err != nil {
		var status statusErr
		if from == sdktranslator.FromString("openai-response") && errors.As(err, &status) && status.code == http.StatusNotAcceptable {
			return e.executeResponsesStreamFallback(ctx, auth, req, opts)
		}
		return nil, err
	}

	var param any
	processor := func(ctx context.Context, line []byte) ([]string, error) {
		appendAPIResponseChunk(ctx, e.cfg, line)
		if detail, ok := parseOpenAIStreamUsage(line); ok {
			reporter.publish(ctx, detail)
		}
		chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
		return chunks, nil
	}

	result := ProcessSSEStream(ctx, httpResp, processor, func(ctx context.Context, err error) {
		recordAPIResponseError(ctx, e.cfg, err)
		reporter.publishFailure(ctx)
	})

	// Wrap the original channel to ensure usage is published after stream completes
	wrappedOut := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(wrappedOut)
		for chunk := range result.Chunks {
			wrappedOut <- chunk
		}
		// Guarantee a usage record exists even if the stream never emitted usage data.
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: wrappedOut}, nil
}

func (e *IFlowExecutor) executeResponsesStreamFallback(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
) (*cliproxyexecutor.StreamResult, error) {
	fallbackReq := req
	if updated, err := sjson.SetBytes(fallbackReq.Payload, "stream", false); err == nil {
		fallbackReq.Payload = updated
	}

	fallbackOpts := opts
	fallbackOpts.Stream = false
	if updated, err := sjson.SetBytes(fallbackOpts.OriginalRequest, "stream", false); err == nil {
		fallbackOpts.OriginalRequest = updated
	}

	resp, err := e.Execute(ctx, auth, fallbackReq, fallbackOpts)
	if err != nil {
		return nil, err
	}

	payload, err := synthesizeOpenAIResponsesCompletionEvent(resp.Payload)
	if err != nil {
		return nil, err
	}

	headers := resp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	headers.Del("Content-Length")

	out := make(chan cliproxyexecutor.StreamChunk, 1)
	out <- cliproxyexecutor.StreamChunk{Payload: payload}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func (e *IFlowExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	enc, err := tokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("iflow executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("iflow executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translated}, nil
}

// Refresh refreshes OAuth tokens or cookie-based API keys and updates the stored API key.
func (e *IFlowExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("iflow executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "iflow executor: missing auth"}
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

	svc := iflowauth.NewIFlowAuth(e.cfg, nil)
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

	// Log refresh start without including token material.
	if oldAccessToken != "" {
		log.Debug("iflow executor: refreshing access token")
	}

	svc := iflowauth.NewIFlowAuth(e.cfg, nil)
	tokenData, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		log.Errorf("iflow executor: token refresh failed: %v", err)
		return nil, classifyIFlowRefreshError(err)
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

	log.Debug("iflow executor: token refresh successful")

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if tokenData.APIKey != "" {
		auth.Attributes["api_key"] = tokenData.APIKey
	}

	return auth, nil
}

func classifyIFlowRefreshError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "iflow token") && strings.Contains(msg, "server busy") {
		return statusErr{code: http.StatusServiceUnavailable, msg: err.Error()}
	}
	if strings.Contains(msg, "provider rejected token request") && (strings.Contains(msg, "code=429") || strings.Contains(msg, "too many requests") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota")) {
		return statusErr{code: http.StatusTooManyRequests, msg: err.Error()}
	}
	if strings.Contains(msg, "provider rejected token request") && strings.Contains(msg, "code=503") {
		return statusErr{code: http.StatusServiceUnavailable, msg: err.Error()}
	}
	if strings.Contains(msg, "provider rejected token request") && strings.Contains(msg, "code=500") {
		return statusErr{code: http.StatusServiceUnavailable, msg: err.Error()}
	}
	return err
}

func applyIFlowHeaders(r *http.Request, apiKey string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiKey)
	r.Header.Set("User-Agent", iflowUserAgent)

	// Generate session-id
	sessionID := "session-" + generateUUID()
	r.Header.Set("session-id", sessionID)

	// Generate timestamp and signature
	timestamp := time.Now().UnixMilli()
	r.Header.Set("x-iflow-timestamp", fmt.Sprintf("%d", timestamp))

	signature := createIFlowSignature(iflowUserAgent, sessionID, timestamp, apiKey)
	if signature != "" {
		r.Header.Set("x-iflow-signature", signature)
	}

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

// createIFlowSignature generates HMAC-SHA256 signature for iFlow API requests.
// The signature payload format is: userAgent:sessionId:timestamp
func createIFlowSignature(userAgent, sessionID string, timestamp int64, apiKey string) string {
	if apiKey == "" {
		return ""
	}
	payload := fmt.Sprintf("%s:%s:%d", userAgent, sessionID, timestamp)
	h := hmac.New(sha256.New, []byte(apiKey))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// generateUUID generates a random UUID v4 string.
func generateUUID() string {
	return uuid.New().String()
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

// preserveReasoningContentInMessages checks if reasoning_content from assistant messages
// is preserved in conversation history for iFlow models that support thinking.
// This is helpful for multi-turn conversations where the model may benefit from seeing
// its previous reasoning to maintain coherent thought chains.
//
// For GLM-4.6/4.7 and MiniMax M2/M2.1, it is recommended to include the full assistant
// response (including reasoning_content) in message history for better context continuity.
func preserveReasoningContentInMessages(body []byte) []byte {
	model := strings.ToLower(gjson.GetBytes(body, "model").String())

	// Only apply to models that support thinking with history preservation
	needsPreservation := strings.HasPrefix(model, "glm-4") || strings.HasPrefix(model, "minimax-m2")

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

func (e *IFlowExecutor) CloseExecutionSession(sessionID string) {}
