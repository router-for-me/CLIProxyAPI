package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	codeAssistEndpoint      = "https://cloudcode-pa.googleapis.com"
	codeAssistVersion       = "v1internal"
	geminiOauthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOauthClientSecret = "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"
)

var geminiOauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// GeminiCLIExecutor talks to the Cloud Code Assist endpoint using OAuth credentials from auth metadata.
type GeminiCLIExecutor struct {
	cfg *config.Config
}

func NewGeminiCLIExecutor(cfg *config.Config) *GeminiCLIExecutor {
	return &GeminiCLIExecutor{cfg: cfg}
}

func (e *GeminiCLIExecutor) Identifier() string { return "gemini-cli" }

func (e *GeminiCLIExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *GeminiCLIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")
	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	if hasOverride {
		basePayload = util.ApplyGeminiCLIThinkingConfig(basePayload, budgetOverride, includeOverride)
	}
	basePayload = fixGeminiCLIImageAspectRatio(req.Model, basePayload)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	projectID := strings.TrimSpace(stringValue(auth.Metadata, "project_id"))
	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	for _, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		if action == "countTokens" {
			payload = deleteJSONField(payload, "project")
			payload = deleteJSONField(payload, "model")
		} else {
			payload = setJSONField(payload, "project", projectID)
			payload = setJSONField(payload, "model", attemptModel)
		}
		payload = disableGeminiThinkingConfig(payload, attemptModel)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return cliproxyexecutor.Response{}, errTok
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, action)
		if opts.Alt != "" && action != "countTokens" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		resp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			return cliproxyexecutor.Response{}, errDo
		}
		data, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			out := sdktranslator.TranslateNonStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), payload, data, &param)
			return cliproxyexecutor.Response{Payload: []byte(out)}, nil
		}
		lastStatus = resp.StatusCode
		lastBody = append([]byte(nil), data...)
		if resp.StatusCode != 429 {
			break
		}
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
	}
	return cliproxyexecutor.Response{}, statusErr{code: lastStatus, msg: string(lastBody)}
}

func (e *GeminiCLIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return nil, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")
	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	if hasOverride {
		basePayload = util.ApplyGeminiCLIThinkingConfig(basePayload, budgetOverride, includeOverride)
	}
	basePayload = fixGeminiCLIImageAspectRatio(req.Model, basePayload)

	projectID := strings.TrimSpace(stringValue(auth.Metadata, "project_id"))

	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	for _, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		payload = setJSONField(payload, "project", projectID)
		payload = setJSONField(payload, "model", attemptModel)
		payload = disableGeminiThinkingConfig(payload, attemptModel)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return nil, errTok
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "streamGenerateContent")
		if opts.Alt == "" {
			url = url + "?alt=sse"
		} else {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return nil, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "text/event-stream")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		resp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			return nil, errDo
		}
		recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, errRead := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				return nil, errRead
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			lastStatus = resp.StatusCode
			lastBody = append([]byte(nil), data...)
			log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, string(data))
			if resp.StatusCode == 429 {
				continue
			}
			return nil, statusErr{code: resp.StatusCode, msg: string(data)}
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		go func(resp *http.Response, reqBody []byte, attempt string) {
			defer close(out)
			defer func() { _ = resp.Body.Close() }()
			if opts.Alt == "" {
				scanner := bufio.NewScanner(resp.Body)
				buf := make([]byte, 20_971_520)
				scanner.Buffer(buf, 20_971_520)
				var param any
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)
					if detail, ok := parseGeminiCLIStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
					if bytes.HasPrefix(line, dataTag) {
						segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone(line), &param)
						for i := range segments {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
						}
					}
				}

				segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
				for i := range segments {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
				}
				if errScan := scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				}
				return
			}

			data, errRead := io.ReadAll(resp.Body)
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				out <- cliproxyexecutor.StreamChunk{Err: errRead}
				return
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, data, &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}

			segments = sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}
		}(resp, append([]byte(nil), payload...), attemptModel)

		return out, nil
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return nil, statusErr{code: lastStatus, msg: string(lastBody)}
}

func (e *GeminiCLIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastStatus int
	var lastBody []byte

	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	for _, attemptModel := range models {
		payload := sdktranslator.TranslateRequest(from, to, attemptModel, bytes.Clone(req.Payload), false)
		if hasOverride {
			payload = util.ApplyGeminiCLIThinkingConfig(payload, budgetOverride, includeOverride)
		}
		payload = deleteJSONField(payload, "project")
		payload = deleteJSONField(payload, "model")
		payload = disableGeminiThinkingConfig(payload, attemptModel)
		payload = fixGeminiCLIImageAspectRatio(attemptModel, payload)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return cliproxyexecutor.Response{}, errTok
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "countTokens")
		if opts.Alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		resp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			return cliproxyexecutor.Response{}, errDo
		}
		data, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			count := gjson.GetBytes(data, "totalTokens").Int()
			translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, data)
			return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
		}
		lastStatus = resp.StatusCode
		lastBody = append([]byte(nil), data...)
		if resp.StatusCode == 429 {
			continue
		}
		break
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return cliproxyexecutor.Response{}, statusErr{code: lastStatus, msg: string(lastBody)}
}

func (e *GeminiCLIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("gemini cli executor: refresh called")
	_ = ctx
	return auth, nil
}

func prepareGeminiCLITokenSource(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (oauth2.TokenSource, map[string]any, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, nil, fmt.Errorf("gemini-cli auth metadata missing")
	}

	var base map[string]any
	if tokenRaw, ok := auth.Metadata["token"].(map[string]any); ok && tokenRaw != nil {
		base = cloneMap(tokenRaw)
	} else {
		base = make(map[string]any)
	}

	var token oauth2.Token
	if len(base) > 0 {
		if raw, err := json.Marshal(base); err == nil {
			_ = json.Unmarshal(raw, &token)
		}
	}

	if token.AccessToken == "" {
		token.AccessToken = stringValue(auth.Metadata, "access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = stringValue(auth.Metadata, "refresh_token")
	}
	if token.TokenType == "" {
		token.TokenType = stringValue(auth.Metadata, "token_type")
	}
	if token.Expiry.IsZero() {
		if expiry := stringValue(auth.Metadata, "expiry"); expiry != "" {
			if ts, err := time.Parse(time.RFC3339, expiry); err == nil {
				token.Expiry = ts
			}
		}
	}

	conf := &oauth2.Config{
		ClientID:     geminiOauthClientID,
		ClientSecret: geminiOauthClientSecret,
		Scopes:       geminiOauthScopes,
		Endpoint:     google.Endpoint,
	}

	ctxToken := ctx
	if httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0); httpClient != nil {
		ctxToken = context.WithValue(ctxToken, oauth2.HTTPClient, httpClient)
	}

	src := conf.TokenSource(ctxToken, &token)
	currentToken, err := src.Token()
	if err != nil {
		return nil, nil, err
	}
	updateGeminiCLITokenMetadata(auth, base, currentToken)
	return oauth2.ReuseTokenSource(currentToken, src), base, nil
}

func updateGeminiCLITokenMetadata(auth *cliproxyauth.Auth, base map[string]any, tok *oauth2.Token) {
	if auth == nil || auth.Metadata == nil || tok == nil {
		return
	}
	if tok.AccessToken != "" {
		auth.Metadata["access_token"] = tok.AccessToken
	}
	if tok.TokenType != "" {
		auth.Metadata["token_type"] = tok.TokenType
	}
	if tok.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tok.RefreshToken
	}
	if !tok.Expiry.IsZero() {
		auth.Metadata["expiry"] = tok.Expiry.Format(time.RFC3339)
	}

	merged := cloneMap(base)
	if merged == nil {
		merged = make(map[string]any)
	}
	if raw, err := json.Marshal(tok); err == nil {
		var tokenMap map[string]any
		if err = json.Unmarshal(raw, &tokenMap); err == nil {
			for k, v := range tokenMap {
				merged[k] = v
			}
		}
	}

	auth.Metadata["token"] = merged
}

func newHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	return newProxyAwareHTTPClient(ctx, cfg, auth, timeout)
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		}
	}
	return ""
}

// applyGeminiCLIHeaders sets required headers for the Gemini CLI upstream.
func applyGeminiCLIHeaders(r *http.Request) {
	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", "google-api-nodejs-client/9.15.1")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Goog-Api-Client", "gl-node/22.17.0")
	misc.EnsureHeader(r.Header, ginHeaders, "Client-Metadata", geminiCLIClientMetadata())
}

// geminiCLIClientMetadata returns a compact metadata string required by upstream.
func geminiCLIClientMetadata() string {
	// Keep parity with CLI client defaults
	return "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
}

// cliPreviewFallbackOrder returns preview model candidates for a base model.
func cliPreviewFallbackOrder(model string) []string {
	switch model {
	case "gemini-2.5-pro":
		return []string{"gemini-2.5-pro-preview-05-06", "gemini-2.5-pro-preview-06-05"}
	case "gemini-2.5-flash":
		return []string{"gemini-2.5-flash-preview-04-17", "gemini-2.5-flash-preview-05-20"}
	case "gemini-2.5-flash-lite":
		return []string{"gemini-2.5-flash-lite-preview-06-17"}
	default:
		return nil
	}
}

func disableGeminiThinkingConfig(body []byte, model string) []byte {
	if !geminiModelDisallowsThinking(model) {
		return body
	}

	updated := deleteJSONField(body, "request.generationConfig.thinkingConfig")
	updated = deleteJSONField(updated, "generationConfig.thinkingConfig")
	return updated
}

func geminiModelDisallowsThinking(model string) bool {
	if model == "" {
		return false
	}
	lower := strings.ToLower(model)
	for _, marker := range []string{"gemini-2.5-flash-image-preview", "gemini-2.5-flash-image"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// setJSONField sets a top-level JSON field on a byte slice payload via sjson.
func setJSONField(body []byte, key, value string) []byte {
	if key == "" {
		return body
	}
	updated, err := sjson.SetBytes(body, key, value)
	if err != nil {
		return body
	}
	return updated
}

// deleteJSONField removes a top-level key if present (best-effort) via sjson.
func deleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

func fixGeminiCLIImageAspectRatio(modelName string, rawJSON []byte) []byte {
	if modelName == "gemini-2.5-flash-image-preview" {
		aspectRatioResult := gjson.GetBytes(rawJSON, "request.generationConfig.imageConfig.aspectRatio")
		if aspectRatioResult.Exists() {
			contents := gjson.GetBytes(rawJSON, "request.contents")
			contentArray := contents.Array()
			if len(contentArray) > 0 {
				hasInlineData := false
			loopContent:
				for i := 0; i < len(contentArray); i++ {
					parts := contentArray[i].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						if parts[j].Get("inlineData").Exists() {
							hasInlineData = true
							break loopContent
						}
					}
				}

				if !hasInlineData {
					emptyImageBase64ed, _ := util.CreateWhiteImageBase64(aspectRatioResult.String())
					emptyImagePart := `{"inlineData":{"mime_type":"image/png","data":""}}`
					emptyImagePart, _ = sjson.Set(emptyImagePart, "inlineData.data", emptyImageBase64ed)
					newPartsJson := `[]`
					newPartsJson, _ = sjson.SetRaw(newPartsJson, "-1", `{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."}`)
					newPartsJson, _ = sjson.SetRaw(newPartsJson, "-1", emptyImagePart)

					parts := contentArray[0].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						newPartsJson, _ = sjson.SetRaw(newPartsJson, "-1", parts[j].Raw)
					}

					rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.contents.0.parts", []byte(newPartsJson))
					rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.generationConfig.responseModalities", []byte(`["Image", "Text"]`))
				}
			}
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.generationConfig.imageConfig")
		}
	}
	return rawJSON
}
