package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

const (
	OpenCodeZenBaseURL = "https://opencode.ai/zen"
	OpenCodeGoBaseURL  = "https://opencode.ai/zen/go"
)

type OpenCodeExecutor struct {
	id      string
	gateway string
	cfg     *config.Config
}

func NewOpenCodeExecutor(id string) *OpenCodeExecutor {
	gateway := "zen"
	if id == constant.OpenCodeGo || id == "opencode-go" {
		gateway = "go"
	}
	return &OpenCodeExecutor{id: id, gateway: gateway}
}

func (e *OpenCodeExecutor) Identifier() string  { return e.id }
func (e *OpenCodeExecutor) ProviderKey() string { return e.id }

func (e *OpenCodeExecutor) RequestToFormat(req cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	protocol := registry.ResolveOpenCodeProtocol(e.gateway, thinking.ParseSuffix(req.Model).ModelName)
	switch protocol {
	case "messages", "gemini":
		return sdktranslator.FormatClaude
	case "responses":
		return sdktranslator.FormatOpenAIResponse
	case "chat":
		return sdktranslator.FormatOpenAI
	}
	return sdktranslator.FormatOpenAI
}

func (e *OpenCodeExecutor) cloneAuthWithBaseURL(auth *clipoauth.Auth) *clipoauth.Auth {
	if auth == nil {
		return nil
	}
	cloned := *auth
	cloned.Attributes = make(map[string]string, len(auth.Attributes)+2)
	for k, v := range auth.Attributes {
		cloned.Attributes[k] = v
	}
	if strings.TrimSpace(cloned.Attributes["base_url"]) == "" {
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
				cloned.Attributes["base_url"] = strings.TrimSpace(v)
			}
		}
	}
	if strings.TrimSpace(cloned.Attributes["base_url"]) == "" {
		switch e.gateway {
		case "go":
			cloned.Attributes["base_url"] = OpenCodeGoBaseURL
		default:
			cloned.Attributes["base_url"] = OpenCodeZenBaseURL
		}
	}
	return &cloned
}

func (e *OpenCodeExecutor) resolveRoute(modelID string) (protocol string, url string, err error) {
	protocol = registry.ResolveOpenCodeProtocol(e.gateway, modelID)
	if protocol == "" {
		return "", "", fmt.Errorf("opencode: model %q is not available on the %s gateway", modelID, e.gateway)
	}
	url = registry.OpenCodeModelPath(e.gateway, modelID)
	return protocol, url, nil
}

func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol, path, errRoute := e.resolveRoute(baseModel)
	if errRoute != nil {
		return cliproxyexecutor.Response{}, errRoute
	}
	clonedAuth := e.cloneAuthWithBaseURL(auth)
	baseURL := strings.TrimSuffix(clonedAuth.Attributes["base_url"], "/")

	log.Debugf("opencode executor [%s]: model=%q protocol=%q path=%s", e.gateway, baseModel, protocol, path)

	switch protocol {
	case "messages":
		ce := NewClaudeExecutor(e.cfg)
		return ce.Execute(ctx, clonedAuth, req, opts)
	case "responses":
		fullURL := baseURL + path
		return e.executeOpenAIEndpoint(ctx, clonedAuth, req, opts, fullURL, sdktranslator.FormatOpenAIResponse)
	case "chat":
		fullURL := baseURL + path
		return e.executeOpenAIEndpoint(ctx, clonedAuth, req, opts, fullURL, sdktranslator.FormatOpenAI)
	case "gemini":
		ge := NewGeminiExecutor(e.cfg)
		return ge.Execute(ctx, clonedAuth, req, opts)
	}
	return cliproxyexecutor.Response{}, fmt.Errorf("opencode: unsupported protocol %q for model %q", protocol, baseModel)
}

func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol, path, errRoute := e.resolveRoute(baseModel)
	if errRoute != nil {
		return nil, errRoute
	}
	clonedAuth := e.cloneAuthWithBaseURL(auth)
	baseURL := strings.TrimSuffix(clonedAuth.Attributes["base_url"], "/")

	log.Debugf("opencode executor [%s]: stream model=%q protocol=%q path=%s", e.gateway, baseModel, protocol, path)

	switch protocol {
	case "messages":
		ce := NewClaudeExecutor(e.cfg)
		return ce.ExecuteStream(ctx, clonedAuth, req, opts)
	case "responses":
		fullURL := baseURL + path
		return e.executeOpenAIEndpointStream(ctx, clonedAuth, req, opts, fullURL, sdktranslator.FormatOpenAIResponse)
	case "chat":
		fullURL := baseURL + path
		return e.executeOpenAIEndpointStream(ctx, clonedAuth, req, opts, fullURL, sdktranslator.FormatOpenAI)
	case "gemini":
		ge := NewGeminiExecutor(e.cfg)
		return ge.ExecuteStream(ctx, clonedAuth, req, opts)
	}
	return nil, fmt.Errorf("opencode: unsupported protocol %q for model %q", protocol, baseModel)
}

func (e *OpenCodeExecutor) executeOpenAIEndpoint(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointURL string, toFormat sdktranslator.Format) (resp cliproxyexecutor.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	upstreamModel := thinking.ParseSuffix(req.Model).ModelName

	body := req.Payload
	if len(body) == 0 && len(opts.OriginalRequest) > 0 {
		body = opts.OriginalRequest
	}
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, toFormat, upstreamModel, body, false, false)
	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), toFormat.String(), e.id)
	if err != nil {
		return resp, fmt.Errorf("opencode executor: apply thinking: %w", err)
	}
	translated, err = sjson.SetBytes(translated, "model", upstreamModel)
	if err != nil {
		return resp, fmt.Errorf("opencode executor: set model: %w", err)
	}

	apiKey, _ := openCodeCreds(auth)
	if apiKey == "" {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-opencode")
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, opts.Headers)

	authID, authLabel, authType, authValue := authAccountInfo(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       endpointURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.id,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("opencode executor: error status: %d, message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenCodeExecutor) executeOpenAIEndpointStream(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointURL string, toFormat sdktranslator.Format) (*cliproxyexecutor.StreamResult, error) {
	var err error
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	upstreamModel := thinking.ParseSuffix(req.Model).ModelName

	body := req.Payload
	if len(body) == 0 && len(opts.OriginalRequest) > 0 {
		body = opts.OriginalRequest
	}
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, toFormat, upstreamModel, body, true, false)
	translated, err = sjson.SetBytes(translated, "stream", true)
	if err != nil {
		return nil, fmt.Errorf("opencode executor: set stream: %w", err)
	}

	apiKey, _ := openCodeCreds(auth)
	if apiKey == "" {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	httpReq.Header.Set("User-Agent", "cli-proxy-opencode")
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, opts.Headers)

	authID, authLabel, authType, authValue := authAccountInfo(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       endpointURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.id,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("opencode executor: stream error status: %d, message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("opencode executor: close response body error: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk, 64)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("opencode executor: close stream body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)

		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			chunks := sdktranslator.TranslateStream(
				ctx,
				from,
				toFormat,
				req.Model,
				opts.OriginalRequest,
				translated,
				bytes.Clone(line),
				&param,
			)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *clipoauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _ := openCodeCreds(auth)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *clipoauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "opencode executor: request is nil"}
	}
	if ctx == nil {
		ctx = req.Context()
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(req.WithContext(ctx))
}

func (e *OpenCodeExecutor) Refresh(_ context.Context, auth *clipoauth.Auth) (*clipoauth.Auth, error) {
	return auth, nil
}

func (e *OpenCodeExecutor) CountTokens(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	payload := []byte("{}")
	if len(req.Payload) > 0 {
		payload = req.Payload
	}
	count := int64(len(payload))
	return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}`, count, count))}, nil
}

func openCodeCreds(auth *clipoauth.Auth) (string, bool) {
	if auth == nil {
		return "", false
	}
	if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
		return token, true
	}
	if auth.Metadata == nil {
		return "", false
	}
	if v, ok := auth.Metadata["api_key"]; ok {
		if token, ok := v.(string); ok && strings.TrimSpace(token) != "" {
			return token, true
		}
	}
	if v, ok := auth.Metadata["access_token"]; ok {
		if token, ok := v.(string); ok && strings.TrimSpace(token) != "" {
			return token, true
		}
	}
	return "", false
}

func authAccountInfo(auth *clipoauth.Auth) (string, string, string, string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue := auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}
