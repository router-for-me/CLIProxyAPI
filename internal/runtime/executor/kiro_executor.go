package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

const (
	// Kiro API common constants
	kiroContentType    = "application/x-amz-json-1.0"
	kiroAcceptStream   = "*/*"
	kiroMaxMessageSize = 10 * 1024 * 1024 // 10MB max message size for event stream
	kiroMaxToolDescLen = 10237            // Kiro API limit is 10240 bytes, leave room for "..."
	// kiroUserAgent matches amq2api format for User-Agent header
	kiroUserAgent = "aws-sdk-rust/1.3.9 os/macos lang/rust/1.87.0"
	// kiroFullUserAgent is the complete x-amz-user-agent header matching amq2api
	kiroFullUserAgent = "aws-sdk-rust/1.3.9 ua/2.1 api/ssooidc/1.88.0 os/macos lang/rust/1.87.0 m/E app/AmazonQ-For-CLI"

	// Thinking mode support - based on amq2api implementation
	// These tags wrap reasoning content in the response stream
	thinkingStartTag = "<thinking>"
	thinkingEndTag   = "</thinking>"
	// thinkingHint is injected into the request to enable interleaved thinking mode
	// This tells the model to use thinking tags and sets the max thinking length
	thinkingHint     = "<thinking_mode>interleaved</thinking_mode><max_thinking_length>16000</max_thinking_length>"

	// kiroAgenticSystemPrompt is injected only for -agentic models to prevent timeouts on large writes.
	// AWS Kiro API has a 2-3 minute timeout for large file write operations.
	kiroAgenticSystemPrompt = `
# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- **MAXIMUM 350 LINES** per single write/edit operation - NO EXCEPTIONS
- **RECOMMENDED 300 LINES** or less for optimal performance
- **NEVER** write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

### For NEW FILES (>300 lines total):
1. FIRST: Write initial chunk (first 250-300 lines) using write_to_file/fsWrite
2. THEN: Append remaining content in 250-300 line chunks using file append operations
3. REPEAT: Continue appending until complete

### For EDITING EXISTING FILES:
1. Use surgical edits (apply_diff/targeted edits) - change ONLY what's needed
2. NEVER rewrite entire files - use incremental modifications
3. Split large refactors into multiple small, focused edits

### For LARGE CODE GENERATION:
1. Generate in logical sections (imports, types, functions separately)
2. Write each section as a separate operation
3. Use append operations for subsequent sections

## EXAMPLES OF CORRECT BEHAVIOR

✅ CORRECT: Writing a 600-line file
- Operation 1: Write lines 1-300 (initial file creation)
- Operation 2: Append lines 301-600

✅ CORRECT: Editing multiple functions
- Operation 1: Edit function A
- Operation 2: Edit function B
- Operation 3: Edit function C

❌ WRONG: Writing 500 lines in single operation → TIMEOUT
❌ WRONG: Rewriting entire file to change 5 lines → TIMEOUT
❌ WRONG: Generating massive code blocks without chunking → TIMEOUT

## WHY THIS MATTERS
- Server has 2-3 minute timeout for operations
- Large writes exceed timeout and FAIL completely
- Chunked writes are FASTER and more RELIABLE
- Failed writes waste time and require retry

REMEMBER: When in doubt, write LESS per operation. Multiple small operations > one large operation.`
)

// kiroEndpointConfig bundles endpoint URL with its compatible Origin and AmzTarget values.
// This solves the "triple mismatch" problem where different endpoints require matching
// Origin and X-Amz-Target header values.
//
// Based on reference implementations:
// - amq2api-main: Uses Amazon Q endpoint with CLI origin and AmazonQDeveloperStreamingService target
// - AIClient-2-API: Uses CodeWhisperer endpoint with AI_EDITOR origin and AmazonCodeWhispererStreamingService target
type kiroEndpointConfig struct {
	URL       string // Endpoint URL
	Origin    string // Request Origin: "CLI" for Amazon Q quota, "AI_EDITOR" for Kiro IDE quota
	AmzTarget string // X-Amz-Target header value
	Name      string // Endpoint name for logging
}

// kiroEndpointConfigs defines the available Kiro API endpoints with their compatible configurations.
// The order determines fallback priority: primary endpoint first, then fallbacks.
//
// CRITICAL: Each endpoint MUST use its compatible Origin and AmzTarget values:
// - CodeWhisperer endpoint (codewhisperer.us-east-1.amazonaws.com): Uses AI_EDITOR origin and AmazonCodeWhispererStreamingService target
// - Amazon Q endpoint (q.us-east-1.amazonaws.com): Uses CLI origin and AmazonQDeveloperStreamingService target
//
// Mismatched combinations will result in 403 Forbidden errors.
//
// NOTE: CodeWhisperer is set as the default endpoint because:
// 1. Most tokens come from Kiro IDE / VSCode extensions (AWS Builder ID auth)
// 2. These tokens use AI_EDITOR origin which is only compatible with CodeWhisperer endpoint
// 3. Amazon Q endpoint requires CLI origin which is for Amazon Q CLI tokens
// This matches the AIClient-2-API-main project's configuration.
var kiroEndpointConfigs = []kiroEndpointConfig{
	{
		URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		Origin:    "AI_EDITOR",
		AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		Name:      "CodeWhisperer",
	},
	{
		URL:       "https://q.us-east-1.amazonaws.com/",
		Origin:    "CLI",
		AmzTarget: "AmazonQDeveloperStreamingService.SendMessage",
		Name:      "AmazonQ",
	},
}

// getKiroEndpointConfigs returns the list of Kiro API endpoint configurations to try in order.
// Supports reordering based on "preferred_endpoint" in auth metadata/attributes.
func getKiroEndpointConfigs(auth *cliproxyauth.Auth) []kiroEndpointConfig {
	if auth == nil {
		return kiroEndpointConfigs
	}

	// Check for preference
	var preference string
	if auth.Metadata != nil {
		if p, ok := auth.Metadata["preferred_endpoint"].(string); ok {
			preference = p
		}
	}
	// Check attributes as fallback (e.g. from HTTP headers)
	if preference == "" && auth.Attributes != nil {
		preference = auth.Attributes["preferred_endpoint"]
	}

	if preference == "" {
		return kiroEndpointConfigs
	}

	preference = strings.ToLower(strings.TrimSpace(preference))
	
	// Create new slice to avoid modifying global state
	var sorted []kiroEndpointConfig
	var remaining []kiroEndpointConfig

	for _, cfg := range kiroEndpointConfigs {
		name := strings.ToLower(cfg.Name)
		// Check for matches
		// CodeWhisperer aliases: codewhisperer, ide
		// AmazonQ aliases: amazonq, q, cli
		isMatch := false
		if (preference == "codewhisperer" || preference == "ide") && name == "codewhisperer" {
			isMatch = true
		} else if (preference == "amazonq" || preference == "q" || preference == "cli") && name == "amazonq" {
			isMatch = true
		}

		if isMatch {
			sorted = append(sorted, cfg)
		} else {
			remaining = append(remaining, cfg)
		}
	}

	// If preference didn't match anything, return default
	if len(sorted) == 0 {
		return kiroEndpointConfigs
	}

	// Combine: preferred first, then others
	return append(sorted, remaining...)
}

// KiroExecutor handles requests to AWS CodeWhisperer (Kiro) API.
type KiroExecutor struct {
	cfg         *config.Config
	refreshMu   sync.Mutex // Serializes token refresh operations to prevent race conditions
}

// NewKiroExecutor creates a new Kiro executor instance.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// PrepareRequest prepares the HTTP request before execution.
func (e *KiroExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }


// Execute sends the request to Kiro API and returns the response.
// Supports automatic token refresh on 401/403 errors.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("kiro: access token not found in auth")
	}
	if profileArn == "" {
		// Only warn if not using builder-id auth (which doesn't need profileArn)
		if auth == nil || auth.Metadata == nil {
			log.Debugf("kiro: profile ARN not found in auth (may be normal for builder-id)")
		} else if authMethod, ok := auth.Metadata["auth_method"].(string); !ok || authMethod != "builder-id" {
			log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
		}
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	// Check if token is expired before making request
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting refresh before request")
		refreshedAuth, refreshErr := e.Refresh(ctx, auth)
		if refreshErr != nil {
			log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
		} else if refreshedAuth != nil {
			auth = refreshedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: token refreshed successfully before request")
		}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)
	
	// Check if this is an agentic model variant
	isAgentic := strings.HasSuffix(req.Model, "-agentic")
	
	// Check if this is a chat-only model variant (no tool calling)
	isChatOnly := strings.HasSuffix(req.Model, "-chat")
	
	// Determine initial origin - always use AI_EDITOR to match AIClient-2-API behavior
	// AIClient-2-API uses AI_EDITOR for all models, which is the Kiro IDE quota
	// Note: CLI origin is for Amazon Q quota, but AIClient-2-API doesn't use it
	currentOrigin := "AI_EDITOR"
	
	// Determine if profileArn should be included based on auth method
	// profileArn is only needed for social auth (Google OAuth), not for builder-id (AWS SSO)
	effectiveProfileArn := profileArn
	if auth != nil && auth.Metadata != nil {
		if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
			effectiveProfileArn = "" // Don't include profileArn for builder-id auth
		}
	}
	
	kiroPayload := e.buildKiroPayload(body, kiroModelID, effectiveProfileArn, currentOrigin, isAgentic, isChatOnly)

	// Execute with retry on 401/403 and 429 (quota exhausted)
	resp, err = e.executeWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, kiroPayload, body, from, to, reporter, currentOrigin, kiroModelID, isAgentic, isChatOnly)
	return resp, err
}

// executeWithRetry performs the actual HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
func (e *KiroExecutor) executeWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from, to sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	endpointConfigs := getKiroEndpointConfigs(auth)

	for endpointIdx := 0; endpointIdx < len(endpointConfigs); endpointIdx++ {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin
		
		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
		
		log.Debugf("kiro: trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return resp, err
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("Accept", kiroAcceptStream)
		// Use endpoint-specific X-Amz-Target (critical for avoiding 403 errors)
		httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
		httpReq.Header.Set("User-Agent", kiroUserAgent)
		httpReq.Header.Set("X-Amz-User-Agent", kiroFullUserAgent)
		httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
		httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

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

		httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 120*time.Second)
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			return resp, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

		// Handle 429 errors (quota exhausted) - try next endpoint
		// Each endpoint has its own quota pool, so we can try different endpoints
		if httpResp.StatusCode == 429 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: %s endpoint quota exhausted (429), will try next endpoint", endpointConfig.Name)
			
			// Break inner retry loop to try next endpoint (which has different quota)
			break
		}

		// Handle 5xx server errors with exponential backoff retry
		if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			if attempt < maxRetries {
				// Exponential backoff: 1s, 2s, 4s... (max 30s)
				backoff := time.Duration(1<<attempt) * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				log.Warnf("kiro: server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
				time.Sleep(backoff)
				continue
			}
			log.Errorf("kiro: server error %d after %d retries", httpResp.StatusCode, maxRetries)
			return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		// Handle 401 errors with token refresh and retry
		// 401 = Unauthorized (token expired/invalid) - refresh token
		if httpResp.StatusCode == 401 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			if attempt < maxRetries {
				log.Warnf("kiro: received 401 error, attempting token refresh and retry (attempt %d/%d)", attempt+1, maxRetries+1)

				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}

				if refreshedAuth != nil {
					auth = refreshedAuth
					accessToken, profileArn = kiroCredentials(auth)
					// Rebuild payload with new profile ARN if changed
					kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
					log.Infof("kiro: token refreshed successfully, retrying request")
					continue
				}
			}

			log.Warnf("kiro request error, status: 401, body: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
			return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		// Handle 402 errors - Monthly Limit Reached
		if httpResp.StatusCode == 402 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: received 402 (monthly limit). Upstream body: %s", string(respBody))

			// Return upstream error body directly
			return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		// Handle 403 errors - Access Denied / Token Expired
		// Do NOT switch endpoints for 403 errors
		if httpResp.StatusCode == 403 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			// Log the 403 error details for debugging
			log.Warnf("kiro: received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			respBodyStr := string(respBody)

			// Check for SUSPENDED status - return immediately without retry
			if strings.Contains(respBodyStr, "SUSPENDED") || strings.Contains(respBodyStr, "TEMPORARILY_SUSPENDED") {
				log.Errorf("kiro: account is suspended, cannot proceed")
				return resp, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + string(respBody)}
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
					return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}
				if refreshedAuth != nil {
					auth = refreshedAuth
					accessToken, profileArn = kiroCredentials(auth)
					kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
					log.Infof("kiro: token refreshed for 403, retrying request")
					continue
				}
			}

			// For non-token 403 or after max retries, return error immediately
			// Do NOT switch endpoints for 403 errors
			log.Warnf("kiro: 403 error, returning immediately (no endpoint switch)")
			return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			appendAPIResponseChunk(ctx, e.cfg, b)
			log.Debugf("kiro request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
			err = statusErr{code: httpResp.StatusCode, msg: string(b)}
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
			return resp, err
		}

		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()

		content, toolUses, usageInfo, err := e.parseEventStream(httpResp.Body)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			return resp, err
		}

		// Fallback for usage if missing from upstream
		if usageInfo.TotalTokens == 0 {
			if enc, encErr := tokenizerForModel(req.Model); encErr == nil {
				if inp, countErr := countOpenAIChatTokens(enc, opts.OriginalRequest); countErr == nil {
					usageInfo.InputTokens = inp
				}
			}
			if len(content) > 0 {
				// Use tiktoken for more accurate output token calculation
				if enc, encErr := tokenizerForModel(req.Model); encErr == nil {
					if tokenCount, countErr := enc.Count(content); countErr == nil {
						usageInfo.OutputTokens = int64(tokenCount)
					}
				}
				// Fallback to character count estimation if tiktoken fails
				if usageInfo.OutputTokens == 0 {
					usageInfo.OutputTokens = int64(len(content) / 4)
					if usageInfo.OutputTokens == 0 {
						usageInfo.OutputTokens = 1
					}
				}
			}
			usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens
		}

		appendAPIResponseChunk(ctx, e.cfg, []byte(content))
		reporter.publish(ctx, usageInfo)

		// Build response in Claude format for Kiro translator
		kiroResponse := e.buildClaudeResponse(content, toolUses, req.Model, usageInfo)
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, kiroResponse, nil)
		resp = cliproxyexecutor.Response{Payload: []byte(out)}
		return resp, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	return resp, fmt.Errorf("kiro: all endpoints exhausted")
}

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors and quota fallback on 429.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
	}
	if profileArn == "" {
		// Only warn if not using builder-id auth (which doesn't need profileArn)
		if auth == nil || auth.Metadata == nil {
			log.Debugf("kiro: profile ARN not found in auth (may be normal for builder-id)")
		} else if authMethod, ok := auth.Metadata["auth_method"].(string); !ok || authMethod != "builder-id" {
			log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
		}
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	// Check if token is expired before making request
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting refresh before stream request")
		refreshedAuth, refreshErr := e.Refresh(ctx, auth)
		if refreshErr != nil {
			log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
		} else if refreshedAuth != nil {
			auth = refreshedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: token refreshed successfully before stream request")
		}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)
	
	// Check if this is an agentic model variant
	isAgentic := strings.HasSuffix(req.Model, "-agentic")
	
	// Check if this is a chat-only model variant (no tool calling)
	isChatOnly := strings.HasSuffix(req.Model, "-chat")
	
	// Determine initial origin - always use AI_EDITOR to match AIClient-2-API behavior
	// AIClient-2-API uses AI_EDITOR for all models, which is the Kiro IDE quota
	currentOrigin := "AI_EDITOR"
	
	// Determine if profileArn should be included based on auth method
	// profileArn is only needed for social auth (Google OAuth), not for builder-id (AWS SSO)
	effectiveProfileArn := profileArn
	if auth != nil && auth.Metadata != nil {
		if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
			effectiveProfileArn = "" // Don't include profileArn for builder-id auth
		}
	}
	
	kiroPayload := e.buildKiroPayload(body, kiroModelID, effectiveProfileArn, currentOrigin, isAgentic, isChatOnly)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	return e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, kiroPayload, body, from, reporter, currentOrigin, kiroModelID, isAgentic, isChatOnly)
}

// executeStreamWithRetry performs the streaming HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool) (<-chan cliproxyexecutor.StreamChunk, error) {
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	endpointConfigs := getKiroEndpointConfigs(auth)

	for endpointIdx := 0; endpointIdx < len(endpointConfigs); endpointIdx++ {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin
		
		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
		
		log.Debugf("kiro: stream trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return nil, err
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("Accept", kiroAcceptStream)
		// Use endpoint-specific X-Amz-Target (critical for avoiding 403 errors)
		httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
		httpReq.Header.Set("User-Agent", kiroUserAgent)
		httpReq.Header.Set("X-Amz-User-Agent", kiroFullUserAgent)
		httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
		httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

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

		httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			return nil, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

		// Handle 429 errors (quota exhausted) - try next endpoint
		// Each endpoint has its own quota pool, so we can try different endpoints
		if httpResp.StatusCode == 429 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: stream %s endpoint quota exhausted (429), will try next endpoint", endpointConfig.Name)
			
			// Break inner retry loop to try next endpoint (which has different quota)
			break
		}

		// Handle 5xx server errors with exponential backoff retry
		if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			if attempt < maxRetries {
				// Exponential backoff: 1s, 2s, 4s... (max 30s)
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

			if attempt < maxRetries {
				log.Warnf("kiro: stream received 401 error, attempting token refresh and retry (attempt %d/%d)", attempt+1, maxRetries+1)

				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}

				if refreshedAuth != nil {
					auth = refreshedAuth
					accessToken, profileArn = kiroCredentials(auth)
					// Rebuild payload with new profile ARN if changed
					kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
					log.Infof("kiro: token refreshed successfully, retrying stream request")
					continue
				}
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
				log.Errorf("kiro: account is suspended, cannot proceed")
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
					accessToken, profileArn = kiroCredentials(auth)
					kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)
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

		go func(resp *http.Response) {
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

			e.streamToChannel(ctx, resp.Body, out, from, req.Model, opts.OriginalRequest, body, reporter)
		}(httpResp)

		return out, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	return nil, fmt.Errorf("kiro: stream all endpoints exhausted")
}


// kiroCredentials extracts access token and profile ARN from auth.
func kiroCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	if auth == nil {
		return "", ""
	}
	
	// Try Metadata first (wrapper format)
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profile_arn"].(string); ok {
			profileArn = arn
		}
	}
	
	// Try Attributes
	if accessToken == "" && auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		profileArn = auth.Attributes["profile_arn"]
	}
	
	// Try direct fields from flat JSON format (new AWS Builder ID format)
	if accessToken == "" && auth.Metadata != nil {
		if token, ok := auth.Metadata["accessToken"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profileArn"].(string); ok {
			profileArn = arn
		}
	}
	
	return accessToken, profileArn
}

// mapModelToKiro maps external model names to Kiro model IDs.
// Supports both Kiro and Amazon Q prefixes since they use the same API.
// Agentic variants (-agentic suffix) map to the same backend model IDs.
func (e *KiroExecutor) mapModelToKiro(model string) string {
	modelMap := map[string]string{
		// Amazon Q format (amazonq- prefix) - same API as Kiro
		"amazonq-auto":                       "auto",
		"amazonq-claude-opus-4-5":            "claude-opus-4.5",
		"amazonq-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4":            "claude-sonnet-4",
		"amazonq-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"amazonq-claude-haiku-4-5":           "claude-haiku-4.5",
		// Kiro format (kiro- prefix) - valid model names that should be preserved
		"kiro-claude-opus-4-5":            "claude-opus-4.5",
		"kiro-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4":            "claude-sonnet-4",
		"kiro-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5":           "claude-haiku-4.5",
		"kiro-auto":                       "auto",
		// Native format (no prefix) - used by Kiro IDE directly
		"claude-opus-4-5":            "claude-opus-4.5",
		"claude-opus-4.5":            "claude-opus-4.5",
		"claude-haiku-4-5":           "claude-haiku-4.5",
		"claude-haiku-4.5":           "claude-haiku-4.5",
		"claude-sonnet-4-5":          "claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-sonnet-4.5":          "claude-sonnet-4.5",
		"claude-sonnet-4":            "claude-sonnet-4",
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"auto":                       "auto",
		// Agentic variants (same backend model IDs, but with special system prompt)
		"claude-opus-4.5-agentic":            "claude-opus-4.5",
		"claude-sonnet-4.5-agentic":          "claude-sonnet-4.5",
		"claude-sonnet-4-agentic":            "claude-sonnet-4",
		"claude-haiku-4.5-agentic":           "claude-haiku-4.5",
		"kiro-claude-opus-4-5-agentic":       "claude-opus-4.5",
		"kiro-claude-sonnet-4-5-agentic":     "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-agentic":       "claude-sonnet-4",
		"kiro-claude-haiku-4-5-agentic":      "claude-haiku-4.5",
	}
	if kiroID, ok := modelMap[model]; ok {
		return kiroID
	}
	
	// Smart fallback: try to infer model type from name patterns
	modelLower := strings.ToLower(model)
	
	// Check for Haiku variants
	if strings.Contains(modelLower, "haiku") {
		log.Debugf("kiro: unknown Haiku model '%s', mapping to claude-haiku-4.5", model)
		return "claude-haiku-4.5"
	}
	
	// Check for Sonnet variants
	if strings.Contains(modelLower, "sonnet") {
		// Check for specific version patterns
		if strings.Contains(modelLower, "3-7") || strings.Contains(modelLower, "3.7") {
			log.Debugf("kiro: unknown Sonnet 3.7 model '%s', mapping to claude-3-7-sonnet-20250219", model)
			return "claude-3-7-sonnet-20250219"
		}
		if strings.Contains(modelLower, "4-5") || strings.Contains(modelLower, "4.5") {
			log.Debugf("kiro: unknown Sonnet 4.5 model '%s', mapping to claude-sonnet-4.5", model)
			return "claude-sonnet-4.5"
		}
		// Default to Sonnet 4
		log.Debugf("kiro: unknown Sonnet model '%s', mapping to claude-sonnet-4", model)
		return "claude-sonnet-4"
	}
	
	// Check for Opus variants
	if strings.Contains(modelLower, "opus") {
		log.Debugf("kiro: unknown Opus model '%s', mapping to claude-opus-4.5", model)
		return "claude-opus-4.5"
	}
	
	// Final fallback to Sonnet 4.5 (most commonly used model)
	log.Warnf("kiro: unknown model '%s', falling back to claude-sonnet-4.5", model)
	return "claude-sonnet-4.5"
}

// Kiro API request structs - field order determines JSON key order

type kiroPayload struct {
	ConversationState kiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
}

type kiroConversationState struct {
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL" - must be first field
	ConversationID  string               `json:"conversationId"`
	CurrentMessage  kiroCurrentMessage   `json:"currentMessage"`
	History         []kiroHistoryMessage `json:"history,omitempty"`
}

type kiroCurrentMessage struct {
	UserInputMessage kiroUserInputMessage `json:"userInputMessage"`
}

type kiroHistoryMessage struct {
	UserInputMessage         *kiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// kiroImage represents an image in Kiro API format
type kiroImage struct {
	Format string          `json:"format"`
	Source kiroImageSource `json:"source"`
}

// kiroImageSource contains the image data
type kiroImageSource struct {
	Bytes string `json:"bytes"` // base64 encoded image data
}

type kiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	ModelID                 string                       `json:"modelId"`
	Origin                  string                       `json:"origin"`
	Images                  []kiroImage                  `json:"images,omitempty"`
	UserInputMessageContext *kiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

type kiroUserInputMessageContext struct {
	ToolResults []kiroToolResult       `json:"toolResults,omitempty"`
	Tools       []kiroToolWrapper      `json:"tools,omitempty"`
}

type kiroToolResult struct {
	Content   []kiroTextContent   `json:"content"`
	Status    string              `json:"status"`
	ToolUseID string              `json:"toolUseId"`
}

type kiroTextContent struct {
	Text string `json:"text"`
}

type kiroToolWrapper struct {
	ToolSpecification kiroToolSpecification `json:"toolSpecification"`
}

type kiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema kiroInputSchema `json:"inputSchema"`
}

type kiroInputSchema struct {
	JSON interface{} `json:"json"`
}

type kiroAssistantResponseMessage struct {
	Content  string         `json:"content"`
	ToolUses []kiroToolUse  `json:"toolUses,omitempty"`
}

type kiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// buildKiroPayload constructs the Kiro API request payload.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// isAgentic parameter enables chunked write optimization prompt for -agentic model variants.
// isChatOnly parameter disables tool calling for -chat model variants (pure conversation mode).
// Supports thinking mode - when Claude API thinking parameter is present, injects thinkingHint.
func (e *KiroExecutor) buildKiroPayload(claudeBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool) []byte {
	// Normalize origin value for Kiro API compatibility
	// Kiro API only accepts "CLI" or "AI_EDITOR" as valid origin values
	switch origin {
	case "KIRO_CLI":
		origin = "CLI"
	case "KIRO_AI_EDITOR":
		origin = "AI_EDITOR"
	case "AMAZON_Q":
		origin = "CLI"
	case "KIRO_IDE":
		origin = "AI_EDITOR"
	// Add any other non-standard origin values that need normalization
	default:
		// Keep the original value if it's already standard
		// Valid values: "CLI", "AI_EDITOR"
	}
	log.Debugf("kiro: normalized origin value: %s", origin)
	
	messages := gjson.GetBytes(claudeBody, "messages")
	
	// For chat-only mode, don't include tools
	var tools gjson.Result
	if !isChatOnly {
		tools = gjson.GetBytes(claudeBody, "tools")
	}
	
	// Extract system prompt - can be string or array of content blocks
	systemField := gjson.GetBytes(claudeBody, "system")
	var systemPrompt string
	if systemField.IsArray() {
		// System is array of content blocks, extract text
		var sb strings.Builder
		for _, block := range systemField.Array() {
			if block.Get("type").String() == "text" {
				sb.WriteString(block.Get("text").String())
			} else if block.Type == gjson.String {
				sb.WriteString(block.String())
			}
		}
		systemPrompt = sb.String()
	} else {
		systemPrompt = systemField.String()
	}

	// Check for thinking parameter in Claude API request
	// Claude API format: {"thinking": {"type": "enabled", "budget_tokens": 16000}}
	// When thinking is enabled, inject dynamic thinkingHint based on budget_tokens
	// This allows reasoning_effort (low/medium/high) to control actual thinking length
	thinkingEnabled := false
	var budgetTokens int64 = 16000 // Default value (same as OpenAI reasoning_effort "medium")
	thinkingField := gjson.GetBytes(claudeBody, "thinking")
	if thinkingField.Exists() {
		// Check if thinking.type is "enabled"
		thinkingType := thinkingField.Get("type").String()
		if thinkingType == "enabled" {
			thinkingEnabled = true
			// Read budget_tokens if specified - this value comes from:
			// - Claude API: thinking.budget_tokens directly
			// - OpenAI API: reasoning_effort -> budget_tokens (low:4000, medium:16000, high:32000)
			if bt := thinkingField.Get("budget_tokens"); bt.Exists() {
				budgetTokens = bt.Int()
				// If budget_tokens <= 0, disable thinking explicitly
				// This allows users to disable thinking by setting budget_tokens to 0
				if budgetTokens <= 0 {
					thinkingEnabled = false
					log.Debugf("kiro: thinking mode disabled via budget_tokens <= 0")
				}
			}
			if thinkingEnabled {
				log.Debugf("kiro: thinking mode enabled via Claude API parameter, budget_tokens: %d", budgetTokens)
			}
		}
	}

	// Inject timestamp context for better temporal awareness
	// Based on amq2api implementation - helps model understand current time context
	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")
	timestampContext := fmt.Sprintf("[Context: Current time is %s]", timestamp)
	if systemPrompt != "" {
		systemPrompt = timestampContext + "\n\n" + systemPrompt
	} else {
		systemPrompt = timestampContext
	}
	log.Debugf("kiro: injected timestamp context: %s", timestamp)

	// Inject agentic optimization prompt for -agentic model variants
	// This prevents AWS Kiro API timeouts during large file write operations
	if isAgentic {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += kiroAgenticSystemPrompt
	}

	// Inject thinking hint when thinking mode is enabled
	// This tells the model to use <thinking> tags in its response
	// DYNAMICALLY set max_thinking_length based on budget_tokens from request
	// This respects the reasoning_effort setting: low(4000), medium(16000), high(32000)
	if thinkingEnabled {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		// Build dynamic thinking hint with the actual budget_tokens value
		dynamicThinkingHint := fmt.Sprintf("<thinking_mode>interleaved</thinking_mode><max_thinking_length>%d</max_thinking_length>", budgetTokens)
		systemPrompt += dynamicThinkingHint
		log.Debugf("kiro: injected dynamic thinking hint into system prompt, max_thinking_length: %d", budgetTokens)
	}

	// Convert Claude tools to Kiro format
	var kiroTools []kiroToolWrapper
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			name := tool.Get("name").String()
			description := tool.Get("description").String()
			inputSchema := tool.Get("input_schema").Value()
			
			// Truncate long descriptions (Kiro API limit is in bytes)
			// Truncate at valid UTF-8 boundary to avoid breaking multi-byte chars
			// Add truncation notice to help model understand the description is incomplete
			if len(description) > kiroMaxToolDescLen {
				// Find a valid UTF-8 boundary before the limit
				// Reserve space for truncation notice (about 30 bytes)
				truncLen := kiroMaxToolDescLen - 30
				for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
					truncLen--
				}
				description = description[:truncLen] + "... (description truncated)"
			}
			
			kiroTools = append(kiroTools, kiroToolWrapper{
				ToolSpecification: kiroToolSpecification{
					Name:        name,
					Description: description,
					InputSchema: kiroInputSchema{JSON: inputSchema},
				},
			})
		}
	}

	var history []kiroHistoryMessage
	var currentUserMsg *kiroUserInputMessage
	var currentToolResults []kiroToolResult

	// Merge adjacent messages with the same role before processing
	// This reduces API call complexity and improves compatibility
	messagesArray := mergeAdjacentMessages(messages.Array())
	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		if role == "user" {
			userMsg, toolResults := e.buildUserMessageStruct(msg, modelID, origin)
			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
				// CRITICAL: Kiro API requires content to be non-empty for history messages too
				if strings.TrimSpace(userMsg.Content) == "" {
					if len(toolResults) > 0 {
						userMsg.Content = "Tool results provided."
					} else {
						userMsg.Content = "Continue"
					}
				}
				// For history messages, embed tool results in context
				if len(toolResults) > 0 {
					userMsg.UserInputMessageContext = &kiroUserInputMessageContext{
						ToolResults: toolResults,
					}
				}
				history = append(history, kiroHistoryMessage{
					UserInputMessage: &userMsg,
				})
			}
		} else if role == "assistant" {
			assistantMsg := e.buildAssistantMessageStruct(msg)
			// If this is the last message and it's an assistant message,
			// we need to add it to history and create a "Continue" user message
			// because Kiro API requires currentMessage to be userInputMessage type
			if isLastMessage {
				history = append(history, kiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
				// Create a "Continue" user message as currentMessage
				currentUserMsg = &kiroUserInputMessage{
					Content: "Continue",
					ModelID: modelID,
					Origin:  origin,
				}
			} else {
				history = append(history, kiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
			}
		}
	}

	// Build content with system prompt
	if currentUserMsg != nil {
		var contentBuilder strings.Builder
		
		// Add system prompt if present
		if systemPrompt != "" {
			contentBuilder.WriteString("--- SYSTEM PROMPT ---\n")
			contentBuilder.WriteString(systemPrompt)
			contentBuilder.WriteString("\n--- END SYSTEM PROMPT ---\n\n")
		}
		
		// Add the actual user message
		contentBuilder.WriteString(currentUserMsg.Content)
		finalContent := contentBuilder.String()
		
		// CRITICAL: Kiro API requires content to be non-empty, even when toolResults are present
		// If content is empty or only whitespace, provide a default message
		if strings.TrimSpace(finalContent) == "" {
			if len(currentToolResults) > 0 {
				finalContent = "Tool results provided."
			} else {
				finalContent = "Continue"
			}
			log.Debugf("kiro: content was empty, using default: %s", finalContent)
		}
		currentUserMsg.Content = finalContent
		
		// Deduplicate currentToolResults before adding to context
		// Kiro API does not accept duplicate toolUseIds
		if len(currentToolResults) > 0 {
			seenIDs := make(map[string]bool)
			uniqueToolResults := make([]kiroToolResult, 0, len(currentToolResults))
			for _, tr := range currentToolResults {
				if !seenIDs[tr.ToolUseID] {
					seenIDs[tr.ToolUseID] = true
					uniqueToolResults = append(uniqueToolResults, tr)
				} else {
					log.Debugf("kiro: skipping duplicate toolResult in currentMessage: %s", tr.ToolUseID)
				}
			}
			currentToolResults = uniqueToolResults
		}
		
		// Build userInputMessageContext with tools and tool results
		if len(kiroTools) > 0 || len(currentToolResults) > 0 {
			currentUserMsg.UserInputMessageContext = &kiroUserInputMessageContext{
				Tools:       kiroTools,
				ToolResults: currentToolResults,
			}
		}
	}

	// Build payload using structs (preserves key order)
	var currentMessage kiroCurrentMessage
	if currentUserMsg != nil {
		currentMessage = kiroCurrentMessage{UserInputMessage: *currentUserMsg}
	} else {
		// Fallback when no user messages - still include system prompt if present
		fallbackContent := ""
		if systemPrompt != "" {
			fallbackContent = "--- SYSTEM PROMPT ---\n" + systemPrompt + "\n--- END SYSTEM PROMPT ---\n"
		}
		currentMessage = kiroCurrentMessage{UserInputMessage: kiroUserInputMessage{
			Content: fallbackContent,
			ModelID: modelID,
			Origin:  origin,
		}}
	}
	
	// Build payload with correct field order (matches struct definition)
	payload := kiroPayload{
		ConversationState: kiroConversationState{
			ChatTriggerType: "MANUAL", // Required by Kiro API - must be first
			ConversationID:  uuid.New().String(),
			CurrentMessage:  currentMessage,
			History:         history, // Now always included (non-nil slice)
		},
		ProfileArn: profileArn,
	}

	result, err := json.Marshal(payload)
	if err != nil {
		log.Debugf("kiro: failed to marshal payload: %v", err)
		return nil
	}
	
	return result
}

// buildUserMessageStruct builds a user message and extracts tool results
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// IMPORTANT: Kiro API does not accept duplicate toolUseIds, so we deduplicate here.
func (e *KiroExecutor) buildUserMessageStruct(msg gjson.Result, modelID, origin string) (kiroUserInputMessage, []kiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []kiroToolResult
	var images []kiroImage
	
	// Track seen toolUseIds to deduplicate - Kiro API rejects duplicate toolUseIds
	seenToolUseIDs := make(map[string]bool)

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "image":
				// Extract image data from Claude API format
				mediaType := part.Get("source.media_type").String()
				data := part.Get("source.data").String()
				
				// Extract format from media_type (e.g., "image/png" -> "png")
				format := ""
				if idx := strings.LastIndex(mediaType, "/"); idx != -1 {
					format = mediaType[idx+1:]
				}
				
				if format != "" && data != "" {
					images = append(images, kiroImage{
						Format: format,
						Source: kiroImageSource{
							Bytes: data,
						},
					})
				}
			case "tool_result":
				// Extract tool result for API
				toolUseID := part.Get("tool_use_id").String()
				
				// Skip duplicate toolUseIds - Kiro API does not accept duplicates
				if seenToolUseIDs[toolUseID] {
					log.Debugf("kiro: skipping duplicate tool_result with toolUseId: %s", toolUseID)
					continue
				}
				seenToolUseIDs[toolUseID] = true
				
				isError := part.Get("is_error").Bool()
				resultContent := part.Get("content")
				
				// Convert content to Kiro format: [{text: "..."}]
				var textContents []kiroTextContent
				if resultContent.IsArray() {
					for _, item := range resultContent.Array() {
						if item.Get("type").String() == "text" {
							textContents = append(textContents, kiroTextContent{Text: item.Get("text").String()})
						} else if item.Type == gjson.String {
							textContents = append(textContents, kiroTextContent{Text: item.String()})
						}
					}
				} else if resultContent.Type == gjson.String {
					textContents = append(textContents, kiroTextContent{Text: resultContent.String()})
				}
				
				// If no content, add default message
				if len(textContents) == 0 {
					textContents = append(textContents, kiroTextContent{Text: "Tool use was cancelled by the user"})
				}
				
				status := "success"
				if isError {
					status = "error"
				}
				
				toolResults = append(toolResults, kiroToolResult{
					ToolUseID: toolUseID,
					Content:   textContents,
					Status:    status,
				})
			}
		}
	} else {
		contentBuilder.WriteString(content.String())
	}

	userMsg := kiroUserInputMessage{
		Content: contentBuilder.String(),
		ModelID: modelID,
		Origin:  origin,
	}

	// Add images to message if present
	if len(images) > 0 {
		userMsg.Images = images
	}

	return userMsg, toolResults
}

// buildAssistantMessageStruct builds an assistant message with tool uses
func (e *KiroExecutor) buildAssistantMessageStruct(msg gjson.Result) kiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []kiroToolUse

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_use":
				// Extract tool use for API
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				toolInput := part.Get("input")
				
				// Convert input to map
				var inputMap map[string]interface{}
				if toolInput.IsObject() {
					inputMap = make(map[string]interface{})
					toolInput.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}
				
				toolUses = append(toolUses, kiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
			}
		}
	} else {
		contentBuilder.WriteString(content.String())
	}

	return kiroAssistantResponseMessage{
		Content:  contentBuilder.String(),
		ToolUses: toolUses,
	}
}

// NOTE: Tool calling is now supported via userInputMessageContext.tools and toolResults

// parseEventStream parses AWS Event Stream binary format.
// Extracts text content and tool uses from the response.
// Supports embedded [Called ...] tool calls and input buffering for toolUseEvent.
func (e *KiroExecutor) parseEventStream(body io.Reader) (string, []kiroToolUse, usage.Detail, error) {
	var content strings.Builder
	var toolUses []kiroToolUse
	var usageInfo usage.Detail
	reader := bufio.NewReader(body)

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *toolUseState

	for {
		prelude := make([]byte, 8)
		_, err := io.ReadFull(reader, prelude)
		if err == io.EOF {
			break
		}
		if err != nil {
			return content.String(), toolUses, usageInfo, fmt.Errorf("failed to read prelude: %w", err)
		}

		totalLen := binary.BigEndian.Uint32(prelude[0:4])
		if totalLen < 8 {
			return content.String(), toolUses, usageInfo, fmt.Errorf("invalid message length: %d", totalLen)
		}
		if totalLen > kiroMaxMessageSize {
			return content.String(), toolUses, usageInfo, fmt.Errorf("message too large: %d bytes", totalLen)
		}
		headersLen := binary.BigEndian.Uint32(prelude[4:8])

		remaining := make([]byte, totalLen-8)
		_, err = io.ReadFull(reader, remaining)
		if err != nil {
			return content.String(), toolUses, usageInfo, fmt.Errorf("failed to read message: %w", err)
		}

		// Validate headersLen to prevent slice out of bounds
		if headersLen+4 > uint32(len(remaining)) {
			log.Warnf("kiro: invalid headersLen %d exceeds remaining buffer %d", headersLen, len(remaining))
			continue
		}

		// Extract event type from headers
		eventType := e.extractEventType(remaining[:headersLen+4])

		payloadStart := 4 + headersLen
		payloadEnd := uint32(len(remaining)) - 4
		if payloadStart >= payloadEnd {
			continue
		}

		payload := remaining[payloadStart:payloadEnd]

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Debugf("kiro: skipping malformed event: %v", err)
			continue
		}

		// DIAGNOSTIC: Log all received event types for debugging
		log.Debugf("kiro: parseEventStream received event type: %s", eventType)
		if log.IsLevelEnabled(log.TraceLevel) {
			log.Tracef("kiro: parseEventStream event payload: %s", string(payload))
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
			return "", nil, usageInfo, fmt.Errorf("kiro API error: %s - %s", errType, errMsg)
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
			return "", nil, usageInfo, fmt.Errorf("kiro API error: %s", errMsg)
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
				// Extract tool uses from response
				if toolUsesRaw, ok := assistantResp["toolUses"].([]interface{}); ok {
					for _, tuRaw := range toolUsesRaw {
						if tu, ok := tuRaw.(map[string]interface{}); ok {
							toolUseID := getString(tu, "toolUseId")
							// Check for duplicate
							if processedIDs[toolUseID] {
								log.Debugf("kiro: skipping duplicate tool use from assistantResponse: %s", toolUseID)
								continue
							}
							processedIDs[toolUseID] = true
							
							toolUse := kiroToolUse{
								ToolUseID: toolUseID,
								Name:      getString(tu, "name"),
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
						toolUseID := getString(tu, "toolUseId")
						// Check for duplicate
						if processedIDs[toolUseID] {
							log.Debugf("kiro: skipping duplicate direct tool use: %s", toolUseID)
							continue
						}
						processedIDs[toolUseID] = true
						
						toolUse := kiroToolUse{
							ToolUseID: toolUseID,
							Name:      getString(tu, "name"),
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
			completedToolUses, newState := e.processToolUseEvent(event, currentToolUse, processedIDs)
			currentToolUse = newState
			toolUses = append(toolUses, completedToolUses...)

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
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
	cleanedContent, embeddedToolUses := e.parseEmbeddedToolCalls(contentStr, processedIDs)
	toolUses = append(toolUses, embeddedToolUses...)

	// Deduplicate all tool uses
	toolUses = deduplicateToolUses(toolUses)

	return cleanedContent, toolUses, usageInfo, nil
}

// extractEventType extracts the event type from AWS Event Stream headers
func (e *KiroExecutor) extractEventType(headerBytes []byte) string {
	// Skip prelude CRC (4 bytes)
	if len(headerBytes) < 4 {
		return ""
	}
	headers := headerBytes[4:]

	offset := 0
	for offset < len(headers) {
		if offset >= len(headers) {
			break
		}
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
		} else {
			// Skip other types
			break
		}
	}
	return ""
}

// getString safely extracts a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// buildClaudeResponse constructs a Claude-compatible response.
// Supports tool_use blocks when tools are present in the response.
// Supports thinking blocks - parses <thinking> tags and converts to Claude thinking content blocks.
func (e *KiroExecutor) buildClaudeResponse(content string, toolUses []kiroToolUse, model string, usageInfo usage.Detail) []byte {
	var contentBlocks []map[string]interface{}

	// Extract thinking blocks and text from content
	// This handles <thinking>...</thinking> tags from Kiro's response
	if content != "" {
		blocks := e.extractThinkingFromContent(content)
		contentBlocks = append(contentBlocks, blocks...)
		
		// DIAGNOSTIC: Log if thinking blocks were extracted
		for _, block := range blocks {
			if block["type"] == "thinking" {
				thinkingContent := block["thinking"].(string)
				log.Infof("kiro: buildClaudeResponse extracted thinking block (len: %d)", len(thinkingContent))
			}
		}
	}

	// Add tool_use blocks
	for _, toolUse := range toolUses {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUse.ToolUseID,
			"name":  toolUse.Name,
			"input": toolUse.Input,
		})
	}

	// Ensure at least one content block (Claude API requires non-empty content)
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	// Determine stop reason
	stopReason := "end_turn"
	if len(toolUses) > 0 {
		stopReason = "tool_use"
	}

	response := map[string]interface{}{
		"id":          "msg_" + uuid.New().String()[:24],
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     contentBlocks,
		"stop_reason": stopReason,
		"usage": map[string]interface{}{
			"input_tokens":  usageInfo.InputTokens,
			"output_tokens": usageInfo.OutputTokens,
		},
	}
	result, _ := json.Marshal(response)
	return result
}

// extractThinkingFromContent parses content to extract thinking blocks and text.
// Returns a list of content blocks in the order they appear in the content.
// Handles interleaved thinking and text blocks correctly.
// Based on the streaming implementation's thinking tag handling.
func (e *KiroExecutor) extractThinkingFromContent(content string) []map[string]interface{} {
	var blocks []map[string]interface{}
	
	if content == "" {
		return blocks
	}
	
	// Check if content contains thinking tags at all
	if !strings.Contains(content, thinkingStartTag) {
		// No thinking tags, return as plain text
		return []map[string]interface{}{
			{
				"type": "text",
				"text": content,
			},
		}
	}
	
	log.Debugf("kiro: extractThinkingFromContent - found thinking tags in content (len: %d)", len(content))
	
	remaining := content
	
	for len(remaining) > 0 {
		// Look for <thinking> tag
		startIdx := strings.Index(remaining, thinkingStartTag)
		
		if startIdx == -1 {
			// No more thinking tags, add remaining as text
			if strings.TrimSpace(remaining) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": remaining,
				})
			}
			break
		}
		
		// Add text before thinking tag (if any meaningful content)
		if startIdx > 0 {
			textBefore := remaining[:startIdx]
			if strings.TrimSpace(textBefore) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": textBefore,
				})
			}
		}
		
		// Move past the opening tag
		remaining = remaining[startIdx+len(thinkingStartTag):]
		
		// Find closing tag
		endIdx := strings.Index(remaining, thinkingEndTag)
		
		if endIdx == -1 {
			// No closing tag found, treat rest as thinking content (incomplete response)
			if strings.TrimSpace(remaining) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type":     "thinking",
					"thinking": remaining,
				})
				log.Warnf("kiro: extractThinkingFromContent - missing closing </thinking> tag")
			}
			break
		}
		
		// Extract thinking content between tags
		thinkContent := remaining[:endIdx]
		if strings.TrimSpace(thinkContent) != "" {
			blocks = append(blocks, map[string]interface{}{
				"type":     "thinking",
				"thinking": thinkContent,
			})
			log.Debugf("kiro: extractThinkingFromContent - extracted thinking block (len: %d)", len(thinkContent))
		}
		
		// Move past the closing tag
		remaining = remaining[endIdx+len(thinkingEndTag):]
	}
	
	// If no blocks were created (all whitespace), return empty text block
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}
	
	return blocks
}

// NOTE: Tool uses are now extracted from API response, not parsed from text


// streamToChannel converts AWS Event Stream to channel-based streaming.
// Supports tool calling - emits tool_use content blocks when tools are used.
// Includes embedded [Called ...] tool call parsing and input buffering for toolUseEvent.
// Implements duplicate content filtering using lastContentEvent detection (based on AIClient-2-API).
func (e *KiroExecutor) streamToChannel(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, targetFormat sdktranslator.Format, model string, originalReq, claudeBody []byte, reporter *usageReporter) {
	reader := bufio.NewReaderSize(body, 20*1024*1024) // 20MB buffer to match other providers
	var totalUsage usage.Detail
	var hasToolUses bool // Track if any tool uses were emitted

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *toolUseState

	// NOTE: Duplicate content filtering removed - it was causing legitimate repeated
	// content (like consecutive newlines) to be incorrectly filtered out.
	// The previous implementation compared lastContentEvent == contentDelta which
	// is too aggressive for streaming scenarios.

	// Streaming token calculation - accumulate content for real-time token counting
	// Based on AIClient-2-API implementation
	var accumulatedContent strings.Builder
	accumulatedContent.Grow(4096) // Pre-allocate 4KB capacity to reduce reallocations

	// Translator param for maintaining tool call state across streaming events
	// IMPORTANT: This must persist across all TranslateStream calls
	var translatorParam any

	// Thinking mode state tracking - based on amq2api implementation
	// Tracks whether we're inside a <thinking> block and handles partial tags
	inThinkBlock := false
	pendingStartTagChars := 0                 // Number of chars that might be start of <thinking>
	pendingEndTagChars := 0                   // Number of chars that might be start of </thinking>
	isThinkingBlockOpen := false              // Track if thinking content block is open
	thinkingBlockIndex := -1                  // Index of the thinking content block

	// Pre-calculate input tokens from request if possible
	if enc, err := tokenizerForModel(model); err == nil {
		// Try OpenAI format first, then fall back to raw byte count estimation
		if inp, err := countOpenAIChatTokens(enc, originalReq); err == nil && inp > 0 {
			totalUsage.InputTokens = inp
		} else {
			// Fallback: estimate from raw request size (roughly 4 chars per token)
			totalUsage.InputTokens = int64(len(originalReq) / 4)
			if totalUsage.InputTokens == 0 && len(originalReq) > 0 {
				totalUsage.InputTokens = 1
			}
		}
		log.Debugf("kiro: streamToChannel pre-calculated input tokens: %d (request size: %d bytes)", totalUsage.InputTokens, len(originalReq))
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

		prelude := make([]byte, 8)
		_, err := io.ReadFull(reader, prelude)
		if err == io.EOF {
			// Flush any incomplete tool use before ending stream
			if currentToolUse != nil && !processedIDs[currentToolUse.toolUseID] {
				log.Warnf("kiro: flushing incomplete tool use at EOF: %s (ID: %s)", currentToolUse.name, currentToolUse.toolUseID)
				fullInput := currentToolUse.inputBuffer.String()
				repairedJSON := repairJSON(fullInput)
				var finalInput map[string]interface{}
				if err := json.Unmarshal([]byte(repairedJSON), &finalInput); err != nil {
					log.Warnf("kiro: failed to parse incomplete tool input at EOF: %v", err)
					finalInput = make(map[string]interface{})
				}
				
				processedIDs[currentToolUse.toolUseID] = true
				contentBlockIndex++
				
				// Send tool_use content block
				blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", currentToolUse.toolUseID, currentToolUse.name)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
				
				// Send tool input as delta
				inputBytes, _ := json.Marshal(finalInput)
				inputDelta := e.buildClaudeInputJsonDeltaEvent(string(inputBytes), contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
				
				// Close block
				blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
				
				hasToolUses = true
				currentToolUse = nil
			}
			
			// Flush any pending tag characters at EOF
			// These are partial tag prefixes that were held back waiting for more data
			// Since no more data is coming, output them as regular text
			var pendingText string
			if pendingStartTagChars > 0 {
				pendingText = thinkingStartTag[:pendingStartTagChars]
				log.Debugf("kiro: flushing pending start tag chars at EOF: %q", pendingText)
				pendingStartTagChars = 0
			}
			if pendingEndTagChars > 0 {
				pendingText += thinkingEndTag[:pendingEndTagChars]
				log.Debugf("kiro: flushing pending end tag chars at EOF: %q", pendingText)
				pendingEndTagChars = 0
			}
			
			// Output pending text if any
			if pendingText != "" {
				// If we're in a thinking block, output as thinking content
				if inThinkBlock && isThinkingBlockOpen {
					thinkingEvent := e.buildClaudeThinkingDeltaEvent(pendingText, thinkingBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
				} else {
					// Output as regular text
					if !isTextBlockOpen {
						contentBlockIndex++
						isTextBlockOpen = true
						blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
						sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}
					
					claudeEvent := e.buildClaudeStreamEvent(pendingText, contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
				}
			}
			break
		}
		if err != nil {
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("failed to read prelude: %w", err)}
			return
		}

		totalLen := binary.BigEndian.Uint32(prelude[0:4])
		if totalLen < 8 {
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("invalid message length: %d", totalLen)}
			return
		}
		if totalLen > kiroMaxMessageSize {
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("message too large: %d bytes", totalLen)}
			return
		}
		headersLen := binary.BigEndian.Uint32(prelude[4:8])

		remaining := make([]byte, totalLen-8)
		_, err = io.ReadFull(reader, remaining)
		if err != nil {
			out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("failed to read message: %w", err)}
			return
		}

		// Validate headersLen to prevent slice out of bounds
		if headersLen+4 > uint32(len(remaining)) {
			log.Warnf("kiro: invalid headersLen %d exceeds remaining buffer %d", headersLen, len(remaining))
			continue
		}

		eventType := e.extractEventType(remaining[:headersLen+4])

		payloadStart := 4 + headersLen
		payloadEnd := uint32(len(remaining)) - 4
		if payloadStart >= payloadEnd {
			continue
		}

		payload := remaining[payloadStart:payloadEnd]
		appendAPIResponseChunk(ctx, e.cfg, payload)

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warnf("kiro: failed to unmarshal event payload: %v, raw: %s", err, string(payload))
			continue
		}

		// DIAGNOSTIC: Log all received event types for debugging
		log.Debugf("kiro: streamToChannel received event type: %s", eventType)
		if log.IsLevelEnabled(log.TraceLevel) {
			log.Tracef("kiro: streamToChannel event payload: %s", string(payload))
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

		// Send message_start on first event
		if !messageStartSent {
			msgStart := e.buildClaudeMessageStartEvent(model, totalUsage.InputTokens)
			sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStart, &translatorParam)
			for _, chunk := range sseData {
				if chunk != "" {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
				}
			}
			messageStartSent = true
		}

		switch eventType {
		case "followupPromptEvent":
			// Filter out followupPrompt events - these are UI suggestions, not content
			log.Debugf("kiro: streamToChannel ignoring followupPrompt event")
			continue

		case "assistantResponseEvent":
			var contentDelta string
			var toolUses []map[string]interface{}
			
			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if c, ok := assistantResp["content"].(string); ok {
					contentDelta = c
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
				// DIAGNOSTIC: Check for thinking tags in response
				if strings.Contains(contentDelta, "<thinking>") || strings.Contains(contentDelta, "</thinking>") {
					log.Infof("kiro: DIAGNOSTIC - Found thinking tag in response (len: %d)", len(contentDelta))
				}

				// NOTE: Duplicate content filtering was removed because it incorrectly
				// filtered out legitimate repeated content (like consecutive newlines "\n\n").
				// Streaming naturally can have identical chunks that are valid content.

				outputLen += len(contentDelta)
				// Accumulate content for streaming token calculation
				accumulatedContent.WriteString(contentDelta)

				// Process content with thinking tag detection - based on amq2api implementation
				// This handles <thinking> and </thinking> tags that may span across chunks
				remaining := contentDelta

				// If we have pending start tag chars from previous chunk, prepend them
				if pendingStartTagChars > 0 {
					remaining = thinkingStartTag[:pendingStartTagChars] + remaining
					pendingStartTagChars = 0
				}
				
				// If we have pending end tag chars from previous chunk, prepend them
				if pendingEndTagChars > 0 {
					remaining = thinkingEndTag[:pendingEndTagChars] + remaining
					pendingEndTagChars = 0
				}

				for len(remaining) > 0 {
					if inThinkBlock {
						// Inside thinking block - look for </thinking> end tag
						endIdx := strings.Index(remaining, thinkingEndTag)
						if endIdx >= 0 {
							// Found end tag - emit any content before end tag, then close block
							thinkContent := remaining[:endIdx]
							if thinkContent != "" {
								// TRUE STREAMING: Emit thinking content immediately
								// Start thinking block if not open
								if !isThinkingBlockOpen {
									contentBlockIndex++
									thinkingBlockIndex = contentBlockIndex
									isThinkingBlockOpen = true
									blockStart := e.buildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								
								// Send thinking delta immediately
								thinkingEvent := e.buildClaudeThinkingDeltaEvent(thinkContent, thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}

							// Note: Partial tag handling is done via pendingEndTagChars
							// When the next chunk arrives, the partial tag will be reconstructed

							// Close thinking block
							if isThinkingBlockOpen {
								blockStop := e.buildClaudeContentBlockStopEvent(thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
								isThinkingBlockOpen = false
							}

							inThinkBlock = false
							remaining = remaining[endIdx+len(thinkingEndTag):]
							log.Debugf("kiro: exited thinking block")
						} else {
							// No end tag found - TRUE STREAMING: emit content immediately
							// Only save potential partial tag length for next iteration
							pendingEnd := pendingTagSuffix(remaining, thinkingEndTag)
							
							// Calculate content to emit immediately (excluding potential partial tag)
							var contentToEmit string
							if pendingEnd > 0 {
								contentToEmit = remaining[:len(remaining)-pendingEnd]
								// Save partial tag length for next iteration (will be reconstructed from thinkingEndTag)
								pendingEndTagChars = pendingEnd
							} else {
								contentToEmit = remaining
							}
							
							// TRUE STREAMING: Emit thinking content immediately
							if contentToEmit != "" {
								// Start thinking block if not open
								if !isThinkingBlockOpen {
									contentBlockIndex++
									thinkingBlockIndex = contentBlockIndex
									isThinkingBlockOpen = true
									blockStart := e.buildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								
								// Send thinking delta immediately - TRUE STREAMING!
								thinkingEvent := e.buildClaudeThinkingDeltaEvent(contentToEmit, thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}
							
							remaining = ""
						}
					} else {
						// Outside thinking block - look for <thinking> start tag
						startIdx := strings.Index(remaining, thinkingStartTag)
						if startIdx >= 0 {
							// Found start tag - emit text before it and switch to thinking mode
							textBefore := remaining[:startIdx]
							if textBefore != "" {
								// Start text content block if needed
								if !isTextBlockOpen {
									contentBlockIndex++
									isTextBlockOpen = true
									blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}

								claudeEvent := e.buildClaudeStreamEvent(textBefore, contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}

							// Close text block before starting thinking block
							if isTextBlockOpen {
								blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
								isTextBlockOpen = false
							}

							inThinkBlock = true
							remaining = remaining[startIdx+len(thinkingStartTag):]
							log.Debugf("kiro: entered thinking block")
						} else {
							// No start tag found - check for partial start tag at buffer end
							pendingStart := pendingTagSuffix(remaining, thinkingStartTag)
							if pendingStart > 0 {
								// Emit text except potential partial tag
								textToEmit := remaining[:len(remaining)-pendingStart]
								if textToEmit != "" {
									// Start text content block if needed
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}

									claudeEvent := e.buildClaudeStreamEvent(textToEmit, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								pendingStartTagChars = pendingStart
								remaining = ""
							} else {
								// No partial tag - emit all as text
								if remaining != "" {
									// Start text content block if needed
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}

									claudeEvent := e.buildClaudeStreamEvent(remaining, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								remaining = ""
							}
						}
					}
				}
			}
			
			// Handle tool uses in response (with deduplication)
			for _, tu := range toolUses {
				toolUseID := getString(tu, "toolUseId")
				
				// Check for duplicate
				if processedIDs[toolUseID] {
					log.Debugf("kiro: skipping duplicate tool use in stream: %s", toolUseID)
					continue
				}
				processedIDs[toolUseID] = true
				
				hasToolUses = true
				// Close text block if open before starting tool_use block
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
					isTextBlockOpen = false
				}
				
				// Emit tool_use content block
				contentBlockIndex++
				toolName := getString(tu, "name")
				
				blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", toolUseID, toolName)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
				
				// Send input_json_delta with the tool input
				if input, ok := tu["input"].(map[string]interface{}); ok {
					inputJSON, err := json.Marshal(input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input: %v", err)
						// Don't continue - still need to close the block
					} else {
						inputDelta := e.buildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}
				}
				
				// Close tool_use block (always close even if input marshal failed)
				blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
			}

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			completedToolUses, newState := e.processToolUseEvent(event, currentToolUse, processedIDs)
			currentToolUse = newState
			
			// Emit completed tool uses
			for _, tu := range completedToolUses {
				hasToolUses = true
				
				// Close text block if open
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
					isTextBlockOpen = false
				}
				
				contentBlockIndex++
				
				blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", tu.ToolUseID, tu.Name)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
				
				if tu.Input != nil {
					inputJSON, err := json.Marshal(tu.Input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input in toolUseEvent: %v", err)
					} else {
						inputDelta := e.buildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}
				}
				
				blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
			}

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
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
	}

	// Close content block if open
	if isTextBlockOpen && contentBlockIndex >= 0 {
		blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
		sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
		for _, chunk := range sseData {
			if chunk != "" {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
			}
		}
	}

	// Streaming token calculation - calculate output tokens from accumulated content
	// This provides more accurate token counting than simple character division
	if totalUsage.OutputTokens == 0 && accumulatedContent.Len() > 0 {
		// Try to use tiktoken for accurate counting
		if enc, err := tokenizerForModel(model); err == nil {
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
	totalUsage.TotalTokens = totalUsage.InputTokens + totalUsage.OutputTokens

	// Determine stop reason based on whether tool uses were emitted
	stopReason := "end_turn"
	if hasToolUses {
		stopReason = "tool_use"
	}

	// Send message_delta event
	msgDelta := e.buildClaudeMessageDeltaEvent(stopReason, totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgDelta, &translatorParam)
	for _, chunk := range sseData {
		if chunk != "" {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
		}
	}

	// Send message_stop event separately
	msgStop := e.buildClaudeMessageStopOnlyEvent()
	sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
	for _, chunk := range sseData {
		if chunk != "" {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
		}
	}
	// reporter.publish is called via defer
}


// Claude SSE event builders
// All builders return complete SSE format with "event:" line for Claude client compatibility.
func (e *KiroExecutor) buildClaudeMessageStartEvent(model string, inputTokens int64) []byte {
	event := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_" + uuid.New().String()[:24],
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": inputTokens, "output_tokens": 0},
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: message_start\ndata: " + string(result))
}

func (e *KiroExecutor) buildClaudeContentBlockStartEvent(index int, blockType, toolUseID, toolName string) []byte {
	var contentBlock map[string]interface{}
	switch blockType {
	case "tool_use":
		contentBlock = map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  toolName,
			"input": map[string]interface{}{},
		}
	case "thinking":
		contentBlock = map[string]interface{}{
			"type":     "thinking",
			"thinking": "",
		}
	default:
		contentBlock = map[string]interface{}{
			"type": "text",
			"text": "",
		}
	}

	event := map[string]interface{}{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_start\ndata: " + string(result))
}

func (e *KiroExecutor) buildClaudeStreamEvent(contentDelta string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": contentDelta,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

// buildClaudeInputJsonDeltaEvent creates an input_json_delta event for tool use streaming
func (e *KiroExecutor) buildClaudeInputJsonDeltaEvent(partialJSON string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": partialJSON,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

func (e *KiroExecutor) buildClaudeContentBlockStopEvent(index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_stop\ndata: " + string(result))
}

// buildClaudeMessageDeltaEvent creates the message_delta event with stop_reason and usage.
func (e *KiroExecutor) buildClaudeMessageDeltaEvent(stopReason string, usageInfo usage.Detail) []byte {
	deltaEvent := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"input_tokens":  usageInfo.InputTokens,
			"output_tokens": usageInfo.OutputTokens,
		},
	}
	deltaResult, _ := json.Marshal(deltaEvent)
	return []byte("event: message_delta\ndata: " + string(deltaResult))
}

// buildClaudeMessageStopOnlyEvent creates only the message_stop event.
func (e *KiroExecutor) buildClaudeMessageStopOnlyEvent() []byte {
	stopEvent := map[string]interface{}{
		"type": "message_stop",
	}
	stopResult, _ := json.Marshal(stopEvent)
	return []byte("event: message_stop\ndata: " + string(stopResult))
}

// buildClaudeFinalEvent constructs the final Claude-style event.
func (e *KiroExecutor) buildClaudeFinalEvent() []byte {
	event := map[string]interface{}{
		"type": "message_stop",
	}
	result, _ := json.Marshal(event)
	return []byte("event: message_stop\ndata: " + string(result))
}

// buildClaudeThinkingDeltaEvent creates a thinking_delta event for Claude API compatibility.
// This is used when streaming thinking content wrapped in <thinking> tags.
func (e *KiroExecutor) buildClaudeThinkingDeltaEvent(thinkingDelta string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":     "thinking_delta",
			"thinking": thinkingDelta,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

// pendingTagSuffix detects if the buffer ends with a partial prefix of the given tag.
// Returns the length of the partial match (0 if no match).
// Based on amq2api implementation for handling cross-chunk tag boundaries.
func pendingTagSuffix(buffer, tag string) int {
	if buffer == "" || tag == "" {
		return 0
	}
	maxLen := len(buffer)
	if maxLen > len(tag)-1 {
		maxLen = len(tag) - 1
	}
	for length := maxLen; length > 0; length-- {
		if len(buffer) >= length && buffer[len(buffer)-length:] == tag[:length] {
			return length
		}
	}
	return 0
}

// CountTokens is not supported for Kiro provider.
// Kiro/Amazon Q backend doesn't expose a token counting API.
func (e *KiroExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for kiro"}
}

// Refresh refreshes the Kiro OAuth token.
// Supports both AWS Builder ID (SSO OIDC) and Google OAuth (social login).
// Uses mutex to prevent race conditions when multiple concurrent requests try to refresh.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	// Serialize token refresh operations to prevent race conditions
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	var authID string
	if auth != nil {
		authID = auth.ID
	} else {
		authID = "<nil>"
	}
	log.Debugf("kiro executor: refresh called for auth %s", authID)
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}

	// Double-check: After acquiring lock, verify token still needs refresh
	// Another goroutine may have already refreshed while we were waiting
	// NOTE: This check has a design limitation - it reads from the auth object passed in,
	// not from persistent storage. If another goroutine returns a new Auth object (via Clone),
	// this check won't see those updates. The mutex still prevents truly concurrent refreshes,
	// but queued goroutines may still attempt redundant refreshes. This is acceptable as
	// the refresh operation is idempotent and the extra API calls are infrequent.
	if auth.Metadata != nil {
		if lastRefresh, ok := auth.Metadata["last_refresh"].(string); ok {
			if refreshTime, err := time.Parse(time.RFC3339, lastRefresh); err == nil {
				// If token was refreshed within the last 30 seconds, skip refresh
				if time.Since(refreshTime) < 30*time.Second {
					log.Debugf("kiro executor: token was recently refreshed by another goroutine, skipping")
					return auth, nil
				}
			}
		}
		// Also check if expires_at is now in the future with sufficient buffer
		if expiresAt, ok := auth.Metadata["expires_at"].(string); ok {
			if expTime, err := time.Parse(time.RFC3339, expiresAt); err == nil {
				// If token expires more than 2 minutes from now, it's still valid
				if time.Until(expTime) > 2*time.Minute {
					log.Debugf("kiro executor: token is still valid (expires in %v), skipping refresh", time.Until(expTime))
					return auth, nil
				}
			}
		}
	}

	var refreshToken string
	var clientID, clientSecret string
	var authMethod string

	if auth.Metadata != nil {
		if rt, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = rt
		}
		if cid, ok := auth.Metadata["client_id"].(string); ok {
			clientID = cid
		}
		if cs, ok := auth.Metadata["client_secret"].(string); ok {
			clientSecret = cs
		}
		if am, ok := auth.Metadata["auth_method"].(string); ok {
			authMethod = am
		}
	}

	if refreshToken == "" {
		return nil, fmt.Errorf("kiro executor: refresh token not found")
	}

	var tokenData *kiroauth.KiroTokenData
	var err error

	// Use SSO OIDC refresh for AWS Builder ID, otherwise use Kiro's OAuth refresh endpoint
	if clientID != "" && clientSecret != "" && authMethod == "builder-id" {
		log.Debugf("kiro executor: using SSO OIDC refresh for AWS Builder ID")
		ssoClient := kiroauth.NewSSOOIDCClient(e.cfg)
		tokenData, err = ssoClient.RefreshToken(ctx, clientID, clientSecret, refreshToken)
	} else {
		log.Debugf("kiro executor: using Kiro OAuth refresh endpoint")
		oauth := kiroauth.NewKiroOAuth(e.cfg)
		tokenData, err = oauth.RefreshToken(ctx, refreshToken)
	}

	if err != nil {
		return nil, fmt.Errorf("kiro executor: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = tokenData.AccessToken
	updated.Metadata["refresh_token"] = tokenData.RefreshToken
	updated.Metadata["expires_at"] = tokenData.ExpiresAt
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	if tokenData.ProfileArn != "" {
		updated.Metadata["profile_arn"] = tokenData.ProfileArn
	}
	if tokenData.AuthMethod != "" {
		updated.Metadata["auth_method"] = tokenData.AuthMethod
	}
	if tokenData.Provider != "" {
		updated.Metadata["provider"] = tokenData.Provider
	}
	// Preserve client credentials for future refreshes (AWS Builder ID)
	if tokenData.ClientID != "" {
		updated.Metadata["client_id"] = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		updated.Metadata["client_secret"] = tokenData.ClientSecret
	}

	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["access_token"] = tokenData.AccessToken
	if tokenData.ProfileArn != "" {
		updated.Attributes["profile_arn"] = tokenData.ProfileArn
	}

	// Set next refresh time to 30 minutes before expiry
	if expiresAt, parseErr := time.Parse(time.RFC3339, tokenData.ExpiresAt); parseErr == nil {
		updated.NextRefreshAfter = expiresAt.Add(-30 * time.Minute)
	}

	log.Infof("kiro executor: token refreshed successfully, expires at %s", tokenData.ExpiresAt)
	return updated, nil
}

// streamEventStream converts AWS Event Stream to SSE (legacy method for gin.Context).
// Note: For full tool calling support, use streamToChannel instead.
func (e *KiroExecutor) streamEventStream(ctx context.Context, body io.Reader, c *gin.Context, targetFormat sdktranslator.Format, model string, originalReq, claudeBody []byte, reporter *usageReporter) error {
	reader := bufio.NewReader(body)
	var totalUsage usage.Detail

	// Translator param for maintaining tool call state across streaming events
	var translatorParam any

	// Pre-calculate input tokens from request if possible
	if enc, err := tokenizerForModel(model); err == nil {
		// Try OpenAI format first, then fall back to raw byte count estimation
		if inp, err := countOpenAIChatTokens(enc, originalReq); err == nil && inp > 0 {
			totalUsage.InputTokens = inp
		} else {
			// Fallback: estimate from raw request size (roughly 4 chars per token)
			totalUsage.InputTokens = int64(len(originalReq) / 4)
			if totalUsage.InputTokens == 0 && len(originalReq) > 0 {
				totalUsage.InputTokens = 1
			}
		}
		log.Debugf("kiro: streamEventStream pre-calculated input tokens: %d (request size: %d bytes)", totalUsage.InputTokens, len(originalReq))
	}

	contentBlockIndex := -1
	messageStartSent := false
	isBlockOpen := false
	var outputLen int

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		prelude := make([]byte, 8)
		_, err := io.ReadFull(reader, prelude)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read prelude: %w", err)
		}

		totalLen := binary.BigEndian.Uint32(prelude[0:4])
		if totalLen < 8 {
			return fmt.Errorf("invalid message length: %d", totalLen)
		}
		if totalLen > kiroMaxMessageSize {
			return fmt.Errorf("message too large: %d bytes", totalLen)
		}
		headersLen := binary.BigEndian.Uint32(prelude[4:8])

		remaining := make([]byte, totalLen-8)
		_, err = io.ReadFull(reader, remaining)
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}

		// Validate headersLen to prevent slice out of bounds
		if headersLen+4 > uint32(len(remaining)) {
			log.Warnf("kiro: invalid headersLen %d exceeds remaining buffer %d", headersLen, len(remaining))
			continue
		}

		eventType := e.extractEventType(remaining[:headersLen+4])

		payloadStart := 4 + headersLen
		payloadEnd := uint32(len(remaining)) - 4
		if payloadStart >= payloadEnd {
			continue
		}

		payload := remaining[payloadStart:payloadEnd]
		appendAPIResponseChunk(ctx, e.cfg, payload)

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warnf("kiro: failed to unmarshal event payload: %v, raw: %s", err, string(payload))
			continue
		}

		if !messageStartSent {
			msgStart := e.buildClaudeMessageStartEvent(model, totalUsage.InputTokens)
			sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStart, &translatorParam)
			for _, chunk := range sseData {
				if chunk != "" {
					c.Writer.Write([]byte(chunk + "\n\n"))
				}
			}
			c.Writer.Flush()
			messageStartSent = true
		}

		switch eventType {
		case "assistantResponseEvent":
			var contentDelta string
			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if ct, ok := assistantResp["content"].(string); ok {
					contentDelta = ct
				}
			}
			if contentDelta == "" {
				if ct, ok := event["content"].(string); ok {
					contentDelta = ct
				}
			}

			if contentDelta != "" {
				outputLen += len(contentDelta)
				// Start text content block if needed
				if !isBlockOpen {
					contentBlockIndex++
					isBlockOpen = true
					blockStart := e.buildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							c.Writer.Write([]byte(chunk + "\n\n"))
						}
					}
					c.Writer.Flush()
				}

				claudeEvent := e.buildClaudeStreamEvent(contentDelta, contentBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						c.Writer.Write([]byte(chunk + "\n\n"))
					}
				}
				c.Writer.Flush()
			}

		// Note: For full toolUseEvent support, use streamToChannel

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}
		}

		if usageEvent, ok := event["supplementaryWebLinksEvent"].(map[string]interface{}); ok {
			if inputTokens, ok := usageEvent["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := usageEvent["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}
		}
	}

	// Close content block if open
	if isBlockOpen && contentBlockIndex >= 0 {
		blockStop := e.buildClaudeContentBlockStopEvent(contentBlockIndex)
		sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
		for _, chunk := range sseData {
			if chunk != "" {
				c.Writer.Write([]byte(chunk + "\n\n"))
			}
		}
		c.Writer.Flush()
	}

	// Fallback for output tokens if not received from upstream
	if totalUsage.OutputTokens == 0 && outputLen > 0 {
		totalUsage.OutputTokens = int64(outputLen / 4)
		if totalUsage.OutputTokens == 0 {
			totalUsage.OutputTokens = 1
		}
	}
	totalUsage.TotalTokens = totalUsage.InputTokens + totalUsage.OutputTokens

	// Send message_delta event
	msgDelta := e.buildClaudeMessageDeltaEvent("end_turn", totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgDelta, &translatorParam)
	for _, chunk := range sseData {
		if chunk != "" {
			c.Writer.Write([]byte(chunk + "\n\n"))
		}
	}
	c.Writer.Flush()

	// Send message_stop event separately
	msgStop := e.buildClaudeMessageStopOnlyEvent()
	sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
	for _, chunk := range sseData {
		if chunk != "" {
			c.Writer.Write([]byte(chunk + "\n\n"))
		}
	}

	c.Writer.Write([]byte("data: [DONE]\n\n"))
	c.Writer.Flush()

	reporter.publish(ctx, totalUsage)
	return nil
}

// isTokenExpired checks if a JWT access token has expired.
// Returns true if the token is expired or cannot be parsed.
func (e *KiroExecutor) isTokenExpired(accessToken string) bool {
	if accessToken == "" {
		return true
	}

	// JWT tokens have 3 parts separated by dots
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		// Not a JWT token, assume not expired
		return false
	}

	// Decode the payload (second part)
	// JWT uses base64url encoding without padding (RawURLEncoding)
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Try with padding added as fallback
		switch len(payload) % 4 {
		case 2:
			payload += "=="
		case 3:
			payload += "="
		}
		decoded, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			log.Debugf("kiro: failed to decode JWT payload: %v", err)
			return false
		}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		log.Debugf("kiro: failed to parse JWT claims: %v", err)
		return false
	}

	if claims.Exp == 0 {
		// No expiration claim, assume not expired
		return false
	}

	expTime := time.Unix(claims.Exp, 0)
	now := time.Now()

	// Consider token expired if it expires within 1 minute (buffer for clock skew)
	isExpired := now.After(expTime) || expTime.Sub(now) < time.Minute
	if isExpired {
		log.Debugf("kiro: token expired at %s (now: %s)", expTime.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	return isExpired
}

// ============================================================================
// Message Merging Support - Merge adjacent messages with the same role
// Based on AIClient-2-API implementation
// ============================================================================

// mergeAdjacentMessages merges adjacent messages with the same role.
// This reduces API call complexity and improves compatibility.
// Based on AIClient-2-API implementation.
func mergeAdjacentMessages(messages []gjson.Result) []gjson.Result {
	if len(messages) <= 1 {
		return messages
	}

	var merged []gjson.Result
	for _, msg := range messages {
		if len(merged) == 0 {
			merged = append(merged, msg)
			continue
		}

		lastMsg := merged[len(merged)-1]
		currentRole := msg.Get("role").String()
		lastRole := lastMsg.Get("role").String()

		if currentRole == lastRole {
			// Merge content from current message into last message
			mergedContent := mergeMessageContent(lastMsg, msg)
			// Create a new merged message JSON
			mergedMsg := createMergedMessage(lastRole, mergedContent)
			merged[len(merged)-1] = gjson.Parse(mergedMsg)
		} else {
			merged = append(merged, msg)
		}
	}

	return merged
}

// mergeMessageContent merges the content of two messages with the same role.
// Handles both string content and array content (with text, tool_use, tool_result blocks).
func mergeMessageContent(msg1, msg2 gjson.Result) string {
	content1 := msg1.Get("content")
	content2 := msg2.Get("content")

	// Extract content blocks from both messages
	var blocks1, blocks2 []map[string]interface{}

	if content1.IsArray() {
		for _, block := range content1.Array() {
			blocks1 = append(blocks1, blockToMap(block))
		}
	} else if content1.Type == gjson.String {
		blocks1 = append(blocks1, map[string]interface{}{
			"type": "text",
			"text": content1.String(),
		})
	}

	if content2.IsArray() {
		for _, block := range content2.Array() {
			blocks2 = append(blocks2, blockToMap(block))
		}
	} else if content2.Type == gjson.String {
		blocks2 = append(blocks2, map[string]interface{}{
			"type": "text",
			"text": content2.String(),
		})
	}

	// Merge text blocks if both end/start with text
	if len(blocks1) > 0 && len(blocks2) > 0 {
		if blocks1[len(blocks1)-1]["type"] == "text" && blocks2[0]["type"] == "text" {
			// Merge the last text block of msg1 with the first text block of msg2
			text1 := blocks1[len(blocks1)-1]["text"].(string)
			text2 := blocks2[0]["text"].(string)
			blocks1[len(blocks1)-1]["text"] = text1 + "\n" + text2
			blocks2 = blocks2[1:] // Remove the merged block from blocks2
		}
	}

	// Combine all blocks
	allBlocks := append(blocks1, blocks2...)

	// Convert to JSON
	result, _ := json.Marshal(allBlocks)
	return string(result)
}

// blockToMap converts a gjson.Result block to a map[string]interface{}
func blockToMap(block gjson.Result) map[string]interface{} {
	result := make(map[string]interface{})
	block.ForEach(func(key, value gjson.Result) bool {
		if value.IsObject() {
			result[key.String()] = blockToMap(value)
		} else if value.IsArray() {
			var arr []interface{}
			for _, item := range value.Array() {
				if item.IsObject() {
					arr = append(arr, blockToMap(item))
				} else {
					arr = append(arr, item.Value())
				}
			}
			result[key.String()] = arr
		} else {
			result[key.String()] = value.Value()
		}
		return true
	})
	return result
}

// createMergedMessage creates a JSON string for a merged message
func createMergedMessage(role string, content string) string {
	msg := map[string]interface{}{
		"role":    role,
		"content": json.RawMessage(content),
	}
	result, _ := json.Marshal(msg)
	return string(result)
}

// ============================================================================
// Tool Calling Support - Embedded tool call parsing and input buffering
// Based on amq2api and AIClient-2-API implementations
// ============================================================================

// toolUseState tracks the state of an in-progress tool use during streaming.
type toolUseState struct {
	toolUseID   string
	name        string
	inputBuffer strings.Builder
	isComplete  bool
}

// Pre-compiled regex patterns for performance (avoid recompilation on each call)
var (
	// embeddedToolCallPattern matches [Called tool_name with args: {...}] format
	// This pattern is used by Kiro when it embeds tool calls in text content
	embeddedToolCallPattern = regexp.MustCompile(`\[Called\s+(\w+)\s+with\s+args:\s*`)
	// whitespaceCollapsePattern collapses multiple whitespace characters into single space
	whitespaceCollapsePattern = regexp.MustCompile(`\s+`)
	// trailingCommaPattern matches trailing commas before closing braces/brackets
	trailingCommaPattern = regexp.MustCompile(`,\s*([}\]])`)
)

// parseEmbeddedToolCalls extracts [Called tool_name with args: {...}] format from text.
// Kiro sometimes embeds tool calls in text content instead of using toolUseEvent.
// Returns the cleaned text (with tool calls removed) and extracted tool uses.
func (e *KiroExecutor) parseEmbeddedToolCalls(text string, processedIDs map[string]bool) (string, []kiroToolUse) {
	if !strings.Contains(text, "[Called") {
		return text, nil
	}

	var toolUses []kiroToolUse
	cleanText := text

	// Find all [Called markers
	matches := embeddedToolCallPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	// Process matches in reverse order to maintain correct indices
	for i := len(matches) - 1; i >= 0; i-- {
		matchStart := matches[i][0]
		toolNameStart := matches[i][2]
		toolNameEnd := matches[i][3]

		if toolNameStart < 0 || toolNameEnd < 0 {
			continue
		}

		toolName := text[toolNameStart:toolNameEnd]

		// Find the JSON object start (after "with args:")
		jsonStart := matches[i][1]
		if jsonStart >= len(text) {
			continue
		}

		// Skip whitespace to find the opening brace
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}

		if jsonStart >= len(text) || text[jsonStart] != '{' {
			continue
		}

		// Find matching closing bracket
		jsonEnd := findMatchingBracket(text, jsonStart)
		if jsonEnd < 0 {
			continue
		}

		// Extract JSON and find the closing bracket of [Called ...]
		jsonStr := text[jsonStart : jsonEnd+1]
		
		// Find the closing ] after the JSON
		closingBracket := jsonEnd + 1
		for closingBracket < len(text) && text[closingBracket] != ']' {
			closingBracket++
		}
		if closingBracket >= len(text) {
			continue
		}

		// Extract and repair the full tool call text
		fullMatch := text[matchStart : closingBracket+1]

		// Repair and parse JSON
		repairedJSON := repairJSON(jsonStr)
		var inputMap map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &inputMap); err != nil {
			log.Debugf("kiro: failed to parse embedded tool call JSON: %v, raw: %s", err, jsonStr)
			continue
		}

		// Generate unique tool ID
		toolUseID := "toolu_" + uuid.New().String()[:12]

		// Check for duplicates using name+input as key
		dedupeKey := toolName + ":" + repairedJSON
		if processedIDs != nil {
			if processedIDs[dedupeKey] {
				log.Debugf("kiro: skipping duplicate embedded tool call: %s", toolName)
				// Still remove from text even if duplicate
				cleanText = strings.Replace(cleanText, fullMatch, "", 1)
				continue
			}
			processedIDs[dedupeKey] = true
		}

		toolUses = append(toolUses, kiroToolUse{
			ToolUseID: toolUseID,
			Name:      toolName,
			Input:     inputMap,
		})

		log.Infof("kiro: extracted embedded tool call: %s (ID: %s)", toolName, toolUseID)

		// Remove from clean text
		cleanText = strings.Replace(cleanText, fullMatch, "", 1)
	}

	// Clean up extra whitespace
	cleanText = strings.TrimSpace(cleanText)
	cleanText = whitespaceCollapsePattern.ReplaceAllString(cleanText, " ")

	return cleanText, toolUses
}

// findMatchingBracket finds the index of the closing brace/bracket that matches
// the opening one at startPos. Handles nested objects and strings correctly.
func findMatchingBracket(text string, startPos int) int {
	if startPos >= len(text) {
		return -1
	}

	openChar := text[startPos]
	var closeChar byte
	switch openChar {
	case '{':
		closeChar = '}'
	case '[':
		closeChar = ']'
	default:
		return -1
	}

	depth := 1
	inString := false
	escapeNext := false

	for i := startPos + 1; i < len(text); i++ {
		char := text[i]

		if escapeNext {
			escapeNext = false
			continue
		}

		if char == '\\' && inString {
			escapeNext = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if !inString {
			if char == openChar {
				depth++
			} else if char == closeChar {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}

	return -1
}

// repairJSON attempts to fix common JSON issues that may occur in tool call arguments.
// Based on AIClient-2-API's JSON repair implementation with a more conservative strategy.
//
// Conservative repair strategy:
// 1. First try to parse JSON directly - if valid, return as-is
// 2. Only attempt repair if parsing fails
// 3. After repair, validate the result - if still invalid, return original
//
// Handles incomplete JSON by balancing brackets and removing trailing incomplete content.
// Uses pre-compiled regex patterns for performance.
func repairJSON(jsonString string) string {
	// Handle empty or invalid input
	if jsonString == "" {
		return "{}"
	}
	
	str := strings.TrimSpace(jsonString)
	if str == "" {
		return "{}"
	}
	
	// CONSERVATIVE STRATEGY: First try to parse directly
	// If the JSON is already valid, return it unchanged
	var testParse interface{}
	if err := json.Unmarshal([]byte(str), &testParse); err == nil {
		log.Debugf("kiro: repairJSON - JSON is already valid, returning unchanged")
		return str
	}
	
	log.Debugf("kiro: repairJSON - JSON parse failed, attempting repair")
	originalStr := str // Keep original for fallback
	
	// First, escape unescaped newlines/tabs within JSON string values
	str = escapeNewlinesInStrings(str)
	// Remove trailing commas before closing braces/brackets
	str = trailingCommaPattern.ReplaceAllString(str, "$1")
	
	// Calculate bracket balance to detect incomplete JSON
	braceCount := 0    // {} balance
	bracketCount := 0  // [] balance
	inString := false
	escape := false
	lastValidIndex := -1
	
	for i := 0; i < len(str); i++ {
		char := str[i]
		
		// Handle escape sequences
		if escape {
			escape = false
			continue
		}
		
		if char == '\\' {
			escape = true
			continue
		}
		
		// Handle string boundaries
		if char == '"' {
			inString = !inString
			continue
		}
		
		// Skip characters inside strings (they don't affect bracket balance)
		if inString {
			continue
		}
		
		// Track bracket balance
		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}
		
		// Record last valid position (where brackets are balanced or positive)
		if braceCount >= 0 && bracketCount >= 0 {
			lastValidIndex = i
		}
	}
	
	// If brackets are unbalanced, try to repair
	if braceCount > 0 || bracketCount > 0 {
		// Truncate to last valid position if we have incomplete content
		if lastValidIndex > 0 && lastValidIndex < len(str)-1 {
			// Check if truncation would help (only truncate if there's trailing garbage)
			truncated := str[:lastValidIndex+1]
			// Recount brackets after truncation
			braceCount = 0
			bracketCount = 0
			inString = false
			escape = false
			for i := 0; i < len(truncated); i++ {
				char := truncated[i]
				if escape {
					escape = false
					continue
				}
				if char == '\\' {
					escape = true
					continue
				}
				if char == '"' {
					inString = !inString
					continue
				}
				if inString {
					continue
				}
				switch char {
				case '{':
					braceCount++
				case '}':
					braceCount--
				case '[':
					bracketCount++
				case ']':
					bracketCount--
				}
			}
			str = truncated
		}
		
		// Add missing closing brackets
		for braceCount > 0 {
			str += "}"
			braceCount--
		}
		for bracketCount > 0 {
			str += "]"
			bracketCount--
		}
	}
	
	// CONSERVATIVE STRATEGY: Validate repaired JSON
	// If repair didn't produce valid JSON, return original string
	if err := json.Unmarshal([]byte(str), &testParse); err != nil {
		log.Warnf("kiro: repairJSON - repair failed to produce valid JSON, returning original")
		return originalStr
	}
	
	log.Debugf("kiro: repairJSON - successfully repaired JSON")
	return str
}

// escapeNewlinesInStrings escapes literal newlines, tabs, and other control characters
// that appear inside JSON string values. This handles cases where streaming fragments
// contain unescaped control characters within string content.
func escapeNewlinesInStrings(raw string) string {
	var result strings.Builder
	result.Grow(len(raw) + 100) // Pre-allocate with some extra space

	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if escaped {
			// Previous character was backslash, this is an escape sequence
			result.WriteByte(c)
			escaped = false
			continue
		}

		if c == '\\' && inString {
			// Start of escape sequence
			result.WriteByte(c)
			escaped = true
			continue
		}

		if c == '"' {
			// Toggle string state
			inString = !inString
			result.WriteByte(c)
			continue
		}

		if inString {
			// Inside a string, escape control characters
			switch c {
			case '\n':
				result.WriteString("\\n")
			case '\r':
				result.WriteString("\\r")
			case '\t':
				result.WriteString("\\t")
			default:
				result.WriteByte(c)
			}
		} else {
			result.WriteByte(c)
		}
	}

	return result.String()
}

// processToolUseEvent handles a toolUseEvent from the Kiro stream.
// It accumulates input fragments and emits tool_use blocks when complete.
// Returns events to emit and updated state.
func (e *KiroExecutor) processToolUseEvent(event map[string]interface{}, currentToolUse *toolUseState, processedIDs map[string]bool) ([]kiroToolUse, *toolUseState) {
	var toolUses []kiroToolUse

	// Extract from nested toolUseEvent or direct format
	tu := event
	if nested, ok := event["toolUseEvent"].(map[string]interface{}); ok {
		tu = nested
	}

	toolUseID := getString(tu, "toolUseId")
	toolName := getString(tu, "name")
	isStop := false
	if stop, ok := tu["stop"].(bool); ok {
		isStop = stop
	}

	// Get input - can be string (fragment) or object (complete)
	var inputFragment string
	var inputMap map[string]interface{}
	
	if inputRaw, ok := tu["input"]; ok {
		switch v := inputRaw.(type) {
		case string:
			inputFragment = v
		case map[string]interface{}:
			inputMap = v
		}
	}

	// New tool use starting
	if toolUseID != "" && toolName != "" {
		if currentToolUse != nil && currentToolUse.toolUseID != toolUseID {
			// New tool use arrived while another is in progress (interleaved events)
			// This is unusual - log warning and complete the previous one
			log.Warnf("kiro: interleaved tool use detected - new ID %s arrived while %s in progress, completing previous",
				toolUseID, currentToolUse.toolUseID)
			// Emit incomplete previous tool use
			if !processedIDs[currentToolUse.toolUseID] {
				incomplete := kiroToolUse{
					ToolUseID: currentToolUse.toolUseID,
					Name:      currentToolUse.name,
				}
				if currentToolUse.inputBuffer.Len() > 0 {
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(currentToolUse.inputBuffer.String()), &input); err == nil {
						incomplete.Input = input
					}
				}
				toolUses = append(toolUses, incomplete)
				processedIDs[currentToolUse.toolUseID] = true
			}
			currentToolUse = nil
		}

		if currentToolUse == nil {
			// Check for duplicate
			if processedIDs != nil && processedIDs[toolUseID] {
				log.Debugf("kiro: skipping duplicate toolUseEvent: %s", toolUseID)
				return nil, nil
			}

			currentToolUse = &toolUseState{
				toolUseID: toolUseID,
				name:      toolName,
			}
			log.Infof("kiro: starting new tool use: %s (ID: %s)", toolName, toolUseID)
		}
	}

	// Accumulate input fragments
	if currentToolUse != nil && inputFragment != "" {
		// Accumulate fragments directly - they form valid JSON when combined
		// The fragments are already decoded from JSON, so we just concatenate them
		currentToolUse.inputBuffer.WriteString(inputFragment)
		log.Debugf("kiro: accumulated input fragment, total length: %d", currentToolUse.inputBuffer.Len())
	}

	// If complete input object provided directly
	if currentToolUse != nil && inputMap != nil {
		inputBytes, _ := json.Marshal(inputMap)
		currentToolUse.inputBuffer.Reset()
		currentToolUse.inputBuffer.Write(inputBytes)
	}

	// Tool use complete
	if isStop && currentToolUse != nil {
		fullInput := currentToolUse.inputBuffer.String()
		
		// Repair and parse the accumulated JSON
		repairedJSON := repairJSON(fullInput)
		var finalInput map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &finalInput); err != nil {
			log.Warnf("kiro: failed to parse accumulated tool input: %v, raw: %s", err, fullInput)
			// Use empty input as fallback
			finalInput = make(map[string]interface{})
		}

		toolUse := kiroToolUse{
			ToolUseID: currentToolUse.toolUseID,
			Name:      currentToolUse.name,
			Input:     finalInput,
		}
		toolUses = append(toolUses, toolUse)

		// Mark as processed
		if processedIDs != nil {
			processedIDs[currentToolUse.toolUseID] = true
		}

		log.Infof("kiro: completed tool use: %s (ID: %s)", currentToolUse.name, currentToolUse.toolUseID)
		return toolUses, nil // Reset state
	}

	return toolUses, currentToolUse
}

// deduplicateToolUses removes duplicate tool uses based on toolUseId and content (name+arguments).
// This prevents both ID-based duplicates and content-based duplicates (same tool call with different IDs).
func deduplicateToolUses(toolUses []kiroToolUse) []kiroToolUse {
	seenIDs := make(map[string]bool)
	seenContent := make(map[string]bool) // Content-based deduplication (name + arguments)
	var unique []kiroToolUse

	for _, tu := range toolUses {
		// Skip if we've already seen this ID
		if seenIDs[tu.ToolUseID] {
			log.Debugf("kiro: removing ID-duplicate tool use: %s (name: %s)", tu.ToolUseID, tu.Name)
			continue
		}

		// Build content key for content-based deduplication
		inputJSON, _ := json.Marshal(tu.Input)
		contentKey := tu.Name + ":" + string(inputJSON)

		// Skip if we've already seen this content (same name + arguments)
		if seenContent[contentKey] {
			log.Debugf("kiro: removing content-duplicate tool use: %s (id: %s)", tu.Name, tu.ToolUseID)
			continue
		}

		seenIDs[tu.ToolUseID] = true
		seenContent[contentKey] = true
		unique = append(unique, tu)
	}

	return unique
}
