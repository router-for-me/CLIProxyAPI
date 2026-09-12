package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// TraeExecutor routes requests through an existing local Trae CLI account.
type TraeExecutor struct {
	cfg *config.Config
}

// NewTraeExecutor creates a Trae provider executor.
func NewTraeExecutor(cfg *config.Config) *TraeExecutor { return &TraeExecutor{cfg: cfg} }

// Identifier returns the executor provider key.
func (e *TraeExecutor) Identifier() string { return traeauth.Provider }

// RequestToFormat reports the OpenAI chat-completions shape used before Trae wrapping.
func (e *TraeExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest reloads the local Trae access token and injects it into a request.
func (e *TraeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if auth == nil {
		return fmt.Errorf("trae executor: auth is nil")
	}
	token, errToken := traeauth.AccessToken(auth.Metadata)
	if errToken != nil {
		return errToken
	}
	req.Header.Set("Authorization", "Cloud-CLI-JWT "+token)
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

// HttpRequest executes an arbitrary HTTP request with the current Trae credential.
func (e *TraeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("trae executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Execute performs a non-streaming request against Trae's raw chat SSE endpoint.
func (e *TraeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errPrepare := e.prepareRequest(ctx, auth, req, opts)
	if errPrepare != nil {
		return resp, errPrepare
	}
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())
	httpResp, errDo := e.doRequest(ctx, auth, body, reporter)
	if errDo != nil {
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close response body error: %v", errClose)
		}
	}()

	accumulator := helps.NewTraeResponseAccumulator(req.Model)
	errScan := helps.ScanTraeSSE(httpResp.Body, func(event helps.TraeSSEEvent) error {
		helpRaw := append([]byte("event: "+event.Name+"\ndata: "), event.Data...)
		helps.AppendAPIResponseChunk(ctx, e.cfg, helpRaw)
		return accumulator.Consume(event)
	})
	if errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		return resp, errScan
	}
	if !accumulator.IsFinished() {
		return resp, fmt.Errorf("trae executor: raw chat stream closed before done event")
	}

	openAIResponse := accumulator.ResponseJSON()
	reporter.Publish(ctx, helps.ParseOpenAIUsage(openAIResponse))
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, opts.OriginalRequest, body, openAIResponse, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming Trae request and translates each event downstream.
func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errPrepare := e.prepareRequest(ctx, auth, req, opts)
	if errPrepare != nil {
		return nil, errPrepare
	}
	reporter.SetTranslatedReasoningEffort(body, e.Identifier())
	httpResp, errDo := e.doRequest(ctx, auth, body, reporter)
	if errDo != nil {
		return nil, errDo
	}

	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	streamState := helps.NewTraeStreamState(req.Model)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("trae executor: close response body error: %v", errClose)
			}
		}()
		originalPayload := opts.OriginalRequest
		if len(originalPayload) == 0 {
			originalPayload = req.Payload
		}
		claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatOpenAI, responseFormat, originalPayload)
		var param any
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		emitLine := func(line []byte) bool {
			streamUsage.ObserveOpenAIStream(line)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, opts.OriginalRequest, body, line, &param, claudeInputTokens)
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		errStream := helps.ScanTraeSSE(httpResp.Body, func(event helps.TraeSSEEvent) error {
			helpRaw := append([]byte("event: "+event.Name+"\ndata: "), event.Data...)
			helps.AppendAPIResponseChunk(ctx, e.cfg, helpRaw)
			line, errConvert := streamState.Convert(event)
			if errConvert != nil {
				return errConvert
			}
			if len(line) > 0 && !emitLine(line) {
				return ctx.Err()
			}
			return nil
		})
		if errStream == nil && !streamState.IsFinished() {
			errStream = fmt.Errorf("trae executor: raw chat stream closed before done event")
		}
		if errStream != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errStream)
			reporter.PublishFailure(ctx, errStream)
			if ctx.Err() == nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errStream}:
				case <-ctx.Done():
				}
			}
			return
		}
		_ = emitLine([]byte("[DONE]"))
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens estimates tokens using the repository's OpenAI chat tokenizer.
func (e *TraeExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, sdktranslator.FormatOpenAI, baseModel, bytes.Clone(req.Payload), false)
	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, from.String(), sdktranslator.FormatOpenAI.String(), e.Identifier())
	if errThinking != nil {
		return cliproxyexecutor.Response{}, errThinking
	}
	enc, errTokenizer := helps.TokenizerForModel(baseModel)
	if errTokenizer != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("trae executor: tokenizer init failed: %w", errTokenizer)
	}
	count, errCount := helps.CountOpenAIChatTokens(enc, body)
	if errCount != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("trae executor: token counting failed: %w", errCount)
	}
	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translated := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FormatOpenAI, cliproxyexecutor.ResponseFormatOrSource(opts), count, usageJSON)
	return cliproxyexecutor.Response{Payload: translated}, nil
}

// Refresh asks the local CLI to validate or renew its login, then reloads referenced state.
func (e *TraeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debug("trae executor: refresh called")
	if refreshed, handled, errHome := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, errHome
	}
	if auth == nil {
		return nil, fmt.Errorf("trae executor: auth is nil")
	}
	cliPath := metadataString(auth.Metadata, "trae_cli_path")
	authPath := metadataString(auth.Metadata, "trae_auth_path")
	if cliPath == "" || authPath == "" {
		return auth, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRefresh()
	models, clientVersion, errModels := traeauth.FetchModels(refreshCtx, cliPath)
	credentials, errLoad := traeauth.LoadCredentials(authPath)
	if errLoad != nil {
		return nil, errLoad
	}
	if errModels != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !traeCredentialUsableAt(credentials, time.Now()) {
			return nil, fmt.Errorf("trae executor: refresh local CLI login: %w", errModels)
		}
		log.Warn("trae executor: model catalog refresh failed; retaining unexpired local credential")
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = traeauth.Provider
	auth.Metadata["user_id"] = credentials.UserID
	auth.Metadata["region"] = credentials.Region
	auth.Metadata["expires_at"] = credentials.ExpiresAt
	auth.Metadata["expired"] = credentials.ExpiresAt
	auth.Metadata["last_refresh"] = credentials.LastRefresh
	if errModels == nil {
		auth.Metadata["models"] = models
		if clientVersion != "" {
			auth.Metadata["client_version"] = clientVersion
		}
	}
	return auth, nil
}

func (e *TraeExecutor) prepareRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	if auth == nil {
		return nil, fmt.Errorf("trae executor: auth is nil")
	}
	from := opts.SourceFormat
	to := sdktranslator.FormatOpenAI
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	originalPayload := opts.OriginalRequest
	if len(originalPayload) == 0 {
		originalPayload = req.Payload
	}
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(originalPayload), false)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, bytes.Clone(req.Payload), false)
	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	var translated map[string]any
	if errUnmarshal := json.Unmarshal(body, &translated); errUnmarshal != nil {
		return nil, fmt.Errorf("trae executor: parse translated request: %w", errUnmarshal)
	}
	model, found := traeauth.ModelFor(auth.Metadata, baseModel)
	if !found {
		model = traeauth.Model{Name: baseModel, ConfigName: baseModel, BackendModel: baseModel}
		if !strings.HasSuffix(strings.ToLower(model.BackendModel), "__dev") {
			model.BackendModel += "__dev"
		}
	}
	conversationID := helps.ProviderSessionUUID(traeauth.Provider, req.Metadata, opts.Metadata)
	if conversationID == "" {
		conversationID = newTraeUUID()
	}
	sessionID := uuid.NewString()
	maxTokens := intValue(translated["max_tokens"])
	if maxTokens <= 0 {
		maxTokens = intValue(translated["max_completion_tokens"])
	}
	if maxTokens <= 0 {
		maxTokens = 32_000
	}
	parallelToolCalls := true
	if configured, ok := translated["parallel_tool_calls"].(bool); ok {
		parallelToolCalls = configured
	}
	messages := normalizeTraeMessages(translated["messages"])
	requestPayload := map[string]any{
		"access_type":         4,
		"biz_context":         map[string]any{"repo_urls": []string{}},
		"config_name":         model.ConfigName,
		"conversation_id":     conversationID,
		"is_preset":           true,
		"max_tokens":          maxTokens,
		"messages":            messages,
		"model_name":          model.BackendModel,
		"parallel_tool_calls": parallelToolCalls,
		"reasoning_effort":    traeReasoningEffort(translated),
		"session_id":          sessionID,
		"tools":               []any{},
		"user_input":          latestTraeUserInput(messages),
	}
	for _, key := range []string{"tools", "tool_choice", "response_format"} {
		if value, ok := translated[key]; ok && value != nil {
			requestPayload[key] = value
		}
	}
	encoded, errMarshal := json.Marshal(requestPayload)
	if errMarshal != nil {
		return nil, fmt.Errorf("trae executor: encode request: %w", errMarshal)
	}
	return encoded, nil
}

func (e *TraeExecutor) doRequest(ctx context.Context, auth *cliproxyauth.Auth, body []byte, reporter *helps.UsageReporter) (*http.Response, error) {
	token, errToken := traeauth.AccessToken(auth.Metadata)
	if errToken != nil {
		return nil, errToken
	}
	baseURLs := traeauth.BaseURLs(auth.Metadata)
	if len(baseURLs) == 0 {
		return nil, fmt.Errorf("trae executor: no upstream endpoint is configured")
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	if reporter != nil {
		httpClient = reporter.TrackHTTPClient(httpClient)
	}
	var lastErr error
	for index, baseURL := range baseURLs {
		url := strings.TrimSuffix(baseURL, "/") + traeauth.RawChatPath
		httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if errRequest != nil {
			return nil, errRequest
		}
		applyTraeHeaders(httpReq, auth, token)
		httpReq.Header.Set("X-Flow-Traceparent", traeTraceparentForSession(metadataStringFromJSON(body, "session_id")))
		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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
		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			lastErr = errDo
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			return httpResp, nil
		}
		errorBody, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close rejected response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, errorBody)
		lastErr = statusErr{code: httpResp.StatusCode, msg: string(errorBody)}
		if index+1 < len(baseURLs) && (httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusBadGateway || httpResp.StatusCode == http.StatusServiceUnavailable) {
			continue
		}
		return nil, lastErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("trae executor: all upstream endpoint candidates failed")
	}
	return nil, lastErr
}

func applyTraeHeaders(request *http.Request, auth *cliproxyauth.Auth, token string) {
	version := metadataString(auth.Metadata, "client_version")
	if version == "" {
		version = buildinfo.Version
	}
	if version == "" || version == "dev" {
		version = "unknown"
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Cloud-CLI-JWT "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Originator", "codex_exec")
	request.Header.Set("User-Agent", fmt.Sprintf("codex_exec/%s (%s; %s) dumb (codex_exec; %s)", version, traePlatformName(), runtime.GOARCH, version))
	request.Header.Set("Version", version)
	request.Header.Set("X-App-Id", traeauth.AppID)
	request.Header.Set("X-Flow-Traceparent", newTraeTraceparent())
	request.Header.Set("X-Ide-Function", "traecli_next")
	request.Header.Set("X-Ide-Version-Code", time.Now().UTC().Format("20060102"))
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
}

func normalizeTraeMessages(value any) []any {
	messages, ok := value.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			continue
		}
		copyMessage := make(map[string]any, len(message))
		for key, field := range message {
			copyMessage[key] = field
		}
		if text, okText := copyMessage["content"].(string); okText {
			copyMessage["content"] = []any{map[string]any{"type": "text", "text": text}}
		}
		out = append(out, copyMessage)
	}
	return out
}

func latestTraeUserInput(messages []any) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || !strings.EqualFold(anyString(message["role"]), "user") {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			return content
		case []any:
			parts := make([]string, 0, len(content))
			for _, rawPart := range content {
				part, okPart := rawPart.(map[string]any)
				if !okPart {
					continue
				}
				text := anyString(part["text"])
				if text == "" {
					text = anyString(part["input_text"])
				}
				if text != "" {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	default:
		return 0
	}
}

func traeReasoningEffort(payload map[string]any) string {
	if effort := strings.TrimSpace(anyString(payload["reasoning_effort"])); effort != "" {
		return effort
	}
	return "xhigh"
}

func traePlatformName() string {
	switch runtime.GOOS {
	case "linux":
		return "Debian 10.0.0"
	case "darwin":
		return "Mac OS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func newTraeUUID() string { return uuid.NewString() }

func newTraeTraceparent() string {
	return traeTraceparentForSession(uuid.NewString())
}

func traeTraceparentForSession(sessionID string) string {
	traceID := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(sessionID), "-", ""))
	if len(traceID) != 32 {
		traceID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return "00-" + traceID + "-" + traceID[:16] + "-01"
}

func metadataStringFromJSON(payload []byte, key string) string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	return strings.TrimSpace(anyString(value[key]))
}

func traeCredentialUsableAt(credentials traeauth.Credentials, now time.Time) bool {
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return false
	}
	expiresAt := strings.TrimSpace(credentials.ExpiresAt)
	if expiresAt == "" {
		return true
	}
	expiry, errParse := time.Parse(time.RFC3339Nano, expiresAt)
	return errParse == nil && expiry.After(now)
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}
