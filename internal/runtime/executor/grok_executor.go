package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	grokauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/grok"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// GrokExecutor is a stateless executor for the xAI Grok API using OpenAI-compatible
// chat completions. It is a direct sibling of KimiExecutor and does NOT embed
// OpenAICompatExecutor — doing so would cause inner resolveCredentials() calls to
// read auth.Attributes["base_url"], which is absent for OAuth-flavored Grok auths,
// producing HTTP 401 "missing provider baseURL" errors.
type GrokExecutor struct {
	cfg     *config.Config
	baseURL string // overridable in tests; production code must NOT read from auth.Attributes

	// grokClientFactory is overridable in tests to redirect token-refresh requests
	// to a fake server. Production callers must leave this nil; NewGrokExecutor
	// sets the default.
	grokClientFactory func(cfg *config.Config) *grokauth.GrokAuth
}

// NewGrokExecutor creates a new Grok executor with the hardcoded xAI base URL.
func NewGrokExecutor(cfg *config.Config) *GrokExecutor {
	return &GrokExecutor{
		cfg:               cfg,
		baseURL:           grokauth.APIBaseURL,
		grokClientFactory: grokauth.NewGrokAuth,
	}
}

// Identifier returns the executor identifier.
func (e *GrokExecutor) Identifier() string { return "grok" }

// grokCreds extracts the access token from auth.
// The synthesizer (synthesizeGrokAuth) stores access_token in Attributes.
// Metadata is checked first to support future OAuth-flow auths that may store
// tokens there (mirroring kimiCreds fallback ordering).
func grokCreds(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	// Check metadata first (future OAuth flow may store tokens here)
	if a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	// Primary path: config synthesizer stores tokens in Attributes
	if a.Attributes != nil {
		if v := a.Attributes["access_token"]; strings.TrimSpace(v) != "" {
			return v
		}
		if v := a.Attributes["api_key"]; strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// applyGrokHeaders sets required headers for Grok API requests.
func applyGrokHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

// PrepareRequest injects Grok credentials into the outgoing HTTP request.
func (e *GrokExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := grokCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Grok credentials and executes the request.
func (e *GrokExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("grok executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat completion request to Grok.
// The base URL is hardcoded to grokauth.APIBaseURL and is NEVER read from
// auth.Attributes["base_url"] — this is the critical regression guard against
// the v1 architectural defect where OpenAICompatExecutor.resolveCredentials()
// would 401 on OAuth auths that have no base_url attribute.
func (e *GrokExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := xaiImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, endpointPath)
	}
	if xaiIsVideoRequest(opts) {
		return e.executeVideos(ctx, auth, req, opts)
	}

	from := opts.SourceFormat
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	token := grokCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "grok", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel, requestPath)

	// Base URL is hardcoded — not read from auth.Attributes.
	url := e.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyGrokHeaders(httpReq, token, false)
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
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("grok executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *GrokExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	if endpointPath == "" {
		endpointPath = xaiDefaultImageEndpointPath
	}
	requestURL := strings.TrimSuffix(e.baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(req.Payload))
	if err != nil {
		return resp, err
	}
	applyGrokHeaders(httpReq, grokCreds(auth), false)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	e.recordGrokRequest(ctx, auth, requestURL, httpReq.Header.Clone(), req.Payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("grok executor: close image response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}
	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

func (e *GrokExecutor) executeVideos(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	method := http.MethodPost
	endpointPath := xaiVideosGenerationsPath
	var body io.Reader = bytes.NewReader(req.Payload)

	switch path := xaiVideoEndpointPath(opts); path {
	case xaiVideosGenerationsPath, xaiVideosEditsPath, xaiVideosExtensionsPath:
		endpointPath = path
	default:
		if requestID := strings.TrimSpace(gjson.GetBytes(req.Payload, "request_id").String()); requestID != "" {
			method = http.MethodGet
			endpointPath = xaiVideosPath + "/" + url.PathEscape(requestID)
			body = nil
		}
	}

	requestURL := strings.TrimSuffix(e.baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return resp, err
	}
	applyGrokHeaders(httpReq, grokCreds(auth), false)
	if method == http.MethodPost {
		key := xaiMetadataString(opts.Metadata, xaiIdempotencyKeyMetaKey)
		if key == "" && opts.Headers != nil {
			key = strings.TrimSpace(opts.Headers.Get("x-idempotency-key"))
		}
		if key != "" {
			httpReq.Header.Set("x-idempotency-key", key)
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	e.recordGrokRequest(ctx, auth, requestURL, httpReq.Header.Clone(), req.Payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("grok executor: close video response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return resp, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}
	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

func (e *GrokExecutor) recordGrokRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

// ExecuteStream performs a streaming chat completion request to Grok.
// Like Execute, the base URL is hardcoded and never read from auth.Attributes.
func (e *GrokExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	token := grokCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "grok", e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel, requestPath)

	// Base URL is hardcoded — not read from auth.Attributes.
	url := e.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyGrokHeaders(httpReq, token, true)
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
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("grok executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("grok executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576) // 1MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens is not supported by the Grok executor.
func (e *GrokExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("grok executor: CountTokens is not supported")
}

// xaiModelsResponse is the OpenAI-shaped list response returned by
// GET https://api.x.ai/v1/models.
type xaiModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// FetchAvailableModels queries api.x.ai/v1/models with the bearer token stored
// in auth and returns the discovered model list as []*registry.ModelInfo.
//
// If the access token is expiring within grokauth.AccessTokenRefreshSkew, a
// proactive token refresh is attempted before calling the upstream endpoint.
// Failures during refresh are logged but do NOT abort the fetch — the current
// (possibly soon-to-expire) token is used as a fallback.
func (e *GrokExecutor) FetchAvailableModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	if auth == nil {
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: auth is nil")
	}

	// Proactively refresh the token if it expires within the skew window.
	if expiry, ok := auth.ExpirationTime(); ok {
		if time.Until(expiry) < grokauth.AccessTokenRefreshSkew {
			if refreshed, err := e.Refresh(ctx, auth); err != nil {
				log.Warnf("grok executor: FetchAvailableModels: token refresh failed, proceeding with current token: %v", err)
			} else {
				auth = refreshed
			}
		}
	}

	token := grokCreds(auth)
	url := e.baseURL + "/models"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: build request: %w", err)
	}
	applyGrokHeaders(httpReq, token, false)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: HTTP request: %w", err)
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("grok executor: FetchAvailableModels: close response body: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: upstream status %d: %s", httpResp.StatusCode, strings.TrimSpace(string(b)))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: read body: %w", err)
	}

	var resp xaiModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("grok executor: FetchAvailableModels: parse response: %w", err)
	}

	models := make([]*registry.ModelInfo, 0, len(resp.Data))
	for _, entry := range resp.Data {
		models = append(models, &registry.ModelInfo{
			ID:      entry.ID,
			Object:  entry.Object,
			Created: entry.Created,
			OwnedBy: entry.OwnedBy,
			Type:    "grok",
		})
	}
	return models, nil
}

// Refresh refreshes the Grok access token using the refresh token.
// Tokens are read from Attributes (where synthesizeGrokAuth stores them).
// After a successful refresh, both Attributes and Metadata are updated so
// that the conductor's persist() call (which skips auths with nil Metadata)
// can save the updated tokens.
func (e *GrokExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("grok executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("grok executor: auth is nil")
	}

	// Read refresh token — synthesizer stores it in Attributes.
	var refreshToken string
	if auth.Attributes != nil {
		if v := auth.Attributes["refresh_token"]; strings.TrimSpace(v) != "" {
			refreshToken = v
		}
	}
	// Also check Metadata for future OAuth-flow auths.
	if strings.TrimSpace(refreshToken) == "" {
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["refresh_token"].(string); ok && strings.TrimSpace(v) != "" {
				refreshToken = v
			}
		}
	}
	if strings.TrimSpace(refreshToken) == "" {
		// Nothing to refresh — no refresh token available.
		return auth, nil
	}

	grokClient := e.grokClientFactory(e.cfg)
	tokenResp, err := grokClient.RefreshAccessToken(ctx, auth.ID, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("grok executor: token refresh failed: %w", err)
	}

	// Update Attributes (primary storage for synthesized auths).
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Attributes["refresh_token"] = tokenResp.RefreshToken
	}
	if tokenResp.IDToken != "" {
		auth.Attributes["id_token"] = tokenResp.IDToken
	}
	if tokenResp.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		auth.Attributes["expired"] = exp
	}

	// Initialize Metadata so the conductor's persist() does not skip this auth.
	// persist() skips auths where Metadata == nil.
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	if tokenResp.IDToken != "" {
		auth.Metadata["id_token"] = tokenResp.IDToken
	}
	if tokenResp.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		auth.Metadata["expired"] = exp
	}
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	auth.Metadata["type"] = "grok"

	// Persist to disk via Storage if this auth was loaded from a file.
	if auth.Storage != nil {
		if gs, ok := auth.Storage.(*grokauth.GrokTokenStorage); ok && gs != nil {
			gs.ApplyRefresh(tokenResp)
			if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
				if saveErr := gs.SaveTokenToFile(fileName); saveErr != nil {
					log.Warnf("grok executor: failed to save token to file %s: %v", fileName, saveErr)
				}
			}
		}
	}

	return auth, nil
}
