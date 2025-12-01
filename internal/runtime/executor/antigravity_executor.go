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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Note: This executor uses "antigravity" format for old translator compatibility.
// The old translator treats "antigravity" and "gemini-cli" identically.
// When UseCanonicalTranslator is enabled, TranslateToGeminiCLI handles the conversion.

const (
	antigravityBaseURLDaily        = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLAutopush     = "https://autopush-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd         = "https://cloudcode-pa.googleapis.com"
	antigravityStreamPath          = "/v1internal:streamGenerateContent"
	antigravityGeneratePath        = "/v1internal:generateContent"
	antigravityModelsPath          = "/v1internal:fetchAvailableModels"
	antigravityClientID            = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret        = "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"
	defaultAntigravityAgent        = "antigravity/1.11.5 windows/amd64"
	antigravityAuthType            = "antigravity"
	refreshSkew                    = 3000 * time.Second
	streamScannerBuffer        int = 20_971_520
)

// Note: We use crypto/rand via uuid package for thread-safe random generation
// instead of math/rand which requires mutex protection

// AntigravityExecutor proxies requests to the antigravity upstream.
type AntigravityExecutor struct {
	cfg *config.Config
}

// NewAntigravityExecutor constructs a new executor instance.
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

// Identifier implements ProviderExecutor.
func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *AntigravityExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

// applyThinkingMetadata applies thinking config from model suffix metadata (e.g., -reasoning, -thinking-N).
// It trusts user intent when suffix is used, even if registry doesn't have Thinking metadata.
func applyThinkingMetadata(translated []byte, metadata map[string]any, model string) []byte {
	budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(metadata)
	if !ok {
		return translated
	}
	if budgetOverride != nil && util.ModelSupportsThinking(model) {
		norm := util.NormalizeThinkingBudget(model, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiCLIThinkingConfig(translated, budgetOverride, includeOverride)
}

// Execute handles non-streaming requests via the antigravity generate endpoint.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	// Translate request: new translator if enabled, otherwise old translator
	var translated []byte
	if e.cfg != nil && e.cfg.UseCanonicalTranslator {
		var errTranslate error
		translated, errTranslate = TranslateToGeminiCLI(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata)
		if errTranslate != nil {
			return resp, fmt.Errorf("failed to translate request: %w", errTranslate)
		}
	} else {
		translated = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	}

	translated = applyThinkingMetadata(translated, req.Metadata, req.Model)

	translated = applyThinkingMetadata(translated, req.Metadata, req.Model)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, false, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return resp, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return resp, err
		}

		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			err = errRead
			return resp, err
		}
		appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
			return resp, err
		}

		reporter.publish(ctx, parseAntigravityUsage(bodyBytes))

		// Use new translator if enabled (no fallback)
		if e.cfg != nil && e.cfg.UseCanonicalTranslator {
			translatedResp, errTranslate := TranslateGeminiCLIResponseNonStream(e.cfg, from, bodyBytes, req.Model)
			if errTranslate != nil {
				return resp, fmt.Errorf("failed to translate response: %w", errTranslate)
			}
			if translatedResp != nil {
				resp = cliproxyexecutor.Response{Payload: translatedResp}
			} else {
				// New translator returned nil - pass through raw response
				resp = cliproxyexecutor.Response{Payload: bodyBytes}
			}
			reporter.ensurePublished(ctx)
			return resp, nil
		}

		// Old translator (only when UseCanonicalTranslator is disabled)
		var param any
		converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bodyBytes, &param)
		resp = cliproxyexecutor.Response{Payload: []byte(converted)}
		reporter.ensurePublished(ctx)
		return resp, nil
	}

	switch {
	case lastStatus != 0:
		err = statusErr{code: lastStatus, msg: string(lastBody)}
	case lastErr != nil:
		err = lastErr
	default:
		err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
	return resp, err
}

// ExecuteStream handles streaming requests via the antigravity upstream.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	ctx = context.WithValue(ctx, "alt", "")

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	// Translate request: new translator if enabled, otherwise old translator
	var translated []byte
	if e.cfg != nil && e.cfg.UseCanonicalTranslator {
		var errTranslate error
		translated, errTranslate = TranslateToGeminiCLI(e.cfg, from, req.Model, bytes.Clone(req.Payload), true, req.Metadata)
		if errTranslate != nil {
			return nil, fmt.Errorf("failed to translate request: %w", errTranslate)
		}
	} else {
		translated = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	}

	translated = applyThinkingMetadata(translated, req.Metadata, req.Model)

	translated = applyThinkingMetadata(translated, req.Metadata, req.Model)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return nil, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return nil, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				lastStatus = 0
				lastBody = nil
				lastErr = errRead
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errRead
				return nil, err
			}
			appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
			return nil, err
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		stream = out
		go func(resp *http.Response) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
			}()
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(nil, streamScannerBuffer)

			// Initialize streaming state (only if new translator is enabled)
			useNewTranslator := e.cfg != nil && e.cfg.UseCanonicalTranslator
			var streamState *GeminiCLIStreamState
			if useNewTranslator {
				// Create state with schema context from original request for tool call normalization
				streamState = NewAntigravityStreamState(opts.OriginalRequest)
			}
			messageID := "chatcmpl-" + req.Model

			var param any
			for scanner.Scan() {
				line := scanner.Bytes()
				appendAPIResponseChunk(ctx, e.cfg, line)

				// Filter usage metadata for all models
				// Only retain usage statistics in the terminal chunk
				filteredLine := FilterSSEUsageMetadata(line)

				payload := jsonPayload(filteredLine)
				if payload != nil {
					if detail, ok := parseAntigravityStreamUsage(payload); ok {
						reporter.publish(ctx, detail)
					}
				}

				// Use new translator if enabled (no fallback)
				if useNewTranslator {
					translatedChunks, errTranslate := TranslateGeminiCLIResponseStream(e.cfg, from, bytes.Clone(line), req.Model, messageID, streamState)
					if errTranslate != nil {
						out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("failed to translate chunk: %w", errTranslate)}
						continue
					}
					for _, chunk := range translatedChunks {
						out <- cliproxyexecutor.StreamChunk{Payload: chunk}
					}
					continue
				}

				// Old translator (only when UseCanonicalTranslator is disabled)
				if payload == nil {
					continue
				}
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bytes.Clone(payload), &param)
				for i := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
				}
			}

			// Send [DONE] only if using old translator (new translator handles finish events internally)
			if !useNewTranslator {
				tail := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, []byte("[DONE]"), &param)
				for i := range tail {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(tail[i])}
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				recordAPIResponseError(ctx, e.cfg, errScan)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errScan}
			} else {
				reporter.ensurePublished(ctx)
			}
		}(httpResp)
		return stream, nil
	}

	switch {
	case lastStatus != 0:
		err = statusErr{code: lastStatus, msg: string(lastBody)}
	case lastErr != nil:
		err = lastErr
	default:
		err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
	return nil, err
}

// Refresh refreshes the OAuth token using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

// CountTokens is not supported for the antigravity provider.
func (e *AntigravityExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported"}
}

// FetchAntigravityModels retrieves available models using the supplied auth.
func FetchAntigravityModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := &AntigravityExecutor{cfg: cfg}
	token, updatedAuth, errToken := exec.ensureAccessToken(ctx, auth)
	if errToken != nil || token == "" {
		return nil
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	for idx, baseURL := range baseURLs {
		modelsURL := baseURL + antigravityModelsPath
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
		if errReq != nil {
			return nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		if host := resolveHost(baseURL); host != "" {
			httpReq.Host = host
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}

		result := gjson.GetBytes(bodyBytes, "models")
		if !result.Exists() {
			return nil
		}

		now := time.Now().Unix()
		models := make([]*registry.ModelInfo, 0, len(result.Map()))

		// Build a lookup map from static Gemini model definitions to inherit
		// Thinking support and other metadata. Antigravity uses Google Cloud Code API
		// which serves the same Gemini models, so we reuse GetGeminiCLIModels() definitions.
		staticModels := registry.GetGeminiCLIModels()
		staticModelMap := make(map[string]*registry.ModelInfo, len(staticModels))
		for _, m := range staticModels {
			if m != nil {
				staticModelMap[m.ID] = m
			}
		}

		for id := range result.Map() {
			id = modelName2Alias(id)
			if id != "" {
				modelInfo := &registry.ModelInfo{
					ID:          id,
					Name:        id,
					Description: id,
					DisplayName: id,
					Version:     id,
					Object:      "model",
					Created:     now,
					OwnedBy:     antigravityAuthType,
					Type:        antigravityAuthType,
				}

				// Inherit metadata from static model definitions if available
				if staticModel, ok := staticModelMap[id]; ok {
					modelInfo.Description = staticModel.Description
					modelInfo.DisplayName = staticModel.DisplayName
					modelInfo.Version = staticModel.Version
					modelInfo.InputTokenLimit = staticModel.InputTokenLimit
					modelInfo.OutputTokenLimit = staticModel.OutputTokenLimit
					modelInfo.SupportedGenerationMethods = staticModel.SupportedGenerationMethods
					modelInfo.Thinking = staticModel.Thinking
				}

				models = append(models, modelInfo)
			}
		}
		return models
	}
	return nil
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	if auth == nil {
		return "", nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry := tokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(refreshSkew)) {
		return accessToken, nil, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return "", nil, errRefresh
	}
	return metaStringValue(updated.Metadata, "access_token"), updated, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "missing refresh token"}
	}

	form := url.Values{}
	form.Set("client_id", antigravityClientID)
	form.Set("client_secret", antigravityClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("User-Agent", defaultAntigravityAgent)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return auth, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return auth, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return auth, statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return auth, errUnmarshal
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	auth.Metadata["timestamp"] = time.Now().UnixMilli()
	auth.Metadata["expired"] = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	return auth, nil
}

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	payload = geminiToAntigravity(modelName, payload)
	payload, _ = sjson.SetBytes(payload, "model", alias2ModelName(modelName))

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

func tokenExpiry(metadata map[string]any) time.Time {
	if metadata == nil {
		return time.Time{}
	}
	if expStr, ok := metadata["expired"].(string); ok {
		expStr = strings.TrimSpace(expStr)
		if expStr != "" {
			if parsed, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
				return parsed
			}
		}
	}
	expiresIn, hasExpires := int64Value(metadata["expires_in"])
	tsMs, hasTimestamp := int64Value(metadata["timestamp"])
	if hasExpires && hasTimestamp {
		return time.Unix(0, tsMs*int64(time.Millisecond)).Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Time{}
}

func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i, true
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		if i, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); errParse == nil {
			return i, true
		}
	}
	return 0, false
}

func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLAutopush
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				return ua
			}
		}
		if auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				return strings.TrimSpace(ua)
			}
		}
	}
	return defaultAntigravityAgent
}

func antigravityBaseURLFallbackOrder(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		antigravityBaseURLDaily,
		antigravityBaseURLAutopush,
		// antigravityBaseURLProd,
	}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

// geminiToAntigravity converts Gemini CLI format to Antigravity format.
// Optimized: single json.Unmarshal → in-memory modifications → single json.Marshal
func geminiToAntigravity(modelName string, payload []byte) []byte {
	var root map[string]interface{}
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}

	root["model"] = modelName
	root["userAgent"] = "antigravity"
	root["project"] = generateProjectID()
	root["requestId"] = generateRequestID()

	request, _ := root["request"].(map[string]interface{})
	if request == nil {
		request = make(map[string]interface{})
		root["request"] = request
	}
	request["sessionId"] = generateSessionID()
	delete(request, "safetySettings")

	if genConfig, ok := request["generationConfig"].(map[string]interface{}); ok {
		delete(genConfig, "maxOutputTokens")

		// TODO: Fix GPT-OSS thinking mode - model gets stuck in infinite planning loops
		// GPT-OSS models have issues with thinking mode - they repeatedly generate
		// the same plan without executing actions. Temporarily disable thinking.
		// See README_Fork.md "Antigravity Provider — UI Client Testing" for details.
		if strings.HasPrefix(modelName, "gpt-oss") {
			delete(genConfig, "thinkingConfig")
		} else if !strings.HasPrefix(modelName, "gemini-3-") {
			if tc, ok := genConfig["thinkingConfig"].(map[string]interface{}); ok {
				if _, has := tc["thinkingLevel"]; has {
					delete(tc, "thinkingLevel")
					tc["thinkingBudget"] = -1
				}
			}
		}
	}

	// Clean tools for Claude models
	if strings.Contains(modelName, "claude") {
		if tools, ok := request["tools"].([]interface{}); ok {
			for _, tool := range tools {
				if tm, ok := tool.(map[string]interface{}); ok {
					if fds, ok := tm["functionDeclarations"].([]interface{}); ok {
						for _, fd := range fds {
							if fdm, ok := fd.(map[string]interface{}); ok {
								var schema map[string]interface{}
								if s, ok := fdm["parametersJsonSchema"].(map[string]interface{}); ok {
									schema = s
								} else if s, ok := fdm["parameters"].(map[string]interface{}); ok {
									schema = s
								}
								if schema != nil {
									delete(schema, "$schema")
									cleanSchemaForClaude(schema)
									fdm["parameters"] = schema
									delete(fdm, "parametersJsonSchema")
								}
							}
						}
					}
				}
			}
		}
	}

	if result, err := json.Marshal(root); err == nil {
		return result
	}
	return payload
}

// cleanSchemaForClaude recursively removes JSON Schema fields that Claude API doesn't support.
// Claude uses JSON Schema draft 2020-12 but doesn't support all features.
// See: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
func cleanSchemaForClaude(schema map[string]interface{}) {
	// CRITICAL: Convert "const" to "enum" before deletion
	// Claude doesn't support "const" but supports "enum" with single value
	// This preserves discriminator semantics (e.g., Pydantic Literal types)
	if constVal, ok := schema["const"]; ok {
		schema["enum"] = []interface{}{constVal}
		delete(schema, "const")
	}

	// Fields that Claude doesn't support in JSON Schema
	// Based on JSON Schema draft 2020-12 compatibility
	unsupportedFields := []string{
		// Composition keywords that Claude doesn't support
		"anyOf", "oneOf", "allOf", "not",
		// Snake_case variants
		"any_of", "one_of", "all_of",
		// Reference keywords
		"$ref", "$defs", "definitions", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor",
		// Schema metadata
		"$schema", "$vocabulary", "$comment",
		// Conditional keywords
		"if", "then", "else", "dependentSchemas", "dependentRequired",
		// Unevaluated keywords
		"unevaluatedItems", "unevaluatedProperties",
		// Content keywords
		"contentEncoding", "contentMediaType", "contentSchema",
		// Deprecated keywords
		"dependencies",
		// Array validation keywords that may not be supported
		"minItems", "maxItems", "uniqueItems", "minContains", "maxContains",
		// String validation keywords that may cause issues
		"minLength", "maxLength", "pattern", "format",
		// Number validation keywords
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		// Object validation keywords that may cause issues
		"minProperties", "maxProperties",
		// Default values - Claude officially doesn't support in input_schema
		"default",
	}

	for _, field := range unsupportedFields {
		delete(schema, field)
	}

	// Recursively clean nested objects in properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaude(propMap)
				properties[key] = propMap
			}
		}
	}

	// Clean items - can be object or array
	if items := schema["items"]; items != nil {
		switch v := items.(type) {
		case map[string]interface{}:
			cleanSchemaForClaude(v)
		case []interface{}:
			for i, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					cleanSchemaForClaude(itemMap)
					v[i] = itemMap
				}
			}
		}
	}

	// Handle prefixItems (tuple validation)
	if prefixItems, ok := schema["prefixItems"].([]interface{}); ok {
		for i, item := range prefixItems {
			if itemMap, ok := item.(map[string]interface{}); ok {
				cleanSchemaForClaude(itemMap)
				prefixItems[i] = itemMap
			}
		}
	}

	// Handle additionalProperties if it's an object
	if addProps, ok := schema["additionalProperties"].(map[string]interface{}); ok {
		cleanSchemaForClaude(addProps)
	}

	// Handle patternProperties
	if patternProps, ok := schema["patternProperties"].(map[string]interface{}); ok {
		for key, prop := range patternProps {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaude(propMap)
				patternProps[key] = propMap
			}
		}
	}

	// Handle propertyNames
	if propNames, ok := schema["propertyNames"].(map[string]interface{}); ok {
		cleanSchemaForClaude(propNames)
	}

	// Handle contains
	if contains, ok := schema["contains"].(map[string]interface{}); ok {
		cleanSchemaForClaude(contains)
	}
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateSessionID() string {
	// Use uuid for thread-safe random generation instead of math/rand
	// Format: negative number string (mimics original behavior)
	uuidStr := uuid.NewString()
	// Convert first 16 hex chars to int64-like string
	return "-" + uuidStr[:8] + uuidStr[9:13] + uuidStr[14:18]
}

func generateProjectID() string {
	adjectives := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	// Use uuid bytes for thread-safe random selection
	uuidBytes := []byte(uuid.NewString())
	adj := adjectives[int(uuidBytes[0])%len(adjectives)]
	noun := nouns[int(uuidBytes[1])%len(nouns)]
	randomPart := strings.ToLower(uuid.NewString())[:5]
	return adj + "-" + noun + "-" + randomPart
}

func modelName2Alias(modelName string) string {
	switch modelName {
	case "rev19-uic3-1p":
		return "gemini-2.5-computer-use-preview-10-2025"
	case "gemini-3-pro-image":
		return "gemini-3-pro-image-preview"
	case "gemini-3-pro-high":
		return "gemini-3-pro-preview"
	case "claude-sonnet-4-5":
		return "gemini-claude-sonnet-4-5"
	case "claude-sonnet-4-5-thinking":
		return "gemini-claude-sonnet-4-5-thinking"
	case "chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-3-pro-low", "gemini-2.5-pro":
		return ""
	default:
		return modelName
	}
}

func alias2ModelName(modelName string) string {
	switch modelName {
	case "gemini-2.5-computer-use-preview-10-2025":
		return "rev19-uic3-1p"
	case "gemini-3-pro-image-preview":
		return "gemini-3-pro-image"
	case "gemini-3-pro-preview":
		return "gemini-3-pro-high"
	case "gemini-claude-sonnet-4-5":
		return "claude-sonnet-4-5"
	case "gemini-claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-5-thinking"
	default:
		return modelName
	}
}
