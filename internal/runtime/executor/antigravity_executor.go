package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	antigravityBaseURL          = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityStreamPath       = "/v1internal:streamGenerateContent"
	antigravityGeneratePath     = "/v1internal:generateContent"
	antigravityModelsPath       = "/v1internal:fetchAvailableModels"
	antigravityClientID         = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret     = "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"
	defaultAntigravityAgent     = "antigravity/1.11.3 windows/amd64"
	antigravityAuthType         = "antigravity"
	refreshSkew                 = 5 * time.Minute
	streamScannerBuffer     int = 20_971_520
)

var randSource = rand.New(rand.NewSource(time.Now().UnixNano()))

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
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, false, opts.Alt)
	if errReq != nil {
		return resp, errReq
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		recordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		recordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
		err = statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		return resp, err
	}

	reporter.publish(ctx, parseAntigravityUsage(bodyBytes))
	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bodyBytes, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(converted)}
	reporter.ensurePublished(ctx)
	return resp, nil
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
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt)
	if errReq != nil {
		return nil, errReq
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		recordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Filter usage metadata for all models
			// Only retain usage statistics in the terminal chunk
			line = FilterSSEUsageMetadata(line)

			payload := jsonPayload(line)
			if payload == nil {
				continue
			}

			if detail, ok := parseAntigravityStreamUsage(payload); ok {
				reporter.publish(ctx, detail)
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bytes.Clone(payload), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		tail := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, []byte("[DONE]"), &param)
		for i := range tail {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(tail[i])}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			reporter.ensurePublished(ctx)
		}
	}()
	return stream, nil
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

	modelsURL := buildBaseURL(auth) + antigravityModelsPath
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
	if errReq != nil {
		return nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if host := resolveHost(auth); host != "" {
		httpReq.Host = host
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return nil
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return nil
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil
	}

	result := gjson.GetBytes(bodyBytes, "models")
	if !result.Exists() {
		return nil
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, len(result.Map()))
	for id := range result.Map() {
		id = modelName2Alias(id)
		if id != "" {
			models = append(models, &registry.ModelInfo{
				ID:      id,
				Object:  "model",
				Created: now,
				OwnedBy: antigravityAuthType,
				Type:    antigravityAuthType,
			})
		}
	}
	return models
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

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := buildBaseURL(auth)
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
	if host := resolveHost(auth); host != "" {
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
	if auth != nil {
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
	}
	return antigravityBaseURL
}

func resolveHost(auth *cliproxyauth.Auth) string {
	base := buildBaseURL(auth)
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

func geminiToAntigravity(modelName string, payload []byte) []byte {
	template, _ := sjson.Set(string(payload), "model", modelName)
	template, _ = sjson.Set(template, "userAgent", "antigravity")
	template, _ = sjson.Set(template, "project", generateProjectID())
	template, _ = sjson.Set(template, "requestId", generateRequestID())
	template, _ = sjson.Set(template, "request.sessionId", generateSessionID())

	template, _ = sjson.Delete(template, "request.safetySettings")
	template, _ = sjson.Set(template, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")

	gjson.Get(template, "request.contents").ForEach(func(key, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			content.Get("parts").ForEach(func(partKey, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					template, _ = sjson.Set(template, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				}
				return true
			})
		}
		return true
	})

	if strings.HasPrefix(modelName, "claude-sonnet-") {
		gjson.Get(template, "request.tools").ForEach(func(key, tool gjson.Result) bool {
			tool.Get("functionDeclarations").ForEach(func(funKey, funcDecl gjson.Result) bool {
				if funcDecl.Get("parametersJsonSchema").Exists() {
					template, _ = sjson.SetRaw(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters", key.Int(), funKey.Int()), funcDecl.Get("parametersJsonSchema").Raw)
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters.$schema", key.Int(), funKey.Int()))
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parametersJsonSchema", key.Int(), funKey.Int()))
				}
				return true
			})
			return true
		})
	}

	return []byte(template)
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateSessionID() string {
	n := randSource.Int63n(9_000_000_000_000_000_000)
	return "-" + strconv.FormatInt(n, 10)
}

func generateProjectID() string {
	adjectives := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	adj := adjectives[randSource.Intn(len(adjectives))]
	noun := nouns[randSource.Intn(len(nouns))]
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
	case "chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-3-pro-low":
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
