/**
 * @file Kiro (Amazon Q) executor implementation
 * @description Optimized executor for Kiro provider with Canonical IR architecture.
 * Includes retry logic, quota fallback, JWT validation, and agentic optimizations.
 */

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/from_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	// Primary endpoint (from Plus version)
	kiroPrimaryURL = "https://q.us-east-1.amazonaws.com"
	// Fallback endpoint (original)
	kiroFallbackURL = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"

	kiroRefreshSkew    = 5 * time.Minute
	kiroRequestTimeout = 120 * time.Second
	kiroMaxRetries     = 2

	// Kiro API headers
	kiroContentType  = "application/x-amz-json-1.0"
	kiroTarget       = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	kiroAcceptStream = "application/vnd.amazon.eventstream"

	// kiroAgenticSystemPrompt prevents AWS Kiro API timeouts during large file operations.
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

REMEMBER: When in doubt, write LESS per operation. Multiple small operations > one large operation.`
)

// kiroModelMapping maps model IDs to Kiro API model IDs.
// Only needed for amazonq- prefix removal - native Kiro IDs pass through as-is.
var kiroModelMapping = map[string]string{
	// Amazon Q prefix → Kiro native format
	"amazonq-auto":              "auto",
	"amazonq-claude-opus-4.5":   "claude-opus-4.5",
	"amazonq-claude-sonnet-4.5": "claude-sonnet-4.5",
	"amazonq-claude-sonnet-4":   "claude-sonnet-4",
	"amazonq-claude-haiku-4.5":  "claude-haiku-4.5",
}

type KiroExecutor struct {
	cfg       *config.Config
	refreshMu sync.Mutex // Serializes token refresh operations
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

func (e *KiroExecutor) Identifier() string { return constant.Kiro }

// isJWTExpired checks if a JWT access token has expired.
// Optimized: extracts exp claim without full JSON unmarshal when possible.
func isJWTExpired(token string) bool {
	if token == "" {
		return true
	}

	// JWT format: header.payload.signature
	firstDot := strings.Index(token, ".")
	if firstDot == -1 {
		return false // Not a JWT, assume valid
	}
	secondDot := strings.Index(token[firstDot+1:], ".")
	if secondDot == -1 {
		return false // Not a JWT, assume valid
	}

	payload := token[firstDot+1 : firstDot+1+secondDot]

	// Base64URL decode (add padding if needed)
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Try with standard padding
		padded := payload
		switch len(payload) % 4 {
		case 2:
			padded += "=="
		case 3:
			padded += "="
		}
		decoded, err = base64.StdEncoding.DecodeString(padded)
		if err != nil {
			return false // Can't decode, assume valid
		}
	}

	// Fast exp extraction using string search instead of full JSON unmarshal
	expIdx := strings.Index(string(decoded), `"exp":`)
	if expIdx == -1 {
		return false // No exp claim, assume valid
	}

	// Parse the number after "exp":
	numStart := expIdx + 6
	for numStart < len(decoded) && (decoded[numStart] == ' ' || decoded[numStart] == '\t') {
		numStart++
	}

	numEnd := numStart
	for numEnd < len(decoded) && decoded[numEnd] >= '0' && decoded[numEnd] <= '9' {
		numEnd++
	}

	if numEnd == numStart {
		return false // No valid number, assume valid
	}

	// Parse exp timestamp
	var exp int64
	for i := numStart; i < numEnd; i++ {
		exp = exp*10 + int64(decoded[i]-'0')
	}

	if exp == 0 {
		return false
	}

	expTime := time.Unix(exp, 0)
	return time.Now().After(expTime) || time.Until(expTime) < time.Minute
}

// determineOrigin returns the origin based on model type.
// Opus models use AI_EDITOR (Kiro IDE quota), others use CLI (Amazon Q quota).
func (e *KiroExecutor) determineOrigin(model string) string {
	if strings.Contains(strings.ToLower(model), "opus") {
		return "AI_EDITOR"
	}
	return "CLI"
}

// isAgenticModel checks if the model is an agentic variant.
func (e *KiroExecutor) isAgenticModel(model string) bool {
	return strings.HasSuffix(model, "-agentic")
}

func (e *KiroExecutor) ensureValidToken(ctx context.Context, auth *coreauth.Auth) (string, *coreauth.Auth, error) {
	if auth == nil {
		return "", nil, fmt.Errorf("kiro: auth is nil")
	}
	token := getMetaString(auth.Metadata, "access_token", "accessToken")
	expiry := parseTokenExpiry(auth.Metadata)

	// Check both metadata expiry and JWT expiry (single call)
	jwtExpired := isJWTExpired(token)
	if token != "" && expiry.After(time.Now().Add(kiroRefreshSkew)) && !jwtExpired {
		return token, nil, nil
	}

	log.Debugf("kiro: token needs refresh (expiry: %v, jwt_expired: %v)", expiry, jwtExpired)
	updatedAuth, err := e.Refresh(ctx, auth)
	if err != nil {
		return "", nil, fmt.Errorf("kiro: token refresh failed: %w", err)
	}
	return getMetaString(updatedAuth.Metadata, "access_token", "accessToken"), updatedAuth, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	// Double-check after acquiring lock
	if auth.Metadata != nil {
		if lastRefresh, ok := auth.Metadata["last_refresh"].(string); ok {
			if refreshTime, err := time.Parse(time.RFC3339, lastRefresh); err == nil {
				if time.Since(refreshTime) < 30*time.Second {
					log.Debugf("kiro: token was recently refreshed, skipping")
					return auth, nil
				}
			}
		}
	}

	var creds kiro.KiroCredentials
	data, _ := json.Marshal(auth.Metadata)
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	newCreds, err := kiro.RefreshTokens(&creds)
	if err != nil {
		return nil, err
	}
	metaBytes, _ := json.Marshal(newCreds)
	var newMeta map[string]interface{}
	json.Unmarshal(metaBytes, &newMeta)
	newMeta["last_refresh"] = time.Now().Format(time.RFC3339)

	updatedAuth := auth.Clone()
	updatedAuth.Metadata = newMeta
	updatedAuth.LastRefreshedAt = time.Now()
	if store, ok := auth.Storage.(*kiro.KiroTokenStorage); ok {
		store.AccessToken = newCreds.AccessToken
		store.RefreshToken = newCreds.RefreshToken
		store.ProfileArn = newCreds.ProfileArn
		store.ExpiresAt = newCreds.ExpiresAt.Format(time.RFC3339)
		store.AuthMethod = newCreds.AuthMethod
		store.Provider = newCreds.Provider
	}

	log.Infof("kiro: token refreshed successfully")
	return updatedAuth, nil
}

type requestContext struct {
	ctx         context.Context
	auth        *coreauth.Auth
	req         cliproxyexecutor.Request
	token       string
	kiroModelID string
	requestID   string
	irReq       *ir.UnifiedChatRequest
	kiroBody    []byte
	origin      string
	isAgentic   bool
}

func (e *KiroExecutor) prepareRequest(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request) (*requestContext, error) {
	rc := &requestContext{
		ctx:       ctx,
		auth:      auth,
		req:       req,
		requestID: uuid.New().String()[:8],
		origin:    e.determineOrigin(req.Model),
		isAgentic: e.isAgenticModel(req.Model),
	}

	var err error
	rc.token, rc.auth, err = e.ensureValidToken(ctx, auth)
	if err != nil {
		return nil, err
	}
	if rc.auth == nil {
		rc.auth = auth
	}

	rc.kiroModelID = mapModelID(req.Model)
	rc.irReq, err = to_ir.ParseOpenAIRequest([]byte(ir.SanitizeText(string(req.Payload))))
	if err != nil {
		return nil, err
	}
	rc.irReq.Model = rc.kiroModelID

	// Initialize metadata if needed (single check)
	if rc.irReq.Metadata == nil {
		rc.irReq.Metadata = make(map[string]any)
	}

	// Set profile ARN
	if arn := getMetaString(rc.auth.Metadata, "profile_arn", "profileArn"); arn != "" {
		rc.irReq.Metadata["profileArn"] = arn
	}

	// Set origin for quota management
	rc.irReq.Metadata["origin"] = rc.origin

	// Inject agentic system prompt if needed
	if rc.isAgentic {
		e.injectAgenticPrompt(rc.irReq)
	}

	rc.kiroBody, err = (&from_ir.KiroProvider{}).ConvertRequest(rc.irReq)
	return rc, err
}

func (e *KiroExecutor) injectAgenticPrompt(req *ir.UnifiedChatRequest) {
	// Find or create system message
	for i, msg := range req.Messages {
		if msg.Role == ir.RoleSystem {
			// Append to existing system message
			for j, part := range msg.Content {
				if part.Type == ir.ContentTypeText {
					req.Messages[i].Content[j].Text += "\n" + kiroAgenticSystemPrompt
					return
				}
			}
		}
	}
	// No system message found, prepend one
	systemMsg := ir.Message{
		Role: ir.RoleSystem,
		Content: []ir.ContentPart{{
			Type: ir.ContentTypeText,
			Text: kiroAgenticSystemPrompt,
		}},
	}
	req.Messages = append([]ir.Message{systemMsg}, req.Messages...)
}

func (e *KiroExecutor) buildHTTPRequest(rc *requestContext, url string) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(rc.ctx, "POST", url, bytes.NewReader(rc.kiroBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", kiroContentType)
	httpReq.Header.Set("x-amz-target", kiroTarget)
	httpReq.Header.Set("Accept", kiroAcceptStream)
	if rc.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+rc.token)
	}
	return httpReq, nil
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	return e.executeWithRetry(rc)
}

func (e *KiroExecutor) executeWithRetry(rc *requestContext) (cliproxyexecutor.Response, error) {
	var lastErr error
	currentOrigin := rc.origin
	initialOrigin := rc.origin // Store initial origin for comparison
	useFallbackURL := false

	for attempt := 0; attempt <= kiroMaxRetries; attempt++ {
		// Update origin in request body if changed from initial
		if currentOrigin != initialOrigin {
			rc.irReq.Metadata["origin"] = currentOrigin
			var err error
			rc.kiroBody, err = (&from_ir.KiroProvider{}).ConvertRequest(rc.irReq)
			if err != nil {
				return cliproxyexecutor.Response{}, err
			}
			initialOrigin = currentOrigin // Update so we don't rebuild unnecessarily
		}

		url := kiroPrimaryURL
		if useFallbackURL {
			url = kiroFallbackURL
		}

		httpReq, err := e.buildHTTPRequest(rc, url)
		if err != nil {
			return cliproxyexecutor.Response{}, err
		}

		client := &http.Client{Timeout: kiroRequestTimeout}
		if proxy := e.cfg.ProxyURL; proxy != "" {
			util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: proxy}, client)
		} else if rc.auth.ProxyURL != "" {
			util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: rc.auth.ProxyURL}, client)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			// Try fallback URL on connection error
			if !useFallbackURL {
				log.Warnf("kiro: primary endpoint failed, trying fallback: %v", err)
				useFallbackURL = true
				continue
			}
			return cliproxyexecutor.Response{}, err
		}

		// Handle 429 (quota exhausted) - switch origin
		if resp.StatusCode == http.StatusTooManyRequests {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if currentOrigin == "CLI" {
				log.Warnf("kiro: CLI quota exhausted (429), switching to AI_EDITOR")
				currentOrigin = "AI_EDITOR"
				continue
			}
			lastErr = fmt.Errorf("quota exhausted: %s", string(body))
			continue
		}

		// Handle 401/403 - refresh token and retry
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if attempt < kiroMaxRetries {
				log.Warnf("kiro: auth error %d, refreshing token (attempt %d/%d)", resp.StatusCode, attempt+1, kiroMaxRetries)
				refreshedAuth, refreshErr := e.Refresh(rc.ctx, rc.auth)
				if refreshErr != nil {
					lastErr = fmt.Errorf("token refresh failed: %w", refreshErr)
					continue
				}
				rc.auth = refreshedAuth
				rc.token = getMetaString(refreshedAuth.Metadata, "access_token", "accessToken")
				continue
			}
			return cliproxyexecutor.Response{}, fmt.Errorf("auth error %d: %s", resp.StatusCode, string(body))
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			// Log detailed error for debugging (Kiro returns useful details in JSON)
			log.Warnf("kiro: upstream error %d for model %s: %s", resp.StatusCode, rc.req.Model, string(body))
			return cliproxyexecutor.Response{}, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
		}

		defer resp.Body.Close()

		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/vnd.amazon.eventstream") {
			return e.handleEventStreamResponse(resp.Body, rc.req.Model)
		}
		return e.handleJSONResponse(resp.Body, rc.req.Model)
	}

	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, fmt.Errorf("kiro: max retries exceeded")
}

func (e *KiroExecutor) handleEventStreamResponse(body io.ReadCloser, model string) (cliproxyexecutor.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 20_971_520) // 20MB buffer to handle large AWS EventStream frames
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()

	for scanner.Scan() {
		payload, err := parseEventPayload(scanner.Bytes())
		if err == nil {
			state.ProcessChunk(payload)
		}
	}

	msg := &ir.Message{Role: ir.RoleAssistant, ToolCalls: state.ToolCalls}
	if state.AccumulatedContent != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: state.AccumulatedContent})
	}

	converted, err := from_ir.ToOpenAIChatCompletion([]ir.Message{*msg}, nil, model, "chatcmpl-"+uuid.New().String())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: converted}, nil
}

func (e *KiroExecutor) handleJSONResponse(body io.ReadCloser, model string) (cliproxyexecutor.Response, error) {
	rawData, err := io.ReadAll(body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	messages, usage, err := to_ir.ParseKiroResponse(rawData)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	converted, err := from_ir.ToOpenAIChatCompletion(messages, usage, model, "chatcmpl-"+uuid.New().String())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: converted}, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}

	return e.executeStreamWithRetry(rc)
}

func (e *KiroExecutor) executeStreamWithRetry(rc *requestContext) (<-chan cliproxyexecutor.StreamChunk, error) {
	var lastErr error
	currentOrigin := rc.origin
	initialOrigin := rc.origin // Store initial origin for comparison
	useFallbackURL := false

	for attempt := 0; attempt <= kiroMaxRetries; attempt++ {
		// Update origin in request body if changed from initial
		if currentOrigin != initialOrigin {
			rc.irReq.Metadata["origin"] = currentOrigin
			var err error
			rc.kiroBody, err = (&from_ir.KiroProvider{}).ConvertRequest(rc.irReq)
			if err != nil {
				return nil, err
			}
			initialOrigin = currentOrigin // Update so we don't rebuild unnecessarily
		}

		url := kiroPrimaryURL
		if useFallbackURL {
			url = kiroFallbackURL
		}

		httpReq, err := e.buildHTTPRequest(rc, url)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Connection", "keep-alive")

		client := &http.Client{}
		if proxy := e.cfg.ProxyURL; proxy != "" {
			util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: proxy}, client)
		} else if rc.auth.ProxyURL != "" {
			util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: rc.auth.ProxyURL}, client)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			if !useFallbackURL {
				log.Warnf("kiro: stream primary endpoint failed, trying fallback: %v", err)
				useFallbackURL = true
				continue
			}
			return nil, err
		}

		// Handle 429 (quota exhausted)
		if resp.StatusCode == http.StatusTooManyRequests {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if currentOrigin == "CLI" {
				log.Warnf("kiro: stream CLI quota exhausted (429), switching to AI_EDITOR")
				currentOrigin = "AI_EDITOR"
				continue
			}
			lastErr = fmt.Errorf("quota exhausted: %s", string(body))
			continue
		}

		// Handle 401/403
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if attempt < kiroMaxRetries {
				log.Warnf("kiro: stream auth error %d, refreshing token", resp.StatusCode)
				refreshedAuth, refreshErr := e.Refresh(rc.ctx, rc.auth)
				if refreshErr != nil {
					lastErr = fmt.Errorf("token refresh failed: %w", refreshErr)
					continue
				}
				rc.auth = refreshedAuth
				rc.token = getMetaString(refreshedAuth.Metadata, "access_token", "accessToken")
				continue
			}
			return nil, fmt.Errorf("auth error %d: %s", resp.StatusCode, string(body))
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Warnf("kiro: stream upstream error %d for model %s: %s", resp.StatusCode, rc.req.Model, string(body))
			return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		go e.processStream(resp, rc.req.Model, rc.req.Payload, out)
		return out, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("kiro: max retries exceeded for stream")
}

func (e *KiroExecutor) processStream(resp *http.Response, model string, requestPayload []byte, out chan<- cliproxyexecutor.StreamChunk) {
	defer resp.Body.Close()
	defer close(out)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 20_971_520) // 20MB buffer to handle large AWS EventStream frames
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()
	messageID := "chatcmpl-" + uuid.New().String()
	idx := 0

	for scanner.Scan() {
		payload, err := parseEventPayload(scanner.Bytes())
		if err != nil {
			continue
		}
		events, _ := state.ProcessChunk(payload)
		for _, ev := range events {
			if chunk, _ := from_ir.ToOpenAIChunk(ev, model, messageID, idx); len(chunk) > 0 {
				out <- cliproxyexecutor.StreamChunk{Payload: chunk}
				idx++
			}
		}
	}

	// Build finish event with usage
	finish := ir.UnifiedEvent{
		Type:         ir.EventTypeFinish,
		FinishReason: state.DetermineFinishReason(),
		Usage:        state.Usage,
	}

	// Fallback: estimate tokens if API didn't return them
	if finish.Usage == nil || finish.Usage.TotalTokens == 0 {
		// Try to use real tokenizer for accurate prompt token count
		var promptTokens int64
		if enc, err := tokenizerForModel("claude"); err == nil {
			if count, err := countOpenAIChatTokens(enc, requestPayload); err == nil {
				promptTokens = count
			}
		}
		// Fallback for prompt tokens if tokenizer failed
		if promptTokens == 0 {
			promptTokens = int64(len(requestPayload) / 4)
			if promptTokens == 0 && len(requestPayload) > 0 {
				promptTokens = 1
			}
		}

		// Estimate completion tokens from accumulated content
		var completionTokens int
		if enc, err := tokenizerForModel("claude"); err == nil {
			if count, err := enc.Count(state.AccumulatedContent); err == nil {
				completionTokens = count
			}
		}
		// Fallback for completion tokens
		if completionTokens == 0 {
			completionTokens = len(state.AccumulatedContent) / 4
			if completionTokens == 0 && len(state.AccumulatedContent) > 0 {
				completionTokens = 1
			}
		}

		finish.Usage = &ir.Usage{
			PromptTokens:     int(promptTokens),
			CompletionTokens: completionTokens,
			TotalTokens:      int(promptTokens) + completionTokens,
		}
	}

	if chunk, _ := from_ir.ToOpenAIChunk(finish, model, messageID, idx); len(chunk) > 0 {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
}

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Kiro uses Claude models, so we use the O200kBase tokenizer (good approximation for Claude)
	enc, err := tokenizerForModel("claude") // Will use O200kBase fallback
	if err != nil {
		// Fallback to heuristic if tokenizer fails
		estTokens := len(req.Payload) / 4
		if estTokens == 0 && len(req.Payload) > 0 {
			estTokens = 1
		}
		return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"total_tokens": %d}`, estTokens))}, nil
	}

	count, err := countOpenAIChatTokens(enc, req.Payload)
	if err != nil {
		// Fallback to heuristic if counting fails
		estTokens := len(req.Payload) / 4
		if estTokens == 0 && len(req.Payload) > 0 {
			estTokens = 1
		}
		return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"total_tokens": %d}`, estTokens))}, nil
	}

	usageJSON := buildOpenAIUsageJSON(count)
	return cliproxyexecutor.Response{Payload: usageJSON}, nil
}

// Helper functions

func getMetaString(meta map[string]interface{}, keys ...string) string {
	if meta == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := meta[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func parseTokenExpiry(meta map[string]interface{}) time.Time {
	if meta == nil {
		return time.Time{}
	}
	for _, key := range []string{"expires_at", "expiresAt"} {
		if exp, ok := meta[key].(string); ok && exp != "" {
			if t, err := time.Parse(time.RFC3339, exp); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func mapModelID(model string) string {
	// Strip -agentic suffix for API call (it's only used for system prompt injection)
	baseModel := strings.TrimSuffix(model, "-agentic")

	// Check explicit mapping (mainly for amazonq- prefix)
	if mapped, ok := kiroModelMapping[baseModel]; ok {
		return mapped
	}

	// Strip amazonq- prefix if present (fallback)
	if strings.HasPrefix(baseModel, "amazonq-") {
		return strings.TrimPrefix(baseModel, "amazonq-")
	}

	// Return as-is (native Kiro format: auto, claude-opus-4.5, etc.)
	return baseModel
}

func splitAWSEventStream(data []byte, atEOF bool) (int, []byte, error) {
	if len(data) < 4 {
		if atEOF && len(data) > 0 {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	totalLen := int(binary.BigEndian.Uint32(data[0:4]))
	if totalLen < 16 || totalLen > 16*1024*1024 {
		return 1, nil, nil
	}
	if len(data) < totalLen {
		if atEOF {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	return totalLen, data[:totalLen], nil
}

func parseEventPayload(frame []byte) ([]byte, error) {
	if len(frame) < 16 {
		return nil, fmt.Errorf("short frame")
	}
	if binary.BigEndian.Uint32(frame[8:12]) != crc32.ChecksumIEEE(frame[0:8]) {
		return nil, fmt.Errorf("crc mismatch")
	}
	totalLen := int(binary.BigEndian.Uint32(frame[0:4]))
	headersLen := int(binary.BigEndian.Uint32(frame[4:8]))
	start, end := 12+headersLen, totalLen-4
	if start >= end || end > len(frame) {
		return nil, fmt.Errorf("bounds")
	}
	return frame[start:end], nil
}
