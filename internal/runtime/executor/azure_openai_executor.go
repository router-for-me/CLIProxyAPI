package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	azureOpenAIProvider           = "azure-openai"
	azureOpenAIUserAgent          = "cli-proxy-azure-openai"
	azureOpenAIPathModeDeployment = "deployment"
	azureOpenAIPathModeV1         = "v1"
	azureOpenAIAuthTypeAPIKey     = "api-key"
	azureOpenAIAuthTypeAAD        = "aad"
)

type AzureOpenAIExecutor struct {
	provider string
	cfg      *config.Config
}

type azureOpenAIOptions struct {
	Endpoint     string
	APIVersion   string
	Deployment   string
	PathMode     string
	AuthType     string
	IncludeUsage bool
	APIKey       string
	AADToken     string
}

func NewAzureOpenAIExecutor(provider string, cfg *config.Config) *AzureOpenAIExecutor {
	if strings.TrimSpace(provider) == "" {
		provider = azureOpenAIProvider
	}
	return &AzureOpenAIExecutor{provider: provider, cfg: cfg}
}

func (e *AzureOpenAIExecutor) Identifier() string { return e.provider }

func (e *AzureOpenAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	azOpts := e.resolveOptions(auth)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, deployment, err := e.translateChatPayload(ctx, req, opts, baseModel, from, to, azOpts, false)
	if err != nil {
		return resp, err
	}
	azOpts.Deployment = deployment
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	requestURL, err := buildAzureChatCompletionsURL(azOpts)
	if err != nil {
		return resp, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", azureOpenAIUserAgent)
	e.applyHeaders(httpReq, auth, azOpts)
	e.recordRequest(ctx, auth, requestURL, httpReq.Header, translated)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("azure openai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *AzureOpenAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	azOpts := e.resolveOptions(auth)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, deployment, err := e.translateChatPayload(ctx, req, opts, baseModel, from, to, azOpts, true)
	if err != nil {
		return nil, err
	}
	azOpts.Deployment = deployment
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	requestURL, err := buildAzureChatCompletionsURL(azOpts)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", azureOpenAIUserAgent)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	e.applyHeaders(httpReq, auth, azOpts)
	e.recordRequest(ctx, auth, requestURL, httpReq.Header, translated)

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
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("azure openai executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("azure openai executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		seenDone := false
		streamFailed := false
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}
			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
					bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
					continue
				}
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamFailed = true
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			dataPayload := bytes.TrimSpace(bytes.TrimPrefix(trimmedLine, []byte("data:")))
			if bytes.Equal(dataPayload, []byte("[DONE]")) {
				seenDone = true
			} else if gjson.GetBytes(dataPayload, "error").Exists() {
				streamFailed = true
				streamErr := statusErr{code: http.StatusBadGateway, msg: string(dataPayload)}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
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
		} else if !seenDone && !streamFailed {
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *AzureOpenAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("azure openai executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("azure openai executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *AzureOpenAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("azure openai executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *AzureOpenAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	e.applyHeaders(req, auth, e.resolveOptions(auth))
	return nil
}

func (e *AzureOpenAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("azure openai executor: request is nil")
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

func (e *AzureOpenAIExecutor) translateChatPayload(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string, from sdktranslator.Format, to sdktranslator.Format, azOpts azureOpenAIOptions, stream bool) ([]byte, string, error) {
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, "", err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	deployment := firstNonEmpty(azOpts.Deployment, baseModel, gjson.GetBytes(translated, "model").String())
	if deployment == "" {
		return nil, "", statusErr{code: http.StatusBadRequest, msg: "missing azure openai deployment"}
	}
	patched, err := patchAzureOpenAIRequestBody(translated, deployment, stream, azOpts.IncludeUsage)
	if err != nil {
		return nil, "", err
	}
	return patched, deployment, nil
}

func (e *AzureOpenAIExecutor) resolveOptions(auth *cliproxyauth.Auth) azureOpenAIOptions {
	opts := azureOpenAIOptions{PathMode: azureOpenAIPathModeDeployment, IncludeUsage: true}
	if compat := e.resolveCompatConfig(auth); compat != nil {
		opts.Endpoint = strings.TrimSpace(compat.BaseURL)
		opts.APIVersion = strings.TrimSpace(compat.APIVersion)
		if len(compat.APIKeyEntries) > 0 {
			opts.APIKey = strings.TrimSpace(compat.APIKeyEntries[0].APIKey)
		}
	}
	if auth != nil && auth.Attributes != nil {
		attrs := auth.Attributes
		opts.Endpoint = firstNonEmpty(attrs["endpoint"], attrs["base_url"], opts.Endpoint)
		opts.APIVersion = firstNonEmpty(attrs["api_version"], attrs["api-version"], opts.APIVersion)
		opts.Deployment = firstNonEmpty(attrs["deployment_id"], attrs["deployment"], attrs["azure_deployment"])
		opts.PathMode = strings.ToLower(firstNonEmpty(attrs["path_mode"], attrs["path-mode"], opts.PathMode))
		opts.AuthType = strings.ToLower(strings.TrimSpace(attrs["auth_type"]))
		opts.APIKey = firstNonEmpty(attrs["api_key"], opts.APIKey)
		opts.AADToken = firstNonEmpty(attrs["aad_token"], attrs["access_token"], attrs["bearer_token"])
		if includeUsage, ok := parseAzureBoolAttr(attrs, "include_usage"); ok {
			opts.IncludeUsage = includeUsage
		}
	}
	if opts.AuthType == "" {
		switch {
		case opts.APIKey != "":
			opts.AuthType = azureOpenAIAuthTypeAPIKey
		case opts.AADToken != "":
			opts.AuthType = azureOpenAIAuthTypeAAD
		default:
			opts.AuthType = azureOpenAIAuthTypeAPIKey
		}
	}
	if opts.AuthType == azureOpenAIAuthTypeAAD && opts.AADToken == "" {
		opts.AADToken = opts.APIKey
	}
	return opts
}

func (e *AzureOpenAIExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *AzureOpenAIExecutor) applyHeaders(req *http.Request, auth *cliproxyauth.Auth, opts azureOpenAIOptions) {
	if req == nil {
		return
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	switch strings.ToLower(strings.TrimSpace(opts.AuthType)) {
	case azureOpenAIAuthTypeAAD:
		req.Header.Del("api-key")
		if opts.AADToken != "" {
			req.Header.Set("Authorization", "Bearer "+opts.AADToken)
		}
	default:
		req.Header.Del("Authorization")
		if opts.APIKey != "" {
			req.Header.Set("api-key", opts.APIKey)
		}
	}
}

func (e *AzureOpenAIExecutor) recordRequest(ctx context.Context, auth *cliproxyauth.Auth, requestURL string, headers http.Header, body []byte) {
	authID, authLabel, authType, authValue := authLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       requestURL,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func buildAzureChatCompletionsURL(opts azureOpenAIOptions) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(opts.Endpoint))
	if err != nil {
		return "", fmt.Errorf("azure openai executor: invalid endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("azure openai executor: invalid endpoint")
	}
	pathMode := strings.ToLower(strings.TrimSpace(opts.PathMode))
	if pathMode == "" {
		pathMode = azureOpenAIPathModeDeployment
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	baseEscapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	switch pathMode {
	case azureOpenAIPathModeDeployment:
		deployment := strings.TrimSpace(opts.Deployment)
		if deployment == "" {
			return "", statusErr{code: http.StatusBadRequest, msg: "missing azure openai deployment"}
		}
		if strings.TrimSpace(opts.APIVersion) == "" {
			return "", statusErr{code: http.StatusBadRequest, msg: "missing azure openai api-version"}
		}
		parsed.Path = basePath + "/openai/deployments/" + deployment + "/chat/completions"
		parsed.RawPath = baseEscapedPath + "/openai/deployments/" + url.PathEscape(deployment) + "/chat/completions"
		query := parsed.Query()
		query.Set("api-version", opts.APIVersion)
		parsed.RawQuery = query.Encode()
	case azureOpenAIPathModeV1:
		parsed.Path = basePath + "/openai/v1/chat/completions"
		parsed.RawPath = baseEscapedPath + "/openai/v1/chat/completions"
		if strings.TrimSpace(opts.APIVersion) != "" {
			query := parsed.Query()
			query.Set("api-version", opts.APIVersion)
			parsed.RawQuery = query.Encode()
		}
	default:
		return "", statusErr{code: http.StatusBadRequest, msg: "unsupported azure openai path_mode: " + pathMode}
	}
	return parsed.String(), nil
}

func azureOpenAIChatCompletionsURL(baseURL, deploymentID, apiVersion string) (string, error) {
	return buildAzureChatCompletionsURL(azureOpenAIOptions{
		Endpoint:   baseURL,
		APIVersion: apiVersion,
		Deployment: deploymentID,
		PathMode:   azureOpenAIPathModeDeployment,
	})
}

func patchAzureOpenAIRequestBody(body []byte, deployment string, stream bool, includeUsage bool) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, statusErr{code: http.StatusBadRequest, msg: "azure openai request body must be a json object"}
	}
	patched, err := sjson.SetBytes(body, "model", deployment)
	if err != nil {
		return nil, err
	}
	if stream && includeUsage {
		patched, err = sjson.SetBytes(patched, "stream_options.include_usage", true)
		if err != nil {
			return nil, err
		}
	}
	return patched, nil
}

func parseAzureBoolAttr(attrs map[string]string, key string) (bool, bool) {
	value, ok := attrs[key]
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func authLogFields(auth *cliproxyauth.Auth) (authID, authLabel, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()
	return authID, authLabel, authType, authValue
}
