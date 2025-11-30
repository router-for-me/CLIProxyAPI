package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CopilotExecutor handles requests to GitHub Copilot API.
// It manages token refresh and proper header injection for Copilot requests.
type CopilotExecutor struct {
	cfg            *config.Config
	tokenMu        sync.RWMutex
	mu             sync.Mutex
	tokenCache     map[string]*cachedToken
	modelMu        sync.Mutex
	initiatorCount map[string]uint64
}

// cachedToken stores the Copilot token and its expiration time.
type cachedToken struct {
	token     string
	expiresAt time.Time
}

// NewCopilotExecutor creates a new CopilotExecutor instance.

func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor {
	return &CopilotExecutor{
		cfg:            cfg,
		tokenCache:     make(map[string]*cachedToken),
		initiatorCount: make(map[string]uint64),
	}
}

func (e *CopilotExecutor) Identifier() string { return "copilot" }

func (e *CopilotExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

// stripCopilotPrefix removes the "copilot-" prefix from model names if present.
// This allows users to explicitly route to Copilot using "copilot-gpt-5" while
// the actual API call uses "gpt-5".
func stripCopilotPrefix(model string) string {
	return strings.TrimPrefix(model, registry.CopilotModelPrefix)
}

// sanitizeCopilotPayload removes fields that Copilot's Chat Completions endpoint
// rejects (strip max_tokens and parallel_tool_calls).
func sanitizeCopilotPayload(body []byte, model string) []byte {
	if len(body) == 0 {
		return body
	}
	if gjson.GetBytes(body, "max_tokens").Exists() {
		if cleaned, err := sjson.DeleteBytes(body, "max_tokens"); err == nil {
			body = cleaned
		}
	}
	if gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		if cleaned, err := sjson.DeleteBytes(body, "parallel_tool_calls"); err == nil {
			body = cleaned
		}
	}
	return body
}

func (e *CopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	copilotToken, accountType, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		return resp, err
	}

	apiModel := stripCopilotPrefix(req.Model)

	translatorModel := req.Model

	reporter := newUsageReporter(ctx, e.Identifier(), apiModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	body := sdktranslator.TranslateRequest(from, to, apiModel, bytes.Clone(req.Payload), false)
	body = applyPayloadConfig(e.cfg, apiModel, body)
	body = sanitizeCopilotPayload(body, apiModel)
	body, _ = sjson.SetBytes(body, "stream", false)

	baseURL := copilotauth.CopilotBaseURL(accountType)
	url := baseURL + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	e.applyCopilotHeaders(httpReq, copilotToken, req.Payload)

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
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = copilotStatusErr(httpResp.StatusCode, string(b))
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	// Parse usage from response
	reporter.publish(ctx, parseOpenAIUsage(data))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, translatorModel, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	copilotToken, accountType, err := e.getCopilotToken(ctx, auth)
	if err != nil {
		return nil, err
	}

	apiModel := stripCopilotPrefix(req.Model)

	translatorModel := req.Model

	reporter := newUsageReporter(ctx, e.Identifier(), apiModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	body := sdktranslator.TranslateRequest(from, to, apiModel, bytes.Clone(req.Payload), true)
	body = applyPayloadConfig(e.cfg, apiModel, body)
	body = sanitizeCopilotPayload(body, apiModel)
	body, _ = sjson.SetBytes(body, "stream", true)

	baseURL := copilotauth.CopilotBaseURL(accountType)
	url := baseURL + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	e.applyCopilotHeaders(httpReq, copilotToken, req.Payload)

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
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = copilotStatusErr(httpResp.StatusCode, string(data))
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("copilot executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		bufSize := e.cfg.ScannerBufferSize
		if bufSize <= 0 {
			bufSize = 20_971_520
		}
		scanner.Buffer(nil, bufSize)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Parse usage from final chunk if present
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if gjson.GetBytes(data, "usage").Exists() {
					reporter.publish(ctx, parseOpenAIUsage(data))
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, translatorModel, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()

	return stream, nil
}

func (e *CopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("copilot executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "copilot executor: auth is nil (copilot_refresh_auth_nil)"}
	}

	var githubToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["github_token"].(string); ok && v != "" {
			githubToken = v
		}
	}
	// Fallback to storage if metadata is missing github_token
	if githubToken == "" {
		if storage, ok := auth.Storage.(*copilotauth.CopilotTokenStorage); ok && storage != nil {
			githubToken = storage.GitHubToken
		}
	}

	if githubToken == "" {
		log.Debug("copilot executor: no github_token in metadata, skipping refresh")
		return auth, nil
	}

	authSvc := copilotauth.NewCopilotAuth(e.cfg)
	tokenResp, err := authSvc.GetCopilotToken(ctx, githubToken)
	if err != nil {
		// Classify error: auth issues get 401, transient issues get 503
		// Use structured HTTPStatusError when available, fall back to sentinel errors
		code := 503
		cause := "copilot_refresh_transient"

		switch {
		case errors.Is(err, copilotauth.ErrNoCopilotSubscription):
			code = 401
			cause = "copilot_no_subscription"
		case errors.Is(err, copilotauth.ErrAccessDenied):
			code = 401
			cause = "copilot_access_denied"
		case errors.Is(err, copilotauth.ErrNoGitHubToken):
			code = 401
			cause = "copilot_no_github_token"
		default:
			// Check for structured HTTP status code from HTTPStatusError
			if httpCode := copilotauth.StatusCode(err); httpCode != 0 {
				if httpCode == 401 || httpCode == 403 {
					code = 401
					cause = "copilot_auth_rejected"
				} else if httpCode >= 500 {
					cause = "copilot_upstream_error"
				}
			}
		}

		log.Warnf("copilot executor: token refresh failed [cause: %s]: %v", cause, err)
		return nil, statusErr{code: code, msg: fmt.Sprintf("copilot token refresh failed (%s): %v", cause, err)}
	}

	// Update in-memory cache
	e.tokenMu.Lock()
	e.tokenCache[githubToken] = &cachedToken{
		token:     tokenResp.Token,
		expiresAt: time.Unix(tokenResp.ExpiresAt, 0),
	}
	e.tokenMu.Unlock()

	// We no longer rely on metadata for token caching, but we update it
	// for the current session in case other components need it.
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["copilot_token"] = tokenResp.Token
	auth.Metadata["copilot_token_expiry"] = time.Unix(tokenResp.ExpiresAt, 0).Format(time.RFC3339)
	auth.Metadata["type"] = "copilot"

	log.Debug("Copilot token refreshed successfully")
	return auth, nil
}

// getCopilotToken retrieves the Copilot token from auth metadata, refreshing if needed.
// Returns statusErr with appropriate HTTP codes:
// - 500 for missing auth or metadata (internal state error, cause: copilot_auth_nil, copilot_metadata_nil)
// - 401 for missing copilot token (auth configuration error, cause: copilot_token_missing)
// This allows callers to distinguish internal state issues from auth configuration problems.
//
// Note on account_type: See sdk/auth/copilot.go for full precedence documentation.
// Attributes["account_type"] is the canonical runtime source; storage is only a fallback.
//
// Note on metadata: auth.Metadata is used as a runtime cache and may be updated from
// CopilotTokenStorage. Both are kept in sync when tokens are refreshed.
func (e *CopilotExecutor) getCopilotToken(ctx context.Context, auth *cliproxyauth.Auth) (string, copilotauth.AccountType, error) {
	if auth == nil {
		return "", "", statusErr{code: 500, msg: "copilot executor: auth is nil (copilot_auth_nil)"}
	}

	copilotauth.EnsureMetadataHydrated(auth)
	githubToken := copilotauth.ResolveGitHubToken(auth)
	accountType := copilotauth.ResolveAccountType(auth)

	// 1. Check Memory Cache
	if token, valid := e.getValidCachedToken(githubToken); valid {
		return token, accountType, nil
	}

	// 2. Check Metadata (Storage) Cache
	copilotToken, copilotExpiry, hasCopilotToken := copilotauth.ResolveCopilotToken(auth)
	if hasCopilotToken {
		if time.Now().Add(60 * time.Second).Before(copilotExpiry) {
			e.setCachedToken(githubToken, copilotToken, copilotExpiry)
			return copilotToken, accountType, nil
		}
	}

	// 3. Refresh if needed
	if githubToken != "" {
		if _, err := e.Refresh(ctx, auth); err == nil {
			if token, valid := e.getValidCachedToken(githubToken); valid {
				return token, accountType, nil
			}
		}
	}

	// 4. Fallback: Use cached token if strictly valid (not expired) but near expiry
	if hasCopilotToken && time.Now().Before(copilotExpiry) {
		return copilotToken, accountType, nil
	}

	return "", accountType, statusErr{code: 401, msg: "no valid token available"}
}

func (e *CopilotExecutor) getValidCachedToken(githubToken string) (string, bool) {
	e.tokenMu.RLock()
	defer e.tokenMu.RUnlock()
	if cached, ok := e.tokenCache[githubToken]; ok {
		if time.Now().Add(60 * time.Second).Before(cached.expiresAt) {
			return cached.token, true
		}
	}
	return "", false
}

func (e *CopilotExecutor) setCachedToken(githubToken, token string, expiresAt time.Time) {
	e.tokenMu.Lock()
	defer e.tokenMu.Unlock()
	e.tokenCache[githubToken] = &cachedToken{
		token:     token,
		expiresAt: expiresAt,
	}
}

// CountTokens provides a token count estimate for Copilot models.
//
// This method uses the Codex/OpenAI tokenizer (via tokenizerForCodexModel) as an
// approximation for Copilot models. Since Copilot routes requests to various
// underlying models, the token counts are best-effort
// estimates rather than exact billing equivalents.
//
// If a Copilot-specific tokenizer becomes available in the future, it can be
// swapped in by replacing the tokenizerForCodexModel call below.
func (e *CopilotExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiModel := stripCopilotPrefix(req.Model)

	// Copilot uses OpenAI models, so we can reuse the OpenAI tokenizer logic
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, apiModel, bytes.Clone(req.Payload), false)

	// Use tiktoken for token counting via tokenizerForCodexModel helper.
	// This provides OpenAI-compatible token estimates.
	enc, err := tokenizerForCodexModel(apiModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("copilot executor: tokenizer init failed: %w", err)
	}

	// Extract messages and count tokens
	var textParts []string
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			content := msg.Get("content")
			if content.Type == gjson.String {
				textParts = append(textParts, strings.TrimSpace(content.String()))
			} else if content.IsArray() {
				for _, part := range content.Array() {
					if part.Get("type").String() == "text" {
						textParts = append(textParts, strings.TrimSpace(part.Get("text").String()))
					}
				}
			}
		}
	}

	text := strings.Join(textParts, "\n")
	count, err := enc.Count(text)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("copilot executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"usage":{"input_tokens":%d,"output_tokens":0}}`, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, int64(count), []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil

}
// copilotStatusErr creates a statusErr with appropriate retry timing for Copilot.

// EvictCopilotModelCache clears cached model data for a given auth ID.
func EvictCopilotModelCache(_ string) {}

// FetchModels returns the static Copilot model list.
func (e *CopilotExecutor) FetchModels(_ context.Context, _ *cliproxyauth.Auth, _ *config.Config) []*registry.ModelInfo {
	return registry.GetCopilotModels()
}

// For 429 errors, it sets a longer retry delay (30 seconds) since Copilot quota
// limits typically require more time to recover than standard rate limits.
func copilotStatusErr(code int, msg string) statusErr {
	err := statusErr{code: code, msg: msg}
	if code == 429 {
		delay := 30 * time.Second
		err.retryAfter = &delay
	}
	return err
}
