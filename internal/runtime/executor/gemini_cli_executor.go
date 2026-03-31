// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Gemini CLI executor that talks to Cloud Code Assist endpoints
// using OAuth credentials from auth metadata.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/geminicli"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
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
	geminiOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOAuthClientSecret = "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"
)

var geminiCLIDefaultRetryDelay = time.Second

var geminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// GeminiCLIExecutor talks to the Cloud Code Assist endpoint using OAuth credentials from auth metadata.
type GeminiCLIExecutor struct {
	cfg *config.Config
}

// NewGeminiCLIExecutor creates a new Gemini CLI executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *GeminiCLIExecutor: A new Gemini CLI executor instance
func NewGeminiCLIExecutor(cfg *config.Config) *GeminiCLIExecutor {
	return &GeminiCLIExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *GeminiCLIExecutor) Identifier() string { return "gemini-cli" }

// PrepareRequest injects Gemini CLI credentials into the outgoing HTTP request.
func (e *GeminiCLIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	misc.ScrubProxyAndFingerprintHeaders(req)
	tokenSource, _, errSource := prepareGeminiCLITokenSource(req.Context(), e.cfg, auth)
	if errSource != nil {
		return errSource
	}
	tok, errTok := tokenSource.Token()
	if errTok != nil {
		return errTok
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	applyGeminiCLIHeaders(req, "unknown")
	return nil
}

// HttpRequest injects Gemini CLI credentials into the request and executes it.
func (e *GeminiCLIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini-cli executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Gemini CLI API.
func (e *GeminiCLIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return resp, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	basePayload := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	basePayload, err = thinking.ApplyThinking(basePayload, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	basePayload = fixGeminiCLIImageAspectRatio(baseModel, basePayload)
	requestedModel := payloadRequestedModel(opts, req.Model)
	basePayload = applyPayloadConfigWithRoot(e.cfg, baseModel, "gemini", "request", basePayload, originalTranslated, requestedModel)

	projectID := resolveGeminiProjectID(auth)
	models := cliPreviewFallbackOrder(baseModel)
	if len(models) == 0 || models[0] != baseModel {
		models = append([]string{baseModel}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()
	userPromptID := uuid.NewString()
	sessionID := generateStableGeminiCLISessionID(basePayload)

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := buildGeminiCLIGeneratePayload(basePayload, projectID, attemptModel, userPromptID, sessionID)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return resp, err
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "generateContent")
		if opts.Alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		statusCode, headers, data, errDo := e.executeGeminiCLIJSONRequest(
			ctx,
			httpClient,
			auth,
			url,
			payload,
			tok.AccessToken,
			attemptModel,
			authID,
			authLabel,
			authType,
			authValue,
		)
		if errDo != nil {
			err = errDo
			return resp, err
		}
		if statusCode >= 200 && statusCode < 300 {
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			out := sdktranslator.TranslateNonStream(respCtx, to, from, attemptModel, opts.OriginalRequest, payload, data, &param)
			resp = cliproxyexecutor.Response{Payload: out, Headers: headers}
			return resp, nil
		}

		lastStatus = statusCode
		lastBody = append([]byte(nil), data...)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", statusCode, summarizeErrorBody(headers.Get("Content-Type"), data))
		if statusCode == 429 {
			if idx+1 < len(models) {
				log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
			} else {
				log.Debug("gemini cli executor: rate limited, no additional fallback model")
			}
			continue
		}

		err = newGeminiStatusErr(statusCode, data)
		return resp, err
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	err = newGeminiStatusErr(lastStatus, lastBody)
	return resp, err
}

// ExecuteStream performs a streaming request to the Gemini CLI API.
func (e *GeminiCLIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return nil, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	basePayload := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	basePayload, err = thinking.ApplyThinking(basePayload, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	basePayload = fixGeminiCLIImageAspectRatio(baseModel, basePayload)
	requestedModel := payloadRequestedModel(opts, req.Model)
	basePayload = applyPayloadConfigWithRoot(e.cfg, baseModel, "gemini", "request", basePayload, originalTranslated, requestedModel)

	projectID := resolveGeminiProjectID(auth)

	models := cliPreviewFallbackOrder(baseModel)
	if len(models) == 0 || models[0] != baseModel {
		models = append([]string{baseModel}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()
	userPromptID := uuid.NewString()
	sessionID := generateStableGeminiCLISessionID(basePayload)

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := buildGeminiCLIGeneratePayload(basePayload, projectID, attemptModel, userPromptID, sessionID)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return nil, err
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
			err = errReq
			return nil, err
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP, attemptModel)
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

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			err = errDo
			return nil, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			data, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini cli executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				err = errRead
				return nil, err
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), data...)
			logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
			if httpResp.StatusCode == 429 {
				if idx+1 < len(models) {
					log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
				} else {
					log.Debug("gemini cli executor: rate limited, no additional fallback model")
				}
				continue
			}
			err = newGeminiStatusErr(httpResp.StatusCode, data)
			return nil, err
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		go func(resp *http.Response, reqBody []byte, attemptModel string) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("gemini cli executor: close response body error: %v", errClose)
				}
			}()
			if opts.Alt == "" {
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(nil, streamScannerBuffer)
				var param any
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)
					if detail, ok := parseGeminiCLIStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
					if bytes.HasPrefix(line, dataTag) {
						segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, opts.OriginalRequest, reqBody, bytes.Clone(line), &param)
						for i := range segments {
							out <- cliproxyexecutor.StreamChunk{Payload: segments[i]}
						}
					}
				}

				segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, opts.OriginalRequest, reqBody, []byte("[DONE]"), &param)
				for i := range segments {
					out <- cliproxyexecutor.StreamChunk{Payload: segments[i]}
				}
				if errScan := scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				}
				return
			}

			data, errRead := io.ReadAll(resp.Body)
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errRead}
				return
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, opts.OriginalRequest, reqBody, data, &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: segments[i]}
			}

			segments = sdktranslator.TranslateStream(respCtx, to, from, attemptModel, opts.OriginalRequest, reqBody, []byte("[DONE]"), &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: segments[i]}
			}
		}(httpResp, append([]byte(nil), payload...), attemptModel)

		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	err = newGeminiStatusErr(lastStatus, lastBody)
	return nil, err
}

// CountTokens counts tokens for the given request using the Gemini CLI API.
func (e *GeminiCLIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	payload := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	payload = buildGeminiCLICountTokensPayload(payload, baseModel)

	tok, errTok := tokenSource.Token()
	if errTok != nil {
		return cliproxyexecutor.Response{}, errTok
	}
	updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

	url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "countTokens")
	if opts.Alt != "" {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}

	statusCode, headers, data, errDo := e.executeGeminiCLIJSONRequest(
		ctx,
		httpClient,
		auth,
		url,
		payload,
		tok.AccessToken,
		baseModel,
		authID,
		authLabel,
		authType,
		authValue,
	)
	if errDo != nil {
		return cliproxyexecutor.Response{}, errDo
	}
	if statusCode >= 200 && statusCode < 300 {
		count := gjson.GetBytes(data, "totalTokens").Int()
		translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, data)
		return cliproxyexecutor.Response{Payload: translated, Headers: headers}, nil
	}
	return cliproxyexecutor.Response{}, newGeminiStatusErr(statusCode, data)
}

// Refresh refreshes the authentication credentials (no-op for Gemini CLI).
func (e *GeminiCLIExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func prepareGeminiCLITokenSource(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (oauth2.TokenSource, map[string]any, error) {
	metadata := geminiOAuthMetadata(auth)
	if auth == nil || metadata == nil {
		return nil, nil, fmt.Errorf("gemini-cli auth metadata missing")
	}

	var base map[string]any
	if tokenRaw, ok := metadata["token"].(map[string]any); ok && tokenRaw != nil {
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
		token.AccessToken = stringValue(metadata, "access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = stringValue(metadata, "refresh_token")
	}
	if token.TokenType == "" {
		token.TokenType = stringValue(metadata, "token_type")
	}
	if token.Expiry.IsZero() {
		if expiry := stringValue(metadata, "expiry"); expiry != "" {
			if ts, err := time.Parse(time.RFC3339, expiry); err == nil {
				token.Expiry = ts
			}
		}
	}

	conf := &oauth2.Config{
		ClientID:     geminiOAuthClientID,
		ClientSecret: geminiOAuthClientSecret,
		Scopes:       geminiOAuthScopes,
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
	if auth == nil || tok == nil {
		return
	}
	merged := buildGeminiTokenMap(base, tok)
	fields := buildGeminiTokenFields(tok, merged)
	shared := geminicli.ResolveSharedCredential(auth.Runtime)
	if shared != nil {
		snapshot := shared.MergeMetadata(fields)
		if !geminicli.IsVirtual(auth.Runtime) {
			auth.Metadata = snapshot
		}
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	for k, v := range fields {
		auth.Metadata[k] = v
	}
}

func buildGeminiTokenMap(base map[string]any, tok *oauth2.Token) map[string]any {
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
	return merged
}

func buildGeminiTokenFields(tok *oauth2.Token, merged map[string]any) map[string]any {
	fields := make(map[string]any, 5)
	if tok.AccessToken != "" {
		fields["access_token"] = tok.AccessToken
	}
	if tok.TokenType != "" {
		fields["token_type"] = tok.TokenType
	}
	if tok.RefreshToken != "" {
		fields["refresh_token"] = tok.RefreshToken
	}
	if !tok.Expiry.IsZero() {
		fields["expiry"] = tok.Expiry.Format(time.RFC3339)
	}
	if len(merged) > 0 {
		fields["token"] = cloneMap(merged)
	}
	return fields
}

func resolveGeminiProjectID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if runtime := auth.Runtime; runtime != nil {
		if virtual, ok := runtime.(*geminicli.VirtualCredential); ok && virtual != nil {
			return strings.TrimSpace(virtual.ProjectID)
		}
	}
	return strings.TrimSpace(stringValue(auth.Metadata, "project_id"))
}

func geminiOAuthMetadata(auth *cliproxyauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		if snapshot := shared.MetadataSnapshot(); len(snapshot) > 0 {
			return snapshot
		}
	}
	return auth.Metadata
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
// User-Agent is always forced to the GeminiCLI format regardless of the client's value,
// so that upstream identifies the request as a native GeminiCLI client.
func applyGeminiCLIHeaders(r *http.Request, model string) {
	r.Header.Set("User-Agent", misc.GeminiCLIUserAgent(model))
}

func buildGeminiCLIGeneratePayload(body []byte, projectID, model, userPromptID, sessionID string) []byte {
	payload := append([]byte(nil), body...)
	payload = setJSONField(payload, "project", projectID)
	payload = setJSONField(payload, "model", model)
	payload = setJSONField(payload, "user_prompt_id", userPromptID)
	payload = setJSONField(payload, "request.session_id", sessionID)
	payload = deleteJSONField(payload, "enabled_credit_types")
	return payload
}

func buildGeminiCLICountTokensPayload(body []byte, model string) []byte {
	payload := []byte(`{"request":{"contents":[]}}`)
	contents := gjson.GetBytes(body, "request.contents")
	if contents.Exists() && strings.TrimSpace(contents.Raw) != "" {
		if updated, err := sjson.SetRawBytes(payload, "request.contents", []byte(contents.Raw)); err == nil {
			payload = updated
		}
	}
	return setJSONField(payload, "request.model", "models/"+model)
}

func generateStableGeminiCLISessionID(payload []byte) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if contents.IsArray() {
		for _, content := range contents.Array() {
			if content.Get("role").String() != "user" {
				continue
			}
			for _, part := range content.Get("parts").Array() {
				text := part.Get("text").String()
				if text == "" {
					continue
				}
				h := sha256.Sum256([]byte(text))
				n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
				return "-" + strconv.FormatInt(n, 10)
			}
		}
	}
	return uuid.NewString()
}

func (e *GeminiCLIExecutor) executeGeminiCLIJSONRequest(
	ctx context.Context,
	httpClient *http.Client,
	auth *cliproxyauth.Auth,
	url string,
	payload []byte,
	accessToken string,
	model string,
	authID string,
	authLabel string,
	authType string,
	authValue string,
) (int, http.Header, []byte, error) {
	attempts := e.geminiCLIRetryAttempts(auth)
	maxWait := e.geminiCLIRetryMaxWait()
	if attempts < 1 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return 0, nil, nil, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+accessToken)
		applyGeminiCLIHeaders(reqHTTP, model)
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

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			if attempt+1 >= attempts {
				return 0, nil, nil, errDo
			}
			wait, ok := geminiCLITransportRetryDelay(maxWait)
			if !ok {
				return 0, nil, nil, errDo
			}
			if errWait := sleepWithContext(ctx, wait); errWait != nil {
				return 0, nil, nil, errWait
			}
			continue
		}

		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini cli executor: close response body error: %v", errClose)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return 0, nil, nil, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			return httpResp.StatusCode, httpResp.Header.Clone(), data, nil
		}
		if attempt+1 >= attempts || !shouldRetryGeminiCLIStatus(httpResp.StatusCode) {
			return httpResp.StatusCode, httpResp.Header.Clone(), data, nil
		}
		wait, ok := geminiCLIStatusRetryDelay(httpResp.StatusCode, data, maxWait)
		if !ok {
			return httpResp.StatusCode, httpResp.Header.Clone(), data, nil
		}
		if errWait := sleepWithContext(ctx, wait); errWait != nil {
			return 0, nil, nil, errWait
		}
	}

	return 0, nil, nil, nil
}

func (e *GeminiCLIExecutor) geminiCLIRetryAttempts(auth *cliproxyauth.Auth) int {
	if auth != nil {
		if override, ok := auth.RequestRetryOverride(); ok {
			if override < 0 {
				return 1
			}
			return override + 1
		}
	}
	if e == nil || e.cfg == nil || e.cfg.RequestRetry < 0 {
		return 1
	}
	return e.cfg.RequestRetry + 1
}

func (e *GeminiCLIExecutor) geminiCLIRetryMaxWait() time.Duration {
	if e == nil || e.cfg == nil || e.cfg.MaxRetryInterval <= 0 {
		return 0
	}
	return time.Duration(e.cfg.MaxRetryInterval) * time.Second
}

func shouldRetryGeminiCLIStatus(statusCode int) bool {
	return statusCode == 429 || statusCode == 499 || (statusCode >= 500 && statusCode <= 599)
}

func geminiCLITransportRetryDelay(maxWait time.Duration) (time.Duration, bool) {
	return capGeminiCLIRetryDelay(geminiCLIDefaultRetryDelay, maxWait)
}

func geminiCLIStatusRetryDelay(statusCode int, body []byte, maxWait time.Duration) (time.Duration, bool) {
	wait := geminiCLIDefaultRetryDelay
	if statusCode == http.StatusTooManyRequests {
		if retryAfter, err := parseRetryDelay(body); err == nil && retryAfter != nil && *retryAfter > 0 {
			wait = *retryAfter
		}
	}
	return capGeminiCLIRetryDelay(wait, maxWait)
}

func capGeminiCLIRetryDelay(wait time.Duration, maxWait time.Duration) (time.Duration, bool) {
	if wait <= 0 {
		return 0, true
	}
	if maxWait > 0 && wait > maxWait {
		return 0, false
	}
	return wait, true
}

func sleepWithContext(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cliPreviewFallbackOrder returns preview model candidates for a base model.
func cliPreviewFallbackOrder(model string) []string {
	switch model {
	case "gemini-2.5-pro":
		return []string{
			// "gemini-2.5-pro-preview-05-06",
			// "gemini-2.5-pro-preview-06-05",
		}
	case "gemini-2.5-flash":
		return []string{
			// "gemini-2.5-flash-preview-04-17",
			// "gemini-2.5-flash-preview-05-20",
		}
	case "gemini-2.5-flash-lite":
		return []string{
			// "gemini-2.5-flash-lite-preview-06-17",
		}
	default:
		return nil
	}
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
					emptyImagePart := []byte(`{"inlineData":{"mime_type":"image/png","data":""}}`)
					emptyImagePart, _ = sjson.SetBytes(emptyImagePart, "inlineData.data", emptyImageBase64ed)
					newPartsJson := []byte(`[]`)
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(`{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."}`))
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", emptyImagePart)

					parts := contentArray[0].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(parts[j].Raw))
					}

					rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.contents.0.parts", newPartsJson)
					rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.generationConfig.responseModalities", []byte(`["IMAGE", "TEXT"]`))
				}
			}
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.generationConfig.imageConfig")
		}
	}
	return rawJSON
}

func newGeminiStatusErr(statusCode int, body []byte) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if retryAfter, parseErr := parseRetryDelay(body); parseErr == nil && retryAfter != nil {
			err.retryAfter = retryAfter
		}
	}
	return err
}

// parseRetryDelay extracts the retry delay from a Google API 429 error response.
// The error response contains a RetryInfo.retryDelay field in the format "0.847655010s".
// Returns the parsed duration or an error if it cannot be determined.
func parseRetryDelay(errorBody []byte) (*time.Duration, error) {
	// Try to parse the retryDelay from the error response
	// Format: error.details[].retryDelay where @type == "type.googleapis.com/google.rpc.RetryInfo"
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
				retryDelay := detail.Get("retryDelay").String()
				if retryDelay != "" {
					// Parse duration string like "0.847655010s"
					duration, err := time.ParseDuration(retryDelay)
					if err != nil {
						return nil, fmt.Errorf("failed to parse duration")
					}
					return &duration, nil
				}
			}
		}

		// Fallback: try ErrorInfo.metadata.quotaResetDelay (e.g., "373.801628ms")
		for _, detail := range details.Array() {
			typeVal := detail.Get("@type").String()
			if typeVal == "type.googleapis.com/google.rpc.ErrorInfo" {
				quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
				if quotaResetDelay != "" {
					duration, err := time.ParseDuration(quotaResetDelay)
					if err == nil {
						return &duration, nil
					}
				}
			}
		}
	}

	// Fallback: parse from error.message "Your quota will reset after Xs."
	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				return new(time.Duration(seconds) * time.Second), nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}
