package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy_ai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codeBuddyAIChatPath = "/v2/chat/completions"
	codeBuddyAIAuthType = "codebuddy-ai"
)

type CodeBuddyAIExecutor struct {
	cfg *config.Config
}

func NewCodeBuddyAIExecutor(cfg *config.Config) *CodeBuddyAIExecutor {
	return &CodeBuddyAIExecutor{cfg: cfg}
}

func (e *CodeBuddyAIExecutor) Identifier() string { return codeBuddyAIAuthType }

func codeBuddyAICredentials(auth *cliproxyauth.Auth) (accessToken, userID, domain string) {
	if auth == nil {
		return "", "", ""
	}
	accessToken = metaStringValue(auth.Metadata, "access_token")
	userID = metaStringValue(auth.Metadata, "user_id")
	domain = metaStringValue(auth.Metadata, "domain")
	if domain == "" {
		domain = codebuddy_ai.DefaultDomain
	}
	return
}

func (e *CodeBuddyAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return fmt.Errorf("codebuddy-ai: missing access token")
	}
	e.applyHeaders(req, accessToken, userID, domain)
	return nil
}

func (e *CodeBuddyAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codebuddy-ai executor: request is nil")
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

func (e *CodeBuddyAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("codebuddy-ai: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	translated, _ = sjson.SetBytes(translated, "stream", true)
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	url := codebuddy_ai.BaseURL + codeBuddyAIChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, accessToken, userID, domain)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

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
		Body:      translated,
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
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy-ai executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if !isHTTPSuccess(httpResp.StatusCode) {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("codebuddy-ai executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	aggregatedBody, usageDetail, err := aggregateOpenAIChatCompletionStream(body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	reporter.publish(ctx, usageDetail)
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregatedBody, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *CodeBuddyAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("codebuddy-ai: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	url := codebuddy_ai.BaseURL + codeBuddyAIChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, accessToken, userID, domain)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

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
		Body:      translated,
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
	if !isHTTPSuccess(httpResp.StatusCode) {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		httpResp.Body.Close()
		log.Debugf("codebuddy-ai executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codebuddy-ai executor: close stream body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

func (e *CodeBuddyAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("codebuddy-ai: missing auth")
	}

	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		log.Debugf("codebuddy-ai executor: no refresh token available, skipping refresh")
		return auth, nil
	}

	accessToken, userID, domain := codeBuddyAICredentials(auth)

	authSvc := codebuddy_ai.NewCodeBuddyAIAuth(e.cfg)
	storage, err := authSvc.RefreshToken(ctx, accessToken, refreshToken, userID, domain)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["access_token"] = storage.AccessToken
	if storage.RefreshToken != "" {
		updated.Metadata["refresh_token"] = storage.RefreshToken
	}
	updated.Metadata["expires_in"] = storage.ExpiresIn
	updated.Metadata["domain"] = storage.Domain
	if storage.UserID != "" {
		updated.Metadata["user_id"] = storage.UserID
	}
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	return updated, nil
}

func (e *CodeBuddyAIExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codebuddy-ai: count tokens not supported")
}

func (e *CodeBuddyAIExecutor) applyHeaders(req *http.Request, accessToken, userID, domain string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codebuddy_ai.UserAgent)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-IDE-Type", "IDE")
	req.Header.Set("X-IDE-Name", "CodeBuddy")
	req.Header.Set("X-IDE-Version", "1.100.0")
	req.Header.Set("X-Product", "cloud")
	req.Header.Set("X-Product-Version", "1.100.0")
}

var codeBuddyAIInternalModelPrefixes = []string{
	"completion-",
	"codewise-",
	"nes-",
	"chat-",
	"enhance-",
}

var codeBuddyAIAllowedInternalModels = map[string]bool{
	"o4-mini": true,
}

func isCodeBuddyAIInternalModel(id string) bool {
	for _, prefix := range codeBuddyAIInternalModelPrefixes {
		if strings.HasPrefix(id, prefix) {
			return !codeBuddyAIAllowedInternalModels[id]
		}
	}
	return false
}

func FetchCodeBuddyAIModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		log.Infof("codebuddy-ai: no access token found, using static model list")
		return registry.GetCodeBuddyAIModels()
	}

	log.Debugf("codebuddy-ai: fetching dynamic models from config API")

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codebuddy_ai.BaseURL+"/v3/config", nil)
	if err != nil {
		log.Warnf("codebuddy-ai: failed to create config request: %v", err)
		return registry.GetCodeBuddyAIModels()
	}

	req.Header.Set("User-Agent", codebuddy_ai.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-IDE-Type", "CodeBuddyIDE")
	req.Header.Set("X-IDE-Name", "CodeBuddyIDE")
	req.Header.Set("X-IDE-Version", "4.9.5")
	req.Header.Set("X-Product-Version", "4.9.5")
	req.Header.Set("X-Env-ID", "production")
	req.Header.Set("X-Product", "SaaS")

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("codebuddy-ai: fetch models canceled: %v", err)
		} else {
			log.Warnf("codebuddy-ai: using static models (config API fetch failed: %v)", err)
		}
		return registry.GetCodeBuddyAIModels()
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy-ai: close config response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("codebuddy-ai: failed to read config response: %v", err)
		return registry.GetCodeBuddyAIModels()
	}

	if resp.StatusCode != http.StatusOK {
		log.Warnf("codebuddy-ai: config API returned status %d", resp.StatusCode)
		return registry.GetCodeBuddyAIModels()
	}

	modelsResult := gjson.GetBytes(body, "data.models")
	if !modelsResult.Exists() || !modelsResult.IsArray() {
		log.Warn("codebuddy-ai: config API response missing data.models array")
		return registry.GetCodeBuddyAIModels()
	}

	var dynamicModels []*registry.ModelInfo
	now := time.Now().Unix()
	count := 0

	modelsResult.ForEach(func(key, value gjson.Result) bool {
		id := value.Get("id").String()
		if id == "" {
			return true
		}

		if isCodeBuddyAIInternalModel(id) {
			return true
		}

		name := value.Get("name").String()
		if name == "" {
			name = id
		}

		descEn := value.Get("descriptionEn").String()
		descZh := value.Get("descriptionZh").String()
		desc := descEn
		if desc == "" {
			desc = descZh
		}
		if desc == "" {
			desc = name + " via CodeBuddy AI"
		}

		maxInputTokens := int(value.Get("maxInputTokens").Int())
		maxOutputTokens := int(value.Get("maxOutputTokens").Int())
		maxAllowedSize := int(value.Get("maxAllowedSize").Int())

		contextLength := maxInputTokens
		if contextLength <= 0 && maxAllowedSize > 0 {
			contextLength = maxAllowedSize
		}
		if contextLength <= 0 {
			contextLength = 128000
		}
		if maxOutputTokens <= 0 {
			maxOutputTokens = 32768
		}

		supportsReasoning := value.Get("supportsReasoning").Bool()
		onlyReasoning := value.Get("onlyReasoning").Bool()

		var thinkingSupport *registry.ThinkingSupport
		if supportsReasoning || onlyReasoning {
			thinkingSupport = &registry.ThinkingSupport{ZeroAllowed: true}
			reasoningEffort := value.Get("reasoning.effort").String()
			if reasoningEffort == "medium" || reasoningEffort == "high" {
				thinkingSupport.DynamicAllowed = true
			}
		}

		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "codebuddy-ai",
			Type:                "codebuddy-ai",
			DisplayName:         name,
			Description:         desc,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxOutputTokens,
			Thinking:            thinkingSupport,
			SupportedEndpoints:  []string{"/chat/completions"},
		})
		count++
		return true
	})

	log.Infof("codebuddy-ai: fetched %d models from config API", count)
	if count == 0 {
		log.Warn("codebuddy-ai: no models parsed from config API, using static fallback")
		return registry.GetCodeBuddyAIModels()
	}

	return dynamicModels
}
