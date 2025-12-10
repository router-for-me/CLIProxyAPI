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
	// kiroEndpoint is the Amazon Q streaming endpoint for chat API (GenerateAssistantResponse).
	// Note: This is different from the CodeWhisperer management endpoint (codewhisperer.us-east-1.amazonaws.com)
	// used in aws_auth.go for GetUsageLimits, ListProfiles, etc. Both endpoints are correct
	// for their respective API operations.
	kiroEndpoint       = "https://q.us-east-1.amazonaws.com"
	kiroTargetChat     = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	kiroContentType    = "application/x-amz-json-1.0"
	kiroAcceptStream   = "application/vnd.amazon.eventstream"
	kiroMaxMessageSize = 10 * 1024 * 1024 // 10MB max message size for event stream
	kiroMaxToolDescLen = 10237            // Kiro API limit is 10240 bytes, leave room for "..."

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
		log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
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
	
	// Determine initial origin based on model type
	// Opus models use AI_EDITOR (Kiro IDE quota), others start with CLI (Amazon Q quota)
	var currentOrigin string
	if strings.Contains(strings.ToLower(req.Model), "opus") {
		currentOrigin = "AI_EDITOR"
	} else {
		currentOrigin = "CLI"
	}
	
	kiroPayload := e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)

	// Execute with retry on 401/403 and 429 (quota exhausted)
	resp, err = e.executeWithRetry(ctx, auth, req, opts, accessToken, profileArn, kiroPayload, body, from, to, reporter, currentOrigin, kiroModelID, isAgentic, isChatOnly)
	return resp, err
}

// executeWithRetry performs the actual HTTP request with automatic retry on auth errors.
// Supports automatic fallback from CLI (Amazon Q) quota to AI_EDITOR (Kiro IDE) quota on 429.
func (e *KiroExecutor) executeWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from, to sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	maxRetries := 2 // Allow retries for token refresh + origin fallback

	for attempt := 0; attempt <= maxRetries; attempt++ {
		url := kiroEndpoint
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return resp, err
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("x-amz-target", kiroTargetChat)
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("Accept", kiroAcceptStream)

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

		// Handle 429 errors (quota exhausted) with origin fallback
		if httpResp.StatusCode == 429 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			// If currently using CLI quota and it's exhausted, switch to AI_EDITOR (Kiro IDE) quota
			if currentOrigin == "CLI" {
				log.Warnf("kiro: Amazon Q (CLI) quota exhausted (429), switching to Kiro (AI_EDITOR) fallback")
				currentOrigin = "AI_EDITOR"

				// Rebuild payload with new origin
				kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)

				// Retry with new origin
				continue
			}

			// Already on AI_EDITOR or other origin, return the error
			log.Debugf("kiro request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
			return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		// Handle 401/403 errors with token refresh and retry
		if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			if attempt < maxRetries {
				log.Warnf("kiro: received %d error, attempting token refresh and retry (attempt %d/%d)", httpResp.StatusCode, attempt+1, maxRetries+1)

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

			log.Debugf("kiro request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
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
				usageInfo.OutputTokens = int64(len(content) / 4)
				if usageInfo.OutputTokens == 0 {
					usageInfo.OutputTokens = 1
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

	return resp, fmt.Errorf("kiro: max retries exceeded")
}

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors and quota fallback on 429.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
	}
	if profileArn == "" {
		log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
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
	
	// Determine initial origin based on model type
	// Opus models use AI_EDITOR (Kiro IDE quota), others start with CLI (Amazon Q quota)
	var currentOrigin string
	if strings.Contains(strings.ToLower(req.Model), "opus") {
		currentOrigin = "AI_EDITOR"
	} else {
		currentOrigin = "CLI"
	}
	
	kiroPayload := e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	return e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, profileArn, kiroPayload, body, from, reporter, currentOrigin, kiroModelID, isAgentic, isChatOnly)
}

// executeStreamWithRetry performs the streaming HTTP request with automatic retry on auth errors.
// Supports automatic fallback from CLI (Amazon Q) quota to AI_EDITOR (Kiro IDE) quota on 429.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from sdktranslator.Format, reporter *usageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool) (<-chan cliproxyexecutor.StreamChunk, error) {
	maxRetries := 2 // Allow retries for token refresh + origin fallback

	for attempt := 0; attempt <= maxRetries; attempt++ {
		url := kiroEndpoint
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return nil, err
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("x-amz-target", kiroTargetChat)
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("Accept", kiroAcceptStream)

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

		// Handle 429 errors (quota exhausted) with origin fallback
		if httpResp.StatusCode == 429 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			// If currently using CLI quota and it's exhausted, switch to AI_EDITOR (Kiro IDE) quota
			if currentOrigin == "CLI" {
				log.Warnf("kiro: stream Amazon Q (CLI) quota exhausted (429), switching to Kiro (AI_EDITOR) fallback")
				currentOrigin = "AI_EDITOR"

				// Rebuild payload with new origin
				kiroPayload = e.buildKiroPayload(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly)

				// Retry with new origin
				continue
			}

			// Already on AI_EDITOR or other origin, return the error
			log.Debugf("kiro stream error, status: %d, body: %s", httpResp.StatusCode, string(respBody))
			return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
		}

		// Handle 401/403 errors with token refresh and retry
		if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			if attempt < maxRetries {
				log.Warnf("kiro: stream received %d error, attempting token refresh and retry (attempt %d/%d)", httpResp.StatusCode, attempt+1, maxRetries+1)

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

			log.Debugf("kiro stream error, status: %d, body: %s", httpResp.StatusCode, string(respBody))
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
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
			}()

			e.streamToChannel(ctx, resp.Body, out, from, req.Model, opts.OriginalRequest, body, reporter)
		}(httpResp)

		return out, nil
	}

	return nil, fmt.Errorf("kiro: max retries exceeded for stream")
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
		"kiro-claude-opus-4.5":   "claude-opus-4.5",
		"kiro-claude-sonnet-4.5": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4":   "claude-sonnet-4",
		"kiro-claude-haiku-4.5":  "claude-haiku-4.5",
		// Amazon Q format (amazonq- prefix) - same API as Kiro
		"amazonq-auto":              "auto",
		"amazonq-claude-opus-4.5":   "claude-opus-4.5",
		"amazonq-claude-sonnet-4.5": "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4":   "claude-sonnet-4",
		"amazonq-claude-haiku-4.5":  "claude-haiku-4.5",
		// Native Kiro format (no prefix) - used by Kiro IDE directly
		"claude-opus-4.5":   "claude-opus-4.5",
		"claude-sonnet-4.5": "claude-sonnet-4.5",
		"claude-sonnet-4":   "claude-sonnet-4",
		"claude-haiku-4.5":  "claude-haiku-4.5",
		"auto":              "auto",
		// Chat variant (no tool calling support)
		"kiro-claude-opus-4.5-chat": "claude-opus-4.5",
		// Agentic variants (same backend model IDs, but with special system prompt)
		"kiro-claude-opus-4.5-agentic":      "claude-opus-4.5",
		"kiro-claude-sonnet-4.5-agentic":    "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-agentic":      "claude-sonnet-4",
		"kiro-claude-haiku-4.5-agentic":     "claude-haiku-4.5",
		"amazonq-claude-sonnet-4.5-agentic": "claude-sonnet-4.5",
	}
	if kiroID, ok := modelMap[model]; ok {
		return kiroID
	}
	log.Debugf("kiro: unknown model '%s', falling back to 'auto'", model)
	return "auto"
}

// Kiro API request structs - field order determines JSON key order

type kiroPayload struct {
	ConversationState kiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
}

type kiroConversationState struct {
	ConversationID  string               `json:"conversationId"`
	History         []kiroHistoryMessage `json:"history"`
	CurrentMessage  kiroCurrentMessage   `json:"currentMessage"`
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL"
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
	ToolUseID string              `json:"toolUseId"`
	Content   []kiroTextContent   `json:"content"`
	Status    string              `json:"status"`
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
func (e *KiroExecutor) buildKiroPayload(claudeBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool) []byte {
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

	// Inject agentic optimization prompt for -agentic model variants
	// This prevents AWS Kiro API timeouts during large file write operations
	if isAgentic {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += kiroAgenticSystemPrompt
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
			if len(description) > kiroMaxToolDescLen {
				// Find a valid UTF-8 boundary before the limit
				truncLen := kiroMaxToolDescLen
				for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
					truncLen--
				}
				description = description[:truncLen] + "..."
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

	messagesArray := messages.Array()
	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		if role == "user" {
			userMsg, toolResults := e.buildUserMessageStruct(msg, modelID, origin)
			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
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
			history = append(history, kiroHistoryMessage{
				AssistantResponseMessage: &assistantMsg,
			})
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
		currentUserMsg.Content = contentBuilder.String()
		
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
	
	payload := kiroPayload{
		ConversationState: kiroConversationState{
			ConversationID:  uuid.New().String(),
			History:         history,
			CurrentMessage:  currentMessage,
			ChatTriggerType: "MANUAL", // Required by Kiro API
		},
		ProfileArn: profileArn,
	}

	// Ensure history is not nil (empty array)
	if payload.ConversationState.History == nil {
		payload.ConversationState.History = []kiroHistoryMessage{}
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
func (e *KiroExecutor) buildUserMessageStruct(msg gjson.Result, modelID, origin string) (kiroUserInputMessage, []kiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []kiroToolResult
	var images []kiroImage

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

		// Handle different event types
		switch eventType {
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
func (e *KiroExecutor) buildClaudeResponse(content string, toolUses []kiroToolUse, model string, usageInfo usage.Detail) []byte {
	var contentBlocks []map[string]interface{}

	// Add text content if present
	if content != "" {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
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

// NOTE: Tool uses are now extracted from API response, not parsed from text


// streamToChannel converts AWS Event Stream to channel-based streaming.
// Supports tool calling - emits tool_use content blocks when tools are used.
// Includes embedded [Called ...] tool call parsing and input buffering for toolUseEvent.
func (e *KiroExecutor) streamToChannel(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, targetFormat sdktranslator.Format, model string, originalReq, claudeBody []byte, reporter *usageReporter) {
	reader := bufio.NewReader(body)
	var totalUsage usage.Detail
	var hasToolUses bool // Track if any tool uses were emitted

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *toolUseState

	// Translator param for maintaining tool call state across streaming events
	// IMPORTANT: This must persist across all TranslateStream calls
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
			continue
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

			// Handle text content
			if contentDelta != "" {
				outputLen += len(contentDelta)
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

				claudeEvent := e.buildClaudeStreamEvent(contentDelta, contentBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, claudeEvent, &translatorParam)
				for _, chunk := range sseData {
					if chunk != "" {
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk + "\n\n")}
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

	// Fallback for output tokens if not received from upstream
	if totalUsage.OutputTokens == 0 && outputLen > 0 {
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

	// Send message_delta and message_stop
	msgStop := e.buildClaudeMessageStopEvent(stopReason, totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
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
	if blockType == "tool_use" {
		contentBlock = map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  toolName,
			"input": map[string]interface{}{},
		}
	} else {
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

func (e *KiroExecutor) buildClaudeMessageStopEvent(stopReason string, usageInfo usage.Detail) []byte {
	// First message_delta
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

	// Then message_stop
	stopEvent := map[string]interface{}{
		"type": "message_stop",
	}
	stopResult, _ := json.Marshal(stopEvent)

	return []byte("event: message_delta\ndata: " + string(deltaResult) + "\n\nevent: message_stop\ndata: " + string(stopResult))
}

// buildClaudeFinalEvent constructs the final Claude-style event.
func (e *KiroExecutor) buildClaudeFinalEvent() []byte {
	event := map[string]interface{}{
		"type": "message_stop",
	}
	result, _ := json.Marshal(event)
	return []byte("event: message_stop\ndata: " + string(result))
}

// CountTokens is not supported for the Kiro provider.
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

	log.Debugf("kiro executor: refresh called for auth %s", auth.ID)
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

	// Always use end_turn (no tool_use support)
	msgStop := e.buildClaudeMessageStopEvent("end_turn", totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
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
	// unquotedKeyPattern matches unquoted JSON keys that need quoting
	unquotedKeyPattern = regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)
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
// Based on AIClient-2-API's JSON repair implementation.
// Uses pre-compiled regex patterns for performance.
func repairJSON(raw string) string {
	// Remove trailing commas before closing braces/brackets
	repaired := trailingCommaPattern.ReplaceAllString(raw, "$1")
	// Fix unquoted keys (basic attempt - handles simple cases)
	repaired = unquotedKeyPattern.ReplaceAllString(repaired, `$1"$2":`)
	return repaired
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

// deduplicateToolUses removes duplicate tool uses based on toolUseId.
func deduplicateToolUses(toolUses []kiroToolUse) []kiroToolUse {
	seen := make(map[string]bool)
	var unique []kiroToolUse

	for _, tu := range toolUses {
		if !seen[tu.ToolUseID] {
			seen[tu.ToolUseID] = true
			unique = append(unique, tu)
		} else {
			log.Debugf("kiro: removing duplicate tool use: %s", tu.ToolUseID)
		}
	}

	return unique
}
