package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/kiro"
	kiroclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/claude"
	kirocommon "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/common"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/util"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors and quota fallback on 429.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
	}

	// Rate limiting: get token key for tracking
	tokenKey := getAccountKey(auth)
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()

	// Check if token is in cooldown period
	if cooldownMgr.IsInCooldown(tokenKey) {
		remaining := cooldownMgr.GetRemainingCooldown(tokenKey)
		reason := cooldownMgr.GetCooldownReason(tokenKey)
		log.Warnf("kiro: token %s is in cooldown (reason: %s), remaining: %v", tokenKey, reason, remaining)
		return nil, fmt.Errorf("kiro: token is in cooldown for %v (reason: %s)", remaining, reason)
	}

	// Wait for rate limiter before proceeding
	log.Debugf("kiro: stream waiting for rate limiter for token %s", tokenKey)
	rateLimiter.WaitForToken(tokenKey)
	log.Debugf("kiro: stream rate limiter cleared for token %s", tokenKey)

	// Check if token is expired before making request (covers both normal and web_search paths)
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery before stream request")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh) for stream, expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh for stream", reloadErr)
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
			} else if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
				}
				accessToken, profileArn = kiroCredentials(auth)
				log.Infof("kiro: token refreshed successfully before stream request")
			}
		}
	}

	// Check for pure web_search request
	// Route to MCP endpoint instead of normal Kiro API
	if kiroclaude.HasWebSearchTool(req.Payload) {
		log.Infof("kiro: detected pure web_search request, routing to MCP endpoint")
		streamWebSearch, errWebSearch := e.handleWebSearchStream(ctx, auth, req, opts, accessToken, profileArn)
		if errWebSearch != nil {
			return nil, errWebSearch
		}
		return &cliproxyexecutor.StreamResult{Chunks: streamWebSearch}, nil
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)

	// Fetch profileArn if missing (for imported accounts from Kiro IDE)
	if profileArn == "" {
		if fetched, _ := e.fetchAndSaveProfileArn(ctx, auth, accessToken); fetched != "" {
			profileArn = fetched
		}
	}

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeStreamWithRetry for each endpoint
	streamKiro, errStreamKiro := e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, body, from, reporter, kiroModelID, isAgentic, isChatOnly, tokenKey)
	if errStreamKiro != nil {
		return nil, errStreamKiro
	}
	return &cliproxyexecutor.StreamResult{Chunks: streamKiro}, nil
}

// executeStreamWithRetry performs the streaming HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
// tokenKey is used for rate limiting and cooldown tracking.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, body []byte, from sdktranslator.Format, reporter *usageReporter, kiroModelID string, isAgentic, isChatOnly bool, tokenKey string) (<-chan cliproxyexecutor.StreamChunk, error) {
	var currentOrigin string
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()
	endpointConfigs := getKiroEndpointConfigs(auth)
	var last429Err error

	for endpointIdx := 0; endpointIdx < len(endpointConfigs); endpointIdx++ {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin

		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload, thinkingEnabled := buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)

		log.Debugf("kiro: stream trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

		for attempt := 0; attempt <= maxRetries; attempt++ {
			// Apply human-like delay before first streaming request (not on retries)
			// This mimics natural user behavior patterns
			// Note: Delay is NOT applied during streaming response - only before initial request
			if attempt == 0 && endpointIdx == 0 {
				kiroauth.ApplyHumanLikeDelay()
			}

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
			if err != nil {
				return nil, err
			}

			httpReq.Header.Set("Content-Type", kiroContentType)
			httpReq.Header.Set("Accept", kiroAcceptStream)
			// Only set X-Amz-Target if specified (Q endpoint doesn't require it)
			if endpointConfig.AmzTarget != "" {
				httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
			}
			// Kiro-specific headers
			httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentMode)
			httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")

			// Apply dynamic fingerprint-based headers
			applyDynamicFingerprint(httpReq, auth)

			httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
			httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

			// Bearer token authentication for all auth types (Builder ID, IDC, social, etc.)
			httpReq.Header.Set("Authorization", "Bearer "+accessToken)

			var attrs map[string]string
			if auth != nil {
				attrs = auth.Attributes
			}
			util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

			var authID, authLabel, authType, authValue string
			if auth != nil {
				authID = auth.ID
				authLabel = auth.Label
				authType, authValue = auth.AccountInfo()
			}
			recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
				URL:       url,
				Method:    http.MethodPost,
				Headers:   httpReq.Header.Clone(),
				Body:      kiroPayload,
				Provider:  e.Identifier(),
				AuthID:    authID,
				AuthLabel: authLabel,
				AuthType:  authType,
				AuthValue: authValue,
			})

			httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 0)
			httpResp, err := httpClient.Do(httpReq)
			if err != nil {
				recordAPIResponseError(ctx, e.cfg, err)

				// Enhanced socket retry for streaming: Check if error is retryable (network timeout, connection reset, etc.)
				retryCfg := defaultRetryConfig()
				if isRetryableError(err) && attempt < retryCfg.MaxRetries {
					delay := calculateRetryDelay(attempt, retryCfg)
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream socket error: %v", err), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				}

				return nil, err
			}
			recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

			// Handle 429 errors (quota exhausted) - try next endpoint
			// Each endpoint has its own quota pool, so we can try different endpoints
			if httpResp.StatusCode == 429 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				// Record failure and set cooldown for 429
				rateLimiter.MarkTokenFailed(tokenKey)
				cooldownDuration := kiroauth.CalculateCooldownFor429(attempt)
				cooldownMgr.SetCooldown(tokenKey, cooldownDuration, kiroauth.CooldownReason429)
				log.Warnf("kiro: stream rate limit hit (429), token %s set to cooldown for %v", tokenKey, cooldownDuration)

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: stream %s endpoint quota exhausted (429), will try next endpoint, body: %s",
					endpointConfig.Name, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				// Break inner retry loop to try next endpoint (which has different quota)
				break
			}

			// Handle 5xx server errors with exponential backoff retry
			// Enhanced: Use retryConfig for consistent retry behavior
			if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				retryCfg := defaultRetryConfig()
				// Check if this specific 5xx code is retryable (502, 503, 504)
				if isRetryableHTTPStatus(httpResp.StatusCode) && attempt < retryCfg.MaxRetries {
					delay := calculateRetryDelay(attempt, retryCfg)
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream HTTP %d", httpResp.StatusCode), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				} else if attempt < maxRetries {
					// Fallback for other 5xx errors (500, 501, etc.)
					backoff := time.Duration(1<<attempt) * time.Second
					if backoff > 30*time.Second {
						backoff = 30 * time.Second
					}
					log.Warnf("kiro: stream server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
					time.Sleep(backoff)
					continue
				}
				log.Errorf("kiro: stream server error %d after %d retries", httpResp.StatusCode, maxRetries)
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 400 errors - Credential/Validation issues
			// Do NOT switch endpoints - return error immediately
			if httpResp.StatusCode == 400 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: received 400 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				// 400 errors indicate request validation issues - return immediately without retry
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 401 errors with token refresh and retry
			// 401 = Unauthorized (token expired/invalid) - refresh token
			if httpResp.StatusCode == 401 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: stream received 401 error, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}

				if refreshedAuth != nil {
					auth = refreshedAuth
					// Persist the refreshed auth to file so subsequent requests use it
					if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
						log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
						// Continue anyway - the token is valid for this request
					}
					accessToken, profileArn = kiroCredentials(auth)
					// Rebuild payload with new profile ARN if changed
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
					if attempt < maxRetries {
						log.Infof("kiro: token refreshed successfully, retrying stream request (attempt %d/%d)", attempt+1, maxRetries+1)
						continue
					}
					log.Infof("kiro: token refreshed successfully, no retries remaining")
				}

				log.Warnf("kiro stream error, status: 401, body: %s", string(respBody))
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 402 errors - Monthly Limit Reached
			if httpResp.StatusCode == 402 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: stream received 402 (monthly limit). Upstream body: %s", string(respBody))

				// Return upstream error body directly
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 403 errors - Access Denied / Token Expired
			// Do NOT switch endpoints for 403 errors
			if httpResp.StatusCode == 403 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				appendAPIResponseChunk(ctx, e.cfg, respBody)

				// Log the 403 error details for debugging
				log.Warnf("kiro: stream received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, string(respBody))

				respBodyStr := string(respBody)

				// Check for SUSPENDED status - return immediately without retry
				if strings.Contains(respBodyStr, "SUSPENDED") || strings.Contains(respBodyStr, "TEMPORARILY_SUSPENDED") {
					// Set long cooldown for suspended accounts
					rateLimiter.CheckAndMarkSuspended(tokenKey, respBodyStr)
					cooldownMgr.SetCooldown(tokenKey, kiroauth.LongCooldown, kiroauth.CooldownReasonSuspended)
					log.Errorf("kiro: stream account is suspended, token %s set to cooldown for %v", tokenKey, kiroauth.LongCooldown)
					return nil, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + string(respBody)}
				}

				// Check if this looks like a token-related 403 (some APIs return 403 for expired tokens)
				isTokenRelated := strings.Contains(respBodyStr, "token") ||
					strings.Contains(respBodyStr, "expired") ||
					strings.Contains(respBodyStr, "invalid") ||
					strings.Contains(respBodyStr, "unauthorized")

				if isTokenRelated && attempt < maxRetries {
					log.Warnf("kiro: 403 appears token-related, attempting token refresh")
					refreshedAuth, refreshErr := e.Refresh(ctx, auth)
					if refreshErr != nil {
						log.Errorf("kiro: token refresh failed: %v", refreshErr)
						// Token refresh failed - return error immediately
						return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
					}
					if refreshedAuth != nil {
						auth = refreshedAuth
						// Persist the refreshed auth to file so subsequent requests use it
						if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
							log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
							// Continue anyway - the token is valid for this request
						}
						accessToken, profileArn = kiroCredentials(auth)
						kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
						log.Infof("kiro: token refreshed for 403, retrying stream request")
						continue
					}
				}

				// For non-token 403 or after max retries, return error immediately
				// Do NOT switch endpoints for 403 errors
				log.Warnf("kiro: 403 error, returning immediately (no endpoint switch)")
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
				b, _ := io.ReadAll(httpResp.Body)
				appendAPIResponseChunk(ctx, e.cfg, b)
				log.Debugf("kiro stream error, status: %d, body: %s", httpResp.StatusCode, string(b))
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
				return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
			}

			out := make(chan cliproxyexecutor.StreamChunk)

			// Record success immediately since connection was established successfully
			// Streaming errors will be handled separately
			rateLimiter.MarkTokenSuccess(tokenKey)
			log.Debugf("kiro: stream request successful, token %s marked as success", tokenKey)

			go func(resp *http.Response, thinkingEnabled bool) {
				defer close(out)
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("kiro: panic in stream handler: %v", r)
						out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("internal error: %v", r)}
					}
				}()
				defer func() {
					if errClose := resp.Body.Close(); errClose != nil {
						log.Errorf("response body close error: %v", errClose)
					}
				}()

				// Kiro API always returns <thinking> tags regardless of request parameters
				// So we always enable thinking parsing for Kiro responses
				log.Debugf("kiro: stream thinkingEnabled = %v (always true for Kiro)", thinkingEnabled)

				e.streamToChannel(ctx, resp.Body, out, from, payloadRequestedModel(opts, req.Model), opts.OriginalRequest, body, reporter, thinkingEnabled)
			}(httpResp, thinkingEnabled)

			return out, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	if last429Err != nil {
		return nil, last429Err
	}
	return nil, fmt.Errorf("kiro: stream all endpoints exhausted")
}

// EventStreamError represents an Event Stream processing error
type EventStreamError struct {
	Type    string // "fatal", "malformed"
	Message string
	Cause   error
}

func (e *EventStreamError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("event stream %s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("event stream %s: %s", e.Type, e.Message)
}

// eventStreamMessage represents a parsed AWS Event Stream message
type eventStreamMessage struct {
	EventType string // Event type from headers (e.g., "assistantResponseEvent")
	Payload   []byte // JSON payload of the message
}

// NOTE: Request building functions moved to pkg/llmproxy/translator/kiro/claude/kiro_claude_request.go
// The executor now uses kiroclaude.BuildKiroPayload() instead

// parseEventStream parses AWS Event Stream binary format.
// Extracts text content, tool uses, and stop_reason from the response.
// Supports embedded [Called ...] tool calls and input buffering for toolUseEvent.
// Returns: content, toolUses, usageInfo, stopReason, error
func (e *KiroExecutor) parseEventStream(body io.Reader) (string, []kiroclaude.KiroToolUse, usage.Detail, string, error) {
	var content strings.Builder
	var toolUses []kiroclaude.KiroToolUse
	var usageInfo usage.Detail
	var stopReason string // Extracted from upstream response
	reader := bufio.NewReader(body)

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *kiroclaude.ToolUseState

	// Upstream usage tracking - Kiro API returns credit usage and context percentage
	var upstreamContextPercentage float64 // Context usage percentage from upstream (e.g., 78.56)

	for {
		msg, eventErr := e.readEventStreamMessage(reader)
		if eventErr != nil {
			log.Errorf("kiro: parseEventStream error: %v", eventErr)
			return content.String(), toolUses, usageInfo, stopReason, eventErr
		}
		if msg == nil {
			// Normal end of stream (EOF)
			break
		}

		eventType := msg.EventType
		payload := msg.Payload
		if len(payload) == 0 {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Debugf("kiro: skipping malformed event: %v", err)
			continue
		}

		// Check for error/exception events in the payload (Kiro API may return errors with HTTP 200)
		// These can appear as top-level fields or nested within the event
		if errType, hasErrType := event["_type"].(string); hasErrType {
			// AWS-style error: {"_type": "com.amazon.aws.codewhisperer#ValidationException", "message": "..."}
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			}
			log.Errorf("kiro: received AWS error in event stream: type=%s, message=%s", errType, errMsg)
			return "", nil, usageInfo, stopReason, fmt.Errorf("kiro API error: %s - %s", errType, errMsg)
		}
		if errType, hasErrType := event["type"].(string); hasErrType && (errType == "error" || errType == "exception") {
			// Generic error event
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Errorf("kiro: received error event in stream: type=%s, message=%s", errType, errMsg)
			return "", nil, usageInfo, stopReason, fmt.Errorf("kiro API error: %s", errMsg)
		}

		// Extract stop_reason from various event formats
		// Kiro/Amazon Q API may include stop_reason in different locations
		if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
			stopReason = sr
			log.Debugf("kiro: parseEventStream found stop_reason (top-level): %s", stopReason)
		}
		if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
			stopReason = sr
			log.Debugf("kiro: parseEventStream found stopReason (top-level): %s", stopReason)
		}

		// Handle different event types
		switch eventType {
		case "followupPromptEvent":
			// Filter out followupPrompt events - these are UI suggestions, not content
			log.Debugf("kiro: parseEventStream ignoring followupPrompt event")
			continue

		case "assistantResponseEvent":
			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if contentText, ok := assistantResp["content"].(string); ok {
					content.WriteString(contentText)
				}
				// Extract stop_reason from assistantResponseEvent
				if sr := kirocommon.GetString(assistantResp, "stop_reason"); sr != "" {
					stopReason = sr
					log.Debugf("kiro: parseEventStream found stop_reason in assistantResponseEvent: %s", stopReason)
				}
				if sr := kirocommon.GetString(assistantResp, "stopReason"); sr != "" {
					stopReason = sr
					log.Debugf("kiro: parseEventStream found stopReason in assistantResponseEvent: %s", stopReason)
				}
				// Extract tool uses from response
				if toolUsesRaw, ok := assistantResp["toolUses"].([]interface{}); ok {
					for _, tuRaw := range toolUsesRaw {
						if tu, ok := tuRaw.(map[string]interface{}); ok {
							toolUseID := kirocommon.GetStringValue(tu, "toolUseId")
							// Check for duplicate
							if processedIDs[toolUseID] {
								log.Debugf("kiro: skipping duplicate tool use from assistantResponse: %s", toolUseID)
								continue
							}
							processedIDs[toolUseID] = true

							toolUse := kiroclaude.KiroToolUse{
								ToolUseID: toolUseID,
								Name:      kirocommon.GetStringValue(tu, "name"),
							}
							if input, ok := tu["input"].(map[string]interface{}); ok {
								toolUse.Input = input
							}
							toolUses = append(toolUses, toolUse)
						}
					}
				}
			}
			// Also try direct format
			if contentText, ok := event["content"].(string); ok {
				content.WriteString(contentText)
			}
			// Direct tool uses
			if toolUsesRaw, ok := event["toolUses"].([]interface{}); ok {
				for _, tuRaw := range toolUsesRaw {
					if tu, ok := tuRaw.(map[string]interface{}); ok {
						toolUseID := kirocommon.GetStringValue(tu, "toolUseId")
						// Check for duplicate
						if processedIDs[toolUseID] {
							log.Debugf("kiro: skipping duplicate direct tool use: %s", toolUseID)
							continue
						}
						processedIDs[toolUseID] = true

						toolUse := kiroclaude.KiroToolUse{
							ToolUseID: toolUseID,
							Name:      kirocommon.GetStringValue(tu, "name"),
						}
						if input, ok := tu["input"].(map[string]interface{}); ok {
							toolUse.Input = input
						}
						toolUses = append(toolUses, toolUse)
					}
				}
			}

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			completedToolUses, newState := kiroclaude.ProcessToolUseEvent(event, currentToolUse, processedIDs)
			currentToolUse = newState
			toolUses = append(toolUses, completedToolUses...)

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
			}

		case "messageStopEvent", "message_stop":
			// Handle message stop events which may contain stop_reason
			if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
				stopReason = sr
				log.Debugf("kiro: parseEventStream found stop_reason in messageStopEvent: %s", stopReason)
			}
			if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
				stopReason = sr
				log.Debugf("kiro: parseEventStream found stopReason in messageStopEvent: %s", stopReason)
			}

		case "messageMetadataEvent", "metadataEvent":
			// Handle message metadata events which contain token counts
			// Official format: { tokenUsage: { outputTokens, totalTokens, uncachedInputTokens, cacheReadInputTokens, cacheWriteInputTokens, contextUsagePercentage } }
			var metadata map[string]interface{}
			if m, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else if m, ok := event["metadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else {
				metadata = event // event itself might be the metadata
			}

			// Check for nested tokenUsage object (official format)
			if tokenUsage, ok := metadata["tokenUsage"].(map[string]interface{}); ok {
				// outputTokens - precise output token count
				if outputTokens, ok := tokenUsage["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
					log.Infof("kiro: parseEventStream found precise outputTokens in tokenUsage: %d", usageInfo.OutputTokens)
				}
				// totalTokens - precise total token count
				if totalTokens, ok := tokenUsage["totalTokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
					log.Infof("kiro: parseEventStream found precise totalTokens in tokenUsage: %d", usageInfo.TotalTokens)
				}
				// uncachedInputTokens - input tokens not from cache
				if uncachedInputTokens, ok := tokenUsage["uncachedInputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(uncachedInputTokens)
					log.Infof("kiro: parseEventStream found uncachedInputTokens in tokenUsage: %d", usageInfo.InputTokens)
				}
				// cacheReadInputTokens - tokens read from cache
				if cacheReadTokens, ok := tokenUsage["cacheReadInputTokens"].(float64); ok {
					// Add to input tokens if we have uncached tokens, otherwise use as input
					if usageInfo.InputTokens > 0 {
						usageInfo.InputTokens += int64(cacheReadTokens)
					} else {
						usageInfo.InputTokens = int64(cacheReadTokens)
					}
					log.Debugf("kiro: parseEventStream found cacheReadInputTokens in tokenUsage: %d", int64(cacheReadTokens))
				}
				// contextUsagePercentage - can be used as fallback for input token estimation
				if ctxPct, ok := tokenUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream found contextUsagePercentage in tokenUsage: %.2f%%", ctxPct)
				}
			}

			// Fallback: check for direct fields in metadata (legacy format)
			if usageInfo.InputTokens == 0 {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
					log.Debugf("kiro: parseEventStream found inputTokens in messageMetadataEvent: %d", usageInfo.InputTokens)
				}
			}
			if usageInfo.OutputTokens == 0 {
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
					log.Debugf("kiro: parseEventStream found outputTokens in messageMetadataEvent: %d", usageInfo.OutputTokens)
				}
			}
			if usageInfo.TotalTokens == 0 {
				if totalTokens, ok := metadata["totalTokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
					log.Debugf("kiro: parseEventStream found totalTokens in messageMetadataEvent: %d", usageInfo.TotalTokens)
				}
			}

		case "usageEvent", "usage":
			// Handle dedicated usage events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
				log.Debugf("kiro: parseEventStream found inputTokens in usageEvent: %d", usageInfo.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: parseEventStream found outputTokens in usageEvent: %d", usageInfo.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				usageInfo.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: parseEventStream found totalTokens in usageEvent: %d", usageInfo.TotalTokens)
			}
			// Also check nested usage object
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: parseEventStream found usage object: input=%d, output=%d, total=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens, usageInfo.TotalTokens)
			}

		case "metricsEvent":
			// Handle metrics events which may contain usage data
			if metrics, ok := event["metricsEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metrics["inputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := metrics["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				}
				log.Debugf("kiro: parseEventStream found metricsEvent: input=%d, output=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens)
			}

		case "meteringEvent":
			// Handle metering events from Kiro API (usage billing information)
			// Official format: { unit: string, unitPlural: string, usage: number }
			if metering, ok := event["meteringEvent"].(map[string]interface{}); ok {
				unit := ""
				if u, ok := metering["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := metering["usage"].(float64); ok {
					usageVal = u
				}
				log.Infof("kiro: parseEventStream received meteringEvent: usage=%.2f %s", usageVal, unit)
				// Store metering info for potential billing/statistics purposes
				// Note: This is separate from token counts - it's AWS billing units
			} else {
				// Try direct fields
				unit := ""
				if u, ok := event["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := event["usage"].(float64); ok {
					usageVal = u
				}
				if unit != "" || usageVal > 0 {
					log.Infof("kiro: parseEventStream received meteringEvent (direct): usage=%.2f %s", usageVal, unit)
				}
			}

		case "contextUsageEvent":
			// Handle context usage events from Kiro API
			// Format: {"contextUsageEvent": {"contextUsagePercentage": 0.53}}
			if ctxUsage, ok := event["contextUsageEvent"].(map[string]interface{}); ok {
				if ctxPct, ok := ctxUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream received contextUsageEvent: %.2f%%", ctxPct*100)
				}
			} else {
				// Try direct field (fallback)
				if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream received contextUsagePercentage (direct): %.2f%%", ctxPct*100)
				}
			}

		case "error", "exception", "internalServerException", "invalidStateEvent":
			// Handle error events from Kiro API stream
			errMsg := ""
			errType := eventType

			// Try to extract error message from various formats
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event[eventType].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			}

			// Check for specific error reasons
			if reason, ok := event["reason"].(string); ok {
				errMsg = fmt.Sprintf("%s (reason: %s)", errMsg, reason)
			}

			log.Errorf("kiro: parseEventStream received error event: type=%s, message=%s", errType, errMsg)

			// For invalidStateEvent, we may want to continue processing other events
			if eventType == "invalidStateEvent" {
				log.Warnf("kiro: invalidStateEvent received, continuing stream processing")
				continue
			}

			// For other errors, return the error
			if errMsg != "" {
				return "", nil, usageInfo, stopReason, fmt.Errorf("kiro API error (%s): %s", errType, errMsg)
			}

		default:
			// Check for contextUsagePercentage in any event
			if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
				upstreamContextPercentage = ctxPct
				log.Debugf("kiro: parseEventStream received context usage: %.2f%%", upstreamContextPercentage)
			}
			// Log unknown event types for debugging (to discover new event formats)
			log.Debugf("kiro: parseEventStream unknown event type: %s, payload: %s", eventType, string(payload))
		}

		// Check for direct token fields in any event (fallback)
		if usageInfo.InputTokens == 0 {
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
				log.Debugf("kiro: parseEventStream found direct inputTokens: %d", usageInfo.InputTokens)
			}
		}
		if usageInfo.OutputTokens == 0 {
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: parseEventStream found direct outputTokens: %d", usageInfo.OutputTokens)
			}
		}

		// Check for usage object in any event (OpenAI format)
		if usageInfo.InputTokens == 0 || usageInfo.OutputTokens == 0 {
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if usageInfo.InputTokens == 0 {
					if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
						usageInfo.InputTokens = int64(inputTokens)
					} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
						usageInfo.InputTokens = int64(inputTokens)
					}
				}
				if usageInfo.OutputTokens == 0 {
					if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
						usageInfo.OutputTokens = int64(outputTokens)
					} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
						usageInfo.OutputTokens = int64(outputTokens)
					}
				}
				if usageInfo.TotalTokens == 0 {
					if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
						usageInfo.TotalTokens = int64(totalTokens)
					}
				}
				log.Debugf("kiro: parseEventStream found usage object (fallback): input=%d, output=%d, total=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens, usageInfo.TotalTokens)
			}
		}

		// Also check nested supplementaryWebLinksEvent
		if usageEvent, ok := event["supplementaryWebLinksEvent"].(map[string]interface{}); ok {
			if inputTokens, ok := usageEvent["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := usageEvent["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
			}
		}
	}

	// Parse embedded tool calls from content (e.g., [Called tool_name with args: {...}])
	contentStr := content.String()
	cleanedContent, embeddedToolUses := kiroclaude.ParseEmbeddedToolCalls(contentStr, processedIDs)
	toolUses = append(toolUses, embeddedToolUses...)

	// Deduplicate all tool uses
	toolUses = kiroclaude.DeduplicateToolUses(toolUses)

	// Apply fallback logic for stop_reason if not provided by upstream
	// Priority: upstream stopReason > tool_use detection > end_turn default
	if stopReason == "" {
		if len(toolUses) > 0 {
			stopReason = "tool_use"
			log.Debugf("kiro: parseEventStream using fallback stop_reason: tool_use (detected %d tool uses)", len(toolUses))
		} else {
			stopReason = "end_turn"
			log.Debugf("kiro: parseEventStream using fallback stop_reason: end_turn")
		}
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit")
	}

	// Use contextUsagePercentage to calculate more accurate input tokens
	// Kiro model has 200k max context, contextUsagePercentage represents the percentage used
	// Formula: input_tokens = contextUsagePercentage * 200000 / 100
	if upstreamContextPercentage > 0 {
		calculatedInputTokens := int64(upstreamContextPercentage * 200000 / 100)
		if calculatedInputTokens > 0 {
			localEstimate := usageInfo.InputTokens
			usageInfo.InputTokens = calculatedInputTokens
			usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens
			log.Infof("kiro: parseEventStream using contextUsagePercentage (%.2f%%) to calculate input tokens: %d (local estimate was: %d)",
				upstreamContextPercentage, calculatedInputTokens, localEstimate)
		}
	}

	return cleanedContent, toolUses, usageInfo, stopReason, nil
}

// readEventStreamMessage reads and validates a single AWS Event Stream message.
// Returns the parsed message or a structured error for different failure modes.
// This function implements boundary protection and detailed error classification.
//
// AWS Event Stream binary format:
// - Prelude (12 bytes): total_length (4) + headers_length (4) + prelude_crc (4)
// - Headers (variable): header entries
// - Payload (variable): JSON data
// - Message CRC (4 bytes): CRC32C of entire message (not validated, just skipped)
func (e *KiroExecutor) readEventStreamMessage(reader *bufio.Reader) (*eventStreamMessage, *EventStreamError) {
	// Read prelude (first 12 bytes: total_len + headers_len + prelude_crc)
	prelude := make([]byte, 12)
	_, err := io.ReadFull(reader, prelude)
	if err == io.EOF {
		return nil, nil // Normal end of stream
	}
	if err != nil {
		return nil, &EventStreamError{
			Type:    ErrStreamFatal,
			Message: "failed to read prelude",
			Cause:   err,
		}
	}

	totalLength := binary.BigEndian.Uint32(prelude[0:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	// Note: prelude[8:12] is prelude_crc - we read it but don't validate (no CRC check per requirements)

	// Boundary check: minimum frame size
	if totalLength < minEventStreamFrameSize {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("invalid message length: %d (minimum is %d)", totalLength, minEventStreamFrameSize),
		}
	}

	// Boundary check: maximum message size
	if totalLength > maxEventStreamMsgSize {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("message too large: %d bytes (maximum is %d)", totalLength, maxEventStreamMsgSize),
		}
	}

	// Boundary check: headers length within message bounds
	// Message structure: prelude(12) + headers(headersLength) + payload + message_crc(4)
	// So: headersLength must be <= totalLength - 16 (12 for prelude + 4 for message_crc)
	if headersLength > totalLength-16 {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("headers length %d exceeds message bounds (total: %d)", headersLength, totalLength),
		}
	}

	// Read the rest of the message (total - 12 bytes already read)
	remaining := make([]byte, totalLength-12)
	_, err = io.ReadFull(reader, remaining)
	if err != nil {
		return nil, &EventStreamError{
			Type:    ErrStreamFatal,
			Message: "failed to read message body",
			Cause:   err,
		}
	}

	// Extract event type from headers
	// Headers start at beginning of 'remaining', length is headersLength
	var eventType string
	if headersLength > 0 && headersLength <= uint32(len(remaining)) {
		eventType = e.extractEventTypeFromBytes(remaining[:headersLength])
	}

	// Calculate payload boundaries
	// Payload starts after headers, ends before message_crc (last 4 bytes)
	payloadStart := headersLength
	payloadEnd := uint32(len(remaining)) - 4 // Skip message_crc at end

	// Validate payload boundaries
	if payloadStart >= payloadEnd {
		// No payload, return empty message
		return &eventStreamMessage{
			EventType: eventType,
			Payload:   nil,
		}, nil
	}

	payload := remaining[payloadStart:payloadEnd]

	return &eventStreamMessage{
		EventType: eventType,
		Payload:   payload,
	}, nil
}

func skipEventStreamHeaderValue(headers []byte, offset int, valueType byte) (int, bool) {
	switch valueType {
	case 0, 1: // bool true / bool false
		return offset, true
	case 2: // byte
		if offset+1 > len(headers) {
			return offset, false
		}
		return offset + 1, true
	case 3: // short
		if offset+2 > len(headers) {
			return offset, false
		}
		return offset + 2, true
	case 4: // int
		if offset+4 > len(headers) {
			return offset, false
		}
		return offset + 4, true
	case 5: // long
		if offset+8 > len(headers) {
			return offset, false
		}
		return offset + 8, true
	case 6: // byte array (2-byte length + data)
		if offset+2 > len(headers) {
			return offset, false
		}
		valueLen := int(binary.BigEndian.Uint16(headers[offset : offset+2]))
		offset += 2
		if offset+valueLen > len(headers) {
			return offset, false
		}
		return offset + valueLen, true
	case 8: // timestamp
		if offset+8 > len(headers) {
			return offset, false
		}
		return offset + 8, true
	case 9: // uuid
		if offset+16 > len(headers) {
			return offset, false
		}
		return offset + 16, true
	default:
		return offset, false
	}
}

// extractEventTypeFromBytes extracts the event type from raw header bytes (without prelude CRC prefix)
func (e *KiroExecutor) extractEventTypeFromBytes(headers []byte) string {
	offset := 0
	for offset < len(headers) {
		nameLen := int(headers[offset])
		offset++
		if offset+nameLen > len(headers) {
			break
		}
		name := string(headers[offset : offset+nameLen])
		offset += nameLen

		if offset >= len(headers) {
			break
		}
		valueType := headers[offset]
		offset++

		if valueType == 7 { // String type
			if offset+2 > len(headers) {
				break
			}
			valueLen := int(binary.BigEndian.Uint16(headers[offset : offset+2]))
			offset += 2
			if offset+valueLen > len(headers) {
				break
			}
			value := string(headers[offset : offset+valueLen])
			offset += valueLen

			if name == ":event-type" {
				return value
			}
			continue
		}

		nextOffset, ok := skipEventStreamHeaderValue(headers, offset, valueType)
		if !ok {
			break
		}
		offset = nextOffset
	}
	return ""
}

// NOTE: Response building functions moved to pkg/llmproxy/translator/kiro/claude/kiro_claude_response.go
// The executor now uses kiroclaude.BuildClaudeResponse() and kiroclaude.ExtractThinkingFromContent() instead

// streamToChannel converts AWS Event Stream to channel-based streaming.
// Supports tool calling - emits tool_use content blocks when tools are used.
// Includes embedded [Called ...] tool call parsing and input buffering for toolUseEvent.
// Implements duplicate content filtering using lastContentEvent detection (based on AIClient-2-API).
// Extracts stop_reason from upstream events when available.
// thinkingEnabled controls whether <thinking> tags are parsed - only parse when request enabled thinking.
func (e *KiroExecutor) streamToChannel(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, targetFormat sdktranslator.Format, model string, originalReq, claudeBody []byte, reporter *usageReporter, thinkingEnabled bool) {
	reader := bufio.NewReaderSize(body, 20*1024*1024) // 20MB buffer to match other providers
	var totalUsage usage.Detail
	var hasToolUses bool          // Track if any tool uses were emitted
	var upstreamStopReason string // Track stop_reason from upstream events

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *kiroclaude.ToolUseState

	// NOTE: Duplicate content filtering removed - it was causing legitimate repeated
	// content (like consecutive newlines) to be incorrectly filtered out.
	// The previous implementation compared lastContentEvent == contentDelta which
	// is too aggressive for streaming scenarios.

	// Streaming token calculation - accumulate content for real-time token counting
	// Based on AIClient-2-API implementation
	var accumulatedContent strings.Builder
	accumulatedContent.Grow(4096) // Pre-allocate 4KB capacity to reduce reallocations

	// Real-time usage estimation state
	// These track when to send periodic usage updates during streaming
	var lastUsageUpdateLen int           // Last accumulated content length when usage was sent
	var lastUsageUpdateTime = time.Now() // Last time usage update was sent
	var lastReportedOutputTokens int64   // Last reported output token count

	// Upstream usage tracking - Kiro API returns credit usage and context percentage
	var upstreamCreditUsage float64       // Credit usage from upstream (e.g., 1.458)
	var upstreamContextPercentage float64 // Context usage percentage from upstream (e.g., 78.56)
	var hasUpstreamUsage bool             // Whether we received usage from upstream

	// Translator param for maintaining tool call state across streaming events
	// IMPORTANT: This must persist across all TranslateStream calls
	var translatorParam any

	// Thinking mode state tracking - tag-based parsing for <thinking> tags in content
	inThinkBlock := false                          // Whether we're currently inside a <thinking> block
	isThinkingBlockOpen := false                   // Track if thinking content block SSE event is open
	thinkingBlockIndex := -1                       // Index of the thinking content block
	var accumulatedThinkingContent strings.Builder // Accumulate thinking content for token counting

	// Buffer for handling partial tag matches at chunk boundaries
	var pendingContent strings.Builder // Buffer content that might be part of a tag

	// Pre-calculate input tokens from request if possible
	// Kiro uses Claude format, so try Claude format first, then OpenAI format, then fallback
	if enc, err := getTokenizer(model); err == nil {
		var inputTokens int64
		var countMethod string

		// Try Claude format first (Kiro uses Claude API format)
		if inp, err := countClaudeChatTokens(enc, claudeBody); err == nil && inp > 0 {
			inputTokens = inp
			countMethod = "claude"
		} else if inp, err := countOpenAIChatTokens(enc, originalReq); err == nil && inp > 0 {
			// Fallback to OpenAI format (for OpenAI-compatible requests)
			inputTokens = inp
			countMethod = "openai"
		} else {
			// Final fallback: estimate from raw request size (roughly 4 chars per token)
			inputTokens = int64(len(claudeBody) / 4)
			if inputTokens == 0 && len(claudeBody) > 0 {
				inputTokens = 1
			}
			countMethod = "estimate"
		}

		totalUsage.InputTokens = inputTokens
		log.Debugf("kiro: streamToChannel pre-calculated input tokens: %d (method: %s, claude body: %d bytes, original req: %d bytes)",
			totalUsage.InputTokens, countMethod, len(claudeBody), len(originalReq))
	}

	contentBlockIndex := -1
	messageStartSent := false
	isTextBlockOpen := false
	var outputLen int

	// Ensure usage is published even on early return
	defer func() {
		reporter.publish(ctx, totalUsage)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, eventErr := e.readEventStreamMessage(reader)
		if eventErr != nil {
			// Log the error
			log.Errorf("kiro: streamToChannel error: %v", eventErr)

			// Send error to channel for client notification
			out <- cliproxyexecutor.StreamChunk{Err: eventErr}
			return
		}
		if msg == nil {
			// Normal end of stream (EOF)
			// Flush any incomplete tool use before ending stream
			if currentToolUse != nil && !processedIDs[currentToolUse.ToolUseID] {
				log.Warnf("kiro: flushing incomplete tool use at EOF: %s (ID: %s)", currentToolUse.Name, currentToolUse.ToolUseID)
				fullInput := currentToolUse.InputBuffer.String()
				repairedJSON := kiroclaude.RepairJSON(fullInput)
				var finalInput map[string]interface{}
				if err := json.Unmarshal([]byte(repairedJSON), &finalInput); err != nil {
					log.Warnf("kiro: failed to parse incomplete tool input at EOF: %v", err)
					finalInput = make(map[string]interface{})
				}

				processedIDs[currentToolUse.ToolUseID] = true
				contentBlockIndex++

				// Send tool_use content block
				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", currentToolUse.ToolUseID, currentToolUse.Name)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				// Send tool input as delta
				inputBytes, _ := json.Marshal(finalInput)
				inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputBytes), contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				// Close block
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				hasToolUses = true
				currentToolUse = nil
			}

			// DISABLED: Tag-based pending character flushing
			// This code block was used for tag-based thinking detection which has been
			// replaced by reasoningContentEvent handling. No pending tag chars to flush.
			// Original code preserved in git history.
			break
		}

		eventType := msg.EventType
		payload := msg.Payload
		if len(payload) == 0 {
			continue
		}
		appendAPIResponseChunk(ctx, e.cfg, payload)

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warnf("kiro: failed to unmarshal event payload: %v, raw: %s", err, string(payload))
			continue
		}

		// Check for error/exception events in the payload (Kiro API may return errors with HTTP 200)
		// These can appear as top-level fields or nested within the event
		if errType, hasErrType := event["_type"].(string); hasErrType {
			// AWS-style error: {"_type": "com.amazon.aws.codewhisperer#ValidationException", "message": "..."}
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			}
			log.Errorf("kiro: received AWS error in stream: type=%s, message=%s", errType, errMsg)
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("kiro API error: %s - %s", errType, errMsg)}
			return
		}
		if errType, hasErrType := event["type"].(string); hasErrType && (errType == "error" || errType == "exception") {
			// Generic error event
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Errorf("kiro: received error event in stream: type=%s, message=%s", errType, errMsg)
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("kiro API error: %s", errMsg)}
			return
		}

		// Extract stop_reason from various event formats (streaming)
		// Kiro/Amazon Q API may include stop_reason in different locations
		if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
			upstreamStopReason = sr
			log.Debugf("kiro: streamToChannel found stop_reason (top-level): %s", upstreamStopReason)
		}
		if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
			upstreamStopReason = sr
			log.Debugf("kiro: streamToChannel found stopReason (top-level): %s", upstreamStopReason)
		}

		// Send message_start on first event
		if !messageStartSent {
			msgStart := kiroclaude.BuildClaudeMessageStartEvent(model, totalUsage.InputTokens)
			sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStart, &translatorParam)
			for _, chunk := range sseData {
				enqueueTranslatedSSE(out, chunk)
			}
			messageStartSent = true
		}

		switch eventType {
		case "followupPromptEvent":
			// Filter out followupPrompt events - these are UI suggestions, not content
			log.Debugf("kiro: streamToChannel ignoring followupPrompt event")
			continue

		case "messageStopEvent", "message_stop":
			// Handle message stop events which may contain stop_reason
			if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
				upstreamStopReason = sr
				log.Debugf("kiro: streamToChannel found stop_reason in messageStopEvent: %s", upstreamStopReason)
			}
			if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
				upstreamStopReason = sr
				log.Debugf("kiro: streamToChannel found stopReason in messageStopEvent: %s", upstreamStopReason)
			}

		case "meteringEvent":
			// Handle metering events from Kiro API (usage billing information)
			// Official format: { unit: string, unitPlural: string, usage: number }
			if metering, ok := event["meteringEvent"].(map[string]interface{}); ok {
				unit := ""
				if u, ok := metering["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := metering["usage"].(float64); ok {
					usageVal = u
				}
				upstreamCreditUsage = usageVal
				hasUpstreamUsage = true
				log.Infof("kiro: streamToChannel received meteringEvent: usage=%.4f %s", usageVal, unit)
			} else {
				// Try direct fields (event is meteringEvent itself)
				if unit, ok := event["unit"].(string); ok {
					if usage, ok := event["usage"].(float64); ok {
						upstreamCreditUsage = usage
						hasUpstreamUsage = true
						log.Infof("kiro: streamToChannel received meteringEvent (direct): usage=%.4f %s", usage, unit)
					}
				}
			}

		case "contextUsageEvent":
			// Handle context usage events from Kiro API
			// Format: {"contextUsageEvent": {"contextUsagePercentage": 0.53}}
			if ctxUsage, ok := event["contextUsageEvent"].(map[string]interface{}); ok {
				if ctxPct, ok := ctxUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel received contextUsageEvent: %.2f%%", ctxPct*100)
				}
			} else {
				// Try direct field (fallback)
				if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel received contextUsagePercentage (direct): %.2f%%", ctxPct*100)
				}
			}

		case "error", "exception", "internalServerException":
			// Handle error events from Kiro API stream
			errMsg := ""
			errType := eventType

			// Try to extract error message from various formats
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event[eventType].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}

			log.Errorf("kiro: streamToChannel received error event: type=%s, message=%s", errType, errMsg)

			// Send error to the stream and exit
			if errMsg != "" {
				out <- cliproxyexecutor.StreamChunk{
					Err: fmt.Errorf("kiro API error (%s): %s", errType, errMsg),
				}
				return
			}

		case "invalidStateEvent":
			// Handle invalid state events - log and continue (non-fatal)
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if stateEvent, ok := event["invalidStateEvent"].(map[string]interface{}); ok {
				if msg, ok := stateEvent["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Warnf("kiro: streamToChannel received invalidStateEvent: %s, continuing", errMsg)
			continue

		case "assistantResponseEvent":
			var contentDelta string
			var toolUses []map[string]interface{}

			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if c, ok := assistantResp["content"].(string); ok {
					contentDelta = c
				}
				// Extract stop_reason from assistantResponseEvent
				if sr := kirocommon.GetString(assistantResp, "stop_reason"); sr != "" {
					upstreamStopReason = sr
					log.Debugf("kiro: streamToChannel found stop_reason in assistantResponseEvent: %s", upstreamStopReason)
				}
				if sr := kirocommon.GetString(assistantResp, "stopReason"); sr != "" {
					upstreamStopReason = sr
					log.Debugf("kiro: streamToChannel found stopReason in assistantResponseEvent: %s", upstreamStopReason)
				}
				// Extract tool uses from response
				if tus, ok := assistantResp["toolUses"].([]interface{}); ok {
					for _, tuRaw := range tus {
						if tu, ok := tuRaw.(map[string]interface{}); ok {
							toolUses = append(toolUses, tu)
						}
					}
				}
			}
			if contentDelta == "" {
				if c, ok := event["content"].(string); ok {
					contentDelta = c
				}
			}
			// Direct tool uses
			if tus, ok := event["toolUses"].([]interface{}); ok {
				for _, tuRaw := range tus {
					if tu, ok := tuRaw.(map[string]interface{}); ok {
						toolUses = append(toolUses, tu)
					}
				}
			}

			// Handle text content with thinking mode support
			if contentDelta != "" {
				// NOTE: Duplicate content filtering was removed because it incorrectly
				// filtered out legitimate repeated content (like consecutive newlines "\n\n").
				// Streaming naturally can have identical chunks that are valid content.

				outputLen += len(contentDelta)
				// Accumulate content for streaming token calculation
				accumulatedContent.WriteString(contentDelta)

				// Real-time usage estimation: Check if we should send a usage update
				// This helps clients track context usage during long thinking sessions
				shouldSendUsageUpdate := false
				if accumulatedContent.Len()-lastUsageUpdateLen >= usageUpdateCharThreshold {
					shouldSendUsageUpdate = true
				} else if time.Since(lastUsageUpdateTime) >= usageUpdateTimeInterval && accumulatedContent.Len() > lastUsageUpdateLen {
					shouldSendUsageUpdate = true
				}

				if shouldSendUsageUpdate {
					// Calculate current output tokens using tiktoken
					var currentOutputTokens int64
					if enc, encErr := getTokenizer(model); encErr == nil {
						if tokenCount, countErr := enc.Count(accumulatedContent.String()); countErr == nil {
							currentOutputTokens = int64(tokenCount)
						}
					}
					// Fallback to character estimation if tiktoken fails
					if currentOutputTokens == 0 {
						currentOutputTokens = int64(accumulatedContent.Len() / 4)
						if currentOutputTokens == 0 {
							currentOutputTokens = 1
						}
					}

					// Only send update if token count has changed significantly (at least 10 tokens)
					if currentOutputTokens > lastReportedOutputTokens+10 {
						// Send ping event with usage information
						// This is a non-blocking update that clients can optionally process
						pingEvent := kiroclaude.BuildClaudePingEventWithUsage(totalUsage.InputTokens, currentOutputTokens)
						sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, pingEvent, &translatorParam)
						for _, chunk := range sseData {
							enqueueTranslatedSSE(out, chunk)
						}

						lastReportedOutputTokens = currentOutputTokens
						log.Debugf("kiro: sent real-time usage update - input: %d, output: %d (accumulated: %d chars)",
							totalUsage.InputTokens, currentOutputTokens, accumulatedContent.Len())
					}

					lastUsageUpdateLen = accumulatedContent.Len()
					lastUsageUpdateTime = time.Now()
				}

				// TAG-BASED THINKING PARSING: Parse <thinking> tags from content
				// Combine pending content with new content for processing
				pendingContent.WriteString(contentDelta)
				processContent := pendingContent.String()
				pendingContent.Reset()

				// Process content looking for thinking tags
				for len(processContent) > 0 {
					if inThinkBlock {
						// We're inside a thinking block, look for </thinking>
						endIdx := strings.Index(processContent, kirocommon.ThinkingEndTag)
						if endIdx >= 0 {
							// Found end tag - emit thinking content before the tag
							thinkingText := processContent[:endIdx]
							if thinkingText != "" {
								// Ensure thinking block is open
								if !isThinkingBlockOpen {
									contentBlockIndex++
									thinkingBlockIndex = contentBlockIndex
									isThinkingBlockOpen = true
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										enqueueTranslatedSSE(out, chunk)
									}
								}
								// Send thinking delta
								thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(thinkingText, thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
								for _, chunk := range sseData {
									enqueueTranslatedSSE(out, chunk)
								}
								accumulatedThinkingContent.WriteString(thinkingText)
							}
							// Close thinking block
							if isThinkingBlockOpen {
								blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									enqueueTranslatedSSE(out, chunk)
								}
								isThinkingBlockOpen = false
							}
							inThinkBlock = false
							processContent = processContent[endIdx+len(kirocommon.ThinkingEndTag):]
							log.Debugf("kiro: closed thinking block, remaining content: %d chars", len(processContent))
						} else {
							// No end tag found - check for partial match at end
							partialMatch := false
							for i := 1; i < len(kirocommon.ThinkingEndTag) && i <= len(processContent); i++ {
								if strings.HasSuffix(processContent, kirocommon.ThinkingEndTag[:i]) {
									// Possible partial tag at end, buffer it
									pendingContent.WriteString(processContent[len(processContent)-i:])
									processContent = processContent[:len(processContent)-i]
									partialMatch = true
									break
								}
							}
							if !partialMatch || len(processContent) > 0 {
								// Emit all as thinking content
								if processContent != "" {
									if !isThinkingBlockOpen {
										contentBlockIndex++
										thinkingBlockIndex = contentBlockIndex
										isThinkingBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											enqueueTranslatedSSE(out, chunk)
										}
									}
									thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(processContent, thinkingBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
									for _, chunk := range sseData {
										enqueueTranslatedSSE(out, chunk)
									}
									accumulatedThinkingContent.WriteString(processContent)
								}
							}
							processContent = ""
						}
					} else {
						// Not in thinking block, look for <thinking>
						startIdx := strings.Index(processContent, kirocommon.ThinkingStartTag)
						if startIdx >= 0 {
							// Found start tag - emit text content before the tag
							textBefore := processContent[:startIdx]
							if textBefore != "" {
								// Close thinking block if open
								if isThinkingBlockOpen {
									blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
									for _, chunk := range sseData {
										enqueueTranslatedSSE(out, chunk)
									}
									isThinkingBlockOpen = false
								}
								// Ensure text block is open
								if !isTextBlockOpen {
									contentBlockIndex++
									isTextBlockOpen = true
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										enqueueTranslatedSSE(out, chunk)
									}
								}
								// Send text delta
								claudeEvent := kiroclaude.BuildClaudeStreamEvent(textBefore, contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
								for _, chunk := range sseData {
									enqueueTranslatedSSE(out, chunk)
								}
							}
							// Close text block before entering thinking
							if isTextBlockOpen {
								blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									enqueueTranslatedSSE(out, chunk)
								}
								isTextBlockOpen = false
							}
							inThinkBlock = true
							processContent = processContent[startIdx+len(kirocommon.ThinkingStartTag):]
							log.Debugf("kiro: entered thinking block")
						} else {
							// No start tag found - check for partial match at end
							partialMatch := false
							for i := 1; i < len(kirocommon.ThinkingStartTag) && i <= len(processContent); i++ {
								if strings.HasSuffix(processContent, kirocommon.ThinkingStartTag[:i]) {
									// Possible partial tag at end, buffer it
									pendingContent.WriteString(processContent[len(processContent)-i:])
									processContent = processContent[:len(processContent)-i]
									partialMatch = true
									break
								}
							}
							if !partialMatch || len(processContent) > 0 {
								// Emit all as text content
								if processContent != "" {
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											enqueueTranslatedSSE(out, chunk)
										}
									}
									claudeEvent := kiroclaude.BuildClaudeStreamEvent(processContent, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										enqueueTranslatedSSE(out, chunk)
									}
								}
							}
							processContent = ""
						}
					}
				}
			}

			// Handle tool uses in response (with deduplication)
			for _, tu := range toolUses {
				toolUseID := kirocommon.GetString(tu, "toolUseId")
				toolName := kirocommon.GetString(tu, "name")

				// Check for duplicate
				if processedIDs[toolUseID] {
					log.Debugf("kiro: skipping duplicate tool use in stream: %s", toolUseID)
					continue
				}
				processedIDs[toolUseID] = true

				hasToolUses = true
				// Close text block if open before starting tool_use block
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						enqueueTranslatedSSE(out, chunk)
					}
					isTextBlockOpen = false
				}

				// Emit tool_use content block
				contentBlockIndex++

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", toolUseID, toolName)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				// Send input_json_delta with the tool input
				if input, ok := tu["input"].(map[string]interface{}); ok {
					inputJSON, err := json.Marshal(input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input: %v", err)
						// Don't continue - still need to close the block
					} else {
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							enqueueTranslatedSSE(out, chunk)
						}
					}
				}

				// Close tool_use block (always close even if input marshal failed)
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}
			}

		case "reasoningContentEvent":
			// Handle official reasoningContentEvent from Kiro API
			// This replaces tag-based thinking detection with the proper event type
			// Official format: { text: string, signature?: string, redactedContent?: base64 }
			var thinkingText string
			var signature string

			if re, ok := event["reasoningContentEvent"].(map[string]interface{}); ok {
				if text, ok := re["text"].(string); ok {
					thinkingText = text
				}
				if sig, ok := re["signature"].(string); ok {
					signature = sig
					if len(sig) > 20 {
						log.Debugf("kiro: reasoningContentEvent has signature: %s...", sig[:20])
					} else {
						log.Debugf("kiro: reasoningContentEvent has signature: %s", sig)
					}
				}
			} else {
				// Try direct fields
				if text, ok := event["text"].(string); ok {
					thinkingText = text
				}
				if sig, ok := event["signature"].(string); ok {
					signature = sig
				}
			}

			if thinkingText != "" {
				// Close text block if open before starting thinking block
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						enqueueTranslatedSSE(out, chunk)
					}
					isTextBlockOpen = false
				}

				// Start thinking block if not already open
				if !isThinkingBlockOpen {
					contentBlockIndex++
					thinkingBlockIndex = contentBlockIndex
					isThinkingBlockOpen = true
					blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
					for _, chunk := range sseData {
						enqueueTranslatedSSE(out, chunk)
					}
				}

				// Send thinking content
				thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(thinkingText, thinkingBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				// Accumulate for token counting
				accumulatedThinkingContent.WriteString(thinkingText)
				log.Debugf("kiro: received reasoningContentEvent, text length: %d, has signature: %v", len(thinkingText), signature != "")
			}

			// Note: We don't close the thinking block here - it will be closed when we see
			// the next assistantResponseEvent or at the end of the stream
			_ = signature // Signature can be used for verification if needed

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			completedToolUses, newState := kiroclaude.ProcessToolUseEvent(event, currentToolUse, processedIDs)
			currentToolUse = newState

			// Emit completed tool uses
			for _, tu := range completedToolUses {
				// Skip truncated tools - don't emit fake marker tool_use
				if tu.IsTruncated {
					log.Warnf("kiro: streamToChannel skipping truncated tool: %s (ID: %s)", tu.Name, tu.ToolUseID)
					continue
				}

				hasToolUses = true

				// Close text block if open
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						enqueueTranslatedSSE(out, chunk)
					}
					isTextBlockOpen = false
				}

				contentBlockIndex++

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", tu.ToolUseID, tu.Name)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}

				if tu.Input != nil {
					inputJSON, err := json.Marshal(tu.Input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input in toolUseEvent: %v", err)
					} else {
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							enqueueTranslatedSSE(out, chunk)
						}
					}
				}

				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					enqueueTranslatedSSE(out, chunk)
				}
			}

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}

		case "messageMetadataEvent", "metadataEvent":
			// Handle message metadata events which contain token counts
			// Official format: { tokenUsage: { outputTokens, totalTokens, uncachedInputTokens, cacheReadInputTokens, cacheWriteInputTokens, contextUsagePercentage } }
			var metadata map[string]interface{}
			if m, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else if m, ok := event["metadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else {
				metadata = event // event itself might be the metadata
			}

			// Check for nested tokenUsage object (official format)
			if tokenUsage, ok := metadata["tokenUsage"].(map[string]interface{}); ok {
				// outputTokens - precise output token count
				if outputTokens, ok := tokenUsage["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
					log.Infof("kiro: streamToChannel found precise outputTokens in tokenUsage: %d", totalUsage.OutputTokens)
				}
				// totalTokens - precise total token count
				if totalTokens, ok := tokenUsage["totalTokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
					log.Infof("kiro: streamToChannel found precise totalTokens in tokenUsage: %d", totalUsage.TotalTokens)
				}
				// uncachedInputTokens - input tokens not from cache
				if uncachedInputTokens, ok := tokenUsage["uncachedInputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(uncachedInputTokens)
					hasUpstreamUsage = true
					log.Infof("kiro: streamToChannel found uncachedInputTokens in tokenUsage: %d", totalUsage.InputTokens)
				}
				// cacheReadInputTokens - tokens read from cache
				if cacheReadTokens, ok := tokenUsage["cacheReadInputTokens"].(float64); ok {
					// Add to input tokens if we have uncached tokens, otherwise use as input
					if totalUsage.InputTokens > 0 {
						totalUsage.InputTokens += int64(cacheReadTokens)
					} else {
						totalUsage.InputTokens = int64(cacheReadTokens)
					}
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found cacheReadInputTokens in tokenUsage: %d", int64(cacheReadTokens))
				}
				// contextUsagePercentage - can be used as fallback for input token estimation
				if ctxPct, ok := tokenUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel found contextUsagePercentage in tokenUsage: %.2f%%", ctxPct)
				}
			}

			// Fallback: check for direct fields in metadata (legacy format)
			if totalUsage.InputTokens == 0 {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found inputTokens in messageMetadataEvent: %d", totalUsage.InputTokens)
				}
			}
			if totalUsage.OutputTokens == 0 {
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found outputTokens in messageMetadataEvent: %d", totalUsage.OutputTokens)
				}
			}
			if totalUsage.TotalTokens == 0 {
				if totalTokens, ok := metadata["totalTokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
					log.Debugf("kiro: streamToChannel found totalTokens in messageMetadataEvent: %d", totalUsage.TotalTokens)
				}
			}

		case "usageEvent", "usage":
			// Handle dedicated usage events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				log.Debugf("kiro: streamToChannel found inputTokens in usageEvent: %d", totalUsage.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: streamToChannel found outputTokens in usageEvent: %d", totalUsage.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				totalUsage.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: streamToChannel found totalTokens in usageEvent: %d", totalUsage.TotalTokens)
			}
			// Also check nested usage object
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: streamToChannel found usage object: input=%d, output=%d, total=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}

		case "metricsEvent":
			// Handle metrics events which may contain usage data
			if metrics, ok := event["metricsEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metrics["inputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := metrics["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				}
				log.Debugf("kiro: streamToChannel found metricsEvent: input=%d, output=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens)

			}
		default:
			// Check for upstream usage events from Kiro API
			// Format: {"unit":"credit","unitPlural":"credits","usage":1.458}
			if unit, ok := event["unit"].(string); ok && unit == "credit" {
				if usage, ok := event["usage"].(float64); ok {
					upstreamCreditUsage = usage
					hasUpstreamUsage = true
					log.Debugf("kiro: received upstream credit usage: %.4f", upstreamCreditUsage)
				}
			}
			// Format: {"contextUsagePercentage":78.56}
			if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
				upstreamContextPercentage = ctxPct
				log.Debugf("kiro: received upstream context usage: %.2f%%", upstreamContextPercentage)
			}

			// Check for token counts in unknown events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				hasUpstreamUsage = true
				log.Debugf("kiro: streamToChannel found inputTokens in event %s: %d", eventType, totalUsage.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				hasUpstreamUsage = true
				log.Debugf("kiro: streamToChannel found outputTokens in event %s: %d", eventType, totalUsage.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				totalUsage.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: streamToChannel found totalTokens in event %s: %d", eventType, totalUsage.TotalTokens)
			}

			// Check for usage object in unknown events (OpenAI/Claude format)
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: streamToChannel found usage object in event %s: input=%d, output=%d, total=%d",
					eventType, totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}

			// Log unknown event types for debugging (to discover new event formats)
			if eventType != "" {
				log.Debugf("kiro: streamToChannel unknown event type: %s, payload: %s", eventType, string(payload))
			}

		}

		// Check nested usage event
		if usageEvent, ok := event["supplementaryWebLinksEvent"].(map[string]interface{}); ok {
			if inputTokens, ok := usageEvent["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := usageEvent["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}
		}

		// Check for direct token fields in any event (fallback)
		if totalUsage.InputTokens == 0 {
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				log.Debugf("kiro: streamToChannel found direct inputTokens: %d", totalUsage.InputTokens)
			}
		}
		if totalUsage.OutputTokens == 0 {
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: streamToChannel found direct outputTokens: %d", totalUsage.OutputTokens)
			}
		}

		// Check for usage object in any event (OpenAI format)
		if totalUsage.InputTokens == 0 || totalUsage.OutputTokens == 0 {
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if totalUsage.InputTokens == 0 {
					if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
						totalUsage.InputTokens = int64(inputTokens)
					} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
						totalUsage.InputTokens = int64(inputTokens)
					}
				}
				if totalUsage.OutputTokens == 0 {
					if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
						totalUsage.OutputTokens = int64(outputTokens)
					} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
						totalUsage.OutputTokens = int64(outputTokens)
					}
				}
				if totalUsage.TotalTokens == 0 {
					if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
						totalUsage.TotalTokens = int64(totalTokens)
					}
				}
				log.Debugf("kiro: streamToChannel found usage object (fallback): input=%d, output=%d, total=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}
		}
	}

	// Close content block if open
	if isTextBlockOpen && contentBlockIndex >= 0 {
		blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
		sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
		for _, chunk := range sseData {
			enqueueTranslatedSSE(out, chunk)
		}
	}

	// Streaming token calculation - calculate output tokens from accumulated content
	// Only use local estimation if server didn't provide usage (server-side usage takes priority)
	if totalUsage.OutputTokens == 0 && accumulatedContent.Len() > 0 {
		// Try to use tiktoken for accurate counting
		if enc, err := getTokenizer(model); err == nil {
			if tokenCount, countErr := enc.Count(accumulatedContent.String()); countErr == nil {
				totalUsage.OutputTokens = int64(tokenCount)
				log.Debugf("kiro: streamToChannel calculated output tokens using tiktoken: %d", totalUsage.OutputTokens)
			} else {
				// Fallback on count error: estimate from character count
				totalUsage.OutputTokens = int64(accumulatedContent.Len() / 4)
				if totalUsage.OutputTokens == 0 {
					totalUsage.OutputTokens = 1
				}
				log.Debugf("kiro: streamToChannel tiktoken count failed, estimated from chars: %d", totalUsage.OutputTokens)
			}
		} else {
			// Fallback: estimate from character count (roughly 4 chars per token)
			totalUsage.OutputTokens = int64(accumulatedContent.Len() / 4)
			if totalUsage.OutputTokens == 0 {
				totalUsage.OutputTokens = 1
			}
			log.Debugf("kiro: streamToChannel estimated output tokens from chars: %d (content len: %d)", totalUsage.OutputTokens, accumulatedContent.Len())
		}
	} else if totalUsage.OutputTokens == 0 && outputLen > 0 {
		// Legacy fallback using outputLen
		totalUsage.OutputTokens = int64(outputLen / 4)
		if totalUsage.OutputTokens == 0 {
			totalUsage.OutputTokens = 1
		}
	}

	// Use contextUsagePercentage to calculate more accurate input tokens
	// Kiro model has 200k max context, contextUsagePercentage represents the percentage used
	// Formula: input_tokens = contextUsagePercentage * 200000 / 100
	// Note: The effective input context is ~170k (200k - 30k reserved for output)
	if upstreamContextPercentage > 0 {
		// Calculate input tokens from context percentage
		// Using 200k as the base since that's what Kiro reports against
		calculatedInputTokens := int64(upstreamContextPercentage * 200000 / 100)

		// Only use calculated value if it's significantly different from local estimate
		// This provides more accurate token counts based on upstream data
		if calculatedInputTokens > 0 {
			localEstimate := totalUsage.InputTokens
			totalUsage.InputTokens = calculatedInputTokens
			log.Debugf("kiro: using contextUsagePercentage (%.2f%%) to calculate input tokens: %d (local estimate was: %d)",
				upstreamContextPercentage, calculatedInputTokens, localEstimate)
		}
	}

	totalUsage.TotalTokens = totalUsage.InputTokens + totalUsage.OutputTokens

	// Log upstream usage information if received
	if hasUpstreamUsage {
		log.Debugf("kiro: upstream usage - credits: %.4f, context: %.2f%%, final tokens - input: %d, output: %d, total: %d",
			upstreamCreditUsage, upstreamContextPercentage,
			totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
	}

	// Determine stop reason: prefer upstream, then detect tool_use, default to end_turn
	stopReason := upstreamStopReason
	if stopReason == "" {
		if hasToolUses {
			stopReason = "tool_use"
			log.Debugf("kiro: streamToChannel using fallback stop_reason: tool_use")
		} else {
			stopReason = "end_turn"
			log.Debugf("kiro: streamToChannel using fallback stop_reason: end_turn")
		}
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit (streamToChannel)")
	}

	// Send message_delta event
	msgDelta := kiroclaude.BuildClaudeMessageDeltaEvent(stopReason, totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgDelta, &translatorParam)
	for _, chunk := range sseData {
		enqueueTranslatedSSE(out, chunk)
	}

	// Send message_stop event separately
	msgStop := kiroclaude.BuildClaudeMessageStopOnlyEvent()
	sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
	for _, chunk := range sseData {
		enqueueTranslatedSSE(out, chunk)
	}
	// reporter.publish is called via defer
}

// ══════════════════════════════════════════════════════════════════════════════
// Web Search Handler (MCP API)
// ══════════════════════════════════════════════════════════════════════════════

// fetchToolDescription caching:
// Uses a mutex + fetched flag to ensure only one goroutine fetches at a time,
// with automatic retry on failure:
// - On failure, fetched stays false so subsequent calls will retry
// - On success, fetched is set to true — subsequent calls skip immediately (mutex-free fast path)
// The cached description is stored in the translator package via kiroclaude.SetWebSearchDescription(),
// enabling the translator's convertClaudeToolsToKiro to read it when building Kiro requests.
var (
	toolDescMu      sync.Mutex
	toolDescFetched atomic.Bool
)

// fetchToolDescription calls MCP tools/list to get the web_search tool description
// and caches it. Safe to call concurrently — only one goroutine fetches at a time.
// If the fetch fails, subsequent calls will retry. On success, no further fetches occur.
// The httpClient parameter allows reusing a shared pooled HTTP client.
func fetchToolDescription(ctx context.Context, mcpEndpoint, authToken string, httpClient *http.Client, auth *cliproxyauth.Auth, authAttrs map[string]string) {
	// Fast path: already fetched successfully, no lock needed
	if toolDescFetched.Load() {
		return
	}

	toolDescMu.Lock()
	defer toolDescMu.Unlock()

	// Double-check after acquiring lock
	if toolDescFetched.Load() {
		return
	}

	handler := newWebSearchHandler(ctx, mcpEndpoint, authToken, httpClient, auth, authAttrs)
	reqBody := []byte(`{"id":"tools_list","jsonrpc":"2.0","method":"tools/list"}`)
	log.Debugf("kiro/websearch MCP tools/list request: %d bytes", len(reqBody))

	req, err := http.NewRequestWithContext(ctx, "POST", mcpEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		log.Warnf("kiro/websearch: failed to create tools/list request: %v", err)
		return
	}

	// Reuse same headers as callMcpAPI
	handler.setMcpHeaders(req)

	resp, err := handler.httpClient.Do(req)
	if err != nil {
		log.Warnf("kiro/websearch: tools/list request failed: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		log.Warnf("kiro/websearch: tools/list returned status %d", resp.StatusCode)
		return
	}
	log.Debugf("kiro/websearch MCP tools/list response: [%d] %d bytes", resp.StatusCode, len(body))

	// Parse: {"result":{"tools":[{"name":"web_search","description":"..."}]}}
	var result struct {
		Result *struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Result == nil {
		log.Warnf("kiro/websearch: failed to parse tools/list response")
		return
	}

	for _, tool := range result.Result.Tools {
		if tool.Name == "web_search" && tool.Description != "" {
			kiroclaude.SetWebSearchDescription(tool.Description)
			toolDescFetched.Store(true) // success — no more fetches
			log.Infof("kiro/websearch: cached web_search description from tools/list (%d bytes)", len(tool.Description))
			return
		}
	}

	// web_search tool not found in response
	log.Warnf("kiro/websearch: web_search tool not found in tools/list response")
}

// webSearchHandler handles web search requests via Kiro MCP API
type webSearchHandler struct {
	ctx         context.Context
	mcpEndpoint string
	httpClient  *http.Client
	authToken   string
	auth        *cliproxyauth.Auth // for applyDynamicFingerprint
	authAttrs   map[string]string  // optional, for custom headers from auth.Attributes
}

// newWebSearchHandler creates a new webSearchHandler.
// If httpClient is nil, a default client with 30s timeout is used.
// Pass a shared pooled client (e.g. from getKiroPooledHTTPClient) for connection reuse.
func newWebSearchHandler(ctx context.Context, mcpEndpoint, authToken string, httpClient *http.Client, auth *cliproxyauth.Auth, authAttrs map[string]string) *webSearchHandler {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	return &webSearchHandler{
		ctx:         ctx,
		mcpEndpoint: mcpEndpoint,
		httpClient:  httpClient,
		authToken:   authToken,
		auth:        auth,
		authAttrs:   authAttrs,
	}
}

// setMcpHeaders sets standard MCP API headers on the request,
// aligned with the GAR request pattern.
func (h *webSearchHandler) setMcpHeaders(req *http.Request) {
	// 1. Content-Type & Accept (aligned with GAR)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	// 2. Kiro-specific headers (aligned with GAR)
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")

	// 3. User-Agent: Reuse applyDynamicFingerprint for consistency
	applyDynamicFingerprint(req, h.auth)

	// 4. AWS SDK identifiers
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

	// 5. Authentication
	req.Header.Set("Authorization", "Bearer "+h.authToken)

	// 6. Custom headers from auth attributes
	util.ApplyCustomHeadersFromAttrs(req, h.authAttrs)
}

// mcpMaxRetries is the maximum number of retries for MCP API calls.
const mcpMaxRetries = 2

// callMcpAPI calls the Kiro MCP API with the given request.
// Includes retry logic with exponential backoff for retryable errors.
func (h *webSearchHandler) callMcpAPI(request *kiroclaude.McpRequest) (*kiroclaude.McpResponse, error) {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MCP request: %w", err)
	}
	log.Debugf("kiro/websearch MCP request → %s (%d bytes)", h.mcpEndpoint, len(requestBody))

	var lastErr error
	for attempt := 0; attempt <= mcpMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			log.Warnf("kiro/websearch: MCP retry %d/%d after %v (last error: %v)", attempt, mcpMaxRetries, backoff, lastErr)
			select {
			case <-h.ctx.Done():
				return nil, h.ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(h.ctx, "POST", h.mcpEndpoint, bytes.NewReader(requestBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}

		h.setMcpHeaders(req)

		resp, err := h.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("MCP API request failed: %w", err)
			continue // network error → retry
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read MCP response: %w", err)
			continue // read error → retry
		}
		log.Debugf("kiro/websearch MCP response ← [%d] (%d bytes)", resp.StatusCode, len(body))

		// Retryable HTTP status codes (aligned with GAR: 502, 503, 504)
		if resp.StatusCode >= 502 && resp.StatusCode <= 504 {
			lastErr = fmt.Errorf("MCP API returned retryable status %d: %s", resp.StatusCode, string(body))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("MCP API returned status %d: %s", resp.StatusCode, string(body))
		}

		var mcpResponse kiroclaude.McpResponse
		if err := json.Unmarshal(body, &mcpResponse); err != nil {
			return nil, fmt.Errorf("failed to parse MCP response: %w", err)
		}

		if mcpResponse.Error != nil {
			code := -1
			if mcpResponse.Error.Code != nil {
				code = *mcpResponse.Error.Code
			}
			msg := "Unknown error"
			if mcpResponse.Error.Message != nil {
				msg = *mcpResponse.Error.Message
			}
			return nil, fmt.Errorf("MCP error %d: %s", code, msg)
		}

		return &mcpResponse, nil
	}

	return nil, lastErr
}

// webSearchAuthAttrs extracts auth attributes for MCP calls.
// Used by handleWebSearch and handleWebSearchStream to pass custom headers.
func webSearchAuthAttrs(auth *cliproxyauth.Auth) map[string]string {
	if auth != nil {
		return auth.Attributes
	}
	return nil
}

const maxWebSearchIterations = 5

// handleWebSearchStream handles web_search requests:
// Step 1: tools/list (sync) → fetch/cache tool description
// Step 2+: MCP search → InjectToolResultsClaude → callKiroAndBuffer loop
// Note: We skip the "model decides to search" step because Claude Code already
// decided to use web_search. The Kiro tool description restricts non-coding
// topics, so asking the model again would cause it to refuse valid searches.
func (e *KiroExecutor) handleWebSearchStream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	accessToken, profileArn string,
) (<-chan cliproxyexecutor.StreamChunk, error) {
	// Extract search query from Claude Code's web_search tool_use
	query := kiroclaude.ExtractSearchQuery(req.Payload)
	if query == "" {
		log.Warnf("kiro/websearch: failed to extract search query, falling back to normal flow")
		return e.callKiroDirectStream(ctx, auth, req, opts, accessToken, profileArn)
	}

	// Build MCP endpoint using shared region resolution (supports api_region + ProfileARN fallback)
	region := resolveKiroAPIRegion(auth)
	mcpEndpoint := kiroclaude.BuildMcpEndpoint(region)

	// ── Step 1: tools/list (SYNC) — cache tool description ──
	{
		authAttrs := webSearchAuthAttrs(auth)
		fetchToolDescription(ctx, mcpEndpoint, accessToken, newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 30*time.Second), auth, authAttrs)
	}

	// Create output channel
	out := make(chan cliproxyexecutor.StreamChunk)

	// Usage reporting: track web search requests like normal streaming requests
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)

	go func() {
		var wsErr error
		defer reporter.trackFailure(ctx, &wsErr)
		defer close(out)

		// Estimate input tokens using tokenizer (matching streamToChannel pattern)
		var totalUsage usage.Detail
		if enc, tokErr := getTokenizer(req.Model); tokErr == nil {
			if inp, e := countClaudeChatTokens(enc, req.Payload); e == nil && inp > 0 {
				totalUsage.InputTokens = inp
			} else {
				totalUsage.InputTokens = int64(len(req.Payload) / 4)
			}
		} else {
			totalUsage.InputTokens = int64(len(req.Payload) / 4)
		}
		if totalUsage.InputTokens == 0 && len(req.Payload) > 0 {
			totalUsage.InputTokens = 1
		}
		var accumulatedOutputLen int
		defer func() {
			if wsErr != nil {
				return // let trackFailure handle failure reporting
			}
			totalUsage.OutputTokens = int64(accumulatedOutputLen / 4)
			if accumulatedOutputLen > 0 && totalUsage.OutputTokens == 0 {
				totalUsage.OutputTokens = 1
			}
			reporter.publish(ctx, totalUsage)
		}()

		// Send message_start event to client (aligned with streamToChannel pattern)
		// Use payloadRequestedModel to return user's original model alias
		msgStart := kiroclaude.BuildClaudeMessageStartEvent(
			payloadRequestedModel(opts, req.Model),
			totalUsage.InputTokens,
		)
		select {
		case <-ctx.Done():
			return
		case out <- cliproxyexecutor.StreamChunk{Payload: append(msgStart, '\n', '\n')}:
		}

		// ── Step 2+: MCP search → InjectToolResultsClaude → callKiroAndBuffer loop ──
		contentBlockIndex := 0
		currentQuery := query

		// Replace web_search tool description with a minimal one that allows re-search.
		// The original tools/list description from Kiro restricts non-coding topics,
		// but we've already decided to search. We keep the tool so the model can
		// request additional searches when results are insufficient.
		simplifiedPayload, simplifyErr := kiroclaude.ReplaceWebSearchToolDescription(bytes.Clone(req.Payload))
		if simplifyErr != nil {
			log.Warnf("kiro/websearch: failed to simplify web_search tool: %v, using original payload", simplifyErr)
			simplifiedPayload = bytes.Clone(req.Payload)
		}

		currentClaudePayload := simplifiedPayload
		totalSearches := 0

		// Generate toolUseId for the first iteration (Claude Code already decided to search)
		currentToolUseId := fmt.Sprintf("srvtoolu_%s", kiroclaude.GenerateToolUseID())

		for iteration := 0; iteration < maxWebSearchIterations; iteration++ {
			log.Infof("kiro/websearch: search iteration %d/%d",
				iteration+1, maxWebSearchIterations)

			// MCP search
			_, mcpRequest := kiroclaude.CreateMcpRequest(currentQuery)

			authAttrs := webSearchAuthAttrs(auth)
			handler := newWebSearchHandler(ctx, mcpEndpoint, accessToken, newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 30*time.Second), auth, authAttrs)
			mcpResponse, mcpErr := handler.callMcpAPI(mcpRequest)

			var searchResults *kiroclaude.WebSearchResults
			if mcpErr != nil {
				log.Warnf("kiro/websearch: MCP API call failed: %v, continuing with empty results", mcpErr)
			} else {
				searchResults = kiroclaude.ParseSearchResults(mcpResponse)
			}

			resultCount := 0
			if searchResults != nil {
				resultCount = len(searchResults.Results)
			}
			totalSearches++
			log.Infof("kiro/websearch: iteration %d — got %d search results", iteration+1, resultCount)

			// Send search indicator events to client
			searchEvents := kiroclaude.GenerateSearchIndicatorEvents(currentQuery, currentToolUseId, searchResults, contentBlockIndex)
			for _, event := range searchEvents {
				select {
				case <-ctx.Done():
					return
				case out <- cliproxyexecutor.StreamChunk{Payload: event}:
				}
			}
			contentBlockIndex += 2

			// Inject tool_use + tool_result into Claude payload, then call GAR
			var err error
			currentClaudePayload, err = kiroclaude.InjectToolResultsClaude(currentClaudePayload, currentToolUseId, currentQuery, searchResults)
			if err != nil {
				log.Warnf("kiro/websearch: failed to inject tool results: %v", err)
				wsErr = fmt.Errorf("failed to inject tool results: %w", err)
				e.sendFallbackText(ctx, out, contentBlockIndex, currentQuery, searchResults)
				return
			}

			// Call GAR with modified Claude payload (full translation pipeline)
			modifiedReq := req
			modifiedReq.Payload = currentClaudePayload
			kiroChunks, kiroErr := e.callKiroAndBuffer(ctx, auth, modifiedReq, opts, accessToken, profileArn)
			if kiroErr != nil {
				log.Warnf("kiro/websearch: Kiro API failed at iteration %d: %v", iteration+1, kiroErr)
				wsErr = fmt.Errorf("kiro API failed at iteration %d: %w", iteration+1, kiroErr)
				e.sendFallbackText(ctx, out, contentBlockIndex, currentQuery, searchResults)
				return
			}

			// Analyze response
			analysis := kiroclaude.AnalyzeBufferedStream(kiroChunks)
			log.Infof("kiro/websearch: iteration %d — stop_reason: %s, has_tool_use: %v",
				iteration+1, analysis.StopReason, analysis.HasWebSearchToolUse)

			if analysis.HasWebSearchToolUse && analysis.WebSearchQuery != "" && iteration+1 < maxWebSearchIterations {
				// Model wants another search
				filteredChunks := kiroclaude.FilterChunksForClient(kiroChunks, analysis.WebSearchToolUseIndex, contentBlockIndex)
				for _, chunk := range filteredChunks {
					select {
					case <-ctx.Done():
						return
					case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
					}
				}

				currentQuery = analysis.WebSearchQuery
				currentToolUseId = analysis.WebSearchToolUseId
				continue
			}

			// Model returned final response — stream to client
			for _, chunk := range kiroChunks {
				if contentBlockIndex > 0 && len(chunk) > 0 {
					adjusted, shouldForward := kiroclaude.AdjustSSEChunk(chunk, contentBlockIndex)
					if !shouldForward {
						continue
					}
					accumulatedOutputLen += len(adjusted)
					select {
					case <-ctx.Done():
						return
					case out <- cliproxyexecutor.StreamChunk{Payload: adjusted}:
					}
				} else {
					accumulatedOutputLen += len(chunk)
					select {
					case <-ctx.Done():
						return
					case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
					}
				}
			}
			log.Infof("kiro/websearch: completed after %d search iteration(s), total searches: %d", iteration+1, totalSearches)
			return
		}

		log.Warnf("kiro/websearch: reached max iterations (%d), stopping search loop", maxWebSearchIterations)
	}()

	return out, nil
}

// handleWebSearch handles web_search requests for non-streaming Execute path.
// Performs MCP search synchronously, injects results into the request payload,
// then calls the normal non-streaming Kiro API path which returns a proper
// Claude JSON response (not SSE chunks).
func (e *KiroExecutor) handleWebSearch(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	accessToken, profileArn string,
) (cliproxyexecutor.Response, error) {
	// Extract search query from Claude Code's web_search tool_use
	query := kiroclaude.ExtractSearchQuery(req.Payload)
	if query == "" {
		log.Warnf("kiro/websearch: non-stream: failed to extract search query, falling back to normal Execute")
		// Fall through to normal non-streaming path
		return e.executeNonStreamFallback(ctx, auth, req, opts, accessToken, profileArn)
	}

	// Build MCP endpoint using shared region resolution (supports api_region + ProfileARN fallback)
	region := resolveKiroAPIRegion(auth)
	mcpEndpoint := kiroclaude.BuildMcpEndpoint(region)

	// Step 1: Fetch/cache tool description (sync)
	{
		authAttrs := webSearchAuthAttrs(auth)
		fetchToolDescription(ctx, mcpEndpoint, accessToken, newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 30*time.Second), auth, authAttrs)
	}

	// Step 2: Perform MCP search
	_, mcpRequest := kiroclaude.CreateMcpRequest(query)

	authAttrs := webSearchAuthAttrs(auth)
	handler := newWebSearchHandler(ctx, mcpEndpoint, accessToken, newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 30*time.Second), auth, authAttrs)
	mcpResponse, mcpErr := handler.callMcpAPI(mcpRequest)

	var searchResults *kiroclaude.WebSearchResults
	if mcpErr != nil {
		log.Warnf("kiro/websearch: non-stream: MCP API call failed: %v, continuing with empty results", mcpErr)
	} else {
		searchResults = kiroclaude.ParseSearchResults(mcpResponse)
	}

	resultCount := 0
	if searchResults != nil {
		resultCount = len(searchResults.Results)
	}
	log.Infof("kiro/websearch: non-stream: got %d search results", resultCount)

	// Step 3: Replace restrictive web_search tool description (align with streaming path)
	simplifiedPayload, simplifyErr := kiroclaude.ReplaceWebSearchToolDescription(bytes.Clone(req.Payload))
	if simplifyErr != nil {
		log.Warnf("kiro/websearch: non-stream: failed to simplify web_search tool: %v, using original payload", simplifyErr)
		simplifiedPayload = bytes.Clone(req.Payload)
	}

	// Step 4: Inject search tool_use + tool_result into Claude payload
	currentToolUseId := fmt.Sprintf("srvtoolu_%s", kiroclaude.GenerateToolUseID())
	modifiedPayload, err := kiroclaude.InjectToolResultsClaude(simplifiedPayload, currentToolUseId, query, searchResults)
	if err != nil {
		log.Warnf("kiro/websearch: non-stream: failed to inject tool results: %v, falling back", err)
		return e.executeNonStreamFallback(ctx, auth, req, opts, accessToken, profileArn)
	}

	// Step 5: Call Kiro API via the normal non-streaming path (executeWithRetry)
	// This path uses parseEventStream → BuildClaudeResponse → TranslateNonStream
	// to produce a proper Claude JSON response
	modifiedReq := req
	modifiedReq.Payload = modifiedPayload

	resp, err := e.executeNonStreamFallback(ctx, auth, modifiedReq, opts, accessToken, profileArn)
	if err != nil {
		return resp, err
	}

	// Step 6: Inject server_tool_use + web_search_tool_result into response
	// so Claude Code can display "Did X searches in Ys"
	indicators := []kiroclaude.SearchIndicator{
		{
			ToolUseID: currentToolUseId,
			Query:     query,
			Results:   searchResults,
		},
	}
	injectedPayload, injErr := kiroclaude.InjectSearchIndicatorsInResponse(resp.Payload, indicators)
	if injErr != nil {
		log.Warnf("kiro/websearch: non-stream: failed to inject search indicators: %v", injErr)
	} else {
		resp.Payload = injectedPayload
	}

	return resp, nil
}

// callKiroAndBuffer calls the Kiro API and buffers all response chunks.
// Returns the buffered chunks for analysis before forwarding to client.
// Usage reporting is NOT done here — the caller (handleWebSearchStream) manages its own reporter.
func (e *KiroExecutor) callKiroAndBuffer(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	accessToken, profileArn string,
) ([][]byte, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	log.Debugf("kiro/websearch GAR request: %d bytes", len(body))

	kiroModelID := e.mapModelToKiro(req.Model)
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	tokenKey := getAccountKey(auth)

	kiroStream, err := e.executeStreamWithRetry(
		ctx, auth, req, opts, accessToken, effectiveProfileArn,
		body, from, nil, kiroModelID, isAgentic, isChatOnly, tokenKey,
	)
	if err != nil {
		return nil, err
	}

	// Buffer all chunks
	var chunks [][]byte
	for chunk := range kiroStream {
		if chunk.Err != nil {
			return chunks, chunk.Err
		}
		if len(chunk.Payload) > 0 {
			chunks = append(chunks, bytes.Clone(chunk.Payload))
		}
	}

	log.Debugf("kiro/websearch GAR response: %d chunks buffered", len(chunks))

	return chunks, nil
}

// callKiroDirectStream creates a direct streaming channel to Kiro API without search.
func (e *KiroExecutor) callKiroDirectStream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	accessToken, profileArn string,
) (<-chan cliproxyexecutor.StreamChunk, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	tokenKey := getAccountKey(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	var streamErr error
	defer reporter.trackFailure(ctx, &streamErr)

	stream, streamErr := e.executeStreamWithRetry(
		ctx, auth, req, opts, accessToken, effectiveProfileArn,
		body, from, reporter, kiroModelID, isAgentic, isChatOnly, tokenKey,
	)
	return stream, streamErr
}

// sendFallbackText sends a simple text response when the Kiro API fails during the search loop.
// Delegates SSE event construction to kiroclaude.BuildFallbackTextEvents() for alignment
// with how streamToChannel() uses BuildClaude*Event() functions.
func (e *KiroExecutor) sendFallbackText(
	ctx context.Context,
	out chan<- cliproxyexecutor.StreamChunk,
	contentBlockIndex int,
	query string,
	searchResults *kiroclaude.WebSearchResults,
) {
	events := kiroclaude.BuildFallbackTextEvents(contentBlockIndex, query, searchResults)
	for _, event := range events {
		select {
		case <-ctx.Done():
			return
		case out <- cliproxyexecutor.StreamChunk{Payload: append(event, '\n', '\n')}:
		}
	}
}

// executeNonStreamFallback runs the standard non-streaming Execute path for a request.
// Used by handleWebSearch after injecting search results, or as a fallback.
func (e *KiroExecutor) executeNonStreamFallback(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	accessToken, profileArn string,
) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)
	tokenKey := getAccountKey(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	var err error
	defer reporter.trackFailure(ctx, &err)

	resp, err := e.executeWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, body, from, to, reporter, kiroModelID, isAgentic, isChatOnly, tokenKey)
	return resp, err
}

func (e *KiroExecutor) CloseExecutionSession(sessionID string) {}

// getAccountKey returns a unique key for rate limiting and cooldown tracking.
func getAccountKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return fmt.Sprintf("kiro:%s:%s", auth.ID, auth.Label)
}

// fetchAndSaveProfileArn fetches the IAM profile ARN for the authenticated user and saves it to the auth record.
func (e *KiroExecutor) fetchAndSaveProfileArn(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) (string, error) {
	// This would fetch the profile ARN from Kiro's identity API
	// and save it to the auth record's attributes
	return "", nil
}

// kiroIDEAgentMode represents the Kiro IDE agent mode.
const kiroIDEAgentMode = "VIBE"

// enqueueTranslatedSSE enqueues a translated SSE chunk to the output channel.
func enqueueTranslatedSSE(out chan<- cliproxyexecutor.StreamChunk, chunk []byte) {
	out <- cliproxyexecutor.StreamChunk{Payload: chunk}
}
