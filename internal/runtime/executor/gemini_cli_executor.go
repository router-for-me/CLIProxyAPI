// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Gemini CLI executor that talks to Cloud Code Assist endpoints
// using OAuth credentials from auth metadata.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	applyGeminiCLIHeaders(req)
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
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return resp, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	basePayload := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	basePayload, err = thinking.ApplyThinking(basePayload, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	basePayload = fixGeminiCLIImageAspectRatio(baseModel, basePayload)
	requestedModel := payloadRequestedModel(opts, req.Model)
	basePayload = applyPayloadConfigWithRoot(e.cfg, baseModel, "gemini", "request", basePayload, originalTranslated, requestedModel)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

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

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		if action == "countTokens" {
			payload = deleteJSONField(payload, "project")
			payload = deleteJSONField(payload, "model")
		} else {
			payload = setJSONField(payload, "project", projectID)
			payload = setJSONField(payload, "model", attemptModel)
		}

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return resp, err
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, action)
		if opts.Alt != "" && action != "countTokens" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			err = errReq
			return resp, err
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

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			err = errDo
			return resp, err
		}

		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini cli executor: close response body error: %v", errClose)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			err = errRead
			return resp, err
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			out := sdktranslator.TranslateNonStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), payload, data, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
			return resp, nil
		}

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
		return resp, err
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
	}
	if lastStatus == 0 {
		lastStatus = 429
	}
	err = newGeminiStatusErr(lastStatus, lastBody)
	return resp, err
}

// ExecuteStream performs a streaming request to the Gemini CLI API.
func (e *GeminiCLIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return nil, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	basePayload := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

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

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		payload = setJSONField(payload, "project", projectID)
		payload = setJSONField(payload, "model", attemptModel)

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
		stream = out
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
						segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone(line), &param)
						for i := range segments {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
						}
					}
				}

				segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
				for i := range segments {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
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
			segments := sdktranslator.TranslateStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), reqBody, data, &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}

			segments = sdktranslator.TranslateStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}
		}(httpResp, append([]byte(nil), payload...), attemptModel)

		return stream, nil
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
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

	models := cliPreviewFallbackOrder(baseModel)
	if len(models) == 0 || models[0] != baseModel {
		models = append([]string{baseModel}, models...)
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

	// The loop variable attemptModel is only used as the concrete model id sent to the upstream
	// Gemini CLI endpoint when iterating fallback variants.
	for range models {
		payload := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

		payload, err = thinking.ApplyThinking(payload, req.Model, from.String(), to.String(), e.Identifier())
		if err != nil {
			return cliproxyexecutor.Response{}, err
		}

		payload = deleteJSONField(payload, "project")
		payload = deleteJSONField(payload, "model")
		payload = deleteJSONField(payload, "request.safetySettings")
		payload = fixGeminiCLIImageAspectRatio(baseModel, payload)

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
			log.Debugf("gemini cli executor: rate limited, retrying with next model")
			continue
		}
		break
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return cliproxyexecutor.Response{}, newGeminiStatusErr(lastStatus, lastBody)
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
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}
