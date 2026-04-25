package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/internal/auth/gitlab"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/registry"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/util"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	gitLabProviderKey             = "gitlab"
	gitLabAuthMethodOAuth         = "oauth"
	gitLabAuthMethodPAT           = "pat"
	gitLabChatEndpoint            = "/api/v4/chat/completions"
	gitLabCodeSuggestionsEndpoint = "/api/v4/code_suggestions/completions"
	gitLabSSEStreamingHeader      = "X-Supports-Sse-Streaming"
	gitLabContext1MBeta           = "context-1m-2025-08-07"
	gitLabNativeUserAgent         = "CLIProxyAPIPlus/GitLab-Duo"
)

type GitLabExecutor struct {
	cfg *config.Config
}

type gitLabCatalogModel struct {
	ID          string
	DisplayName string
	Provider    string
}

type gitLabPrompt struct {
	Instruction           string
	FileName              string
	ContentAboveCursor    string
	ChatContext           []map[string]any
	CodeSuggestionContext []map[string]any
}

type gitLabOpenAIStreamState struct {
	ID           string
	Model        string
	Created      int64
	LastFullText string
	Started      bool
	Finished     bool
}

var gitLabAgenticCatalog = []gitLabCatalogModel{
	{ID: "duo-chat-gpt-5-1", DisplayName: "GitLab Duo (GPT-5.1)", Provider: "openai"},
	{ID: "duo-chat-opus-4-6", DisplayName: "GitLab Duo (Claude Opus 4.6)", Provider: "anthropic"},
	{ID: "duo-chat-opus-4-5", DisplayName: "GitLab Duo (Claude Opus 4.5)", Provider: "anthropic"},
	{ID: "duo-chat-sonnet-4-6", DisplayName: "GitLab Duo (Claude Sonnet 4.6)", Provider: "anthropic"},
	{ID: "duo-chat-sonnet-4-5", DisplayName: "GitLab Duo (Claude Sonnet 4.5)", Provider: "anthropic"},
	{ID: "duo-chat-gpt-5-mini", DisplayName: "GitLab Duo (GPT-5 Mini)", Provider: "openai"},
	{ID: "duo-chat-gpt-5-2", DisplayName: "GitLab Duo (GPT-5.2)", Provider: "openai"},
	{ID: "duo-chat-gpt-5-2-codex", DisplayName: "GitLab Duo (GPT-5.2 Codex)", Provider: "openai"},
	{ID: "duo-chat-gpt-5-codex", DisplayName: "GitLab Duo (GPT-5 Codex)", Provider: "openai"},
	{ID: "duo-chat-haiku-4-5", DisplayName: "GitLab Duo (Claude Haiku 4.5)", Provider: "anthropic"},
}

var gitLabModelAliases = map[string]string{
	"duo-chat-haiku-4-6": "duo-chat-haiku-4-5",
}

func NewGitLabExecutor(cfg *config.Config) *GitLabExecutor {
	return &GitLabExecutor{cfg: cfg}
}

func (e *GitLabExecutor) Identifier() string { return gitLabProviderKey }

func (e *GitLabExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if nativeExec, nativeAuth, nativeReq, ok := e.nativeGateway(auth, req); ok {
		return nativeExec.Execute(ctx, nativeAuth, nativeReq, opts)
	}
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

	text, err := e.invokeText(ctx, auth, prompt)
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
	if nativeExec, nativeAuth, nativeReq, ok := e.nativeGateway(auth, req); ok {
		return nativeExec.ExecuteStream(ctx, nativeAuth, nativeReq, opts)
	}
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

	if result, streamErr := e.requestCodeSuggestionsStream(ctx, auth, prompt, translated, req, opts, reporter); streamErr == nil {
		return result, nil
	} else if !shouldFallbackToCodeSuggestions(streamErr) {
		return nil, streamErr
	}

	text, err := e.invokeText(ctx, auth, prompt)
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
	if nativeExec, nativeAuth, nativeReq, ok := e.nativeGateway(auth, req); ok {
		return nativeExec.CountTokens(ctx, nativeAuth, nativeReq, opts)
	}
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
	if nativeExec, nativeAuth := e.nativeGatewayHTTP(auth); nativeExec != nil {
		return nativeExec.HttpRequest(ctx, nativeAuth, req)
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

func (e *GitLabExecutor) nativeGateway(
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
) (cliproxyauth.ProviderExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, bool) {
	if nativeAuth, ok := buildGitLabAnthropicGatewayAuth(auth, req.Model); ok {
		nativeReq := req
		nativeReq.Model = gitLabResolvedModel(auth, req.Model)
		return NewClaudeExecutor(e.cfg), nativeAuth, nativeReq, true
	}
	if nativeAuth, ok := buildGitLabOpenAIGatewayAuth(auth, req.Model); ok {
		nativeReq := req
		nativeReq.Model = gitLabResolvedModel(auth, req.Model)
		return NewCodexExecutor(e.cfg), nativeAuth, nativeReq, true
	}
	return nil, nil, req, false
}

func (e *GitLabExecutor) nativeGatewayHTTP(auth *cliproxyauth.Auth) (cliproxyauth.ProviderExecutor, *cliproxyauth.Auth) {
	if nativeAuth, ok := buildGitLabAnthropicGatewayAuth(auth, ""); ok {
		return NewClaudeExecutor(e.cfg), nativeAuth
	}
	if nativeAuth, ok := buildGitLabOpenAIGatewayAuth(auth, ""); ok {
		return NewCodexExecutor(e.cfg), nativeAuth
	}
	return nil, nil
}

func (e *GitLabExecutor) invokeText(ctx context.Context, auth *cliproxyauth.Auth, prompt gitLabPrompt) (string, error) {
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
		"intent":           "generation",
		"generation_type":  "small_file",
		"user_instruction": prompt.Instruction,
		"stream":           false,
	}
	if len(prompt.CodeSuggestionContext) > 0 {
		body["context"] = prompt.CodeSuggestionContext
	}
	return e.doJSONTextRequest(ctx, auth, gitLabCodeSuggestionsEndpoint, body)
}

func (e *GitLabExecutor) requestCodeSuggestionsStream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	prompt gitLabPrompt,
	translated []byte,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	reporter *usageReporter,
) (*cliproxyexecutor.StreamResult, error) {
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
		"intent":           "generation",
		"generation_type":  "small_file",
		"user_instruction": prompt.Instruction,
		"stream":           true,
	}
	if len(prompt.CodeSuggestionContext) > 0 {
		body["context"] = prompt.CodeSuggestionContext
	}

	httpResp, bodyRaw, err := e.doJSONRequest(ctx, auth, gitLabCodeSuggestionsEndpoint, body, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, readErr := io.ReadAll(httpResp.Body)
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, respBody)
		return nil, statusErr{code: httpResp.StatusCode, msg: strings.TrimSpace(string(respBody))}
	}

	responseModel := gitLabResolvedModel(auth, req.Model)
	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer func() { _ = httpResp.Body.Close() }()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)

		var (
			param     any
			eventName string
			state     gitLabOpenAIStreamState
		)
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			appendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("event:")) {
				eventName = strings.TrimSpace(string(trimmed[len("event:"):]))
				continue
			}
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			normalized := normalizeGitLabStreamChunk(eventName, payload, responseModel, &state)
			eventName = ""
			for _, item := range normalized {
				if detail, ok := parseOpenAIStreamUsage(item); ok {
					reporter.publish(ctx, detail)
				}
				chunks := sdktranslator.TranslateStream(
					ctx,
					sdktranslator.FromString("openai"),
					opts.SourceFormat,
					req.Model,
					opts.OriginalRequest,
					translated,
					item,
					&param,
				)
				for i := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
			return
		}
		if !state.Finished {
			for _, item := range finalizeGitLabStream(responseModel, &state) {
				chunks := sdktranslator.TranslateStream(
					ctx,
					sdktranslator.FromString("openai"),
					opts.SourceFormat,
					req.Model,
					opts.OriginalRequest,
					translated,
					item,
					&param,
				)
				for i := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
				}
			}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: cloneGitLabStreamHeaders(httpResp.Header, bodyRaw),
		Chunks:  out,
	}, nil
}

func (e *GitLabExecutor) doJSONTextRequest(ctx context.Context, auth *cliproxyauth.Auth, endpoint string, payload map[string]any) (string, error) {
	resp, _, err := e.doJSONRequest(ctx, auth, endpoint, payload, "application/json")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

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

func (e *GitLabExecutor) doJSONRequest(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	endpoint string,
	payload map[string]any,
	accept string,
) (*http.Response, []byte, error) {
	token := gitLabPrimaryToken(auth)
	baseURL := gitLabBaseURL(auth)
	if token == "" || baseURL == "" {
		return nil, nil, statusErr{code: http.StatusUnauthorized, msg: "gitlab duo executor: missing credentials"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab duo executor: marshal request failed: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "CLIProxyAPI/GitLab-Duo")
	applyGitLabRequestHeaders(req, auth)
	if strings.EqualFold(accept, "text/event-stream") {
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set(gitLabSSEStreamingHeader, "true")
		req.Header.Set("Accept-Encoding", "identity")
	}

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
		return nil, body, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	return resp, body, nil
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

func applyGitLabRequestHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	for key, value := range gitLabGatewayHeaders(auth, "") {
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func gitLabGatewayHeaders(auth *cliproxyauth.Auth, targetProvider string) map[string]string {
	out := make(map[string]string)
	if auth != nil && auth.Metadata != nil {
		raw, ok := auth.Metadata["duo_gateway_headers"]
		if ok {
			switch typed := raw.(type) {
			case map[string]string:
				for key, value := range typed {
					key = strings.TrimSpace(key)
					value = strings.TrimSpace(value)
					if key != "" && value != "" {
						out[key] = value
					}
				}
			case map[string]any:
				for key, value := range typed {
					key = strings.TrimSpace(key)
					if key == "" {
						continue
					}
					strValue := strings.TrimSpace(fmt.Sprint(value))
					if strValue != "" {
						out[key] = strValue
					}
				}
			}
		}
	}
	if _, ok := out["User-Agent"]; !ok {
		out["User-Agent"] = gitLabNativeUserAgent
	}
	// Always set anthropic-beta for both openai and anthropic gateways to ensure
	// context-1m support is enabled when using GitLab Duo with Claude models.
	if strings.EqualFold(strings.TrimSpace(targetProvider), "openai") ||
		strings.EqualFold(strings.TrimSpace(targetProvider), "anthropic") {
		if _, ok := out["anthropic-beta"]; !ok {
			out["anthropic-beta"] = gitLabContext1MBeta
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneGitLabStreamHeaders(headers http.Header, _ []byte) http.Header {
	cloned := headers.Clone()
	if cloned == nil {
		cloned = make(http.Header)
	}
	cloned.Set("Content-Type", "text/event-stream")
	return cloned
}

func normalizeGitLabStreamChunk(eventName string, payload []byte, fallbackModel string, state *gitLabOpenAIStreamState) [][]byte {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		return finalizeGitLabStream(fallbackModel, state)
	}

	root := gjson.ParseBytes(payload)
	if root.Exists() {
		if obj := root.Get("object").String(); obj == "chat.completion.chunk" {
			return [][]byte{append([]byte("data: "), bytes.Clone(payload)...)}
		}
		if root.Get("choices.0.delta").Exists() || root.Get("choices.0.finish_reason").Exists() {
			return [][]byte{append([]byte("data: "), bytes.Clone(payload)...)}
		}
	}

	state.ensureInitialized(fallbackModel, root)

	switch strings.TrimSpace(eventName) {
	case "stream_end":
		return finalizeGitLabStream(fallbackModel, state)
	case "stream_start":
		if text := extractGitLabStreamText(root); text != "" {
			return state.emitText(text)
		}
		return nil
	}

	if done := root.Get("done"); done.Exists() && done.Bool() {
		return finalizeGitLabStream(fallbackModel, state)
	}
	if finishReason := strings.TrimSpace(root.Get("finish_reason").String()); finishReason != "" {
		out := state.emitText(extractGitLabStreamText(root))
		return append(out, state.finish(finishReason)...)
	}

	return state.emitText(extractGitLabStreamText(root))
}

func extractGitLabStreamText(root gjson.Result) string {
	for _, key := range []string{
		"choices.0.delta.content",
		"choices.0.text",
		"delta.content",
		"content_chunk",
		"content",
		"text",
		"response",
		"completion",
	} {
		if value := root.Get(key).String(); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func finalizeGitLabStream(fallbackModel string, state *gitLabOpenAIStreamState) [][]byte {
	if state == nil {
		return nil
	}
	state.ensureInitialized(fallbackModel, gjson.Result{})
	return state.finish("stop")
}

func (s *gitLabOpenAIStreamState) ensureInitialized(fallbackModel string, root gjson.Result) {
	if s == nil {
		return
	}
	if s.ID == "" {
		s.ID = fmt.Sprintf("gitlab-%d", time.Now().UnixNano())
	}
	if s.Created == 0 {
		s.Created = time.Now().Unix()
	}
	if s.Model == "" {
		for _, key := range []string{"model.name", "model", "metadata.model_name"} {
			if value := strings.TrimSpace(root.Get(key).String()); value != "" {
				s.Model = value
				break
			}
		}
	}
	if s.Model == "" {
		s.Model = fallbackModel
	}
}

func (s *gitLabOpenAIStreamState) emitText(text string) [][]byte {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	delta := s.nextDelta(text)
	if delta == "" {
		return nil
	}
	out := make([][]byte, 0, 2)
	if !s.Started {
		out = append(out, s.buildChunk(map[string]any{"role": "assistant"}, ""))
		s.Started = true
	}
	out = append(out, s.buildChunk(map[string]any{"content": delta}, ""))
	return out
}

func (s *gitLabOpenAIStreamState) finish(reason string) [][]byte {
	if s == nil || s.Finished {
		return nil
	}
	if !s.Started {
		s.Started = true
	}
	s.Finished = true
	return [][]byte{
		s.buildChunk(map[string]any{}, reason),
		[]byte("data: [DONE]"),
	}
}

func (s *gitLabOpenAIStreamState) nextDelta(text string) string {
	if s == nil {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if s.LastFullText == "" {
		s.LastFullText = text
		return text
	}
	if text == s.LastFullText {
		return ""
	}
	if strings.HasPrefix(text, s.LastFullText) {
		delta := text[len(s.LastFullText):]
		s.LastFullText = text
		return delta
	}
	s.LastFullText += text
	return text
}

func (s *gitLabOpenAIStreamState) buildChunk(delta map[string]any, finishReason string) []byte {
	payload := map[string]any{
		"id":      s.ID,
		"object":  "chat.completion.chunk",
		"created": s.Created,
		"model":   s.Model,
		"choices": []map[string]any{{
			"index": 0,
			"delta": delta,
		}},
	}
	if finishReason != "" {
		payload["choices"] = []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}}
	}
	raw, _ := json.Marshal(payload)
	return append([]byte("data: "), raw...)
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

func buildGitLabAnthropicGatewayAuth(auth *cliproxyauth.Auth, requestedModel string) (*cliproxyauth.Auth, bool) {
	if !gitLabUsesAnthropicGateway(auth, requestedModel) {
		return nil, false
	}
	baseURL := gitLabAnthropicGatewayBaseURL(auth)
	token := gitLabMetadataString(auth.Metadata, "duo_gateway_token")
	if baseURL == "" || token == "" {
		return nil, false
	}

	nativeAuth := auth.Clone()
	nativeAuth.Provider = "claude"
	if nativeAuth.Attributes == nil {
		nativeAuth.Attributes = make(map[string]string)
	}
	nativeAuth.Attributes["api_key"] = token
	nativeAuth.Attributes["base_url"] = baseURL
	nativeAuth.Attributes["gitlab_duo_force_context_1m"] = "true"
	for key, value := range gitLabGatewayHeaders(auth, "anthropic") {
		if key == "" || value == "" {
			continue
		}
		nativeAuth.Attributes["header:"+key] = value
	}
	return nativeAuth, true
}

func buildGitLabOpenAIGatewayAuth(auth *cliproxyauth.Auth, requestedModel string) (*cliproxyauth.Auth, bool) {
	if !gitLabUsesOpenAIGateway(auth, requestedModel) {
		return nil, false
	}
	baseURL := gitLabOpenAIGatewayBaseURL(auth)
	token := gitLabMetadataString(auth.Metadata, "duo_gateway_token")
	if baseURL == "" || token == "" {
		return nil, false
	}

	nativeAuth := auth.Clone()
	nativeAuth.Provider = "codex"
	if nativeAuth.Attributes == nil {
		nativeAuth.Attributes = make(map[string]string)
	}
	nativeAuth.Attributes["api_key"] = token
	nativeAuth.Attributes["base_url"] = baseURL
	for key, value := range gitLabGatewayHeaders(auth, "openai") {
		if key == "" || value == "" {
			continue
		}
		nativeAuth.Attributes["header:"+key] = value
	}
	return nativeAuth, true
}

func gitLabUsesAnthropicGateway(auth *cliproxyauth.Auth, requestedModel string) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	provider := gitLabGatewayProvider(auth, requestedModel)
	return provider == "anthropic" &&
		gitLabMetadataString(auth.Metadata, "duo_gateway_base_url") != "" &&
		gitLabMetadataString(auth.Metadata, "duo_gateway_token") != ""
}

func gitLabUsesOpenAIGateway(auth *cliproxyauth.Auth, requestedModel string) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	provider := gitLabGatewayProvider(auth, requestedModel)
	return provider == "openai" &&
		gitLabMetadataString(auth.Metadata, "duo_gateway_base_url") != "" &&
		gitLabMetadataString(auth.Metadata, "duo_gateway_token") != ""
}

func gitLabGatewayProvider(auth *cliproxyauth.Auth, requestedModel string) string {
	modelName := strings.TrimSpace(gitLabResolvedModel(auth, requestedModel))
	if provider := inferGitLabProviderFromModel(modelName); provider != "" {
		return provider
	}
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	provider := strings.ToLower(gitLabMetadataString(auth.Metadata, "model_provider"))
	if provider == "" {
		provider = inferGitLabProviderFromModel(gitLabMetadataString(auth.Metadata, "model_name"))
	}
	return provider
}

func inferGitLabProviderFromModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "claude"):
		return "anthropic"
	case strings.Contains(model, "gpt"), strings.Contains(model, "o1"), strings.Contains(model, "o3"), strings.Contains(model, "o4"):
		return "openai"
	default:
		return ""
	}
}

func gitLabAnthropicGatewayBaseURL(auth *cliproxyauth.Auth) string {
	raw := strings.TrimSpace(gitLabMetadataString(auth.Metadata, "duo_gateway_base_url"))
	if raw == "" {
		return ""
	}
	base, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	path := strings.TrimRight(base.EscapedPath(), "/")
	switch {
	case strings.HasSuffix(path, "/ai/v1/proxy/anthropic"), strings.HasSuffix(path, "/v1/proxy/anthropic"):
		return strings.TrimRight(base.String(), "/")
	case path == "/ai":
		base.Path = "/ai/v1/proxy/anthropic"
	case path != "":
		base.Path = strings.TrimRight(path, "/") + "/v1/proxy/anthropic"
	case strings.Contains(strings.ToLower(base.Host), "gitlab.com"):
		base.Path = "/ai/v1/proxy/anthropic"
	default:
		base.Path = "/v1/proxy/anthropic"
	}
	return strings.TrimRight(base.String(), "/")
}

func gitLabOpenAIGatewayBaseURL(auth *cliproxyauth.Auth) string {
	raw := strings.TrimSpace(gitLabMetadataString(auth.Metadata, "duo_gateway_base_url"))
	if raw == "" {
		return ""
	}
	base, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	path := strings.TrimRight(base.EscapedPath(), "/")
	switch {
	case strings.HasSuffix(path, "/ai/v1/proxy/openai/v1"), strings.HasSuffix(path, "/v1/proxy/openai/v1"):
		return strings.TrimRight(base.String(), "/")
	case path == "/ai":
		base.Path = "/ai/v1/proxy/openai/v1"
	case path != "":
		base.Path = strings.TrimRight(path, "/") + "/v1/proxy/openai/v1"
	case strings.Contains(strings.ToLower(base.Host), "gitlab.com"):
		base.Path = "/ai/v1/proxy/openai/v1"
	default:
		base.Path = "/v1/proxy/openai/v1"
	}
	return strings.TrimRight(base.String(), "/")
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
		if mapped, ok := gitLabModelAliases[strings.ToLower(requested)]; ok && strings.TrimSpace(mapped) != "" {
			return mapped
		}
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
	models := make([]*registry.ModelInfo, 0, len(gitLabAgenticCatalog)+4)
	seen := make(map[string]struct{}, len(gitLabAgenticCatalog)+4)
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
	for _, model := range gitLabAgenticCatalog {
		addModel(model.ID, model.DisplayName, model.Provider)
	}
	for alias, upstream := range gitLabModelAliases {
		target := strings.TrimSpace(upstream)
		displayName := "GitLab Duo Alias"
		provider := strings.TrimSpace(inferGitLabProviderFromModel(target))
		if provider != "" {
			displayName = fmt.Sprintf("GitLab Duo Alias (%s)", provider)
		}
		addModel(alias, displayName, provider)
	}
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
