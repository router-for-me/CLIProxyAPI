package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	iflowauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
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

	// requestExecutionMetadata in handlers stores idempotency under this key.
	iflowSessionIDMetadataKey = "idempotency_key"

	// Optional metadata key for explicit conversation binding.
	iflowConversationIDMetadataKey = "conversation_id"
)

// IFlowExecutor executes OpenAI-compatible chat completions against the iFlow API using API keys derived from OAuth.
type IFlowExecutor struct {
	cfg *config.Config
}

type iflowRequestAttempt struct {
	WithSignature bool
	UserAgent     string
	SessionID     string
	Conversation  string
	Accept        string
	ContentType   string
	HasSignature  bool
	HasTimestamp  bool
}

// NewIFlowExecutor constructs a new executor instance.
func NewIFlowExecutor(cfg *config.Config) *IFlowExecutor { return &IFlowExecutor{cfg: cfg} }

// Identifier returns the provider key.
func (e *IFlowExecutor) Identifier() string { return "iflow" }

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
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = fmt.Errorf("iflow executor: missing api key")
		return resp, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

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
	sessionID, conversationID := iflowRequestIDs(opts, body)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := e.executeChatCompletionsRequest(ctx, httpClient, auth, endpoint, apiKey, body, false, sessionID, conversationID)
	if err != nil {
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
		logWithRequestID(ctx).Debugf("request error, error status: %d error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	if bizErr, ok := parseIFlowBusinessStatusError(data); ok {
		logWithRequestID(ctx).
			WithField("mapped_status", bizErr.code).
			Warnf("iflow executor: upstream returned business error payload: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = bizErr
		return resp, err
	}
	reporter.publish(ctx, parseOpenAIUsage(data))
	// Ensure usage is recorded even if upstream omits usage metadata.
	reporter.ensurePublished(ctx)

	var param any
	// Note: TranslateNonStream uses req.Model (original with suffix) to preserve
	// the original model name in the response for client compatibility.
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: httpResp.Header.Clone()}
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
		err = fmt.Errorf("iflow executor: missing api key")
		return nil, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

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
	sessionID, conversationID := iflowRequestIDs(opts, body)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := e.executeChatCompletionsRequest(ctx, httpClient, auth, endpoint, apiKey, body, true, sessionID, conversationID)
	if err != nil {
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("iflow executor: close response body error: %v", errClose)
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		logWithRequestID(ctx).Debugf("request error, error status: %d error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	responseContentType := strings.ToLower(strings.TrimSpace(httpResp.Header.Get("Content-Type")))
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("iflow executor: close response body error: %v", errClose)
			}
		}()

		var param any
		emitChunks := func(chunks []string) {
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		emitDone := func() {
			doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("data: [DONE]"), &param)
			emitChunks(doneChunks)
			if len(doneChunks) == 0 {
				doneChunks = sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
				emitChunks(doneChunks)
			}
		}

		if !strings.Contains(responseContentType, "text/event-stream") {
			data, errRead := io.ReadAll(httpResp.Body)
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errRead}
				return
			}

			appendAPIResponseChunk(ctx, e.cfg, data)
			if bizErr, ok := parseIFlowBusinessStatusError(data); ok {
				reporter.publishFailure(ctx)
				logWithRequestID(ctx).
					WithField("mapped_status", bizErr.code).
					Warnf("iflow executor: upstream returned business error payload on stream request: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
				out <- cliproxyexecutor.StreamChunk{Err: bizErr}
				return
			}

			reporter.publish(ctx, parseOpenAIUsage(data))
			logWithRequestID(ctx).
				WithField("response_content_type", strings.TrimSpace(httpResp.Header.Get("Content-Type"))).
				Warn("iflow executor: upstream returned non-SSE response for stream request, synthesizing stream chunks")

			fallbackChunks := synthesizeOpenAIStreamingChunksFromNonStream(data)
			if len(fallbackChunks) == 0 {
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{
					Err: statusErr{
						code: http.StatusBadGateway,
						msg:  "iflow executor: upstream returned non-SSE response without valid choices",
					},
				}
				return
			}
			for i := range fallbackChunks {
				line := append([]byte("data: "), fallbackChunks[i]...)
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, line, &param)
				emitChunks(chunks)
			}
			emitDone()
			// Guarantee a usage record exists even if no usage metadata was emitted in stream chunks.
			reporter.ensurePublished(ctx)
			return
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		emittedPayload := false
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if streamErr, ok := parseOpenAIStreamNetworkErrorWithoutContent(line); ok {
				reporter.publishFailure(ctx)
				if emittedPayload {
					logWithRequestID(ctx).Warnf("iflow executor: upstream stream ended with network_error after payload: %s", streamErr.msg)
				}
				out <- cliproxyexecutor.StreamChunk{Err: streamErr}
				return
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			if len(chunks) > 0 {
				emittedPayload = true
			}
			emitChunks(chunks)
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			emitDone()
		}
		// Guarantee a usage record exists even if the stream never emitted usage data.
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
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

func (e *IFlowExecutor) executeChatCompletionsRequest(
	ctx context.Context,
	httpClient *http.Client,
	auth *cliproxyauth.Auth,
	endpoint, apiKey string,
	body []byte,
	stream bool,
	sessionID, conversationID string,
) (*http.Response, error) {
	send := func(withSignature bool) (*http.Response, iflowRequestAttempt, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, iflowRequestAttempt{}, err
		}
		applyIFlowHeaders(httpReq, apiKey, stream, withSignature, sessionID, conversationID)
		attempt := snapshotIFlowRequestAttempt(httpReq, withSignature)

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

		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			return nil, iflowRequestAttempt{}, err
		}
		return httpResp, attempt, nil
	}

	httpResp, firstAttempt, err := send(true)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode == http.StatusForbidden {
		logIFlowRejectedRequestDiagnostic(ctx, httpResp.StatusCode, firstAttempt, httpResp.Header.Clone(), "", false)
		return httpResp, nil
	}
	if httpResp.StatusCode != http.StatusNotAcceptable {
		return httpResp, nil
	}

	firstBody, _ := io.ReadAll(httpResp.Body)
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("iflow executor: close response body error: %v", errClose)
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	appendAPIResponseChunk(ctx, e.cfg, firstBody)
	logIFlowRejectedRequestDiagnostic(
		ctx,
		httpResp.StatusCode,
		firstAttempt,
		httpResp.Header.Clone(),
		summarizeErrorBody(httpResp.Header.Get("Content-Type"), firstBody),
		true,
	)

	retryResp, retryAttempt, err := send(false)
	if err != nil {
		return nil, err
	}
	if retryResp.StatusCode == http.StatusForbidden || retryResp.StatusCode == http.StatusNotAcceptable {
		logIFlowRejectedRequestDiagnostic(ctx, retryResp.StatusCode, retryAttempt, retryResp.Header.Clone(), "", false)
	}
	return retryResp, nil
}

func applyIFlowHeaders(r *http.Request, apiKey string, stream bool, withSignature bool, sessionID, conversationID string) {
	// iFlow CLI does not set an explicit Accept header for chat/completions requests.
	// The upstream decides stream format from payload.stream.
	_ = stream

	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiKey)
	r.Header.Set("user-agent", iflowUserAgent)
	r.Header.Set("session-id", sessionID)
	r.Header.Set("conversation-id", conversationID)

	if withSignature {
		// Generate timestamp and signature
		timestamp := time.Now().UnixMilli()
		r.Header.Set("x-iflow-timestamp", fmt.Sprintf("%d", timestamp))

		signature := createIFlowSignature(iflowUserAgent, sessionID, timestamp, apiKey)
		if signature != "" {
			r.Header.Set("x-iflow-signature", signature)
		}
	}
	r.Header.Del("Accept")
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

func iflowRequestIDs(opts cliproxyexecutor.Options, body []byte) (sessionID, conversationID string) {
	if len(opts.Metadata) > 0 {
		if v, ok := opts.Metadata[iflowSessionIDMetadataKey]; ok {
			sessionID = normalizeMetadataString(v)
		}
		if v, ok := opts.Metadata[iflowConversationIDMetadataKey]; ok {
			conversationID = normalizeMetadataString(v)
		}
	}

	// Best-effort extraction from request payload when metadata is unavailable.
	if conversationID == "" {
		conversationID = strings.TrimSpace(gjson.GetBytes(body, "conversation_id").String())
	}
	if conversationID == "" && len(opts.OriginalRequest) > 0 {
		conversationID = strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "conversation_id").String())
	}
	if conversationID == "" && len(opts.OriginalRequest) > 0 {
		conversationID = strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "conversationId").String())
	}

	if sessionID == "" {
		sessionID = strings.TrimSpace(gjson.GetBytes(body, "session_id").String())
	}
	if sessionID == "" && len(opts.OriginalRequest) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "session_id").String())
	}
	if sessionID == "" && len(opts.OriginalRequest) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "sessionId").String())
	}

	// Keep session id stable and non-empty for signature generation.
	if sessionID == "" && conversationID != "" {
		sessionID = conversationID
	}
	if sessionID == "" {
		sessionID = generateUUID()
	}

	return sessionID, conversationID
}

func normalizeMetadataString(v any) string {
	switch typed := v.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}

func snapshotIFlowRequestAttempt(r *http.Request, withSignature bool) iflowRequestAttempt {
	if r == nil {
		return iflowRequestAttempt{WithSignature: withSignature}
	}
	return iflowRequestAttempt{
		WithSignature: withSignature,
		UserAgent:     strings.TrimSpace(r.Header.Get("user-agent")),
		SessionID:     strings.TrimSpace(r.Header.Get("session-id")),
		Conversation:  strings.TrimSpace(r.Header.Get("conversation-id")),
		Accept:        strings.TrimSpace(r.Header.Get("Accept")),
		ContentType:   strings.TrimSpace(r.Header.Get("Content-Type")),
		HasSignature:  strings.TrimSpace(r.Header.Get("x-iflow-signature")) != "",
		HasTimestamp:  strings.TrimSpace(r.Header.Get("x-iflow-timestamp")) != "",
	}
}

func logIFlowRejectedRequestDiagnostic(
	ctx context.Context,
	status int,
	attempt iflowRequestAttempt,
	responseHeaders http.Header,
	errorSummary string,
	retryingWithoutSignature bool,
) {
	if status != http.StatusForbidden && status != http.StatusNotAcceptable {
		return
	}

	fields := log.Fields{
		"status":                     status,
		"with_signature":             attempt.WithSignature,
		"request_user_agent":         attempt.UserAgent,
		"request_session_id":         util.HideAPIKey(attempt.SessionID),
		"request_conversation_id":    util.HideAPIKey(attempt.Conversation),
		"request_accept":             attempt.Accept,
		"request_content_type":       attempt.ContentType,
		"request_has_signature":      attempt.HasSignature,
		"request_has_timestamp":      attempt.HasTimestamp,
		"response_content_type":      strings.TrimSpace(responseHeaders.Get("Content-Type")),
		"response_server":            strings.TrimSpace(responseHeaders.Get("Server")),
		"response_www_authenticate":  util.MaskSensitiveHeaderValue("WWW-Authenticate", strings.TrimSpace(responseHeaders.Get("WWW-Authenticate"))),
		"response_x_request_id":      strings.TrimSpace(responseHeaders.Get("X-Request-Id")),
		"retrying_without_signature": retryingWithoutSignature,
	}
	if errorSummary != "" {
		fields["error_summary"] = errorSummary
	}

	logWithRequestID(ctx).WithFields(fields).Warn("iflow executor: upstream rejected request")
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

func synthesizeOpenAIStreamingChunksFromNonStream(raw []byte) [][]byte {
	root := gjson.ParseBytes(raw)
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() || len(choices.Array()) == 0 {
		return nil
	}

	chunk := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[]}`
	chunk, _ = sjson.Set(chunk, "id", root.Get("id").String())
	chunk, _ = sjson.Set(chunk, "object", "chat.completion.chunk")
	chunk, _ = sjson.Set(chunk, "created", root.Get("created").Int())
	chunk, _ = sjson.Set(chunk, "model", root.Get("model").String())

	choices.ForEach(func(key, choice gjson.Result) bool {
		index := int(choice.Get("index").Int())
		if !choice.Get("index").Exists() {
			index = int(key.Int())
		}

		streamChoice := `{"index":0,"delta":{},"finish_reason":null}`
		streamChoice, _ = sjson.Set(streamChoice, "index", index)

		role := strings.TrimSpace(choice.Get("message.role").String())
		if role != "" {
			streamChoice, _ = sjson.Set(streamChoice, "delta.role", role)
		}

		content := choice.Get("message.content")
		if content.Exists() && content.Type != gjson.Null {
			if content.Type == gjson.String {
				streamChoice, _ = sjson.Set(streamChoice, "delta.content", content.String())
			} else {
				streamChoice, _ = sjson.SetRaw(streamChoice, "delta.content", content.Raw)
			}
		}

		reasoning := choice.Get("message.reasoning_content")
		if reasoning.Exists() && reasoning.Type != gjson.Null {
			streamChoice, _ = sjson.SetRaw(streamChoice, "delta.reasoning_content", reasoning.Raw)
		}

		toolCalls := choice.Get("message.tool_calls")
		if toolCalls.Exists() && toolCalls.Type != gjson.Null {
			streamChoice, _ = sjson.SetRaw(streamChoice, "delta.tool_calls", toolCalls.Raw)
		}

		finishReason := choice.Get("finish_reason")
		if finishReason.Exists() && finishReason.Type != gjson.Null {
			streamChoice, _ = sjson.Set(streamChoice, "finish_reason", finishReason.String())
		}

		path := fmt.Sprintf("choices.%d", index)
		chunk, _ = sjson.SetRaw(chunk, path, streamChoice)
		return true
	})

	out := [][]byte{[]byte(chunk)}
	usage := root.Get("usage")
	if usage.Exists() && usage.Type != gjson.Null {
		usageChunk := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[],"usage":{}}`
		usageChunk, _ = sjson.Set(usageChunk, "id", root.Get("id").String())
		usageChunk, _ = sjson.Set(usageChunk, "object", "chat.completion.chunk")
		usageChunk, _ = sjson.Set(usageChunk, "created", root.Get("created").Int())
		usageChunk, _ = sjson.Set(usageChunk, "model", root.Get("model").String())
		usageChunk, _ = sjson.SetRaw(usageChunk, "usage", usage.Raw)
		out = append(out, []byte(usageChunk))
	}
	return out
}

func parseIFlowBusinessStatusError(raw []byte) (statusErr, bool) {
	root := gjson.ParseBytes(raw)

	message := strings.TrimSpace(root.Get("msg").String())
	if message == "" {
		message = strings.TrimSpace(root.Get("message").String())
	}
	if message == "" {
		message = strings.TrimSpace(root.Get("error.message").String())
	}

	statusRaw := strings.TrimSpace(root.Get("status").String())
	statusCode := parseNumericStatus(statusRaw)
	if statusCode == 0 {
		statusCode = int(root.Get("status").Int())
	}
	if statusCode == 0 {
		statusCode = parseNumericStatus(strings.TrimSpace(root.Get("error.code").String()))
	}

	normalized := normalizeIFlowBusinessStatus(statusCode, message)
	if normalized > 0 {
		if message == "" {
			message = fmt.Sprintf("status %d", normalized)
		}
		return statusErr{code: normalized, msg: message}, true
	}

	// No explicit business status. If an error object is present, treat it as a bad request.
	if root.Get("error").Exists() {
		if message == "" {
			message = strings.TrimSpace(root.Get("error").Raw)
		}
		if message == "" {
			message = "iflow upstream returned error payload"
		}
		return statusErr{code: http.StatusBadRequest, msg: message}, true
	}
	return statusErr{}, false
}

func parseNumericStatus(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	code, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return code
}

func normalizeIFlowBusinessStatus(statusCode int, message string) int {
	if statusCode == 449 {
		// iFlow business status used for rate-limiting.
		return http.StatusTooManyRequests
	}
	if statusCode >= 400 && statusCode < 600 {
		return statusCode
	}

	msg := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "too many requests"),
		strings.Contains(msg, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(msg, "forbidden"):
		return http.StatusForbidden
	case strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "invalid api key"),
		strings.Contains(msg, "invalid token"):
		return http.StatusUnauthorized
	case strings.Contains(msg, "not acceptable"):
		return http.StatusNotAcceptable
	case strings.Contains(msg, "timeout"):
		return http.StatusRequestTimeout
	default:
		return 0
	}
}

func parseOpenAIStreamNetworkErrorWithoutContent(line []byte) (statusErr, bool) {
	payload := bytes.TrimSpace(line)
	if len(payload) == 0 {
		return statusErr{}, false
	}

	// SSE data frame.
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return statusErr{}, false
	}
	if !gjson.ValidBytes(payload) {
		return statusErr{}, false
	}

	root := gjson.ParseBytes(payload)
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() || len(choices.Array()) == 0 {
		return statusErr{}, false
	}

	hasNetworkError := false
	hasContent := false
	choices.ForEach(func(_, choice gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(choice.Get("finish_reason").String()), "network_error") {
			hasNetworkError = true
		}

		content := strings.TrimSpace(choice.Get("delta.content").String())
		reasoning := strings.TrimSpace(choice.Get("delta.reasoning_content").String())
		toolCalls := choice.Get("delta.tool_calls")
		if content != "" || reasoning != "" || (toolCalls.Exists() && toolCalls.Type != gjson.Null && strings.TrimSpace(toolCalls.Raw) != "" && toolCalls.Raw != "[]") {
			hasContent = true
		}
		return true
	})

	if !hasNetworkError || hasContent {
		return statusErr{}, false
	}

	model := strings.TrimSpace(root.Get("model").String())
	if model == "" {
		model = "unknown"
	}
	msg := fmt.Sprintf("iflow upstream stream network_error for model %s", model)
	return statusErr{code: http.StatusBadGateway, msg: msg}, true
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
