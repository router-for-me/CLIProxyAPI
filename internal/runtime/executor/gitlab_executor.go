package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gitlab"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	gitLabProviderKey            = "gitlab"
	gitLabAuthMethodOAuth        = "oauth"
	gitLabAuthMethodPAT          = "pat"
	gitLabChatEndpoint           = "/api/v4/chat/completions"
	gitLabCodeSuggestionsEndpoint = "/api/v4/code_suggestions/completions"
)

type GitLabExecutor struct {
	cfg *config.Config
}

type gitLabPrompt struct {
	Instruction          string
	FileName             string
	ContentAboveCursor   string
	ChatContext          []map[string]any
	CodeSuggestionContext []map[string]any
}

func NewGitLabExecutor(cfg *config.Config) *GitLabExecutor {
	return &GitLabExecutor{cfg: cfg}
}

func (e *GitLabExecutor) Identifier() string { return gitLabProviderKey }

func (e *GitLabExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	translated, err := e.translateToOpenAI(req, opts)
	if err != nil {
		return resp, err
	}
	prompt := buildGitLabPrompt(translated)
	if strings.TrimSpace(prompt.Instruction) == "" && strings.TrimSpace(prompt.ContentAboveCursor) == "" {
		err = statusErr{code: http.StatusBadRequest, msg: "gitlab duo executor: request has no usable text content"}
		return resp, err
	}

	text, err := e.invoke(ctx, auth, prompt)
	if err != nil {
		return resp, err
	}

	responseModel := gitLabResolvedModel(auth, req.Model)
	openAIResponse := buildGitLabOpenAIResponse(responseModel, text, translated)
	reporter.publish(ctx, parseOpenAIUsage(openAIResponse))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		sdktranslator.FromString("openai"),
		opts.SourceFormat,
		req.Model,
		opts.OriginalRequest,
		translated,
		openAIResponse,
		&param,
	)
	return cliproxyexecutor.Response{Payload: []byte(out), Headers: make(http.Header)}, nil
}

func (e *GitLabExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	translated, err := e.translateToOpenAI(req, opts)
	if err != nil {
		return nil, err
	}
	prompt := buildGitLabPrompt(translated)
	if strings.TrimSpace(prompt.Instruction) == "" && strings.TrimSpace(prompt.ContentAboveCursor) == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "gitlab duo executor: request has no usable text content"}
	}

	text, err := e.invoke(ctx, auth, prompt)
	if err != nil {
		return nil, err
	}

	responseModel := gitLabResolvedModel(auth, req.Model)
	openAIResponse := buildGitLabOpenAIResponse(responseModel, text, translated)
	reporter.publish(ctx, parseOpenAIUsage(openAIResponse))
	reporter.ensurePublished(ctx)

	out := make(chan cliproxyexecutor.StreamChunk, 8)
	go func() {
		defer close(out)
		var param any
		lines := buildGitLabOpenAIStream(responseModel, text)
		for _, line := range lines {
			chunks := sdktranslator.TranslateStream(
				ctx,
				sdktranslator.FromString("openai"),
				opts.SourceFormat,
				req.Model,
				opts.OriginalRequest,
				translated,
				[]byte(line),
				&param,
			)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: make(http.Header), Chunks: out}, nil
}

func (e *GitLabExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("gitlab duo executor: auth is nil")
	}
	baseURL := gitLabBaseURL(auth)
	token := gitLabPrimaryToken(auth)
	if baseURL == "" || token == "" {
		return nil, fmt.Errorf("gitlab duo executor: missing base URL or token")
	}

	client := gitlab.NewAuthClient(e.cfg)
	method := strings.ToLower(strings.TrimSpace(gitLabMetadataString(auth.Metadata, "auth_method", "auth_kind")))
	if method == "" {
		method = gitLabAuthMethodOAuth
	}

	if method == gitLabAuthMethodOAuth {
		if refreshed, refreshErr := e.refreshOAuthToken(ctx, client, auth, baseURL); refreshErr == nil && refreshed != nil {
			token = refreshed.AccessToken
			applyGitLabTokenMetadata(auth.Metadata, refreshed)
		}
	}

	direct, err := client.FetchDirectAccess(ctx, baseURL, token)
	if err != nil && method == gitLabAuthMethodOAuth {
		if refreshed, refreshErr := e.refreshOAuthToken(ctx, client, auth, baseURL); refreshErr == nil && refreshed != nil {
			token = refreshed.AccessToken
			applyGitLabTokenMetadata(auth.Metadata, refreshed)
			direct, err = client.FetchDirectAccess(ctx, baseURL, token)
		}
	}
	if err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = gitLabProviderKey
	auth.Metadata["auth_method"] = method
	auth.Metadata["auth_kind"] = gitLabAuthKind(method)
	auth.Metadata["base_url"] = gitlab.NormalizeBaseURL(baseURL)
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	mergeGitLabDirectAccessMetadata(auth.Metadata, direct)
	return auth, nil
}

func (e *GitLabExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	translated := sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FromString("openai"), baseModel, req.Payload, false)
	enc, err := tokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("gitlab duo executor: tokenizer init failed: %w", err)
	}
	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(count), Headers: make(http.Header)}, nil
}

func (e *GitLabExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gitlab duo executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if token := gitLabPrimaryToken(auth); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	return newProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

func (e *GitLabExecutor) translateToOpenAI(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	return sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FromString("openai"), baseModel, req.Payload, opts.Stream), nil
}

func (e *GitLabExecutor) invoke(ctx context.Context, auth *cliproxyauth.Auth, prompt gitLabPrompt) (string, error) {
	if text, err := e.requestChat(ctx, auth, prompt); err == nil {
		return text, nil
	} else if !shouldFallbackToCodeSuggestions(err) {
		return "", err
	}
	return e.requestCodeSuggestions(ctx, auth, prompt)
}

func (e *GitLabExecutor) requestChat(ctx context.Context, auth *cliproxyauth.Auth, prompt gitLabPrompt) (string, error) {
	body := map[string]any{
		"content":            prompt.Instruction,
		"with_clean_history": true,
	}
	if len(prompt.ChatContext) > 0 {
		body["additional_context"] = prompt.ChatContext
	}
	return e.doJSONTextRequest(ctx, auth, gitLabChatEndpoint, body)
}

func (e *GitLabExecutor) requestCodeSuggestions(ctx context.Context, auth *cliproxyauth.Auth, prompt gitLabPrompt) (string, error) {
	contentAbove := strings.TrimSpace(prompt.ContentAboveCursor)
	if contentAbove == "" {
		contentAbove = prompt.Instruction
	}
	body := map[string]any{
		"current_file": map[string]any{
			"file_name":            prompt.FileName,
			"content_above_cursor": contentAbove,
			"content_below_cursor": "",
		},
		"intent":          "generation",
		"generation_type": "small_file",
		"user_instruction": prompt.Instruction,
		"stream":          false,
	}
	if len(prompt.CodeSuggestionContext) > 0 {
		body["context"] = prompt.CodeSuggestionContext
	}
	return e.doJSONTextRequest(ctx, auth, gitLabCodeSuggestionsEndpoint, body)
}

func (e *GitLabExecutor) doJSONTextRequest(ctx context.Context, auth *cliproxyauth.Auth, endpoint string, payload map[string]any) (string, error) {
	token := gitLabPrimaryToken(auth)
	baseURL := gitLabBaseURL(auth)
	if token == "" || baseURL == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "gitlab duo executor: missing credentials"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("gitlab duo executor: marshal request failed: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/GitLab-Duo")

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   req.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return "", err
	}
	appendAPIResponseChunk(ctx, e.cfg, respBody)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", statusErr{code: resp.StatusCode, msg: strings.TrimSpace(string(respBody))}
	}

	text, err := parseGitLabTextResponse(endpoint, respBody)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (e *GitLabExecutor) refreshOAuthToken(ctx context.Context, client *gitlab.AuthClient, auth *cliproxyauth.Auth, baseURL string) (*gitlab.TokenResponse, error) {
	if auth == nil {
		return nil, fmt.Errorf("gitlab duo executor: auth is nil")
	}
	refreshToken := gitLabMetadataString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return nil, fmt.Errorf("gitlab duo executor: refresh token missing")
	}
	if !gitLabOAuthTokenNeedsRefresh(auth.Metadata) && gitLabPrimaryToken(auth) != "" {
		return nil, nil
	}
	return client.RefreshTokens(
		ctx,
		baseURL,
		gitLabMetadataString(auth.Metadata, "oauth_client_id"),
		gitLabMetadataString(auth.Metadata, "oauth_client_secret"),
		refreshToken,
	)
}

func buildGitLabPrompt(payload []byte) gitLabPrompt {
	root := gjson.ParseBytes(payload)
	prompt := gitLabPrompt{
		FileName: "prompt.txt",
	}

	msgs := root.Get("messages")
	if msgs.Exists() && msgs.IsArray() {
		systemIndex := 0
		contextIndex := 0
		transcript := make([]string, 0, len(msgs.Array()))
		var lastUser string
		msgs.ForEach(func(_, msg gjson.Result) bool {
			role := strings.TrimSpace(msg.Get("role").String())
			if role == "" {
				role = "user"
			}
			content := openAIContentText(msg.Get("content"))
			if content == "" {
				return true
			}
			switch role {
			case "system":
				systemIndex++
				prompt.ChatContext = append(prompt.ChatContext, map[string]any{
					"category": "snippet",
					"id":       fmt.Sprintf("system-%d", systemIndex),
					"content":  content,
				})
			case "user":
				lastUser = content
				contextIndex++
				prompt.CodeSuggestionContext = append(prompt.CodeSuggestionContext, map[string]any{
					"type":    "snippet",
					"name":    fmt.Sprintf("user-%d", contextIndex),
					"content": content,
				})
				transcript = append(transcript, "User:\n"+content)
			default:
				contextIndex++
				prompt.ChatContext = append(prompt.ChatContext, map[string]any{
					"category": "snippet",
					"id":       fmt.Sprintf("%s-%d", role, contextIndex),
					"content":  content,
				})
				prompt.CodeSuggestionContext = append(prompt.CodeSuggestionContext, map[string]any{
					"type":    "snippet",
					"name":    fmt.Sprintf("%s-%d", role, contextIndex),
					"content": content,
				})
				transcript = append(transcript, strings.Title(role)+":\n"+content)
			}
			return true
		})
		prompt.Instruction = strings.TrimSpace(lastUser)
		prompt.ContentAboveCursor = truncateGitLabPrompt(strings.Join(transcript, "\n\n"), 12000)
	}

	if prompt.Instruction == "" {
		for _, key := range []string{"prompt", "input", "instructions"} {
			if value := strings.TrimSpace(root.Get(key).String()); value != "" {
				prompt.Instruction = value
				break
			}
		}
	}
	if prompt.ContentAboveCursor == "" {
		prompt.ContentAboveCursor = prompt.Instruction
	}
	prompt.Instruction = truncateGitLabPrompt(prompt.Instruction, 4000)
	prompt.ContentAboveCursor = truncateGitLabPrompt(prompt.ContentAboveCursor, 12000)
	return prompt
}

func openAIContentText(content gjson.Result) string {
	segments := make([]string, 0, 8)
	collectOpenAIContent(content, &segments)
	return strings.TrimSpace(strings.Join(segments, "\n"))
}

func truncateGitLabPrompt(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit])
}

func parseGitLabTextResponse(endpoint string, body []byte) (string, error) {
	if endpoint == gitLabChatEndpoint {
		var text string
		if err := json.Unmarshal(body, &text); err == nil {
			return text, nil
		}
		if value := strings.TrimSpace(gjson.GetBytes(body, "response").String()); value != "" {
			return value, nil
		}
	}
	if value := strings.TrimSpace(gjson.GetBytes(body, "choices.0.text").String()); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(gjson.GetBytes(body, "response").String()); value != "" {
		return value, nil
	}
	var plain string
	if err := json.Unmarshal(body, &plain); err == nil && strings.TrimSpace(plain) != "" {
		return plain, nil
	}
	return "", fmt.Errorf("gitlab duo executor: upstream returned no text payload")
}

func shouldFallbackToCodeSuggestions(err error) bool {
	if err == nil {
		return false
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		return false
	}
	switch status.StatusCode() {
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func buildGitLabOpenAIResponse(model, text string, translatedReq []byte) []byte {
	promptTokens, completionTokens := gitLabUsage(model, translatedReq, text)
	payload := map[string]any{
		"id":      fmt.Sprintf("gitlab-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func buildGitLabOpenAIStream(model, text string) []string {
	now := time.Now().Unix()
	id := fmt.Sprintf("gitlab-%d", time.Now().UnixNano())
	chunks := []map[string]any{
		{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": now,
			"model":   model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"role": "assistant"},
			}},
		},
		{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": now,
			"model":   model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"content": text},
			}},
		},
		{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": now,
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		},
	}
	lines := make([]string, 0, len(chunks)+1)
	for _, chunk := range chunks {
		raw, _ := json.Marshal(chunk)
		lines = append(lines, "data: "+string(raw))
	}
	lines = append(lines, "data: [DONE]")
	return lines
}

func gitLabUsage(model string, translatedReq []byte, text string) (int64, int64) {
	enc, err := tokenizerForModel(model)
	if err != nil {
		return 0, 0
	}
	promptTokens, err := countOpenAIChatTokens(enc, translatedReq)
	if err != nil {
		promptTokens = 0
	}
	completionCount, err := enc.Count(strings.TrimSpace(text))
	if err != nil {
		return promptTokens, 0
	}
	return promptTokens, int64(completionCount)
}

func gitLabPrimaryToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if token := gitLabMetadataString(auth.Metadata, "access_token"); token != "" {
		return token
	}
	return gitLabMetadataString(auth.Metadata, "personal_access_token")
}

func gitLabBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	return gitlab.NormalizeBaseURL(gitLabMetadataString(auth.Metadata, "base_url"))
}

func gitLabResolvedModel(auth *cliproxyauth.Auth, requested string) string {
	requested = strings.TrimSpace(thinking.ParseSuffix(requested).ModelName)
	if requested != "" && !strings.EqualFold(requested, "gitlab-duo") {
		return requested
	}
	if auth != nil && auth.Metadata != nil {
		for _, model := range gitlab.ExtractDiscoveredModels(auth.Metadata) {
			if name := strings.TrimSpace(model.ModelName); name != "" {
				return name
			}
		}
	}
	if requested != "" {
		return requested
	}
	return "gitlab-duo"
}

func gitLabMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if metadata == nil {
			return ""
		}
		if value, ok := metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func gitLabOAuthTokenNeedsRefresh(metadata map[string]any) bool {
	expiry := gitLabMetadataString(metadata, "oauth_expires_at")
	if expiry == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return true
	}
	return time.Until(ts) <= 5*time.Minute
}

func applyGitLabTokenMetadata(metadata map[string]any, tokenResp *gitlab.TokenResponse) {
	if metadata == nil || tokenResp == nil {
		return
	}
	if accessToken := strings.TrimSpace(tokenResp.AccessToken); accessToken != "" {
		metadata["access_token"] = accessToken
	}
	if refreshToken := strings.TrimSpace(tokenResp.RefreshToken); refreshToken != "" {
		metadata["refresh_token"] = refreshToken
	}
	if tokenType := strings.TrimSpace(tokenResp.TokenType); tokenType != "" {
		metadata["token_type"] = tokenType
	}
	if scope := strings.TrimSpace(tokenResp.Scope); scope != "" {
		metadata["scope"] = scope
	}
	if expiry := gitlab.TokenExpiry(time.Now(), tokenResp); !expiry.IsZero() {
		metadata["oauth_expires_at"] = expiry.Format(time.RFC3339)
	}
}

func mergeGitLabDirectAccessMetadata(metadata map[string]any, direct *gitlab.DirectAccessResponse) {
	if metadata == nil || direct == nil {
		return
	}
	if base := strings.TrimSpace(direct.BaseURL); base != "" {
		metadata["duo_gateway_base_url"] = base
	}
	if token := strings.TrimSpace(direct.Token); token != "" {
		metadata["duo_gateway_token"] = token
	}
	if direct.ExpiresAt > 0 {
		expiry := time.Unix(direct.ExpiresAt, 0).UTC()
		metadata["duo_gateway_expires_at"] = expiry.Format(time.RFC3339)
		if ttl := expiry.Sub(time.Now().UTC()); ttl > 0 {
			interval := int(ttl.Seconds()) / 2
			switch {
			case interval < 60:
				interval = 60
			case interval > 240:
				interval = 240
			}
			metadata["refresh_interval_seconds"] = interval
		}
	}
	if len(direct.Headers) > 0 {
		headers := make(map[string]string, len(direct.Headers))
		for key, value := range direct.Headers {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			headers[key] = value
		}
		if len(headers) > 0 {
			metadata["duo_gateway_headers"] = headers
		}
	}
	if direct.ModelDetails != nil {
		modelDetails := map[string]any{}
		if provider := strings.TrimSpace(direct.ModelDetails.ModelProvider); provider != "" {
			modelDetails["model_provider"] = provider
			metadata["model_provider"] = provider
		}
		if model := strings.TrimSpace(direct.ModelDetails.ModelName); model != "" {
			modelDetails["model_name"] = model
			metadata["model_name"] = model
		}
		if len(modelDetails) > 0 {
			metadata["model_details"] = modelDetails
		}
	}
}

func gitLabAuthKind(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case gitLabAuthMethodPAT:
		return "personal_access_token"
	default:
		return "oauth"
	}
}

func GitLabModelsFromAuth(auth *cliproxyauth.Auth) []*registry.ModelInfo {
	models := make([]*registry.ModelInfo, 0, 4)
	seen := make(map[string]struct{}, 4)
	addModel := func(id, displayName, provider string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		models = append(models, &registry.ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     time.Now().Unix(),
			OwnedBy:     "gitlab",
			Type:        "gitlab",
			DisplayName: displayName,
			Description: provider,
			UserDefined: true,
		})
	}

	addModel("gitlab-duo", "GitLab Duo", "gitlab")
	if auth == nil {
		return models
	}
	for _, model := range gitlab.ExtractDiscoveredModels(auth.Metadata) {
		name := strings.TrimSpace(model.ModelName)
		if name == "" {
			continue
		}
		displayName := "GitLab Duo"
		if provider := strings.TrimSpace(model.ModelProvider); provider != "" {
			displayName = fmt.Sprintf("GitLab Duo (%s)", provider)
		}
		addModel(name, displayName, strings.TrimSpace(model.ModelProvider))
	}
	return models
}
