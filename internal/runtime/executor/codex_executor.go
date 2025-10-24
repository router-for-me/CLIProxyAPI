package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	copilotoauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var dataTag = []byte("data:")

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
    identifier string
    cfg        *config.Config
}

// NewCodexExecutor keeps upstream-compatible constructor, defaulting identifier to "codex".
func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{identifier: "codex", cfg: cfg} }

// NewCodexExecutorWithID allows custom identifier while preserving compatibility.
func NewCodexExecutorWithID(cfg *config.Config, identifier string) *CodexExecutor {
    if identifier == "" {
        identifier = "codex"
    }
    return &CodexExecutor{identifier: identifier, cfg: cfg}
}

func (e *CodexExecutor) Identifier() string { return e.identifier }

func (e *CodexExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := codexCreds(auth)

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	// Special-case Copilot: call Copilot chat/completions with Bearer token and required headers
	if e.Identifier() == "copilot" {
		base := baseURL
		if base == "" || strings.Contains(base, "chatgpt.com") {
			base = "https://api.githubcopilot.com"
		}
		base = strings.TrimSuffix(base, "/")
		base = strings.TrimSuffix(base, "/backend-api/codex")
		url := base + "/chat/completions"
		// Use original OpenAI JSON; force non-stream
		body := bytes.Clone(opts.OriginalRequest)
		body, _ = sjson.SetBytes(body, "stream", false)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil { return resp, err }
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		if strings.TrimSpace(apiKey) != "" { httpReq.Header.Set("Authorization", "Bearer "+apiKey) }
		// Minimal Copilot headers (aligning with official clients)
		httpReq.Header.Set("user-agent", "GitHubCopilotChat/0.26.7")
		httpReq.Header.Set("editor-version", "vscode/1.0")
		httpReq.Header.Set("editor-plugin-version", "copilot-chat/0.26.7")
		httpReq.Header.Set("openai-intent", "conversation-panel")
		httpReq.Header.Set("x-github-api-version", "2025-04-01")
		httpReq.Header.Set("x-request-id", uuid.NewString())
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier()})
		httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, err := httpClient.Do(httpReq)
		if err != nil { recordAPIResponseError(ctx, e.cfg, err); return resp, err }
		defer func() { _ = httpResp.Body.Close() }()
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			appendAPIResponseChunk(ctx, e.cfg, b)
			return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
		}
		data, err := io.ReadAll(httpResp.Body)
		if err != nil { recordAPIResponseError(ctx, e.cfg, err); return resp, err }
		appendAPIResponseChunk(ctx, e.cfg, data)
		resp = cliproxyexecutor.Response{Payload: data}
		return resp, nil
	}
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	if util.InArray([]string{"gpt-5", "gpt-5-minimal", "gpt-5-low", "gpt-5-medium", "gpt-5-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5")
		switch req.Model {
		case "gpt-5-minimal":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "minimal")
		case "gpt-5-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	} else if util.InArray([]string{"gpt-5-codex", "gpt-5-codex-low", "gpt-5-codex-medium", "gpt-5-codex-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5-codex")
		switch req.Model {
		case "gpt-5-codex-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-codex-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	}

	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")

    url := strings.TrimSuffix(baseURL, "/") + "/responses"
    httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey)
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
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, safeErrorPreview(b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		line = bytes.TrimSpace(line[5:])
		if gjson.GetBytes(line, "type").String() != "response.completed" {
			continue
		}

		if detail, ok := parseCodexUsage(line); ok {
			reporter.publish(ctx, detail)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, line, &param)
		resp = cliproxyexecutor.Response{Payload: []byte(out)}
		return resp, nil
	}
	err = statusErr{code: 408, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	return resp, err
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseURL := codexCreds(auth)

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	if util.InArray([]string{"gpt-5", "gpt-5-minimal", "gpt-5-low", "gpt-5-medium", "gpt-5-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5")
		switch req.Model {
		case "gpt-5-minimal":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "minimal")
		case "gpt-5-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	} else if util.InArray([]string{"gpt-5-codex", "gpt-5-codex-low", "gpt-5-codex-medium", "gpt-5-codex-high"}, req.Model) {
		body, _ = sjson.SetBytes(body, "model", "gpt-5-codex")
		switch req.Model {
		case "gpt-5-codex-low":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "low")
		case "gpt-5-codex-medium":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "medium")
		case "gpt-5-codex-high":
			body, _ = sjson.SetBytes(body, "reasoning.effort", "high")
		}
	}

	body, _ = sjson.DeleteBytes(body, "previous_response_id")

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey)
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
            log.Errorf("codex executor: close response body error: %v", errClose)
        }
        if readErr != nil {
            recordAPIResponseError(ctx, e.cfg, readErr)
            return nil, readErr
        }
        appendAPIResponseChunk(ctx, e.cfg, data)
        log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, safeErrorPreview(data))
        err = statusErr{code: httpResp.StatusCode, msg: string(data)}
        return nil, err
    }
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		buf := make([]byte, 20_971_520)
		scanner.Buffer(buf, 20_971_520)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if gjson.GetBytes(data, "type").String() == "response.completed" {
					if detail, ok := parseCodexUsage(data); ok {
						reporter.publish(ctx, detail)
					}
				}
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
	}()
	return stream, nil
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte{}}, fmt.Errorf("not implemented")
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}

	// Copilot branch: use GitHub device-flow token to fetch new copilot token
	if e.Identifier() == "copilot" {
		var ghPAT string
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["github_access_token"].(string); ok {
				ghPAT = strings.TrimSpace(v)
			}
		}
		if ghPAT == "" {
			// nothing to do
			return auth, nil
		}
		apiBase := strings.TrimSuffix(e.cfg.Copilot.GitHubAPIBaseURL, "/")
		if apiBase == "" {
			apiBase = strings.TrimSuffix(copilotoauth.DefaultGitHubAPIBaseURL, "/")
		}
		url := apiBase + copilotoauth.DefaultCopilotTokenPath
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "token "+ghPAT)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "cli-proxy-copilot")
		req.Header.Set("OpenAI-Intent", "copilot-cli-refresh")
		req.Header.Set("Editor-Plugin-Name", "cli-proxy")
		req.Header.Set("Editor-Plugin-Version", "1.0.0")
		req.Header.Set("Editor-Version", "cli/1.0")
		req.Header.Set("X-GitHub-Api-Version", "2023-07-07")
		httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		resp, err := httpClient.Do(req)
		if err != nil { return nil, err }
		defer func(){ _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(resp.Body)
			return nil, statusErr{code: resp.StatusCode, msg: string(b)}
		}
		var out struct{ Token string `json:"token"`; ExpiresAt int64 `json:"expires_at"`; RefreshIn int `json:"refresh_in"` }
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
		if auth.Metadata == nil { auth.Metadata = make(map[string]any) }
		auth.Metadata["access_token"] = out.Token
		auth.Metadata["expires_at"] = out.ExpiresAt
		auth.Metadata["refresh_in"] = out.RefreshIn
		auth.Metadata["expired"] = time.Now().Add(time.Duration(out.RefreshIn) * time.Second).Format(time.RFC3339)
		auth.Metadata["type"] = "copilot"
		auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
		return auth, nil
	}

	// Default (Codex/OpenAI) refresh using refresh_token
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuth(e.cfg)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

// safeErrorPreview returns a redacted description of an error payload without exposing
// user content. It preserves the original log format placeholder while providing
// minimal diagnostics.
func safeErrorPreview(b []byte) string {
    if len(b) == 0 {
        return "[empty]"
    }
    // Replace body with a redacted marker and length to aid debugging without content leakage.
    return fmt.Sprintf("[redacted,len=%d]", len(b))
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
    var cache codexCache
    if from == "claude" {
        userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
        if userIDResult.Exists() {
            var hasKey bool
            key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
            if cache, hasKey = codexCacheMap[key]; !hasKey || cache.Expire.Before(time.Now()) {
                cache = codexCache{
                    ID:     uuid.New().String(),
                    Expire: time.Now().Add(1 * time.Hour),
                }
                codexCacheMap[key] = cache
            }
        }
    }

    rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
    if err != nil {
        return nil, err
    }
    httpReq.Header.Set("Conversation_id", cache.ID)
    httpReq.Header.Set("Session_id", cache.ID)
    return httpReq, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string) {
	r.Header.Set("Content-Type", "application/json")
	// Use Authorization Bearer for all providers including copilot
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "Version", "0.21.0")
	misc.EnsureHeader(r.Header, ginHeaders, "Openai-Beta", "responses=experimental")
	misc.EnsureHeader(r.Header, ginHeaders, "Session_id", uuid.NewString())

	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if !isAPIKey {
		r.Header.Set("Originator", "codex_cli_rs")
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
    if a == nil {
        return "", ""
    }
    // Prefer attributes when explicitly provided (e.g., api_key/base_url via management API)
    if a.Attributes != nil {
        apiKey = a.Attributes["api_key"]
        baseURL = a.Attributes["base_url"]
    }
    // Fallback to persisted metadata from auth JSON
    if a.Metadata != nil {
        if apiKey == "" {
            if v, ok := a.Metadata["access_token"].(string); ok {
                apiKey = v
            }
        }
        // Some flows persist base_url into metadata rather than attributes.
        if baseURL == "" {
            if v, ok := a.Metadata["base_url"].(string); ok {
                baseURL = v
            }
        }
    }
    // Copilot-specific convenience: derive base_url from access_token when absent.
    // The Copilot token often embeds a "proxy-ep=" hint (e.g., proxy.individual.githubcopilot.com).
    // We map it to "https://<proxy-ep>/backend-api/codex" to keep existing routing behavior.
    if strings.EqualFold(a.Provider, "copilot") && baseURL == "" && strings.TrimSpace(apiKey) != "" {
        if derived := deriveCopilotBaseFromToken(apiKey); derived != "" {
            baseURL = derived
        }
    }
    return
}

// deriveCopilotBaseFromToken extracts proxy endpoint from a Copilot access_token when present.
// Example token fragment: "...;proxy-ep=proxy.individual.githubcopilot.com;..."
// Returns: "https://<proxy-ep>/backend-api/codex" or empty string when not derivable.
func deriveCopilotBaseFromToken(tok string) string {
    tok = strings.TrimSpace(tok)
    if tok == "" {
        return ""
    }
    // Quick path: look for "proxy-ep=" marker
    const marker = "proxy-ep="
    if idx := strings.Index(tok, marker); idx >= 0 {
        rest := tok[idx+len(marker):]
        // end at next semicolon or string end
        if end := strings.IndexByte(rest, ';'); end >= 0 {
            rest = rest[:end]
        }
        host := strings.TrimSpace(rest)
        if host != "" {
            // If already has scheme keep it, else default to https
            if !strings.Contains(host, "://") {
                host = "https://" + host
            }
            host = strings.TrimRight(host, "/")
            if !strings.HasSuffix(host, "/backend-api/codex") {
                host += "/backend-api/codex"
            }
            return host
        }
    }
    return ""
}
