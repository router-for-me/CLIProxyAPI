package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
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
	githubCopilotBaseURL       = "https://api.githubcopilot.com"
	githubCopilotChatPath      = "/chat/completions"
	githubCopilotResponsesPath = "/responses"
	githubCopilotAuthType      = "github-copilot"
	githubCopilotTokenCacheTTL = 25 * time.Minute
	// tokenExpiryBuffer is the time before expiry when we should refresh the token.
	tokenExpiryBuffer = 5 * time.Minute
	// maxScannerBufferSize is the maximum buffer size for SSE scanning (20MB).
	maxScannerBufferSize = 20_971_520

	// Copilot API header values.
	copilotUserAgent     = "GitHubCopilotChat/0.35.0"
	copilotEditorVersion = "vscode/1.107.0"
	copilotPluginVersion = "copilot-chat/0.35.0"
	copilotIntegrationID = "vscode-chat"
	copilotOpenAIIntent  = "conversation-edits"
)

// GitHubCopilotExecutor handles requests to the GitHub Copilot API.
type GitHubCopilotExecutor struct {
	cfg   *config.Config
	mu    sync.RWMutex
	cache map[string]*cachedAPIToken
}

// cachedAPIToken stores a cached Copilot API token with its expiry.
type cachedAPIToken struct {
	token     string
	expiresAt time.Time
}

// NewGitHubCopilotExecutor constructs a new executor instance.
func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{
		cfg:   cfg,
		cache: make(map[string]*cachedAPIToken),
	}
}

// Identifier implements ProviderExecutor.
func (e *GitHubCopilotExecutor) Identifier() string { return githubCopilotAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *GitHubCopilotExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return errToken
	}
	e.applyHeaders(req, apiToken, nil)
	return nil
}

// HttpRequest injects GitHub Copilot credentials into the request and executes it.
func (e *GitHubCopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	useResponses := useGitHubCopilotResponsesEndpoint(from, req.Model)
	to := sdktranslator.FromString("openai")
	if useResponses {
		to = sdktranslator.FromString("openai-response")
	}
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = e.normalizeModel(req.Model, body)
	body = flattenAssistantContent(body)

	thinkingProvider := "openai"
	if useResponses {
		thinkingProvider = "codex"
	}
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), thinkingProvider, e.Identifier())
	if err != nil {
		return resp, err
	}

	if useResponses {
		body = normalizeGitHubCopilotResponsesInput(body)
		body = normalizeGitHubCopilotResponsesTools(body)
	} else {
		body = normalizeGitHubCopilotChatTools(body)
	}
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "stream", false)

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := githubCopilotBaseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, apiToken, body)

	// Add Copilot-Vision-Request header if the request contains vision content
	if detectVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
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
		Headers:   httpReq.Header.Clone(),
		Body:      body,
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
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	detail := parseOpenAIUsage(data)
	if useResponses && detail.TotalTokens == 0 {
		detail = parseOpenAIResponsesUsage(data)
	}
	if detail.TotalTokens > 0 {
		reporter.publish(ctx, detail)
	}

	var param any
	converted := ""
	if useResponses && from.String() == "claude" {
		converted = translateGitHubCopilotResponsesNonStreamToClaude(data)
	} else {
		converted = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	}
	resp = cliproxyexecutor.Response{Payload: []byte(converted)}
	reporter.ensurePublished(ctx)
	return resp, nil
}

// ExecuteStream handles streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	useResponses := useGitHubCopilotResponsesEndpoint(from, req.Model)
	to := sdktranslator.FromString("openai")
	if useResponses {
		to = sdktranslator.FromString("openai-response")
	}
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body = e.normalizeModel(req.Model, body)
	body = flattenAssistantContent(body)

	thinkingProvider := "openai"
	if useResponses {
		thinkingProvider = "codex"
	}
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), thinkingProvider, e.Identifier())
	if err != nil {
		return nil, err
	}

	if useResponses {
		body = normalizeGitHubCopilotResponsesInput(body)
		body = normalizeGitHubCopilotResponsesTools(body)
	} else {
		body = normalizeGitHubCopilotChatTools(body)
	}
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	// Enable stream options for usage stats in stream
	if !useResponses {
		body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	}

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := githubCopilotBaseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, apiToken, body)

	// Add Copilot-Vision-Request header if the request contains vision content
	if detectVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
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
		Headers:   httpReq.Header.Clone(),
		Body:      body,
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
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("github-copilot executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any

		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Parse SSE data
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if detail, ok := parseOpenAIStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				} else if useResponses {
					if detail, ok := parseOpenAIResponsesStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
				}
			}

			var chunks []string
			if useResponses && from.String() == "claude" {
				chunks = translateGitHubCopilotResponsesStreamToClaude(bytes.Clone(line), &param)
			} else {
				chunks = sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			}
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			reporter.ensurePublished(ctx)
		}
	}()

	return stream, nil
}

// CountTokens is not supported for GitHub Copilot.
func (e *GitHubCopilotExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for github-copilot"}
}

// Refresh validates the GitHub token is still working.
// GitHub OAuth tokens don't expire traditionally, so we just validate.
func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	// Validate the token can still get a Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("github-copilot token validation failed: %v", err)}
	}

	return auth, nil
}

// ensureAPIToken gets or refreshes the Copilot API token.
func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing github access token"}
	}

	// Check for cached API token using thread-safe access
	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(tokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, nil
	}
	e.mu.RUnlock()

	// Get a new Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("failed to get copilot api token: %v", err)}
	}

	// Cache the token with thread-safe access
	expiresAt := time.Now().Add(githubCopilotTokenCacheTTL)
	if apiToken.ExpiresAt > 0 {
		expiresAt = time.Unix(apiToken.ExpiresAt, 0)
	}
	e.mu.Lock()
	e.cache[accessToken] = &cachedAPIToken{
		token:     apiToken.Token,
		expiresAt: expiresAt,
	}
	e.mu.Unlock()

	return apiToken.Token, nil
}

// applyHeaders sets the required headers for GitHub Copilot API requests.
func (e *GitHubCopilotExecutor) applyHeaders(r *http.Request, apiToken string, body []byte) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	r.Header.Set("X-Request-Id", uuid.NewString())

	initiator := "user"
	if len(body) > 0 {
		if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
			arr := messages.Array()
			if len(arr) > 0 {
				lastRole := arr[len(arr)-1].Get("role").String()
				if lastRole != "" && lastRole != "user" {
					initiator = "agent"
				}
			}
		}
	}
	r.Header.Set("X-Initiator", initiator)
}

// detectVisionContent checks if the request body contains vision/image content.
// Returns true if the request includes image_url or image type content blocks.
func detectVisionContent(body []byte) bool {
	// Parse messages array
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return false
	}

	// Check each message for vision content
	for _, message := range messagesResult.Array() {
		content := message.Get("content")

		// If content is an array, check each content block
		if content.IsArray() {
			for _, block := range content.Array() {
				blockType := block.Get("type").String()
				// Check for image_url or image type
				if blockType == "image_url" || blockType == "image" {
					return true
				}
			}
		}
	}

	return false
}

// normalizeModel strips the suffix (e.g. "(medium)") from the model name
// before sending to GitHub Copilot, as the upstream API does not accept
// suffixed model identifiers.
func (e *GitHubCopilotExecutor) normalizeModel(model string, body []byte) []byte {
	baseModel := thinking.ParseSuffix(model).ModelName
	if baseModel != model {
		body, _ = sjson.SetBytes(body, "model", baseModel)
	}
	return body
}

func useGitHubCopilotResponsesEndpoint(sourceFormat sdktranslator.Format, model string) bool {
	if sourceFormat.String() == "openai-response" {
		return true
	}
	baseModel := strings.ToLower(thinking.ParseSuffix(model).ModelName)
	return strings.Contains(baseModel, "codex")
}

// flattenAssistantContent converts assistant message content from array format
// to a joined string. GitHub Copilot requires assistant content as a string;
// sending it as an array causes Claude models to re-answer all previous prompts.
func flattenAssistantContent(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	result := body
	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		var textParts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				if t := part.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			}
		}
		joined := strings.Join(textParts, "")
		path := fmt.Sprintf("messages.%d.content", i)
		result, _ = sjson.SetBytes(result, path, joined)
	}
	return result
}

func normalizeGitHubCopilotChatTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() {
		filtered := "[]"
		if tools.IsArray() {
			for _, tool := range tools.Array() {
				if tool.Get("type").String() != "function" {
					continue
				}
				filtered, _ = sjson.SetRaw(filtered, "-1", tool.Raw)
			}
		}
		body, _ = sjson.SetRawBytes(body, "tools", []byte(filtered))
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "auto", "none", "required":
			return body
		}
	}
	body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	return body
}

func normalizeGitHubCopilotResponsesInput(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if input.Exists() {
		if input.Type == gjson.String {
			return body
		}
		inputString := input.Raw
		if input.Type != gjson.JSON {
			inputString = input.String()
		}
		body, _ = sjson.SetBytes(body, "input", inputString)
		return body
	}

	var parts []string
	if system := gjson.GetBytes(body, "system"); system.Exists() {
		if text := strings.TrimSpace(collectTextFromNode(system)); text != "" {
			parts = append(parts, text)
		}
	}
	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			if text := strings.TrimSpace(collectTextFromNode(msg.Get("content"))); text != "" {
				parts = append(parts, text)
			}
		}
	}
	body, _ = sjson.SetBytes(body, "input", strings.Join(parts, "\n"))
	return body
}

func normalizeGitHubCopilotResponsesTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() {
		filtered := "[]"
		if tools.IsArray() {
			for _, tool := range tools.Array() {
				toolType := tool.Get("type").String()
				// Accept OpenAI format (type="function") and Claude format
				// (no type field, but has top-level name + input_schema).
				if toolType != "" && toolType != "function" {
					continue
				}
				name := tool.Get("name").String()
				if name == "" {
					name = tool.Get("function.name").String()
				}
				if name == "" {
					continue
				}
				normalized := `{"type":"function","name":""}`
				normalized, _ = sjson.Set(normalized, "name", name)
				if desc := tool.Get("description").String(); desc != "" {
					normalized, _ = sjson.Set(normalized, "description", desc)
				} else if desc = tool.Get("function.description").String(); desc != "" {
					normalized, _ = sjson.Set(normalized, "description", desc)
				}
				if params := tool.Get("parameters"); params.Exists() {
					normalized, _ = sjson.SetRaw(normalized, "parameters", params.Raw)
				} else if params = tool.Get("function.parameters"); params.Exists() {
					normalized, _ = sjson.SetRaw(normalized, "parameters", params.Raw)
				} else if params = tool.Get("input_schema"); params.Exists() {
					normalized, _ = sjson.SetRaw(normalized, "parameters", params.Raw)
				}
				filtered, _ = sjson.SetRaw(filtered, "-1", normalized)
			}
		}
		body, _ = sjson.SetRawBytes(body, "tools", []byte(filtered))
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "auto", "none", "required":
			return body
		default:
			body, _ = sjson.SetBytes(body, "tool_choice", "auto")
			return body
		}
	}
	if toolChoice.Type == gjson.JSON {
		choiceType := toolChoice.Get("type").String()
		if choiceType == "function" {
			name := toolChoice.Get("name").String()
			if name == "" {
				name = toolChoice.Get("function.name").String()
			}
			if name != "" {
				normalized := `{"type":"function","name":""}`
				normalized, _ = sjson.Set(normalized, "name", name)
				body, _ = sjson.SetRawBytes(body, "tool_choice", []byte(normalized))
				return body
			}
		}
	}
	body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	return body
}

func collectTextFromNode(node gjson.Result) string {
	if !node.Exists() {
		return ""
	}
	if node.Type == gjson.String {
		return node.String()
	}
	if node.IsArray() {
		var parts []string
		for _, item := range node.Array() {
			if item.Type == gjson.String {
				if text := item.String(); text != "" {
					parts = append(parts, text)
				}
				continue
			}
			if text := item.Get("text").String(); text != "" {
				parts = append(parts, text)
				continue
			}
			if nested := collectTextFromNode(item.Get("content")); nested != "" {
				parts = append(parts, nested)
			}
		}
		return strings.Join(parts, "\n")
	}
	if node.Type == gjson.JSON {
		if text := node.Get("text").String(); text != "" {
			return text
		}
		if nested := collectTextFromNode(node.Get("content")); nested != "" {
			return nested
		}
		return node.Raw
	}
	return node.String()
}

type githubCopilotResponsesStreamToolState struct {
	Index int
	ID    string
	Name  string
}

type githubCopilotResponsesStreamState struct {
	MessageStarted    bool
	MessageStopSent   bool
	TextBlockStarted  bool
	TextBlockIndex    int
	NextContentIndex  int
	HasToolUse        bool
	OutputIndexToTool map[int]*githubCopilotResponsesStreamToolState
	ItemIDToTool      map[string]*githubCopilotResponsesStreamToolState
}

func translateGitHubCopilotResponsesNonStreamToClaude(data []byte) string {
	root := gjson.ParseBytes(data)
	out := `{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`
	out, _ = sjson.Set(out, "id", root.Get("id").String())
	out, _ = sjson.Set(out, "model", root.Get("model").String())

	hasToolUse := false
	if output := root.Get("output"); output.Exists() && output.IsArray() {
		for _, item := range output.Array() {
			switch item.Get("type").String() {
			case "message":
				if content := item.Get("content"); content.Exists() && content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() != "output_text" {
							continue
						}
						text := part.Get("text").String()
						if text == "" {
							continue
						}
						block := `{"type":"text","text":""}`
						block, _ = sjson.Set(block, "text", text)
						out, _ = sjson.SetRaw(out, "content.-1", block)
					}
				}
			case "function_call":
				hasToolUse = true
				toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
				toolID := item.Get("call_id").String()
				if toolID == "" {
					toolID = item.Get("id").String()
				}
				toolUse, _ = sjson.Set(toolUse, "id", toolID)
				toolUse, _ = sjson.Set(toolUse, "name", item.Get("name").String())
				if args := item.Get("arguments").String(); args != "" && gjson.Valid(args) {
					argObj := gjson.Parse(args)
					if argObj.IsObject() {
						toolUse, _ = sjson.SetRaw(toolUse, "input", argObj.Raw)
					}
				}
				out, _ = sjson.SetRaw(out, "content.-1", toolUse)
			}
		}
	}

	inputTokens := root.Get("usage.input_tokens").Int()
	outputTokens := root.Get("usage.output_tokens").Int()
	out, _ = sjson.Set(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.Set(out, "usage.output_tokens", outputTokens)
	if hasToolUse {
		out, _ = sjson.Set(out, "stop_reason", "tool_use")
	} else {
		out, _ = sjson.Set(out, "stop_reason", "end_turn")
	}
	return out
}

func translateGitHubCopilotResponsesStreamToClaude(line []byte, param *any) []string {
	if *param == nil {
		*param = &githubCopilotResponsesStreamState{
			TextBlockIndex:    -1,
			OutputIndexToTool: make(map[int]*githubCopilotResponsesStreamToolState),
			ItemIDToTool:      make(map[string]*githubCopilotResponsesStreamToolState),
		}
	}
	state := (*param).(*githubCopilotResponsesStreamState)

	if !bytes.HasPrefix(line, dataTag) {
		return nil
	}
	payload := bytes.TrimSpace(line[5:])
	if bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	if !gjson.ValidBytes(payload) {
		return nil
	}

	event := gjson.GetBytes(payload, "type").String()
	results := make([]string, 0, 4)
	ensureMessageStart := func() {
		if state.MessageStarted {
			return
		}
		messageStart := `{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`
		messageStart, _ = sjson.Set(messageStart, "message.id", gjson.GetBytes(payload, "response.id").String())
		messageStart, _ = sjson.Set(messageStart, "message.model", gjson.GetBytes(payload, "response.model").String())
		results = append(results, "event: message_start\ndata: "+messageStart+"\n\n")
		state.MessageStarted = true
	}
	startTextBlockIfNeeded := func() {
		if state.TextBlockStarted {
			return
		}
		if state.TextBlockIndex < 0 {
			state.TextBlockIndex = state.NextContentIndex
			state.NextContentIndex++
		}
		contentBlockStart := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
		contentBlockStart, _ = sjson.Set(contentBlockStart, "index", state.TextBlockIndex)
		results = append(results, "event: content_block_start\ndata: "+contentBlockStart+"\n\n")
		state.TextBlockStarted = true
	}
	stopTextBlockIfNeeded := func() {
		if !state.TextBlockStarted {
			return
		}
		contentBlockStop := `{"type":"content_block_stop","index":0}`
		contentBlockStop, _ = sjson.Set(contentBlockStop, "index", state.TextBlockIndex)
		results = append(results, "event: content_block_stop\ndata: "+contentBlockStop+"\n\n")
		state.TextBlockStarted = false
		state.TextBlockIndex = -1
	}
	resolveTool := func(itemID string, outputIndex int) *githubCopilotResponsesStreamToolState {
		if itemID != "" {
			if tool, ok := state.ItemIDToTool[itemID]; ok {
				return tool
			}
		}
		if tool, ok := state.OutputIndexToTool[outputIndex]; ok {
			if itemID != "" {
				state.ItemIDToTool[itemID] = tool
			}
			return tool
		}
		return nil
	}

	switch event {
	case "response.created":
		ensureMessageStart()
	case "response.output_text.delta":
		ensureMessageStart()
		startTextBlockIfNeeded()
		delta := gjson.GetBytes(payload, "delta").String()
		if delta != "" {
			contentDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`
			contentDelta, _ = sjson.Set(contentDelta, "index", state.TextBlockIndex)
			contentDelta, _ = sjson.Set(contentDelta, "delta.text", delta)
			results = append(results, "event: content_block_delta\ndata: "+contentDelta+"\n\n")
		}
	case "response.output_item.added":
		if gjson.GetBytes(payload, "item.type").String() != "function_call" {
			break
		}
		ensureMessageStart()
		stopTextBlockIfNeeded()
		state.HasToolUse = true
		tool := &githubCopilotResponsesStreamToolState{
			Index: state.NextContentIndex,
			ID:    gjson.GetBytes(payload, "item.call_id").String(),
			Name:  gjson.GetBytes(payload, "item.name").String(),
		}
		if tool.ID == "" {
			tool.ID = gjson.GetBytes(payload, "item.id").String()
		}
		state.NextContentIndex++
		outputIndex := int(gjson.GetBytes(payload, "output_index").Int())
		state.OutputIndexToTool[outputIndex] = tool
		if itemID := gjson.GetBytes(payload, "item.id").String(); itemID != "" {
			state.ItemIDToTool[itemID] = tool
		}
		contentBlockStart := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`
		contentBlockStart, _ = sjson.Set(contentBlockStart, "index", tool.Index)
		contentBlockStart, _ = sjson.Set(contentBlockStart, "content_block.id", tool.ID)
		contentBlockStart, _ = sjson.Set(contentBlockStart, "content_block.name", tool.Name)
		results = append(results, "event: content_block_start\ndata: "+contentBlockStart+"\n\n")
	case "response.output_item.delta":
		item := gjson.GetBytes(payload, "item")
		if item.Get("type").String() != "function_call" {
			break
		}
		tool := resolveTool(item.Get("id").String(), int(gjson.GetBytes(payload, "output_index").Int()))
		if tool == nil {
			break
		}
		partial := gjson.GetBytes(payload, "delta").String()
		if partial == "" {
			partial = item.Get("arguments").String()
		}
		if partial == "" {
			break
		}
		inputDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
		inputDelta, _ = sjson.Set(inputDelta, "index", tool.Index)
		inputDelta, _ = sjson.Set(inputDelta, "delta.partial_json", partial)
		results = append(results, "event: content_block_delta\ndata: "+inputDelta+"\n\n")
	case "response.output_item.done":
		if gjson.GetBytes(payload, "item.type").String() != "function_call" {
			break
		}
		tool := resolveTool(gjson.GetBytes(payload, "item.id").String(), int(gjson.GetBytes(payload, "output_index").Int()))
		if tool == nil {
			break
		}
		contentBlockStop := `{"type":"content_block_stop","index":0}`
		contentBlockStop, _ = sjson.Set(contentBlockStop, "index", tool.Index)
		results = append(results, "event: content_block_stop\ndata: "+contentBlockStop+"\n\n")
	case "response.completed":
		ensureMessageStart()
		stopTextBlockIfNeeded()
		if !state.MessageStopSent {
			stopReason := "end_turn"
			if state.HasToolUse {
				stopReason = "tool_use"
			}
			messageDelta := `{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
			messageDelta, _ = sjson.Set(messageDelta, "delta.stop_reason", stopReason)
			messageDelta, _ = sjson.Set(messageDelta, "usage.input_tokens", gjson.GetBytes(payload, "response.usage.input_tokens").Int())
			messageDelta, _ = sjson.Set(messageDelta, "usage.output_tokens", gjson.GetBytes(payload, "response.usage.output_tokens").Int())
			results = append(results, "event: message_delta\ndata: "+messageDelta+"\n\n")
			results = append(results, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			state.MessageStopSent = true
		}
	}

	return results
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
