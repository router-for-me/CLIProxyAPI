package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	copilotauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	githubCopilotBaseURL       = "https://api.githubcopilot.com"
	githubCopilotChatPath      = "/chat/completions"
	githubCopilotResponsesPath = "/responses"
	githubCopilotAuthType      = "github-copilot"
	githubCopilotTokenCacheTTL = 25 * time.Minute
	tokenExpiryBuffer          = 5 * time.Minute
	maxScannerBufferSize       = 20_971_520

	copilotUserAgent     = "GitHubCopilotChat/0.35.0"
	copilotEditorVersion = "vscode/1.107.0"
	copilotPluginVersion = "copilot-chat/0.35.0"
	copilotIntegrationID = "vscode-chat"
	copilotOpenAIIntent  = "conversation-edits"
	copilotGitHubAPIVer  = "2025-04-01"
)

// GitHubCopilotExecutor handles requests to the GitHub Copilot API.
type GitHubCopilotExecutor struct {
	cfg   *config.Config
	mu    sync.RWMutex
	cache map[string]*cachedAPIToken
}

type cachedAPIToken struct {
	token       string
	apiEndpoint string
	expiresAt   time.Time
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
	apiToken, _, errToken := e.ensureAPIToken(ctx, auth)
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
	if nativeExec, nativeAuth, nativeReq, ok, errGateway := e.nativeGateway(ctx, auth, req); errGateway != nil {
		return resp, errGateway
	} else if ok {
		return nativeExec.Execute(ctx, nativeAuth, nativeReq, opts)
	}

	apiToken, baseURL, errToken := e.ensureAPIToken(ctx, auth)
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
	body = stripUnsupportedBetas(body)

	// Detect vision content before input normalization removes messages
	hasVision := detectVisionContent(body)

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
		body = applyGitHubCopilotResponsesDefaults(body)
	} else {
		body = normalizeGitHubCopilotChatTools(body)
	}
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = ojson.SetBytes(body, "stream", false)

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, apiToken, body)

	if hasVision {
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
	var converted []byte
	if useResponses && from.String() == "claude" {
		converted = translateGitHubCopilotResponsesNonStreamToClaude(data)
	} else {
		data = normalizeGitHubCopilotReasoningField(data)
		converted = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	}
	resp = cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}
	reporter.ensurePublished(ctx)
	return resp, nil
}

// ExecuteStream handles streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if nativeExec, nativeAuth, nativeReq, ok, errGateway := e.nativeGateway(ctx, auth, req); errGateway != nil {
		return nil, errGateway
	} else if ok {
		return nativeExec.ExecuteStream(ctx, nativeAuth, nativeReq, opts)
	}

	apiToken, baseURL, errToken := e.ensureAPIToken(ctx, auth)
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
	body = stripUnsupportedBetas(body)

	// Detect vision content before input normalization removes messages
	hasVision := detectVisionContent(body)

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
		body = applyGitHubCopilotResponsesDefaults(body)
	} else {
		body = normalizeGitHubCopilotChatTools(body)
	}
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = ojson.SetBytes(body, "stream", true)
	if !useResponses {
		body, _ = ojson.SetBytes(body, "stream_options.include_usage", true)
	}

	path := githubCopilotChatPath
	if useResponses {
		path = githubCopilotResponsesPath
	}
	url := baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, apiToken, body)

	if hasVision {
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
			terminalResponse := false

			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if useResponses {
					if ojson.ValidBytes(data) {
						switch ojson.GetBytes(data, "type").String() {
						case "response.completed", "response.incomplete":
							terminalResponse = true
							if detail, ok := helps.ParseCodexUsage(data); ok {
								reporter.publish(ctx, detail)
							}
							reporter.ensurePublished(ctx)
						case "response.failed", "error":
							terminalResponse = true
							reporter.publishFailure(ctx)
						}
					}
				} else if detail, ok := parseOpenAIStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				}
			}

			var chunks [][]byte
			if useResponses && from.String() == "claude" {
				chunks = translateGitHubCopilotResponsesStreamToClaude(bytes.Clone(line), &param)
			} else {
				// Strip SSE "data: " prefix before reasoning field normalization,
				// since normalizeGitHubCopilotReasoningField expects pure JSON.
				// Re-wrap with the prefix afterward for the translator.
				normalizedLine := bytes.Clone(line)
				if bytes.HasPrefix(line, dataTag) {
					sseData := bytes.TrimSpace(line[len(dataTag):])
					if !bytes.Equal(sseData, []byte("[DONE]")) && ojson.ValidBytes(sseData) {
						normalized := normalizeGitHubCopilotReasoningField(bytes.Clone(sseData))
						if !bytes.Equal(normalized, sseData) {
							normalizedLine = append(append([]byte(nil), dataTag...), normalized...)
						}
					}
				}
				if useResponses && from == to {
					// Scanner removes delimiters; passthrough streams must retain SSE framing.
					normalizedLine = append(normalizedLine, '\n')
					if terminalResponse {
						// Finish the terminal SSE frame without another upstream read.
						normalizedLine = append(normalizedLine, '\n')
					}
				}
				chunks = sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, normalizedLine, &param)
			}
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunks[i])}
			}
			if terminalResponse {
				return
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

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// CountTokens estimates token count locally using tiktoken, since the GitHub
// Copilot API does not expose a dedicated token counting endpoint.
func (e *GitHubCopilotExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if nativeExec, nativeAuth, nativeReq, ok, errGateway := e.nativeGateway(ctx, auth, req); errGateway != nil {
		return cliproxyexecutor.Response{}, errGateway
	} else if ok {
		return nativeExec.CountTokens(ctx, nativeAuth, nativeReq, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("github copilot executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("github copilot executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh validates the GitHub token is still working.
// GitHub OAuth tokens don't expire traditionally, so we just validate.
func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("github-copilot token validation failed: %v", err)}
	}

	return auth, nil
}

func (e *GitHubCopilotExecutor) nativeGateway(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
) (cliproxyauth.ProviderExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, bool, error) {
	if !githubCopilotUsesAnthropicGateway(req.Model) {
		return nil, nil, req, false, nil
	}
	if auth == nil || metaStringValue(auth.Metadata, "access_token") == "" {
		return nil, nil, req, false, nil
	}
	apiToken, baseURL, err := e.ensureAPIToken(ctx, auth)
	if err != nil {
		return nil, nil, req, false, err
	}
	nativeAuth := buildCopilotAnthropicGatewayAuth(auth, apiToken, baseURL, req.Payload)
	if nativeAuth == nil {
		return nil, nil, req, false, nil
	}
	return NewClaudeExecutor(e.cfg), nativeAuth, req, true, nil
}

func githubCopilotUsesAnthropicGateway(model string) bool {
	baseModel := strings.ToLower(thinking.ParseSuffix(model).ModelName)
	return strings.HasPrefix(baseModel, "claude-")
}

func buildCopilotAnthropicGatewayAuth(auth *cliproxyauth.Auth, apiToken, baseURL string, body []byte) *cliproxyauth.Auth {
	apiToken = strings.TrimSpace(apiToken)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if apiToken == "" || baseURL == "" {
		return nil
	}

	nativeAuth := auth.Clone()
	if nativeAuth == nil {
		nativeAuth = &cliproxyauth.Auth{}
	}
	nativeAuth.Provider = "claude"
	if nativeAuth.Attributes == nil {
		nativeAuth.Attributes = make(map[string]string)
	}
	nativeAuth.Attributes["api_key"] = apiToken
	nativeAuth.Attributes["base_url"] = baseURL
	nativeAuth.Attributes["header:Content-Type"] = "application/json"
	nativeAuth.Attributes["header:Accept"] = "application/json"
	nativeAuth.Attributes["header:User-Agent"] = copilotUserAgent
	nativeAuth.Attributes["header:Editor-Version"] = copilotEditorVersion
	nativeAuth.Attributes["header:Editor-Plugin-Version"] = copilotPluginVersion
	nativeAuth.Attributes["header:Openai-Intent"] = copilotOpenAIIntent
	nativeAuth.Attributes["header:Copilot-Integration-Id"] = copilotIntegrationID
	nativeAuth.Attributes["header:X-Github-Api-Version"] = copilotGitHubAPIVer
	nativeAuth.Attributes["header:X-Request-Id"] = uuid.NewString()
	if isAgentInitiated(body) {
		nativeAuth.Attributes["header:X-Initiator"] = "agent"
	} else {
		nativeAuth.Attributes["header:X-Initiator"] = "user"
	}
	if detectVisionContent(body) {
		nativeAuth.Attributes["header:Copilot-Vision-Request"] = "true"
	}
	return nativeAuth
}

func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *cliproxyauth.Auth) (string, string, error) {
	if auth == nil {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "missing github access token"}
	}

	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(tokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, cached.apiEndpoint, nil
	}
	e.mu.RUnlock()

	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("failed to get copilot api token: %v", err)}
	}

	apiEndpoint := githubCopilotBaseURL
	if apiToken.Endpoints.API != "" {
		apiEndpoint = strings.TrimRight(apiToken.Endpoints.API, "/")
	}

	expiresAt := time.Now().Add(githubCopilotTokenCacheTTL)
	if apiToken.ExpiresAt > 0 {
		expiresAt = time.Unix(apiToken.ExpiresAt, 0)
	}
	e.mu.Lock()
	e.cache[accessToken] = &cachedAPIToken{
		token:       apiToken.Token,
		apiEndpoint: apiEndpoint,
		expiresAt:   expiresAt,
	}
	e.mu.Unlock()

	return apiToken.Token, apiEndpoint, nil
}

func (e *GitHubCopilotExecutor) applyHeaders(r *http.Request, apiToken string, body []byte) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	r.Header.Set("X-Github-Api-Version", copilotGitHubAPIVer)
	r.Header.Set("X-Request-Id", uuid.NewString())

	initiator := "user"
	if isAgentInitiated(body) {
		initiator = "agent"
	}
	r.Header.Set("X-Initiator", initiator)
}

// isAgentInitiated infers tool-loop continuations for Copilot's X-Initiator
// billing header. Claude tool results can have role "user", so role alone
// does not distinguish a continuation from a new user prompt.
func isAgentInitiated(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	if messages := ojson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		arr := messages.Array()
		if len(arr) == 0 {
			return false
		}

		lastRole := ""
		for i := len(arr) - 1; i >= 0; i-- {
			if r := arr[i].Get("role").String(); r != "" {
				lastRole = r
				break
			}
		}

		if lastRole == "assistant" || lastRole == "tool" {
			return true
		}

		// Older assistant messages alone would misclassify genuine user follow-ups.
		if lastRole == "user" {
			lastContent := arr[len(arr)-1].Get("content")
			if lastContent.Exists() && lastContent.IsArray() {
				for _, part := range lastContent.Array() {
					if part.Get("type").String() == "tool_result" {
						return true
					}
				}
			}
			if len(arr) >= 2 {
				prev := arr[len(arr)-2]
				if prev.Get("role").String() == "assistant" {
					prevContent := prev.Get("content")
					if prevContent.Exists() && prevContent.IsArray() {
						for _, part := range prevContent.Array() {
							if part.Get("type").String() == "tool_use" {
								return true
							}
						}
					}
				}
			}
		}

		return false
	}

	if inputs := ojson.GetBytes(body, "input"); inputs.Exists() && inputs.IsArray() {
		arr := inputs.Array()
		if len(arr) == 0 {
			return false
		}

		last := arr[len(arr)-1]
		if role := last.Get("role").String(); role == "assistant" {
			return true
		}
		switch last.Get("type").String() {
		case "function_call", "function_call_arguments", "computer_call":
			return true
		case "function_call_output", "function_call_response", "tool_result", "computer_call_output":
			return true
		}

		for _, item := range arr {
			if role := item.Get("role").String(); role == "assistant" {
				return true
			}
			switch item.Get("type").String() {
			case "function_call", "function_call_output", "function_call_response",
				"function_call_arguments", "computer_call", "computer_call_output":
				return true
			}
		}
	}

	return false
}

func detectVisionContent(body []byte) bool {
	messagesResult := ojson.GetBytes(body, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return false
	}

	for _, message := range messagesResult.Array() {
		content := message.Get("content")

		if content.IsArray() {
			for _, block := range content.Array() {
				blockType := block.Get("type").String()
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
		body, _ = ojson.SetBytes(body, "model", baseModel)
	}
	return body
}

// copilotUnsupportedBetas lists beta headers that are Anthropic-specific and
// must not be forwarded to GitHub Copilot. The context-1m beta enables 1M
// context on Anthropic's API, but Copilot's Claude models are limited to
// ~128K-200K. Passing it through would not enable 1M on Copilot, but stripping
// it from the translated body avoids confusing downstream translators.
var copilotUnsupportedBetas = []string{
	"context-1m-2025-08-07",
}

// stripUnsupportedBetas removes Anthropic-specific beta entries from the
// translated request body. In OpenAI format the betas may appear under
// "metadata.betas" or a top-level "betas" array; in Claude format they sit at
// "betas". This function checks all known locations.
func stripUnsupportedBetas(body []byte) []byte {
	betaPaths := []string{"betas", "metadata.betas"}
	for _, path := range betaPaths {
		arr := ojson.GetBytes(body, path)
		if !arr.Exists() || !arr.IsArray() {
			continue
		}
		var filtered []string
		changed := false
		for _, item := range arr.Array() {
			beta := item.String()
			if isCopilotUnsupportedBeta(beta) {
				changed = true
				continue
			}
			filtered = append(filtered, beta)
		}
		if !changed {
			continue
		}
		if len(filtered) == 0 {
			body, _ = ojson.DeleteBytes(body, path)
		} else {
			body, _ = ojson.SetBytes(body, path, filtered)
		}
	}
	return body
}

func isCopilotUnsupportedBeta(beta string) bool {
	return slices.Contains(copilotUnsupportedBetas, beta)
}

// normalizeGitHubCopilotReasoningField maps Copilot's non-standard
// 'reasoning_text' field to the standard OpenAI 'reasoning_content' field
// that the SDK translator expects. This handles both streaming deltas
// (choices[].delta.reasoning_text) and non-streaming messages
// (choices[].message.reasoning_text). The field is only renamed when
// 'reasoning_content' is absent or null, preserving standard responses.
// All choices are processed to support n>1 requests.
func normalizeGitHubCopilotReasoningField(data []byte) []byte {
	choices := ojson.GetBytes(data, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return data
	}
	for i := range choices.Array() {
		msgRT := fmt.Sprintf("choices.%d.message.reasoning_text", i)
		msgRC := fmt.Sprintf("choices.%d.message.reasoning_content", i)
		if rt := ojson.GetBytes(data, msgRT); rt.Exists() && rt.String() != "" {
			if rc := ojson.GetBytes(data, msgRC); !rc.Exists() || rc.Type == ojson.Null || rc.String() == "" {
				data, _ = ojson.SetBytes(data, msgRC, rt.String())
			}
		}
		deltaRT := fmt.Sprintf("choices.%d.delta.reasoning_text", i)
		deltaRC := fmt.Sprintf("choices.%d.delta.reasoning_content", i)
		if rt := ojson.GetBytes(data, deltaRT); rt.Exists() && rt.String() != "" {
			if rc := ojson.GetBytes(data, deltaRC); !rc.Exists() || rc.Type == ojson.Null || rc.String() == "" {
				data, _ = ojson.SetBytes(data, deltaRC, rt.String())
			}
		}
	}
	return data
}

func useGitHubCopilotResponsesEndpoint(sourceFormat sdktranslator.Format, model string) bool {
	if sourceFormat.String() == "openai-response" {
		return true
	}
	baseModel := strings.ToLower(thinking.ParseSuffix(model).ModelName)
	if info := registry.GetGlobalRegistry().GetModelInfo(baseModel, githubCopilotAuthType); info != nil {
		return len(info.SupportedEndpoints) > 0 && !containsEndpoint(info.SupportedEndpoints, githubCopilotChatPath) && containsEndpoint(info.SupportedEndpoints, githubCopilotResponsesPath)
	}
	if info := lookupGitHubCopilotStaticModelInfo(baseModel); info != nil {
		return len(info.SupportedEndpoints) > 0 && !containsEndpoint(info.SupportedEndpoints, githubCopilotChatPath) && containsEndpoint(info.SupportedEndpoints, githubCopilotResponsesPath)
	}
	return strings.Contains(baseModel, "codex")
}

func lookupGitHubCopilotStaticModelInfo(model string) *registry.ModelInfo {
	for _, info := range registry.GetStaticModelDefinitionsByChannel(githubCopilotAuthType) {
		if info != nil && strings.EqualFold(info.ID, model) {
			return info
		}
	}
	return nil
}

func containsEndpoint(endpoints []string, endpoint string) bool {
	return slices.Contains(endpoints, endpoint)
}

// flattenAssistantContent converts assistant message content from array format
// to a joined string. GitHub Copilot requires assistant content as a string;
// sending it as an array causes Claude models to re-answer all previous prompts.
func flattenAssistantContent(body []byte) []byte {
	messages := ojson.GetBytes(body, "messages")
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
		// Skip flattening if the content contains non-text blocks (tool_use, thinking, etc.)
		hasNonText := false
		for _, part := range content.Array() {
			if t := part.Get("type").String(); t != "" && t != "text" {
				hasNonText = true
				break
			}
		}
		if hasNonText {
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
		result, _ = ojson.SetBytes(result, path, joined)
	}
	return result
}

func normalizeGitHubCopilotChatTools(body []byte) []byte {
	tools := ojson.GetBytes(body, "tools")
	if tools.Exists() {
		filtered := "[]"
		if tools.IsArray() {
			for _, tool := range tools.Array() {
				if tool.Get("type").String() != "function" {
					continue
				}
				filtered, _ = ojson.SetRaw(filtered, "-1", tool.Raw)
			}
		}
		body, _ = ojson.SetRawBytes(body, "tools", []byte(filtered))
	}

	toolChoice := ojson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body
	}
	if toolChoice.Type == ojson.String {
		switch toolChoice.String() {
		case "auto", "none", "required":
			return body
		}
	}
	body, _ = ojson.SetBytes(body, "tool_choice", "auto")
	return body
}

func normalizeGitHubCopilotResponsesInput(body []byte) []byte {
	body = stripGitHubCopilotResponsesUnsupportedFields(body)
	input := ojson.GetBytes(body, "input")
	if input.Exists() {
		if input.Type == ojson.String || input.IsArray() {
			return body
		}
		body, _ = ojson.SetBytes(body, "input", input.Raw)
		return body
	}

	// Preserve roles and tool calls when converting Claude messages to Responses input.
	inputArr := "[]"

	if system := ojson.GetBytes(body, "system"); system.Exists() {
		var systemParts []string
		if system.IsArray() {
			for _, part := range system.Array() {
				if txt := part.Get("text").String(); txt != "" {
					systemParts = append(systemParts, txt)
				}
			}
		} else if system.Type == ojson.String {
			systemParts = append(systemParts, system.String())
		}
		if len(systemParts) > 0 {
			msg := `{"type":"message","role":"developer","content":[]}`
			for _, txt := range systemParts {
				part := `{"type":"input_text","text":""}`
				part, _ = ojson.Set(part, "text", txt)
				msg, _ = ojson.SetRaw(msg, "content.-1", part)
			}
			inputArr, _ = ojson.SetRaw(inputArr, "-1", msg)
		}
	}

	if messages := ojson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			role := msg.Get("role").String()
			content := msg.Get("content")

			if !content.Exists() {
				continue
			}

			if content.Type == ojson.String {
				textType := "input_text"
				if role == "assistant" {
					textType = "output_text"
				}
				item := `{"type":"message","role":"","content":[]}`
				item, _ = ojson.Set(item, "role", role)
				part := fmt.Sprintf(`{"type":"%s","text":""}`, textType)
				part, _ = ojson.Set(part, "text", content.String())
				item, _ = ojson.SetRaw(item, "content.-1", part)
				inputArr, _ = ojson.SetRaw(inputArr, "-1", item)
				continue
			}

			if !content.IsArray() {
				continue
			}

			// Array content: split into message parts vs tool items
			var msgParts []string
			for _, c := range content.Array() {
				cType := c.Get("type").String()
				switch cType {
				case "text":
					textType := "input_text"
					if role == "assistant" {
						textType = "output_text"
					}
					part := fmt.Sprintf(`{"type":"%s","text":""}`, textType)
					part, _ = ojson.Set(part, "text", c.Get("text").String())
					msgParts = append(msgParts, part)
				case "image":
					source := c.Get("source")
					if source.Exists() {
						data := source.Get("data").String()
						if data == "" {
							data = source.Get("base64").String()
						}
						mediaType := source.Get("media_type").String()
						if mediaType == "" {
							mediaType = source.Get("mime_type").String()
						}
						if mediaType == "" {
							mediaType = "application/octet-stream"
						}
						if data != "" {
							part := `{"type":"input_image","image_url":""}`
							part, _ = ojson.Set(part, "image_url", fmt.Sprintf("data:%s;base64,%s", mediaType, data))
							msgParts = append(msgParts, part)
						}
					}
				case "tool_use":
					if len(msgParts) > 0 {
						item := `{"type":"message","role":"","content":[]}`
						item, _ = ojson.Set(item, "role", role)
						for _, p := range msgParts {
							item, _ = ojson.SetRaw(item, "content.-1", p)
						}
						inputArr, _ = ojson.SetRaw(inputArr, "-1", item)
						msgParts = nil
					}
					fc := `{"type":"function_call","call_id":"","name":"","arguments":""}`
					fc, _ = ojson.Set(fc, "call_id", c.Get("id").String())
					fc, _ = ojson.Set(fc, "name", c.Get("name").String())
					if inputRaw := c.Get("input"); inputRaw.Exists() {
						fc, _ = ojson.Set(fc, "arguments", inputRaw.Raw)
					}
					inputArr, _ = ojson.SetRaw(inputArr, "-1", fc)
				case "tool_result":
					if len(msgParts) > 0 {
						item := `{"type":"message","role":"","content":[]}`
						item, _ = ojson.Set(item, "role", role)
						for _, p := range msgParts {
							item, _ = ojson.SetRaw(item, "content.-1", p)
						}
						inputArr, _ = ojson.SetRaw(inputArr, "-1", item)
						msgParts = nil
					}
					fco := `{"type":"function_call_output","call_id":"","output":""}`
					fco, _ = ojson.Set(fco, "call_id", c.Get("tool_use_id").String())
					resultContent := c.Get("content")
					if resultContent.Type == ojson.String {
						fco, _ = ojson.Set(fco, "output", resultContent.String())
					} else if resultContent.IsArray() {
						var resultParts []string
						for _, rc := range resultContent.Array() {
							if txt := rc.Get("text").String(); txt != "" {
								resultParts = append(resultParts, txt)
							}
						}
						fco, _ = ojson.Set(fco, "output", strings.Join(resultParts, "\n"))
					} else if resultContent.Exists() {
						fco, _ = ojson.Set(fco, "output", resultContent.String())
					}
					inputArr, _ = ojson.SetRaw(inputArr, "-1", fco)
				case "thinking":
					// Skip thinking blocks - not part of the API input
				}
			}

			if len(msgParts) > 0 {
				item := `{"type":"message","role":"","content":[]}`
				item, _ = ojson.Set(item, "role", role)
				for _, p := range msgParts {
					item, _ = ojson.SetRaw(item, "content.-1", p)
				}
				inputArr, _ = ojson.SetRaw(inputArr, "-1", item)
			}
		}
	}

	body, _ = ojson.SetRawBytes(body, "input", []byte(inputArr))
	body, _ = ojson.DeleteBytes(body, "messages")
	body, _ = ojson.DeleteBytes(body, "system")
	return body
}

func stripGitHubCopilotResponsesUnsupportedFields(body []byte) []byte {
	// GitHub Copilot /responses rejects service_tier, so always remove it.
	body, _ = ojson.DeleteBytes(body, "service_tier")
	return body
}

// applyGitHubCopilotResponsesDefaults sets required fields for the Responses API
// that both vscode-copilot-chat and pi-ai always include.
//
// References:
//   - vscode-copilot-chat: src/platform/endpoint/node/responsesApi.ts
//   - pi-ai (badlogic/pi-mono): packages/ai/src/providers/openai-responses.ts
func applyGitHubCopilotResponsesDefaults(body []byte) []byte {
	if !ojson.GetBytes(body, "store").Exists() {
		body, _ = ojson.SetBytes(body, "store", false)
	}

	// include: ["reasoning.encrypted_content"] — enables reasoning content
	// reuse across turns, avoiding redundant computation
	if !ojson.GetBytes(body, "include").Exists() {
		body, _ = ojson.SetRawBytes(body, "include", []byte(`["reasoning.encrypted_content"]`))
	}

	if ojson.GetBytes(body, "reasoning.effort").Exists() && !ojson.GetBytes(body, "reasoning.summary").Exists() {
		body, _ = ojson.SetBytes(body, "reasoning.summary", "auto")
	}

	return body
}

func normalizeGitHubCopilotResponsesTools(body []byte) []byte {
	tools := ojson.GetBytes(body, "tools")
	if tools.Exists() {
		filtered := "[]"
		if tools.IsArray() {
			for _, tool := range tools.Array() {
				toolType := tool.Get("type").String()
				if isGitHubCopilotResponsesBuiltinTool(toolType) {
					filtered, _ = ojson.SetRaw(filtered, "-1", tool.Raw)
					continue
				}
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
				normalized, _ = ojson.Set(normalized, "name", name)
				if desc := tool.Get("description").String(); desc != "" {
					normalized, _ = ojson.Set(normalized, "description", desc)
				} else if desc = tool.Get("function.description").String(); desc != "" {
					normalized, _ = ojson.Set(normalized, "description", desc)
				}
				if params := tool.Get("parameters"); params.Exists() {
					normalized, _ = ojson.SetRaw(normalized, "parameters", params.Raw)
				} else if params = tool.Get("function.parameters"); params.Exists() {
					normalized, _ = ojson.SetRaw(normalized, "parameters", params.Raw)
				} else if params = tool.Get("input_schema"); params.Exists() {
					normalized, _ = ojson.SetRaw(normalized, "parameters", params.Raw)
				}
				filtered, _ = ojson.SetRaw(filtered, "-1", normalized)
			}
		}
		body, _ = ojson.SetRawBytes(body, "tools", []byte(filtered))
	}

	toolChoice := ojson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body
	}
	if toolChoice.Type == ojson.String {
		switch toolChoice.String() {
		case "auto", "none", "required":
			return body
		default:
			body, _ = ojson.SetBytes(body, "tool_choice", "auto")
			return body
		}
	}
	if toolChoice.Type == ojson.JSON {
		choiceType := toolChoice.Get("type").String()
		if isGitHubCopilotResponsesBuiltinTool(choiceType) {
			body, _ = ojson.SetRawBytes(body, "tool_choice", []byte(toolChoice.Raw))
			return body
		}
		if choiceType == "function" {
			name := toolChoice.Get("name").String()
			if name == "" {
				name = toolChoice.Get("function.name").String()
			}
			if name != "" {
				normalized := `{"type":"function","name":""}`
				normalized, _ = ojson.Set(normalized, "name", name)
				body, _ = ojson.SetRawBytes(body, "tool_choice", []byte(normalized))
				return body
			}
		}
	}
	body, _ = ojson.SetBytes(body, "tool_choice", "auto")
	return body
}

func isGitHubCopilotResponsesBuiltinTool(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case "computer", "computer_use_preview", "namespace", "tool_search":
		return true
	default:
		return false
	}
}

func collectTextFromNode(node ojson.Result) string {
	if !node.Exists() {
		return ""
	}
	if node.Type == ojson.String {
		return node.String()
	}
	if node.IsArray() {
		var parts []string
		for _, item := range node.Array() {
			if item.Type == ojson.String {
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
	if node.Type == ojson.JSON {
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
	ReasoningActive   bool
	ReasoningIndex    int
	OutputIndexToTool map[int]*githubCopilotResponsesStreamToolState
	ItemIDToTool      map[string]*githubCopilotResponsesStreamToolState
}

func translateGitHubCopilotResponsesNonStreamToClaude(data []byte) []byte {
	root := ojson.ParseBytes(data)
	out := `{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`
	out, _ = ojson.Set(out, "id", root.Get("id").String())
	out, _ = ojson.Set(out, "model", root.Get("model").String())

	hasToolUse := false
	if output := root.Get("output"); output.Exists() && output.IsArray() {
		for _, item := range output.Array() {
			switch item.Get("type").String() {
			case "reasoning":
				var thinkingText string
				if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
					var parts []string
					for _, part := range summary.Array() {
						if txt := part.Get("text").String(); txt != "" {
							parts = append(parts, txt)
						}
					}
					thinkingText = strings.Join(parts, "")
				}
				if thinkingText == "" {
					if content := item.Get("content"); content.Exists() && content.IsArray() {
						var parts []string
						for _, part := range content.Array() {
							if txt := part.Get("text").String(); txt != "" {
								parts = append(parts, txt)
							}
						}
						thinkingText = strings.Join(parts, "")
					}
				}
				if thinkingText != "" {
					block := `{"type":"thinking","thinking":""}`
					block, _ = ojson.Set(block, "thinking", thinkingText)
					out, _ = ojson.SetRaw(out, "content.-1", block)
				}
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
						block, _ = ojson.Set(block, "text", text)
						out, _ = ojson.SetRaw(out, "content.-1", block)
					}
				}
			case "function_call":
				hasToolUse = true
				toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
				toolID := item.Get("call_id").String()
				if toolID == "" {
					toolID = item.Get("id").String()
				}
				toolUse, _ = ojson.Set(toolUse, "id", toolID)
				toolUse, _ = ojson.Set(toolUse, "name", item.Get("name").String())
				if args := item.Get("arguments").String(); args != "" && ojson.Valid(args) {
					argObj := ojson.Parse(args)
					if argObj.IsObject() {
						toolUse, _ = ojson.SetRaw(toolUse, "input", argObj.Raw)
					}
				}
				out, _ = ojson.SetRaw(out, "content.-1", toolUse)
			}
		}
	}

	inputTokens := root.Get("usage.input_tokens").Int()
	outputTokens := root.Get("usage.output_tokens").Int()
	cachedTokens := root.Get("usage.input_tokens_details.cached_tokens").Int()
	if cachedTokens > 0 && inputTokens >= cachedTokens {
		inputTokens -= cachedTokens
	}
	out, _ = ojson.Set(out, "usage.input_tokens", inputTokens)
	out, _ = ojson.Set(out, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		out, _ = ojson.Set(out, "usage.cache_read_input_tokens", cachedTokens)
	}
	if hasToolUse {
		out, _ = ojson.Set(out, "stop_reason", "tool_use")
	} else if sr := root.Get("stop_reason").String(); sr == "max_tokens" || sr == "stop" {
		out, _ = ojson.Set(out, "stop_reason", sr)
	} else {
		out, _ = ojson.Set(out, "stop_reason", "end_turn")
	}
	return []byte(out)
}

func translateGitHubCopilotResponsesStreamToClaude(line []byte, param *any) [][]byte {
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
	if !ojson.ValidBytes(payload) {
		return nil
	}

	event := ojson.GetBytes(payload, "type").String()
	results := make([][]byte, 0, 4)
	appendResult := func(chunk string) {
		results = append(results, []byte(chunk))
	}
	ensureMessageStart := func() {
		if state.MessageStarted {
			return
		}
		messageStart := `{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`
		messageStart, _ = ojson.Set(messageStart, "message.id", ojson.GetBytes(payload, "response.id").String())
		messageStart, _ = ojson.Set(messageStart, "message.model", ojson.GetBytes(payload, "response.model").String())
		appendResult("event: message_start\ndata: " + messageStart + "\n\n")
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
		contentBlockStart, _ = ojson.Set(contentBlockStart, "index", state.TextBlockIndex)
		appendResult("event: content_block_start\ndata: " + contentBlockStart + "\n\n")
		state.TextBlockStarted = true
	}
	stopTextBlockIfNeeded := func() {
		if !state.TextBlockStarted {
			return
		}
		contentBlockStop := `{"type":"content_block_stop","index":0}`
		contentBlockStop, _ = ojson.Set(contentBlockStop, "index", state.TextBlockIndex)
		appendResult("event: content_block_stop\ndata: " + contentBlockStop + "\n\n")
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
		delta := ojson.GetBytes(payload, "delta").String()
		if delta != "" {
			contentDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`
			contentDelta, _ = ojson.Set(contentDelta, "index", state.TextBlockIndex)
			contentDelta, _ = ojson.Set(contentDelta, "delta.text", delta)
			appendResult("event: content_block_delta\ndata: " + contentDelta + "\n\n")
		}
	case "response.reasoning_summary_part.added":
		ensureMessageStart()
		state.ReasoningActive = true
		state.ReasoningIndex = state.NextContentIndex
		state.NextContentIndex++
		thinkingStart := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`
		thinkingStart, _ = ojson.Set(thinkingStart, "index", state.ReasoningIndex)
		appendResult("event: content_block_start\ndata: " + thinkingStart + "\n\n")
	case "response.reasoning_summary_text.delta":
		if state.ReasoningActive {
			delta := ojson.GetBytes(payload, "delta").String()
			if delta != "" {
				thinkingDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`
				thinkingDelta, _ = ojson.Set(thinkingDelta, "index", state.ReasoningIndex)
				thinkingDelta, _ = ojson.Set(thinkingDelta, "delta.thinking", delta)
				appendResult("event: content_block_delta\ndata: " + thinkingDelta + "\n\n")
			}
		}
	case "response.reasoning_summary_part.done":
		if state.ReasoningActive {
			thinkingStop := `{"type":"content_block_stop","index":0}`
			thinkingStop, _ = ojson.Set(thinkingStop, "index", state.ReasoningIndex)
			appendResult("event: content_block_stop\ndata: " + thinkingStop + "\n\n")
			state.ReasoningActive = false
		}
	case "response.output_item.added":
		if ojson.GetBytes(payload, "item.type").String() != "function_call" {
			break
		}
		ensureMessageStart()
		stopTextBlockIfNeeded()
		state.HasToolUse = true
		tool := &githubCopilotResponsesStreamToolState{
			Index: state.NextContentIndex,
			ID:    ojson.GetBytes(payload, "item.call_id").String(),
			Name:  ojson.GetBytes(payload, "item.name").String(),
		}
		if tool.ID == "" {
			tool.ID = ojson.GetBytes(payload, "item.id").String()
		}
		state.NextContentIndex++
		outputIndex := int(ojson.GetBytes(payload, "output_index").Int())
		state.OutputIndexToTool[outputIndex] = tool
		if itemID := ojson.GetBytes(payload, "item.id").String(); itemID != "" {
			state.ItemIDToTool[itemID] = tool
		}
		contentBlockStart := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`
		contentBlockStart, _ = ojson.Set(contentBlockStart, "index", tool.Index)
		contentBlockStart, _ = ojson.Set(contentBlockStart, "content_block.id", tool.ID)
		contentBlockStart, _ = ojson.Set(contentBlockStart, "content_block.name", tool.Name)
		appendResult("event: content_block_start\ndata: " + contentBlockStart + "\n\n")
	case "response.output_item.delta":
		item := ojson.GetBytes(payload, "item")
		if item.Get("type").String() != "function_call" {
			break
		}
		tool := resolveTool(item.Get("id").String(), int(ojson.GetBytes(payload, "output_index").Int()))
		if tool == nil {
			break
		}
		partial := ojson.GetBytes(payload, "delta").String()
		if partial == "" {
			partial = item.Get("arguments").String()
		}
		if partial == "" {
			break
		}
		inputDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
		inputDelta, _ = ojson.Set(inputDelta, "index", tool.Index)
		inputDelta, _ = ojson.Set(inputDelta, "delta.partial_json", partial)
		appendResult("event: content_block_delta\ndata: " + inputDelta + "\n\n")
	case "response.function_call_arguments.delta":
		// Copilot sends tool call arguments via this event type (not response.output_item.delta).
		// Data format: {"delta":"...", "item_id":"...", "output_index":N, ...}
		itemID := ojson.GetBytes(payload, "item_id").String()
		outputIndex := int(ojson.GetBytes(payload, "output_index").Int())
		tool := resolveTool(itemID, outputIndex)
		if tool == nil {
			break
		}
		partial := ojson.GetBytes(payload, "delta").String()
		if partial == "" {
			break
		}
		inputDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`
		inputDelta, _ = ojson.Set(inputDelta, "index", tool.Index)
		inputDelta, _ = ojson.Set(inputDelta, "delta.partial_json", partial)
		appendResult("event: content_block_delta\ndata: " + inputDelta + "\n\n")
	case "response.output_item.done":
		if ojson.GetBytes(payload, "item.type").String() != "function_call" {
			break
		}
		tool := resolveTool(ojson.GetBytes(payload, "item.id").String(), int(ojson.GetBytes(payload, "output_index").Int()))
		if tool == nil {
			break
		}
		contentBlockStop := `{"type":"content_block_stop","index":0}`
		contentBlockStop, _ = ojson.Set(contentBlockStop, "index", tool.Index)
		appendResult("event: content_block_stop\ndata: " + contentBlockStop + "\n\n")
	case "response.completed":
		ensureMessageStart()
		stopTextBlockIfNeeded()
		if !state.MessageStopSent {
			stopReason := "end_turn"
			if state.HasToolUse {
				stopReason = "tool_use"
			} else if sr := ojson.GetBytes(payload, "response.stop_reason").String(); sr == "max_tokens" || sr == "stop" {
				stopReason = sr
			}
			inputTokens := ojson.GetBytes(payload, "response.usage.input_tokens").Int()
			outputTokens := ojson.GetBytes(payload, "response.usage.output_tokens").Int()
			cachedTokens := ojson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int()
			if cachedTokens > 0 && inputTokens >= cachedTokens {
				inputTokens -= cachedTokens
			}
			messageDelta := `{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
			messageDelta, _ = ojson.Set(messageDelta, "delta.stop_reason", stopReason)
			messageDelta, _ = ojson.Set(messageDelta, "usage.input_tokens", inputTokens)
			messageDelta, _ = ojson.Set(messageDelta, "usage.output_tokens", outputTokens)
			if cachedTokens > 0 {
				messageDelta, _ = ojson.Set(messageDelta, "usage.cache_read_input_tokens", cachedTokens)
			}
			appendResult("event: message_delta\ndata: " + messageDelta + "\n\n")
			appendResult("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			state.MessageStopSent = true
		}
	}

	return results
}

func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

const (
	defaultCopilotContextLength       = 128000
	defaultCopilotMaxCompletionTokens = 16384
)

// FetchGitHubCopilotModels dynamically fetches available models from the GitHub Copilot API.
// It exchanges the GitHub access token stored in auth.Metadata for a Copilot API token,
// then queries the /models endpoint. Falls back to the static registry on any failure.
func FetchGitHubCopilotModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if auth == nil {
		log.Debug("github-copilot: auth is nil, using static models")
		return registry.GetGitHubCopilotModels()
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		log.Debug("github-copilot: no access_token in auth metadata, using static models")
		return registry.GetGitHubCopilotModels()
	}

	copilotAuth := copilotauth.NewCopilotAuth(cfg)

	entries, err := copilotAuth.ListModelsWithGitHubToken(ctx, accessToken)
	if err != nil {
		log.Warnf("github-copilot: failed to fetch dynamic models: %v, using static models", err)
		return registry.GetGitHubCopilotModels()
	}

	if len(entries) == 0 {
		log.Debug("github-copilot: API returned no models, using static models")
		return registry.GetGitHubCopilotModels()
	}

	// Build a lookup from the static definitions so we can enrich dynamic entries
	// with known context lengths, thinking support, etc.
	staticMap := make(map[string]*registry.ModelInfo)
	for _, m := range registry.GetGitHubCopilotModels() {
		staticMap[m.ID] = m
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		// Deduplicate model IDs to avoid incorrect reference counting.
		if _, dup := seen[entry.ID]; dup {
			continue
		}
		seen[entry.ID] = struct{}{}

		m := &registry.ModelInfo{
			ID:      entry.ID,
			Object:  "model",
			Created: now,
			OwnedBy: "github-copilot",
			Type:    "github-copilot",
		}

		if entry.Created > 0 {
			m.Created = entry.Created
		}
		if entry.Name != "" {
			m.DisplayName = entry.Name
		} else {
			m.DisplayName = entry.ID
		}

		if static, ok := staticMap[entry.ID]; ok {
			if m.DisplayName == entry.ID && static.DisplayName != "" {
				m.DisplayName = static.DisplayName
			}
			m.Description = static.Description
			m.ContextLength = static.ContextLength
			m.MaxCompletionTokens = static.MaxCompletionTokens
			m.SupportedEndpoints = static.SupportedEndpoints
			m.Thinking = static.Thinking
		} else {
			m.Description = entry.ID + " via GitHub Copilot"
			m.ContextLength = defaultCopilotContextLength
			m.MaxCompletionTokens = defaultCopilotMaxCompletionTokens
		}

		// Prefer per-account API limits over static defaults. max_prompt_tokens
		// is the enforced prompt limit, so use it as ContextLength.
		if limits := entry.Limits(); limits != nil {
			if limits.MaxPromptTokens > 0 {
				m.ContextLength = limits.MaxPromptTokens
			}
			if limits.MaxOutputTokens > 0 {
				m.MaxCompletionTokens = limits.MaxOutputTokens
			}
		}

		models = append(models, m)
	}

	log.Infof("github-copilot: fetched %d models from API", len(models))
	return models
}
