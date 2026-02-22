package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/kiro/claude"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/kiro/common"
	kiroopenai "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/kiro/openai"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	// Kiro API common constants
	kiroContentType  = "application/json"
	kiroAcceptStream = "*/*"

	// Event Stream frame size constants for boundary protection
	// AWS Event Stream binary format: prelude (12 bytes) + headers + payload + message_crc (4 bytes)
	// Prelude consists of: total_length (4) + headers_length (4) + prelude_crc (4)
	minEventStreamFrameSize = 16       // Minimum: 4(total_len) + 4(headers_len) + 4(prelude_crc) + 4(message_crc)
	maxEventStreamMsgSize   = 10 << 20 // Maximum message length: 10MB

	// Event Stream error type constants
	ErrStreamFatal     = "fatal"     // Connection/authentication errors, not recoverable
	ErrStreamMalformed = "malformed" // Format errors, data cannot be parsed

	// kiroUserAgent matches Amazon Q CLI style for User-Agent header
	kiroUserAgent = "aws-sdk-rust/1.3.9 os/macos lang/rust/1.87.0"
	// kiroFullUserAgent is the complete x-amz-user-agent header (Amazon Q CLI style)
	kiroFullUserAgent = "aws-sdk-rust/1.3.9 ua/2.1 api/ssooidc/1.88.0 os/macos lang/rust/1.87.0 m/E app/AmazonQ-For-CLI"

	// Kiro IDE style headers for IDC auth
	kiroIDEUserAgent     = "aws-sdk-js/1.0.27 ua/2.1 os/win32#10.0.19044 lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E"
	kiroIDEAmzUserAgent  = "aws-sdk-js/1.0.27"
	kiroIDEAgentModeVibe = "vibe"

	// Socket retry configuration constants
	// Maximum number of retry attempts for socket/network errors
	kiroSocketMaxRetries = 3
	// Base delay between retry attempts (uses exponential backoff: delay * 2^attempt)
	kiroSocketBaseRetryDelay = 1 * time.Second
	// Maximum delay between retry attempts (cap for exponential backoff)
	kiroSocketMaxRetryDelay = 30 * time.Second
	// First token timeout for streaming responses (how long to wait for first response)
	kiroFirstTokenTimeout = 15 * time.Second
	// Streaming read timeout (how long to wait between chunks)
	kiroStreamingReadTimeout = 300 * time.Second
)

// retryableHTTPStatusCodes defines HTTP status codes that are considered retryable.
// Based on kiro2Api reference: 502 (Bad Gateway), 503 (Service Unavailable), 504 (Gateway Timeout)
var retryableHTTPStatusCodes = map[int]bool{
	502: true, // Bad Gateway - upstream server error
	503: true, // Service Unavailable - server temporarily overloaded
	504: true, // Gateway Timeout - upstream server timeout
}

// Real-time usage estimation configuration
// These control how often usage updates are sent during streaming
var (
	usageUpdateCharThreshold = 5000             // Send usage update every 5000 characters
	usageUpdateTimeInterval  = 15 * time.Second // Or every 15 seconds, whichever comes first
)

// Global FingerprintManager for dynamic User-Agent generation per token
// Each token gets a unique fingerprint on first use, which is cached for subsequent requests
var (
	globalFingerprintManager     *kiroauth.FingerprintManager
	globalFingerprintManagerOnce sync.Once
)

// getGlobalFingerprintManager returns the global FingerprintManager instance
func getGlobalFingerprintManager() *kiroauth.FingerprintManager {
	globalFingerprintManagerOnce.Do(func() {
		globalFingerprintManager = kiroauth.NewFingerprintManager()
		log.Infof("kiro: initialized global FingerprintManager for dynamic UA generation")
	})
	return globalFingerprintManager
}

// retryConfig holds configuration for socket retry logic.
// Based on kiro2Api Python implementation patterns.
type retryConfig struct {
	MaxRetries      int           // Maximum number of retry attempts
	BaseDelay       time.Duration // Base delay between retries (exponential backoff)
	MaxDelay        time.Duration // Maximum delay cap
	RetryableErrors []string      // List of retryable error patterns
	RetryableStatus map[int]bool  // HTTP status codes to retry
	FirstTokenTmout time.Duration // Timeout for first token in streaming
	StreamReadTmout time.Duration // Timeout between stream chunks
}

// defaultRetryConfig returns the default retry configuration for Kiro socket operations.
func defaultRetryConfig() retryConfig {
	return retryConfig{
		MaxRetries:      kiroSocketMaxRetries,
		BaseDelay:       kiroSocketBaseRetryDelay,
		MaxDelay:        kiroSocketMaxRetryDelay,
		RetryableStatus: retryableHTTPStatusCodes,
		RetryableErrors: []string{
			"connection reset",
			"connection refused",
			"broken pipe",
			"EOF",
			"timeout",
			"temporary failure",
			"no such host",
			"network is unreachable",
			"i/o timeout",
		},
		FirstTokenTmout: kiroFirstTokenTimeout,
		StreamReadTmout: kiroStreamingReadTimeout,
	}
}

// isRetryableError checks if an error is retryable based on error type and message.
// Returns true for network timeouts, connection resets, and temporary failures.
// Based on kiro2Api's retry logic patterns.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context cancellation - not retryable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Check for net.Error (timeout, temporary)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			log.Debugf("kiro: isRetryableError: network timeout detected")
			return true
		}
		// Note: Temporary() is deprecated but still useful for some error types
	}

	// Check for specific syscall errors (connection reset, broken pipe, etc.)
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		switch syscallErr {
		case syscall.ECONNRESET: // Connection reset by peer
			log.Debugf("kiro: isRetryableError: ECONNRESET detected")
			return true
		case syscall.ECONNREFUSED: // Connection refused
			log.Debugf("kiro: isRetryableError: ECONNREFUSED detected")
			return true
		case syscall.EPIPE: // Broken pipe
			log.Debugf("kiro: isRetryableError: EPIPE (broken pipe) detected")
			return true
		case syscall.ETIMEDOUT: // Connection timed out
			log.Debugf("kiro: isRetryableError: ETIMEDOUT detected")
			return true
		case syscall.ENETUNREACH: // Network is unreachable
			log.Debugf("kiro: isRetryableError: ENETUNREACH detected")
			return true
		case syscall.EHOSTUNREACH: // No route to host
			log.Debugf("kiro: isRetryableError: EHOSTUNREACH detected")
			return true
		}
	}

	// Check for net.OpError wrapping other errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		log.Debugf("kiro: isRetryableError: net.OpError detected, op=%s", opErr.Op)
		// Recursively check the wrapped error
		if opErr.Err != nil {
			return isRetryableError(opErr.Err)
		}
		return true
	}

	// Check error message for retryable patterns
	errMsg := strings.ToLower(err.Error())
	cfg := defaultRetryConfig()
	for _, pattern := range cfg.RetryableErrors {
		if strings.Contains(errMsg, pattern) {
			log.Debugf("kiro: isRetryableError: pattern '%s' matched in error: %s", pattern, errMsg)
			return true
		}
	}

	// Check for EOF which may indicate connection was closed
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		log.Debugf("kiro: isRetryableError: EOF/UnexpectedEOF detected")
		return true
	}

	return false
}

// isRetryableHTTPStatus checks if an HTTP status code is retryable.
// Based on kiro2Api: 502, 503, 504 are retryable server errors.
func isRetryableHTTPStatus(statusCode int) bool {
	return retryableHTTPStatusCodes[statusCode]
}

// calculateRetryDelay calculates the delay for the next retry attempt using exponential backoff.
// delay = min(baseDelay * 2^attempt, maxDelay)
// Adds ±30% jitter to prevent thundering herd.
func calculateRetryDelay(attempt int, cfg retryConfig) time.Duration {
	return kiroauth.ExponentialBackoffWithJitter(attempt, cfg.BaseDelay, cfg.MaxDelay)
}

// logRetryAttempt logs a retry attempt with relevant context.
func logRetryAttempt(attempt, maxRetries int, reason string, delay time.Duration, endpoint string) {
	log.Warnf("kiro: retry attempt %d/%d for %s, waiting %v before next attempt (endpoint: %s)",
		attempt+1, maxRetries, reason, delay, endpoint)
}

// kiroHTTPClientPool provides a shared HTTP client with connection pooling for Kiro API.
// This reduces connection overhead and improves performance for concurrent requests.
// Based on kiro2Api's connection pooling pattern.
var (
	kiroHTTPClientPool     *http.Client
	kiroHTTPClientPoolOnce sync.Once
)

// getKiroPooledHTTPClient returns a shared HTTP client with optimized connection pooling.
// The client is lazily initialized on first use and reused across requests.
// This is especially beneficial for:
// - Reducing TCP handshake overhead
// - Enabling HTTP/2 multiplexing
// - Better handling of keep-alive connections
func getKiroPooledHTTPClient() *http.Client {
	kiroHTTPClientPoolOnce.Do(func() {
		transport := &http.Transport{
			// Connection pool settings
			MaxIdleConns:        100,              // Max idle connections across all hosts
			MaxIdleConnsPerHost: 20,               // Max idle connections per host
			MaxConnsPerHost:     50,               // Max total connections per host
			IdleConnTimeout:     90 * time.Second, // How long idle connections stay in pool

			// Timeouts for connection establishment
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second, // TCP connection timeout
				KeepAlive: 30 * time.Second, // TCP keep-alive interval
			}).DialContext,

			// TLS handshake timeout
			TLSHandshakeTimeout: 10 * time.Second,

			// Response header timeout
			ResponseHeaderTimeout: 30 * time.Second,

			// Expect 100-continue timeout
			ExpectContinueTimeout: 1 * time.Second,

			// Enable HTTP/2 when available
			ForceAttemptHTTP2: true,
		}

		kiroHTTPClientPool = &http.Client{
			Transport: transport,
			// No global timeout - let individual requests set their own timeouts via context
		}

		log.Debugf("kiro: initialized pooled HTTP client (MaxIdleConns=%d, MaxIdleConnsPerHost=%d, MaxConnsPerHost=%d)",
			transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	})

	return kiroHTTPClientPool
}

// newKiroHTTPClientWithPooling creates an HTTP client that uses connection pooling when appropriate.
// It respects proxy configuration from auth or config, falling back to the pooled client.
// This provides the best of both worlds: custom proxy support + connection reuse.
func newKiroHTTPClientWithPooling(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Check if a proxy is configured - if so, we need a custom client
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If proxy is configured, use the existing proxy-aware client (doesn't pool)
	if proxyURL != "" {
		log.Debugf("kiro: using proxy-aware HTTP client (proxy=%s)", proxyURL)
		return newProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// No proxy - use pooled client for better performance
	pooledClient := getKiroPooledHTTPClient()

	// If timeout is specified, we need to wrap the pooled transport with timeout
	if timeout > 0 {
		return &http.Client{
			Transport: pooledClient.Transport,
			Timeout:   timeout,
		}
	}

	return pooledClient
}

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

// kiroDefaultRegion is the default AWS region for Kiro API endpoints.
// Used when no region is specified in auth metadata.
const kiroDefaultRegion = "us-east-1"

// extractRegionFromProfileARN extracts the AWS region from a ProfileARN.
// ARN format: arn:aws:codewhisperer:REGION:ACCOUNT:profile/PROFILE_ID
// Returns empty string if region cannot be extracted.
func extractRegionFromProfileARN(profileArn string) string {
	if profileArn == "" {
		return ""
	}
	parts := strings.Split(profileArn, ":")
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return ""
}

// buildKiroEndpointConfigs creates endpoint configurations for the specified region.
// This enables dynamic region support for Enterprise/IdC users in non-us-east-1 regions.
//
// Uses Q endpoint (q.{region}.amazonaws.com) as primary for ALL auth types:
// - Works universally across all AWS regions (CodeWhisperer endpoint only exists in us-east-1)
// - Uses /generateAssistantResponse path with AI_EDITOR origin
// - Does NOT require X-Amz-Target header
//
// The AmzTarget field is kept for backward compatibility but should be empty
// to indicate that the header should NOT be set.
func buildKiroEndpointConfigs(region string) []kiroEndpointConfig {
	if region == "" {
		region = kiroDefaultRegion
	}
	return []kiroEndpointConfig{
		{
			// Primary: Q endpoint - works for all regions and auth types
			URL:       fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "", // Empty = don't set X-Amz-Target header
			Name:      "AmazonQ",
		},
		{
			// Fallback: CodeWhisperer endpoint (legacy, only works in us-east-1)
			URL:       fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			Name:      "CodeWhisperer",
		},
	}
}

// resolveKiroAPIRegion determines the AWS region for Kiro API calls.
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func resolveKiroAPIRegion(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return kiroDefaultRegion
	}
	// Priority 1: Explicit api_region override
	if r, ok := auth.Metadata["api_region"].(string); ok && r != "" {
		log.Debugf("kiro: using region %s (source: api_region)", r)
		return r
	}
	// Priority 2: Extract from ProfileARN
	if profileArn, ok := auth.Metadata["profile_arn"].(string); ok && profileArn != "" {
		if arnRegion := extractRegionFromProfileARN(profileArn); arnRegion != "" {
			log.Debugf("kiro: using region %s (source: profile_arn)", arnRegion)
			return arnRegion
		}
	}
	// Note: OIDC "region" field is NOT used for API endpoint
	// Kiro API only exists in us-east-1, while OIDC region can vary (e.g., ap-northeast-2)
	// Using OIDC region for API calls causes DNS failures
	log.Debugf("kiro: using region %s (source: default)", kiroDefaultRegion)
	return kiroDefaultRegion
}

// kiroEndpointConfigs is kept for backward compatibility with default us-east-1 region.
// Prefer using buildKiroEndpointConfigs(region) for dynamic region support.
var kiroEndpointConfigs = buildKiroEndpointConfigs(kiroDefaultRegion)

// getKiroEndpointConfigs returns the list of Kiro API endpoint configurations to try in order.
// Supports dynamic region based on auth metadata "api_region", "profile_arn", or "region" field.
// Supports reordering based on "preferred_endpoint" in auth metadata/attributes.
//
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func getKiroEndpointConfigs(auth *cliproxyauth.Auth) []kiroEndpointConfig {
	if auth == nil {
		return kiroEndpointConfigs
	}

	// Determine API region using shared resolution logic
	region := resolveKiroAPIRegion(auth)

	// Build endpoint configs for the specified region
	endpointConfigs := buildKiroEndpointConfigs(region)

	// For IDC auth, use Q endpoint with AI_EDITOR origin
	// IDC tokens work with Q endpoint using Bearer auth
	// The difference is only in how tokens are refreshed (OIDC with clientId/clientSecret for IDC)
	// NOT in how API calls are made - both Social and IDC use the same endpoint/origin
	if auth.Metadata != nil {
		authMethod, _ := auth.Metadata["auth_method"].(string)
		if strings.ToLower(authMethod) == "idc" {
			log.Debugf("kiro: IDC auth, using Q endpoint (region: %s)", region)
			return endpointConfigs
		}
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
		return endpointConfigs
	}

	preference = strings.ToLower(strings.TrimSpace(preference))

	// Create new slice to avoid modifying global state
	var sorted []kiroEndpointConfig
	var remaining []kiroEndpointConfig

	for _, cfg := range endpointConfigs {
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
		return endpointConfigs
	}

	// Combine: preferred first, then others
	return append(sorted, remaining...)
}

// KiroExecutor handles requests to AWS CodeWhisperer (Kiro) API.
type KiroExecutor struct {
	cfg       *config.Config
	refreshMu sync.Mutex // Serializes token refresh operations to prevent race conditions
}

// isIDCAuth checks if the auth uses IDC (Identity Center) authentication method.
func isIDCAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	authMethod, _ := auth.Metadata["auth_method"].(string)
	return strings.ToLower(authMethod) == "idc"
}

// buildKiroPayloadForFormat builds the Kiro API payload based on the source format.
// This is critical because OpenAI and Claude formats have different tool structures:
// - OpenAI: tools[].function.name, tools[].function.description
// - Claude: tools[].name, tools[].description
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// Returns the serialized JSON payload and a boolean indicating whether thinking mode was injected.
func buildKiroPayloadForFormat(body []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, sourceFormat sdktranslator.Format, headers http.Header) ([]byte, bool) {
	switch sourceFormat.String() {
	case "openai":
		log.Debugf("kiro: using OpenAI payload builder for source format: %s", sourceFormat.String())
		return kiroopenai.BuildKiroPayloadFromOpenAI(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	case "kiro":
		// Body is already in Kiro format — pass through directly
		log.Debugf("kiro: body already in Kiro format, passing through directly")
		return body, false
	default:
		// Default to Claude format
		log.Debugf("kiro: using Claude payload builder for source format: %s", sourceFormat.String())
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	}
}

// NewKiroExecutor creates a new Kiro executor instance.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// applyDynamicFingerprint applies token-specific fingerprint headers to the request
// For IDC auth, uses dynamic fingerprint-based User-Agent
// For other auth types, uses static Amazon Q CLI style headers
func applyDynamicFingerprint(req *http.Request, auth *cliproxyauth.Auth) {
	if isIDCAuth(auth) {
		// Get token-specific fingerprint for dynamic UA generation
		tokenKey := getTokenKey(auth)
		fp := getGlobalFingerprintManager().GetFingerprint(tokenKey)

		// Use fingerprint-generated dynamic User-Agent
		req.Header.Set("User-Agent", fp.BuildUserAgent())
		req.Header.Set("X-Amz-User-Agent", fp.BuildAmzUserAgent())
		req.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentModeVibe)

		log.Debugf("kiro: using dynamic fingerprint for token %s (SDK:%s, OS:%s/%s, Kiro:%s)",
			tokenKey[:8]+"...", fp.SDKVersion, fp.OSType, fp.OSVersion, fp.KiroVersion)
	} else {
		// Use static Amazon Q CLI style headers for non-IDC auth
		req.Header.Set("User-Agent", kiroUserAgent)
		req.Header.Set("X-Amz-User-Agent", kiroFullUserAgent)
	}
}

// PrepareRequest prepares the HTTP request before execution.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, _ := kiroCredentials(auth)
	if strings.TrimSpace(accessToken) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	// Apply dynamic fingerprint-based headers
	applyDynamicFingerprint(req, auth)

	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kiro credentials into the request and executes it.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// getTokenKey returns a unique key for rate limiting based on auth credentials.
// Uses auth ID if available, otherwise falls back to a hash of the access token.
func getTokenKey(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.ID != "" {
		return auth.ID
	}
	accessToken, _ := kiroCredentials(auth)
	if len(accessToken) > 16 {
		return accessToken[:16]
	}
	return accessToken
}

// Execute sends the request to Kiro API and returns the response.
// Supports automatic token refresh on 401/403 errors.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("kiro: access token not found in auth")
	}

	// Rate limiting: get token key for tracking
	tokenKey := getTokenKey(auth)
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()

	// Check if token is in cooldown period
	if cooldownMgr.IsInCooldown(tokenKey) {
		remaining := cooldownMgr.GetRemainingCooldown(tokenKey)
		reason := cooldownMgr.GetCooldownReason(tokenKey)
		log.Warnf("kiro: token %s is in cooldown (reason: %s), remaining: %v", tokenKey, reason, remaining)
		return resp, fmt.Errorf("kiro: token is in cooldown for %v (reason: %s)", remaining, reason)
	}

	// Wait for rate limiter before proceeding
	log.Debugf("kiro: waiting for rate limiter for token %s", tokenKey)
	rateLimiter.WaitForToken(tokenKey)
	log.Debugf("kiro: rate limiter cleared for token %s", tokenKey)

	// Check if token is expired before making request (covers both normal and web_search paths)
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh), expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh", reloadErr)
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
				log.Infof("kiro: token refreshed successfully before request")
			}
		}
	}

	// Check for pure web_search request
	// Route to MCP endpoint instead of normal Kiro API
	if kiroclaude.HasWebSearchTool(req.Payload) {
		log.Infof("kiro: detected pure web_search request (non-stream), routing to MCP endpoint")
		return e.handleWebSearch(ctx, auth, req, opts, accessToken, profileArn)
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeWithRetry for each endpoint
	resp, err = e.executeWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, to, reporter, "", kiroModelID, isAgentic, isChatOnly, tokenKey)
	return resp, err
}

// executeWithRetry performs the actual HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
// tokenKey is used for rate limiting and cooldown tracking.
func (e *KiroExecutor) executeWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from, to sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool, tokenKey string) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
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
		kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)

		log.Debugf("kiro: trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

		for attempt := 0; attempt <= maxRetries; attempt++ {
			// Apply human-like delay before first request (not on retries)
			// This mimics natural user behavior patterns
			if attempt == 0 && endpointIdx == 0 {
				kiroauth.ApplyHumanLikeDelay()
			}

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
			if err != nil {
				return resp, err
			}

			httpReq.Header.Set("Content-Type", kiroContentType)
			httpReq.Header.Set("Accept", kiroAcceptStream)
			// Only set X-Amz-Target if specified (Q endpoint doesn't require it)
			if endpointConfig.AmzTarget != "" {
				httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
			}
			// Kiro-specific headers
			httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentModeVibe)
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

			httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 120*time.Second)
			httpResp, err := httpClient.Do(httpReq)
			if err != nil {
				// Check for context cancellation first - client disconnected, not a server error
				// Use 499 (Client Closed Request - nginx convention) instead of 500
				if errors.Is(err, context.Canceled) {
					log.Debugf("kiro: request canceled by client (context.Canceled)")
					return resp, statusErr{code: 499, msg: "client canceled request"}
				}

				// Check for context deadline exceeded - request timed out
				// Return 504 Gateway Timeout instead of 500
				if errors.Is(err, context.DeadlineExceeded) {
					log.Debugf("kiro: request timed out (context.DeadlineExceeded)")
					return resp, statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
				}

				recordAPIResponseError(ctx, e.cfg, err)

				// Enhanced socket retry: Check if error is retryable (network timeout, connection reset, etc.)
				retryCfg := defaultRetryConfig()
				if isRetryableError(err) && attempt < retryCfg.MaxRetries {
					delay := calculateRetryDelay(attempt, retryCfg)
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("socket error: %v", err), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				}

				return resp, err
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
				log.Warnf("kiro: rate limit hit (429), token %s set to cooldown for %v", tokenKey, cooldownDuration)

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: %s endpoint quota exhausted (429), will try next endpoint, body: %s",
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
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("HTTP %d", httpResp.StatusCode), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				} else if attempt < maxRetries {
					// Fallback for other 5xx errors (500, 501, etc.)
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

				log.Warnf("kiro: received 401 error, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
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
						log.Infof("kiro: token refreshed successfully, retrying request (attempt %d/%d)", attempt+1, maxRetries+1)
						continue
					}
					log.Infof("kiro: token refreshed successfully, no retries remaining")
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
					// Set long cooldown for suspended accounts
					rateLimiter.CheckAndMarkSuspended(tokenKey, respBodyStr)
					cooldownMgr.SetCooldown(tokenKey, kiroauth.LongCooldown, kiroauth.CooldownReasonSuspended)
					log.Errorf("kiro: account is suspended, token %s set to cooldown for %v", tokenKey, kiroauth.LongCooldown)
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
						// Persist the refreshed auth to file so subsequent requests use it
						if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
							log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
							// Continue anyway - the token is valid for this request
						}
						accessToken, profileArn = kiroCredentials(auth)
						kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
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

			// 1. Estimate InputTokens if missing
			if usageInfo.InputTokens == 0 {
				if enc, encErr := getTokenizer(req.Model); encErr == nil {
					if inp, countErr := countOpenAIChatTokens(enc, opts.OriginalRequest); countErr == nil {
						usageInfo.InputTokens = inp
					}
				}
			}

			// 2. Estimate OutputTokens if missing and content is available
			if usageInfo.OutputTokens == 0 && len(content) > 0 {
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

			// 3. Update TotalTokens
			usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens

			appendAPIResponseChunk(ctx, e.cfg, []byte(content))
			reporter.publish(ctx, usageInfo)

			// Record success for rate limiting
			rateLimiter.MarkTokenSuccess(tokenKey)
			log.Debugf("kiro: request successful, token %s marked as success", tokenKey)

			// Build response in Claude format for Kiro translator
			// stopReason is extracted from upstream response by parseEventStream
			requestedModel := payloadRequestedModel(opts, req.Model)
			kiroResponse := kiroclaude.BuildClaudeResponse(content, toolUses, requestedModel, usageInfo, stopReason)
			out := sdktranslator.TranslateNonStream(ctx, to, from, requestedModel, bytes.Clone(opts.OriginalRequest), body, kiroResponse, nil)
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
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
	}

	// Rate limiting: get token key for tracking
	tokenKey := getTokenKey(auth)
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

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeStreamWithRetry for each endpoint
	streamKiro, errStreamKiro := e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, reporter, "", kiroModelID, isAgentic, isChatOnly, tokenKey)
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
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool, tokenKey string) (<-chan cliproxyexecutor.StreamChunk, error) {
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
			httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentModeVibe)
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

// determineAgenticMode determines if the model is an agentic or chat-only variant.
// Returns (isAgentic, isChatOnly) based on model name suffixes.
func determineAgenticMode(model string) (isAgentic, isChatOnly bool) {
	isAgentic = strings.HasSuffix(model, "-agentic")
	isChatOnly = strings.HasSuffix(model, "-chat")
	return isAgentic, isChatOnly
}

// getEffectiveProfileArn determines if profileArn should be included based on auth method.
// profileArn is only needed for social auth (Google OAuth), not for AWS SSO OIDC (Builder ID/IDC).
//
// Detection logic (matching kiro-openai-gateway):
// 1. Check auth_method field: "builder-id" or "idc"
// 2. Check auth_type field: "aws_sso_oidc" (from kiro-cli tokens)
// 3. Check for client_id + client_secret presence (AWS SSO OIDC signature)
