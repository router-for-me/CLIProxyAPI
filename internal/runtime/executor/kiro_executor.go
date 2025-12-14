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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/claude"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
	kiroopenai "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/openai"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"

)

const (
	// Kiro API common constants
	kiroContentType  = "application/x-amz-json-1.0"
	kiroAcceptStream = "*/*"

	// Event Stream frame size constants for boundary protection
	// AWS Event Stream binary format: prelude (12 bytes) + headers + payload + message_crc (4 bytes)
	// Prelude consists of: total_length (4) + headers_length (4) + prelude_crc (4)
	minEventStreamFrameSize = 16       // Minimum: 4(total_len) + 4(headers_len) + 4(prelude_crc) + 4(message_crc)
	maxEventStreamMsgSize   = 10 << 20 // Maximum message length: 10MB

	// Event Stream error type constants
	ErrStreamFatal     = "fatal"     // Connection/authentication errors, not recoverable
	ErrStreamMalformed = "malformed" // Format errors, data cannot be parsed
	// kiroUserAgent matches amq2api format for User-Agent header
	kiroUserAgent = "aws-sdk-rust/1.3.9 os/macos lang/rust/1.87.0"
	// kiroFullUserAgent is the complete x-amz-user-agent header matching amq2api
	kiroFullUserAgent = "aws-sdk-rust/1.3.9 ua/2.1 api/ssooidc/1.88.0 os/macos lang/rust/1.87.0 m/E app/AmazonQ-For-CLI"
)

// Real-time usage estimation configuration
// These control how often usage updates are sent during streaming
var (
	usageUpdateCharThreshold = 5000             // Send usage update every 5000 characters
	usageUpdateTimeInterval  = 15 * time.Second // Or every 15 seconds, whichever comes first
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
	cfg       *config.Config
	refreshMu sync.Mutex // Serializes token refresh operations to prevent race conditions
}

// buildKiroPayloadForFormat builds the Kiro API payload based on the source format.
// This is critical because OpenAI and Claude formats have different tool structures:
// - OpenAI: tools[].function.name, tools[].function.description
// - Claude: tools[].name, tools[].description
func buildKiroPayloadForFormat(body []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, sourceFormat sdktranslator.Format) []byte {
	switch sourceFormat.String() {
	case "openai":
		log.Debugf("kiro: using OpenAI payload builder for source format: %s", sourceFormat.String())
		return kiroopenai.BuildKiroPayloadFromOpenAI(body, modelID, profileArn, origin, isAgentic, isChatOnly)
	default:
		// Default to Claude format (also handles "claude", "kiro", etc.)
		log.Debugf("kiro: using Claude payload builder for source format: %s", sourceFormat.String())
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, isAgentic, isChatOnly)
	}
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

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeWithRetry for each endpoint
	resp, err = e.executeWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, to, reporter, "", kiroModelID, isAgentic, isChatOnly)
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
	var last429Err error

	for endpointIdx := 0; endpointIdx < len(endpointConfigs); endpointIdx++ {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin

		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)

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

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: %s endpoint quota exhausted (429), will try next endpoint, body: %s",
					endpointConfig.Name, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

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
						kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)
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
						kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)
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

			content, toolUses, usageInfo, stopReason, err := e.parseEventStream(httpResp.Body)
			if err != nil {
				recordAPIResponseError(ctx, e.cfg, err)
				return resp, err
			}

			// Fallback for usage if missing from upstream
			if usageInfo.TotalTokens == 0 {
				if enc, encErr := getTokenizer(req.Model); encErr == nil {
					if inp, countErr := countOpenAIChatTokens(enc, opts.OriginalRequest); countErr == nil {
						usageInfo.InputTokens = inp
					}
				}
				if len(content) > 0 {
					// Use tiktoken for more accurate output token calculation
					if enc, encErr := getTokenizer(req.Model); encErr == nil {
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
			// stopReason is extracted from upstream response by parseEventStream
			kiroResponse := kiroclaude.BuildClaudeResponse(content, toolUses, req.Model, usageInfo, stopReason)
			out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, kiroResponse, nil)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
			return resp, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	if last429Err != nil {
		return resp, last429Err
	}
	return resp, fmt.Errorf("kiro: all endpoints exhausted")
}

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors and quota fallback on 429.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
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

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeStreamWithRetry for each endpoint
	return e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, reporter, "", kiroModelID, isAgentic, isChatOnly)
}

// executeStreamWithRetry performs the streaming HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool) (<-chan cliproxyexecutor.StreamChunk, error) {
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	endpointConfigs := getKiroEndpointConfigs(auth)
	var last429Err error

	for endpointIdx := 0; endpointIdx < len(endpointConfigs); endpointIdx++ {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin

		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)

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

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: stream %s endpoint quota exhausted (429), will try next endpoint, body: %s",
					endpointConfig.Name, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

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
						kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)
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
						kiroPayload = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from)
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

				// Check if thinking mode was enabled in the original request
				// Only parse <thinking> tags when thinking was explicitly requested
				// Check multiple sources: original request, pre-translation payload, and translated body
				// This handles different client formats (Claude API, OpenAI, AMP/Cursor)
				thinkingEnabled := kiroclaude.IsThinkingEnabled(opts.OriginalRequest)
				if !thinkingEnabled {
					thinkingEnabled = kiroclaude.IsThinkingEnabled(req.Payload)
				}
				if !thinkingEnabled {
					thinkingEnabled = kiroclaude.IsThinkingEnabled(body)
				}
				log.Debugf("kiro: stream thinkingEnabled = %v", thinkingEnabled)

				e.streamToChannel(ctx, resp.Body, out, from, req.Model, opts.OriginalRequest, body, reporter, thinkingEnabled)
			}(httpResp)

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

// findRealThinkingEndTag finds the real </thinking> end tag, skipping false positives.
// Returns -1 if no real end tag is found.
//
// Real </thinking> tags from Kiro API have specific characteristics:
// - Usually preceded by newline (.\n</thinking>)
// - Usually followed by newline (\n\n)
// - Not inside code blocks or inline code
//
// False positives (discussion text) have characteristics:
// - In the middle of a sentence
// - Preceded by discussion words like "标签", "tag", "returns"
// - Inside code blocks or inline code
//
// Parameters:
// - content: the content to search in
// - alreadyInCodeBlock: whether we're already inside a code block from previous chunks
// - alreadyInInlineCode: whether we're already inside inline code from previous chunks
func findRealThinkingEndTag(content string, alreadyInCodeBlock, alreadyInInlineCode bool) int {
	searchStart := 0
	for {
		endIdx := strings.Index(content[searchStart:], kirocommon.ThinkingEndTag)
		if endIdx < 0 {
			return -1
		}
		endIdx += searchStart // Adjust to absolute position

		textBeforeEnd := content[:endIdx]
		textAfterEnd := content[endIdx+len(kirocommon.ThinkingEndTag):]

		// Check 1: Is it inside inline code?
		// Count backticks in current content and add state from previous chunks
		backtickCount := strings.Count(textBeforeEnd, "`")
		effectiveInInlineCode := alreadyInInlineCode
		if backtickCount%2 == 1 {
			effectiveInInlineCode = !effectiveInInlineCode
		}
		if effectiveInInlineCode {
			log.Debugf("kiro: found </thinking> inside inline code at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 2: Is it inside a code block?
		// Count fences in current content and add state from previous chunks
		fenceCount := strings.Count(textBeforeEnd, "```")
		altFenceCount := strings.Count(textBeforeEnd, "~~~")
		effectiveInCodeBlock := alreadyInCodeBlock
		if fenceCount%2 == 1 || altFenceCount%2 == 1 {
			effectiveInCodeBlock = !effectiveInCodeBlock
		}
		if effectiveInCodeBlock {
			log.Debugf("kiro: found </thinking> inside code block at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 3: Real </thinking> tags are usually preceded by newline or at start
		// and followed by newline or at end. Check the format.
		charBeforeTag := byte(0)
		if endIdx > 0 {
			charBeforeTag = content[endIdx-1]
		}
		charAfterTag := byte(0)
		if len(textAfterEnd) > 0 {
			charAfterTag = textAfterEnd[0]
		}

		// Real end tag format: preceded by newline OR end of sentence (. ! ?)
		// and followed by newline OR end of content
		isPrecededByNewlineOrSentenceEnd := charBeforeTag == '\n' || charBeforeTag == '.' ||
			charBeforeTag == '!' || charBeforeTag == '?' || charBeforeTag == 0
		isFollowedByNewlineOrEnd := charAfterTag == '\n' || charAfterTag == 0

		// If the tag has proper formatting (newline before/after), it's likely real
		if isPrecededByNewlineOrSentenceEnd && isFollowedByNewlineOrEnd {
			log.Debugf("kiro: found properly formatted </thinking> at pos %d", endIdx)
			return endIdx
		}

		// Check 4: Is the tag preceded by discussion keywords on the same line?
		lastNewlineIdx := strings.LastIndex(textBeforeEnd, "\n")
		lineBeforeTag := textBeforeEnd
		if lastNewlineIdx >= 0 {
			lineBeforeTag = textBeforeEnd[lastNewlineIdx+1:]
		}
		lineBeforeTagLower := strings.ToLower(lineBeforeTag)

		// Discussion patterns - if found, this is likely discussion text
		discussionPatterns := []string{
			"标签", "返回", "输出", "包含", "使用", "解析", "转换", "生成", // Chinese
			"tag", "return", "output", "contain", "use", "parse", "emit", "convert", "generate", // English
			"<thinking>", // discussing both tags together
			"`</thinking>`", // explicitly in inline code
		}
		isDiscussion := false
		for _, pattern := range discussionPatterns {
			if strings.Contains(lineBeforeTagLower, pattern) {
				isDiscussion = true
				break
			}
		}
		if isDiscussion {
			log.Debugf("kiro: found </thinking> after discussion text at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 5: Is there text immediately after on the same line?
		// Real end tags don't have text immediately after on the same line
		if len(textAfterEnd) > 0 && charAfterTag != '\n' && charAfterTag != 0 {
			// Find the next newline
			nextNewline := strings.Index(textAfterEnd, "\n")
			var textOnSameLine string
			if nextNewline >= 0 {
				textOnSameLine = textAfterEnd[:nextNewline]
			} else {
				textOnSameLine = textAfterEnd
			}
			// If there's non-whitespace text on the same line after the tag, it's discussion
			if strings.TrimSpace(textOnSameLine) != "" {
				log.Debugf("kiro: found </thinking> with text after on same line at pos %d, skipping", endIdx)
				searchStart = endIdx + len(kirocommon.ThinkingEndTag)
				continue
			}
		}

		// Check 6: Is there another <thinking> tag after this </thinking>?
		if strings.Contains(textAfterEnd, kirocommon.ThinkingStartTag) {
			nextStartIdx := strings.Index(textAfterEnd, kirocommon.ThinkingStartTag)
			textBeforeNextStart := textAfterEnd[:nextStartIdx]
			nextBacktickCount := strings.Count(textBeforeNextStart, "`")
			nextFenceCount := strings.Count(textBeforeNextStart, "```")
			nextAltFenceCount := strings.Count(textBeforeNextStart, "~~~")

			// If the next <thinking> is NOT in code, then this </thinking> is discussion text
			if nextBacktickCount%2 == 0 && nextFenceCount%2 == 0 && nextAltFenceCount%2 == 0 {
				log.Debugf("kiro: found </thinking> followed by <thinking> at pos %d, likely discussion text, skipping", endIdx)
				searchStart = endIdx + len(kirocommon.ThinkingEndTag)
				continue
			}
		}

		// This looks like a real end tag
		return endIdx
	}
}

// determineAgenticMode determines if the model is an agentic or chat-only variant.
// Returns (isAgentic, isChatOnly) based on model name suffixes.
func determineAgenticMode(model string) (isAgentic, isChatOnly bool) {
	isAgentic = strings.HasSuffix(model, "-agentic")
	isChatOnly = strings.HasSuffix(model, "-chat")
	return isAgentic, isChatOnly
}

// getEffectiveProfileArn determines if profileArn should be included based on auth method.
// profileArn is only needed for social auth (Google OAuth), not for builder-id (AWS SSO).
func getEffectiveProfileArn(auth *cliproxyauth.Auth, profileArn string) string {
	if auth != nil && auth.Metadata != nil {
		if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
			return "" // Don't include profileArn for builder-id auth
		}
	}
	return profileArn
}

// getEffectiveProfileArnWithWarning determines if profileArn should be included based on auth method,
// and logs a warning if profileArn is missing for non-builder-id auth.
// This consolidates the auth_method check that was previously done separately.
func getEffectiveProfileArnWithWarning(auth *cliproxyauth.Auth, profileArn string) string {
	if auth != nil && auth.Metadata != nil {
		if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
			// builder-id auth doesn't need profileArn
			return ""
		}
	}
	// For non-builder-id auth (social auth), profileArn is required
	if profileArn == "" {
		log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
	}
	return profileArn
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
		"claude-opus-4.5-agentic":        "claude-opus-4.5",
		"claude-sonnet-4.5-agentic":      "claude-sonnet-4.5",
		"claude-sonnet-4-agentic":        "claude-sonnet-4",
		"claude-haiku-4.5-agentic":       "claude-haiku-4.5",
		"kiro-claude-opus-4-5-agentic":   "claude-opus-4.5",
		"kiro-claude-sonnet-4-5-agentic": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-agentic":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5-agentic":  "claude-haiku-4.5",
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

// NOTE: Request building functions moved to internal/translator/kiro/claude/kiro_claude_request.go
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

		case "messageMetadataEvent":
			// Handle message metadata events which may contain token counts
			if metadata, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
					log.Debugf("kiro: parseEventStream found inputTokens in messageMetadataEvent: %d", usageInfo.InputTokens)
				}
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
					log.Debugf("kiro: parseEventStream found outputTokens in messageMetadataEvent: %d", usageInfo.OutputTokens)
				}
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


// NOTE: Response building functions moved to internal/translator/kiro/claude/kiro_claude_response.go
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
	var upstreamCreditUsage float64        // Credit usage from upstream (e.g., 1.458)
	var upstreamContextPercentage float64  // Context usage percentage from upstream (e.g., 78.56)
	var hasUpstreamUsage bool              // Whether we received usage from upstream

	// Translator param for maintaining tool call state across streaming events
	// IMPORTANT: This must persist across all TranslateStream calls
	var translatorParam any

	// Thinking mode state tracking - based on amq2api implementation
	// Tracks whether we're inside a <thinking> block and handles partial tags
	inThinkBlock := false
	pendingStartTagChars := 0    // Number of chars that might be start of <thinking>
	pendingEndTagChars := 0      // Number of chars that might be start of </thinking>
	isThinkingBlockOpen := false // Track if thinking content block is open
	thinkingBlockIndex := -1     // Index of the thinking content block

	// Code block state tracking for heuristic thinking tag parsing
	// When inside a markdown code block, <thinking> tags should NOT be parsed
	// This prevents false positives when the model outputs code examples containing these tags
	inCodeBlock := false
	codeFenceType := "" // Track which fence type opened the block ("```" or "~~~")

	// Inline code state tracking - when inside backticks, don't parse thinking tags
	// This handles cases like `<thinking>` being discussed in text
	inInlineCode := false

	// Track if we've seen any non-whitespace content before a thinking tag
	// Real thinking blocks from Kiro always start at the very beginning of the response
	// If we see content before <thinking>, subsequent <thinking> tags are likely discussion text
	hasSeenNonThinkingContent := false
	thinkingBlockCompleted := false // Track if we've already completed a thinking block

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
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}

				// Send tool input as delta
				inputBytes, _ := json.Marshal(finalInput)
				inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputBytes), contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}

				// Close block
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
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
				pendingText = kirocommon.ThinkingStartTag[:pendingStartTagChars]
				log.Debugf("kiro: flushing pending start tag chars at EOF: %q", pendingText)
				pendingStartTagChars = 0
			}
			if pendingEndTagChars > 0 {
				pendingText += kirocommon.ThinkingEndTag[:pendingEndTagChars]
				log.Debugf("kiro: flushing pending end tag chars at EOF: %q", pendingText)
				pendingEndTagChars = 0
			}

			// Output pending text if any
			if pendingText != "" {
				// If we're in a thinking block, output as thinking content
				if inThinkBlock && isThinkingBlockOpen {
					thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(pendingText, thinkingBlockIndex)
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
						blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
						sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}

					claudeEvent := kiroclaude.BuildClaudeStreamEvent(pendingText, contentBlockIndex)
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
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}

						lastReportedOutputTokens = currentOutputTokens
						log.Debugf("kiro: sent real-time usage update - input: %d, output: %d (accumulated: %d chars)",
							totalUsage.InputTokens, currentOutputTokens, accumulatedContent.Len())
					}

					lastUsageUpdateLen = accumulatedContent.Len()
					lastUsageUpdateTime = time.Now()
				}

				// Process content with thinking tag detection - based on amq2api implementation
				// This handles <thinking> and </thinking> tags that may span across chunks
				remaining := contentDelta

				// If we have pending start tag chars from previous chunk, prepend them
				if pendingStartTagChars > 0 {
					remaining = kirocommon.ThinkingStartTag[:pendingStartTagChars] + remaining
					pendingStartTagChars = 0
				}

				// If we have pending end tag chars from previous chunk, prepend them
				if pendingEndTagChars > 0 {
					remaining = kirocommon.ThinkingEndTag[:pendingEndTagChars] + remaining
					pendingEndTagChars = 0
				}

				for len(remaining) > 0 {
					// CRITICAL FIX: Only parse <thinking> tags when thinking mode was enabled in the request.
					// When thinking is NOT enabled, <thinking> tags in responses should be treated as
					// regular text content, not as thinking blocks. This prevents normal text content
					// from being incorrectly parsed as thinking when the model outputs <thinking> tags
					// without the user requesting thinking mode.
					if !thinkingEnabled {
						// Thinking not enabled - emit all content as regular text without parsing tags
						if remaining != "" {
							if !isTextBlockOpen {
								contentBlockIndex++
								isTextBlockOpen = true
								blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}

							claudeEvent := kiroclaude.BuildClaudeStreamEvent(remaining, contentBlockIndex)
							sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
							for _, chunk := range sseData {
								if chunk != "" {
									out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
								}
							}
						}
						break // Exit the for loop - all content processed as text
					}

					// HEURISTIC FIX: Track code block and inline code state to avoid parsing <thinking> tags
					// inside code contexts. When the model outputs code examples containing these tags,
					// they should be treated as text.
					if !inThinkBlock {
						// Check for inline code backticks first (higher priority than code fences)
						// This handles cases like `<thinking>` being discussed in text
						backtickIdx := strings.Index(remaining, kirocommon.InlineCodeMarker)
						thinkingIdx := strings.Index(remaining, kirocommon.ThinkingStartTag)

						// If backtick comes before thinking tag, handle inline code
						if backtickIdx >= 0 && (thinkingIdx < 0 || backtickIdx < thinkingIdx) {
							if inInlineCode {
								// Closing backtick - emit content up to and including backtick, exit inline code
								textToEmit := remaining[:backtickIdx+1]
								if textToEmit != "" {
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}
									claudeEvent := kiroclaude.BuildClaudeStreamEvent(textToEmit, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								remaining = remaining[backtickIdx+1:]
								inInlineCode = false
								continue
							} else {
								// Opening backtick - emit content before backtick, enter inline code
								textToEmit := remaining[:backtickIdx+1]
								if textToEmit != "" {
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}
									claudeEvent := kiroclaude.BuildClaudeStreamEvent(textToEmit, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								remaining = remaining[backtickIdx+1:]
								inInlineCode = true
								continue
							}
						}

						// If inside inline code, emit all content as text (don't parse thinking tags)
						if inInlineCode {
							if remaining != "" {
								if !isTextBlockOpen {
									contentBlockIndex++
									isTextBlockOpen = true
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								claudeEvent := kiroclaude.BuildClaudeStreamEvent(remaining, contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}
							break // Exit loop - remaining content is inside inline code
						}

						// Check for code fence markers (``` or ~~~) to toggle code block state
						fenceIdx := strings.Index(remaining, kirocommon.CodeFenceMarker)
						altFenceIdx := strings.Index(remaining, kirocommon.AltCodeFenceMarker)

						// Find the earliest fence marker
						earliestFenceIdx := -1
						earliestFenceType := ""
						if fenceIdx >= 0 && (altFenceIdx < 0 || fenceIdx < altFenceIdx) {
							earliestFenceIdx = fenceIdx
							earliestFenceType = kirocommon.CodeFenceMarker
						} else if altFenceIdx >= 0 {
							earliestFenceIdx = altFenceIdx
							earliestFenceType = kirocommon.AltCodeFenceMarker
						}

						if earliestFenceIdx >= 0 {
							// Check if this fence comes before any thinking tag
							thinkingIdx := strings.Index(remaining, kirocommon.ThinkingStartTag)
							if inCodeBlock {
								// Inside code block - check if this fence closes it
								if earliestFenceType == codeFenceType {
									// This fence closes the code block
									// Emit content up to and including the fence as text
									textToEmit := remaining[:earliestFenceIdx+len(earliestFenceType)]
									if textToEmit != "" {
										if !isTextBlockOpen {
											contentBlockIndex++
											isTextBlockOpen = true
											blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
											sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
											for _, chunk := range sseData {
												if chunk != "" {
													out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
												}
											}
										}
										claudeEvent := kiroclaude.BuildClaudeStreamEvent(textToEmit, contentBlockIndex)
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}
									remaining = remaining[earliestFenceIdx+len(earliestFenceType):]
									inCodeBlock = false
									codeFenceType = ""
									log.Debugf("kiro: exited code block")
									continue
								}
							} else if thinkingIdx < 0 || earliestFenceIdx < thinkingIdx {
								// Not in code block, and fence comes before thinking tag (or no thinking tag)
								// Emit content up to and including the fence as text, then enter code block
								textToEmit := remaining[:earliestFenceIdx+len(earliestFenceType)]
								if textToEmit != "" {
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}
									claudeEvent := kiroclaude.BuildClaudeStreamEvent(textToEmit, contentBlockIndex)
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								remaining = remaining[earliestFenceIdx+len(earliestFenceType):]
								inCodeBlock = true
								codeFenceType = earliestFenceType
								log.Debugf("kiro: entered code block with fence: %s", earliestFenceType)
								continue
							}
						}

						// If inside code block, emit all content as text (don't parse thinking tags)
						if inCodeBlock {
							if remaining != "" {
								if !isTextBlockOpen {
									contentBlockIndex++
									isTextBlockOpen = true
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}
								claudeEvent := kiroclaude.BuildClaudeStreamEvent(remaining, contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}
							break // Exit loop - all remaining content is inside code block
						}
					}

					if inThinkBlock {
						// Inside thinking block - look for </thinking> end tag
						// CRITICAL FIX: Skip </thinking> tags that are not the real end tag
						// This prevents false positives when thinking content discusses these tags
						// Pass current code block/inline code state for accurate detection
						endIdx := findRealThinkingEndTag(remaining, inCodeBlock, inInlineCode)
						
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
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}

								// Send thinking delta immediately
								thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(thinkContent, thinkingBlockIndex)
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
								blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(thinkingBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
								isThinkingBlockOpen = false
							}

							inThinkBlock = false
							thinkingBlockCompleted = true // Mark that we've completed a thinking block
							remaining = remaining[endIdx+len(kirocommon.ThinkingEndTag):]
							log.Debugf("kiro: exited thinking block, subsequent <thinking> tags will be treated as text")
						} else {
							// No end tag found - TRUE STREAMING: emit content immediately
							// Only save potential partial tag length for next iteration
							pendingEnd := kiroclaude.PendingTagSuffix(remaining, kirocommon.ThinkingEndTag)

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
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}

								// Send thinking delta immediately - TRUE STREAMING!
								thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(contentToEmit, thinkingBlockIndex)
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
						// CRITICAL FIX: Only parse <thinking> tags at the very beginning of the response
						// or if we haven't completed a thinking block yet.
						// After a thinking block is completed, subsequent <thinking> tags are likely
						// discussion text (e.g., "Kiro returns `<thinking>` tags") and should NOT be parsed.
						startIdx := -1
						if !thinkingBlockCompleted && !hasSeenNonThinkingContent {
							startIdx = strings.Index(remaining, kirocommon.ThinkingStartTag)
							// If there's non-whitespace content before the tag, it's not a real thinking block
							if startIdx > 0 {
								textBefore := remaining[:startIdx]
								if strings.TrimSpace(textBefore) != "" {
									// There's real content before the tag - this is discussion text, not thinking
									hasSeenNonThinkingContent = true
									startIdx = -1
									log.Debugf("kiro: found <thinking> tag after non-whitespace content, treating as text")
								}
							}
						}
						if startIdx >= 0 {
							// Found start tag - emit text before it and switch to thinking mode
							textBefore := remaining[:startIdx]
							if textBefore != "" {
								// Only whitespace before thinking tag is allowed
								// Start text content block if needed
								if !isTextBlockOpen {
									contentBlockIndex++
									isTextBlockOpen = true
									blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
									sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
									for _, chunk := range sseData {
										if chunk != "" {
											out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
										}
									}
								}

								claudeEvent := kiroclaude.BuildClaudeStreamEvent(textBefore, contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
							}

							// Close text block before starting thinking block
							if isTextBlockOpen {
								blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
								sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
								for _, chunk := range sseData {
									if chunk != "" {
										out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
									}
								}
								isTextBlockOpen = false
							}

							inThinkBlock = true
							remaining = remaining[startIdx+len(kirocommon.ThinkingStartTag):]
							log.Debugf("kiro: entered thinking block")
						} else {
							// No start tag found - check for partial start tag at buffer end
							// Only check for partial tags if we haven't completed a thinking block yet
							pendingStart := 0
							if !thinkingBlockCompleted && !hasSeenNonThinkingContent {
								pendingStart = kiroclaude.PendingTagSuffix(remaining, kirocommon.ThinkingStartTag)
							}
							if pendingStart > 0 {
								// Emit text except potential partial tag
								textToEmit := remaining[:len(remaining)-pendingStart]
								if textToEmit != "" {
									// Mark that we've seen non-thinking content
									if strings.TrimSpace(textToEmit) != "" {
										hasSeenNonThinkingContent = true
									}
									// Start text content block if needed
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}

									claudeEvent := kiroclaude.BuildClaudeStreamEvent(textToEmit, contentBlockIndex)
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
									// Mark that we've seen non-thinking content
									if strings.TrimSpace(remaining) != "" {
										hasSeenNonThinkingContent = true
									}
									// Start text content block if needed
									if !isTextBlockOpen {
										contentBlockIndex++
										isTextBlockOpen = true
										blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
										sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
										for _, chunk := range sseData {
											if chunk != "" {
												out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
											}
										}
									}

									claudeEvent := kiroclaude.BuildClaudeStreamEvent(remaining, contentBlockIndex)
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
				toolUseID := kirocommon.GetString(tu, "toolUseId")

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
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
					isTextBlockOpen = false
				}

				// Emit tool_use content block
				contentBlockIndex++
				toolName := kirocommon.GetString(tu, "name")

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", toolUseID, toolName)
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
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}
				}

				// Close tool_use block (always close even if input marshal failed)
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
					}
				}
			}

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			completedToolUses, newState := kiroclaude.ProcessToolUseEvent(event, currentToolUse, processedIDs)
			currentToolUse = newState

			// Emit completed tool uses
			for _, tu := range completedToolUses {
				hasToolUses = true

				// Close text block if open
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if chunk != "" {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
						}
					}
					isTextBlockOpen = false
				}

				contentBlockIndex++

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", tu.ToolUseID, tu.Name)
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
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if chunk != "" {
								out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
							}
						}
					}
				}

				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
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

		case "messageMetadataEvent":
			// Handle message metadata events which may contain token counts
			if metadata, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					log.Debugf("kiro: streamToChannel found inputTokens in messageMetadataEvent: %d", totalUsage.InputTokens)
				}
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					log.Debugf("kiro: streamToChannel found outputTokens in messageMetadataEvent: %d", totalUsage.OutputTokens)
				}
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
			if chunk != "" {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
			}
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
			log.Infof("kiro: using contextUsagePercentage (%.2f%%) to calculate input tokens: %d (local estimate was: %d)",
				upstreamContextPercentage, calculatedInputTokens, localEstimate)
		}
	}

	totalUsage.TotalTokens = totalUsage.InputTokens + totalUsage.OutputTokens

	// Log upstream usage information if received
	if hasUpstreamUsage {
		log.Infof("kiro: upstream usage - credits: %.4f, context: %.2f%%, final tokens - input: %d, output: %d, total: %d",
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
		if chunk != "" {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
		}
	}

	// Send message_stop event separately
	msgStop := kiroclaude.BuildClaudeMessageStopOnlyEvent()
	sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
	for _, chunk := range sseData {
		if chunk != "" {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
		}
	}
	// reporter.publish is called via defer
}

// NOTE: Claude SSE event builders moved to internal/translator/kiro/claude/kiro_claude_stream.go
// The executor now uses kiroclaude.BuildClaude*Event() functions instead

// CountTokens counts tokens locally using tiktoken since Kiro API doesn't expose a token counting endpoint.
// This provides approximate token counts for client requests.
func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Use tiktoken for local token counting
	enc, err := getTokenizer(req.Model)
	if err != nil {
		log.Warnf("kiro: CountTokens failed to get tokenizer: %v, falling back to estimate", err)
		// Fallback: estimate from payload size (roughly 4 chars per token)
		estimatedTokens := len(req.Payload) / 4
		if estimatedTokens == 0 && len(req.Payload) > 0 {
			estimatedTokens = 1
		}
		return cliproxyexecutor.Response{
			Payload: []byte(fmt.Sprintf(`{"count":%d}`, estimatedTokens)),
		}, nil
	}

	// Try to count tokens from the request payload
	var totalTokens int64

	// Try OpenAI chat format first
	if tokens, countErr := countOpenAIChatTokens(enc, req.Payload); countErr == nil && tokens > 0 {
		totalTokens = tokens
		log.Debugf("kiro: CountTokens counted %d tokens using OpenAI chat format", totalTokens)
	} else {
		// Fallback: count raw payload tokens
		if tokenCount, countErr := enc.Count(string(req.Payload)); countErr == nil {
			totalTokens = int64(tokenCount)
			log.Debugf("kiro: CountTokens counted %d tokens from raw payload", totalTokens)
		} else {
			// Final fallback: estimate from payload size
			totalTokens = int64(len(req.Payload) / 4)
			if totalTokens == 0 && len(req.Payload) > 0 {
				totalTokens = 1
			}
			log.Debugf("kiro: CountTokens estimated %d tokens from payload size", totalTokens)
		}
	}

	return cliproxyexecutor.Response{
		Payload: []byte(fmt.Sprintf(`{"count":%d}`, totalTokens)),
	}, nil
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
				// If token expires more than 5 minutes from now, it's still valid
				if time.Until(expTime) > 5*time.Minute {
					log.Debugf("kiro executor: token is still valid (expires in %v), skipping refresh", time.Until(expTime))
					// CRITICAL FIX: Set NextRefreshAfter to prevent frequent refresh checks
					// Without this, shouldRefresh() will return true again in 5 seconds
					updated := auth.Clone()
					// Set next refresh to 5 minutes before expiry, or at least 30 seconds from now
					nextRefresh := expTime.Add(-5 * time.Minute)
					minNextRefresh := time.Now().Add(30 * time.Second)
					if nextRefresh.Before(minNextRefresh) {
						nextRefresh = minNextRefresh
					}
					updated.NextRefreshAfter = nextRefresh
					log.Debugf("kiro executor: setting NextRefreshAfter to %v (in %v)", nextRefresh.Format(time.RFC3339), time.Until(nextRefresh))
					return updated, nil
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

	// NextRefreshAfter is aligned with RefreshLead (5min)
	if expiresAt, parseErr := time.Parse(time.RFC3339, tokenData.ExpiresAt); parseErr == nil {
		updated.NextRefreshAfter = expiresAt.Add(-5 * time.Minute)
	}

	log.Infof("kiro executor: token refreshed successfully, expires at %s", tokenData.ExpiresAt)
	return updated, nil
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

// NOTE: Message merging functions moved to internal/translator/kiro/common/message_merge.go
// NOTE: Tool calling support functions moved to internal/translator/kiro/claude/kiro_claude_tools.go
// The executor now uses kiroclaude.* and kirocommon.* functions instead
