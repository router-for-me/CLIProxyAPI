package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	antigravityEndpoint          = "https://antigravity.googleapis.com"
	antigravityVersion           = "v1"
	antigravityOauthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthClientSecret = "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"
)

var antigravityOauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// AntigravityExecutor executes requests against Google Antigravity API endpoints.
type AntigravityExecutor struct {
	cfg *config.Config
}

func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

func (e *AntigravityExecutor) Identifier() string { return "antigravity" }

func (e *AntigravityExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	tokenSource, baseTokenData, err := prepareAntigravityTokenSource(ctx, e.cfg, auth)
	if err != nil {
		return resp, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	basePayload = util.StripThinkingConfigIfUnsupported(req.Model, basePayload)
	basePayload = applyPayloadConfigWithRoot(e.cfg, req.Model, "antigravity", "request", basePayload)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	models := []string{req.Model}

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		if action == "countTokens" {
			setJSONField(payload, "project", "antigravity")
			deleteJSONField(payload, "model")
		} else {
			setJSONField(payload, "project", "antigravity")
			setJSONField(payload, "model", attemptModel)
		}

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return resp, err
		}
		updateAntigravityTokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", antigravityEndpoint, antigravityVersion, action)
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
		applyAntigravityHeaders(reqHTTP)
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
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			err = errRead
			return resp, err
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			reporter.publish(ctx, parseAntigravityUsage(data))
			var param any
			out := sdktranslator.TranslateNonStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), payload, data, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
			return resp, nil
		}

		lastStatus = httpResp.StatusCode
		lastBody = append([]byte(nil), data...)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		if httpResp.StatusCode == 429 {
			if idx+1 < len(models) {
				log.Debugf("antigravity executor: rate limited, retrying with next model: %s", models[idx+1])
			} else {
				log.Debug("antigravity executor: rate limited, no additional fallback model")
			}
			continue
		}

		err = statusErr{code: lastStatus, msg: string(data)}
		return resp, err
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
	}
	if lastStatus == 0 {
		lastStatus = 429
	}
	err = statusErr{code: lastStatus, msg: string(lastBody)}
	return resp, err
}

func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	tokenSource, baseTokenData, err := prepareAntigravityTokenSource(ctx, e.cfg, auth)
	if err != nil {
		return nil, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	basePayload = util.StripThinkingConfigIfUnsupported(req.Model, basePayload)
	basePayload = applyPayloadConfigWithRoot(e.cfg, req.Model, "antigravity", "request", basePayload)

	models := []string{req.Model}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		setJSONField(payload, "project", "antigravity")
		setJSONField(payload, "model", attemptModel)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return nil, err
		}
		updateAntigravityTokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", antigravityEndpoint, antigravityVersion, "streamGenerateContent")
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
		applyAntigravityHeaders(reqHTTP)
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
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				err = errRead
				return nil, err
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), data...)
			log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
			if httpResp.StatusCode == 429 {
				if idx+1 < len(models) {
					log.Debugf("antigravity executor: rate limited, retrying with next model: %s", models[idx+1])
				} else {
					log.Debug("antigravity executor: rate limited, no additional fallback model")
				}
				continue
			}
			err = statusErr{code: httpResp.StatusCode, msg: string(data)}
			return nil, err
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		stream = out
		go func(resp *http.Response, reqBody []byte, attempt string) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
			}()
			if opts.Alt == "" {
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(nil, 20_971_520)
				var param any
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)
					if detail, ok := parseAntigravityStreamUsage(line); ok {
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
			reporter.publish(ctx, parseAntigravityUsage(data))
			var param any
			segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, data, &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}

			segments = sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
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
	err = statusErr{code: lastStatus, msg: string(lastBody)}
	return nil, err
}

func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	tokenSource, baseTokenData, err := prepareAntigravityTokenSource(ctx, e.cfg, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")

	models := []string{req.Model}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastStatus int
	var lastBody []byte

	for _, attemptModel := range models {
		payload := sdktranslator.TranslateRequest(from, to, attemptModel, bytes.Clone(req.Payload), false)
		setJSONField(payload, "project", "antigravity")
		deleteJSONField(payload, "model")
		deleteJSONField(payload, "request.safetySettings")
		payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return cliproxyexecutor.Response{}, errTok
		}
		updateAntigravityTokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", antigravityEndpoint, antigravityVersion, "countTokens")
		if opts.Alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyAntigravityHeaders(reqHTTP)
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
			log.Debugf("antigravity executor: rate limited, retrying with next model")
			continue
		}
		break
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return cliproxyexecutor.Response{}, statusErr{code: lastStatus, msg: string(lastBody)}
}

func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("antigravity executor: refresh called")
	_ = ctx
	return auth, nil
}

func prepareAntigravityTokenSource(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (oauth2.TokenSource, map[string]any, error) {
	metadata := antigravityOAuthMetadata(auth)
	if auth == nil || metadata == nil {
		return nil, nil, fmt.Errorf("antigravity auth metadata missing")
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
		ClientID:     antigravityOauthClientID,
		ClientSecret: antigravityOAuthClientSecret,
		Scopes:       antigravityOauthScopes,
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
	updateAntigravityTokenMetadata(auth, base, currentToken)
	return oauth2.ReuseTokenSource(currentToken, src), base, nil
}

func updateAntigravityTokenMetadata(auth *cliproxyauth.Auth, base map[string]any, tok *oauth2.Token) {
	if auth == nil || tok == nil {
		return
	}
	merged := buildAntigravityTokenMap(base, tok)
	fields := buildAntigravityTokenFields(tok, merged)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	for k, v := range fields {
		auth.Metadata[k] = v
	}
}

func buildAntigravityTokenMap(base map[string]any, tok *oauth2.Token) map[string]any {
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

func buildAntigravityTokenFields(tok *oauth2.Token, merged map[string]any) map[string]any {
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

func antigravityOAuthMetadata(auth *cliproxyauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	return auth.Metadata
}

func applyAntigravityHeaders(r *http.Request) {
	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", "google-api-go-client/2.0.0")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Goog-Api-Client", "gl-go/1.0.0")
	misc.EnsureHeader(r.Header, ginHeaders, "Client-Metadata", antigravityClientMetadata())
}

func antigravityClientMetadata() string {
	return "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=ANTIGRAVITY"
}

func parseAntigravityUsage(data []byte) usage.Detail {
	result := gjson.GetBytes(data, "usageMetadata")
	if !result.Exists() {
		return usage.Detail{}
	}
	return usage.Detail{
		InputTokens:  result.Get("promptTokenCount").Int(),
		OutputTokens: result.Get("candidatesTokenCount").Int(),
		TotalTokens:  result.Get("totalTokenCount").Int(),
	}
}

func parseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	data := gjson.GetBytes(line, "usageMetadata")
	if !data.Exists() {
		return usage.Detail{}, false
	}
	return usage.Detail{
		InputTokens:  data.Get("promptTokenCount").Int(),
		OutputTokens: data.Get("candidatesTokenCount").Int(),
		TotalTokens:  data.Get("totalTokenCount").Int(),
	}, true
}
