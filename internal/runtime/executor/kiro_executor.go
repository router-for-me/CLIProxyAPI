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

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	kiroDefaultRegion            = "us-east-1"
	kiroProviderKey              = "kiro"
	kiroDefaultTokenExpirySeconds = 28800
	kiroScannerBufferSize        = 52_428_800 // 50MB - matches other executors for large streaming responses
)

type KiroExecutor struct {
	cfg *config.Config
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor { return &KiroExecutor{cfg: cfg} }

func (e *KiroExecutor) Identifier() string { return kiroProviderKey }

func kiroEndpoint(region string) string {
	if region == "" {
		region = kiroDefaultRegion
	}
	return fmt.Sprintf("https://kiro.%s.amazonaws.com", region)
}

func getStringFromMetadata(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getJSONStringWithFallback(data []byte, primaryKey, fallbackKey string) string {
	if v := gjson.GetBytes(data, primaryKey).String(); v != "" {
		return v
	}
	return gjson.GetBytes(data, fallbackKey).String()
}

func getJSONIntWithFallback(data []byte, primaryKey, fallbackKey string, defaultVal int64) int64 {
	if v := gjson.GetBytes(data, primaryKey).Int(); v != 0 {
		return v
	}
	if v := gjson.GetBytes(data, fallbackKey).Int(); v != 0 {
		return v
	}
	return defaultVal
}

func kiroCreds(a *cliproxyauth.Auth) (accessToken, region string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		accessToken = a.Attributes["api_key"]
		region = a.Attributes["region"]
	}
	if accessToken == "" && a.Metadata != nil {
		accessToken = getStringFromMetadata(a.Metadata, "access_token")
		if region == "" {
			region = getStringFromMetadata(a.Metadata, "region")
		}
	}
	if region == "" {
		region = kiroDefaultRegion
	}
	return
}

func kiroRefreshCreds(a *cliproxyauth.Auth) (refreshToken, clientID, clientSecret, region, authMethod string) {
	if a == nil || a.Metadata == nil {
		return
	}
	refreshToken = getStringFromMetadata(a.Metadata, "refresh_token")
	clientID = getStringFromMetadata(a.Metadata, "client_id")
	clientSecret = getStringFromMetadata(a.Metadata, "client_secret")
	region = getStringFromMetadata(a.Metadata, "region")
	authMethod = getStringFromMetadata(a.Metadata, "auth_method")
	if region == "" {
		region = kiroDefaultRegion
	}
	return
}

func applyKiroHeaders(req *http.Request, auth *cliproxyauth.Auth, accessToken string, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if auth != nil && auth.Attributes != nil {
		for k, v := range auth.Attributes {
			if strings.HasPrefix(strings.ToLower(k), "x-") || strings.HasPrefix(strings.ToLower(k), "anthropic-") {
				req.Header.Set(k, v)
			}
		}
	}
}

func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, _ := kiroCreds(auth)
	if strings.TrimSpace(accessToken) == "" {
		return nil
	}
	req.Header.Del("x-api-key")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return nil
}

func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func stripKiroModelPrefix(model string) string {
	if strings.HasPrefix(model, "kiro-") {
		return strings.TrimPrefix(model, "kiro-")
	}
	return model
}

type kiroPreparedRequest struct {
	body              []byte
	upstreamModel     string
	baseModel         string
	baseURL           string
	accessToken       string
	from              sdktranslator.Format
	to                sdktranslator.Format
}

func (e *KiroExecutor) prepareKiroRequest(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*kiroPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := stripKiroModelPrefix(baseModel)

	accessToken, region := kiroCreds(auth)
	baseURL := kiroEndpoint(region)

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, upstreamModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, upstreamModel, bytes.Clone(req.Payload), stream)
	body, _ = sjson.SetBytes(body, "model", upstreamModel)

	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, upstreamModel, to.String(), "", body, originalTranslated, requestedModel)
	body = disableThinkingIfToolChoiceForced(body)

	return &kiroPreparedRequest{
		body:          body,
		upstreamModel: upstreamModel,
		baseModel:     baseModel,
		baseURL:       baseURL,
		accessToken:   accessToken,
		from:          from,
		to:            to,
	}, nil
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, err := e.prepareKiroRequest(auth, req, opts, opts.SourceFormat != sdktranslator.FromString("claude"))
	if err != nil {
		return resp, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), prepared.baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	url := fmt.Sprintf("%s/v1/messages", prepared.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return resp, err
	}
	applyKiroHeaders(httpReq, auth, prepared.accessToken, false)

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
		Body:      prepared.body,
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
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("kiro request error, status: %d, message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}

	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	data, err := io.ReadAll(decodedBody)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	stream := prepared.from != prepared.to
	if stream {
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if detail, ok := parseClaudeStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
		}
	} else {
		reporter.publish(ctx, parseClaudeUsage(data))
	}

	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		prepared.to,
		prepared.from,
		req.Model,
		bytes.Clone(opts.OriginalRequest),
		prepared.body,
		data,
		&param,
	)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	prepared, err := e.prepareKiroRequest(auth, req, opts, true)
	if err != nil {
		return nil, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), prepared.baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	url := fmt.Sprintf("%s/v1/messages", prepared.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	applyKiroHeaders(httpReq, auth, prepared.accessToken, true)

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
		Body:      prepared.body,
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
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("kiro stream request error, status: %d, message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out

	go func() {
		defer close(out)
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()

		if prepared.from == prepared.to {
			scanner := bufio.NewScanner(decodedBody)
			scanner.Buffer(nil, kiroScannerBufferSize)
			for scanner.Scan() {
				line := scanner.Bytes()
				appendAPIResponseChunk(ctx, e.cfg, line)
				if detail, ok := parseClaudeStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				}
				cloned := make([]byte, len(line)+1)
				copy(cloned, line)
				cloned[len(line)] = '\n'
				out <- cliproxyexecutor.StreamChunk{Payload: cloned}
			}
			if errScan := scanner.Err(); errScan != nil {
				recordAPIResponseError(ctx, e.cfg, errScan)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errScan}
			}
			return
		}

		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, kiroScannerBufferSize)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseClaudeStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(
				ctx,
				prepared.to,
				prepared.from,
				req.Model,
				bytes.Clone(opts.OriginalRequest),
				prepared.body,
				bytes.Clone(line),
				&param,
			)
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

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("kiro: count_tokens not supported")
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}

	refreshToken, clientID, clientSecret, region, authMethod := kiroRefreshCreds(auth)
	if refreshToken == "" {
		log.Debugf("kiro executor: no refresh token available for %s", auth.ID)
		return auth, nil
	}

	var tokenURL string
	var reqBody []byte
	var marshalErr error

	if strings.EqualFold(authMethod, "social") {
		tokenURL = fmt.Sprintf("https://prod.%s.auth.desktop.kiro.dev/refreshToken", region)
		reqBody, marshalErr = json.Marshal(map[string]string{
			"refreshToken": refreshToken,
		})
	} else {
		if clientID == "" || clientSecret == "" {
			log.Debugf("kiro executor: missing client credentials for IdC refresh, auth: %s", auth.ID)
			return auth, nil
		}
		tokenURL = fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
		reqBody, marshalErr = json.Marshal(map[string]string{
			"clientId":     clientID,
			"clientSecret": clientSecret,
			"grantType":    "refresh_token",
			"refreshToken": refreshToken,
		})
	}

	if marshalErr != nil {
		return nil, fmt.Errorf("kiro executor: failed to marshal refresh request: %w", marshalErr)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("kiro executor: failed to create refresh request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("kiro executor: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kiro executor: failed to read refresh response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro executor: refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	accessToken := getJSONStringWithFallback(body, "accessToken", "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("kiro executor: no access_token in refresh response")
	}

	expiresIn := getJSONIntWithFallback(body, "expiresIn", "expires_in", kiroDefaultTokenExpirySeconds)

	newRefreshToken := getJSONStringWithFallback(body, "refreshToken", "refresh_token")
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = accessToken
	auth.Metadata["refresh_token"] = newRefreshToken
	auth.Metadata["expires_at"] = time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)

	log.Infof("kiro executor: successfully refreshed token for %s, expires in %d seconds", auth.ID, expiresIn)
	return auth, nil
}
