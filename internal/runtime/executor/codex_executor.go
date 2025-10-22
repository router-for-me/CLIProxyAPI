package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
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
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := codexCreds(auth)

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
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

	additionalHeaders := make(map[string]string)
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			var cache codexCache
			var hasKey bool
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			if cache, hasKey = codexCacheMap[key]; !hasKey || cache.Expire.Before(time.Now()) {
				cache = codexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				codexCacheMap[key] = cache
			}
			additionalHeaders["Conversation_id"] = cache.ID
			additionalHeaders["Session_id"] = cache.ID
			body, _ = sjson.SetBytes(body, "prompt_cache_key", cache.ID)
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	for k, v := range additionalHeaders {
		httpReq.Header.Set(k, v)
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
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, string(b))
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

	additionalHeaders := make(map[string]string)
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			var cache codexCache
			var hasKey bool
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			if cache, hasKey = codexCacheMap[key]; !hasKey || cache.Expire.Before(time.Now()) {
				cache = codexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				codexCacheMap[key] = cache
			}
			additionalHeaders["Conversation_id"] = cache.ID
			additionalHeaders["Session_id"] = cache.ID
			body, _ = sjson.SetBytes(body, "prompt_cache_key", cache.ID)
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	for k, v := range additionalHeaders {
		httpReq.Header.Set(k, v)
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
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, string(data))
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

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string) {
	r.Header.Set("Content-Type", "application/json")
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
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}
