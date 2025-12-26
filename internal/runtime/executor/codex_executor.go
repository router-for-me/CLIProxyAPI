package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var dataTag = []byte("data:")

// applyCodexSpecificFields adds Codex-specific fields to the request body.
// These fields are required for proper Codex API compatibility:
// - store: false - disable storage
// - include: ["reasoning.encrypted_content"] - include encrypted reasoning
// - reasoning.summary: "auto" - automatic reasoning summary
// - parallel_tool_calls: true - enable parallel tool calls
// - web_search tool mapping - convert web_search_20250305 to native web_search
// - tool name shortening - shorten MCP tool names to 64 chars max
func applyCodexSpecificFields(body []byte, modelName string) []byte {
	// 1. Set store to false
	body, _ = sjson.SetBytes(body, "store", false)

	// 2. Include encrypted reasoning content
	body, _ = sjson.SetBytes(body, "include", []string{"reasoning.encrypted_content"})

	// 3. Set reasoning.summary to auto if not already set
	if !gjson.GetBytes(body, "reasoning.summary").Exists() {
		body, _ = sjson.SetBytes(body, "reasoning.summary", "auto")
	}

	// 4. Set parallel_tool_calls to true if not already set
	if !gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		body, _ = sjson.SetBytes(body, "parallel_tool_calls", true)
	}

	// 5. Apply Codex instructions if not already set
	if !gjson.GetBytes(body, "instructions").Exists() || gjson.GetBytes(body, "instructions").String() == "" {
		_, instructions := misc.CodexInstructionsForModel(modelName, "")
		if instructions != "" {
			body, _ = sjson.SetBytes(body, "instructions", instructions)
		}
	}

	// 6. Map special web search tools to native Codex web_search
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		toolsArr := tools.Array()
		for i, t := range toolsArr {
			toolType := t.Get("type").String()
			toolName := t.Get("name").String()

			// Map web_search_20250305 (Claude) or googleSearch (Gemini) to native web_search
			// Also handle "web_search" explicitly to ensure it has correct type/structure if passed
			if toolType == "web_search_20250305" || toolName == "web_search" || toolName == "googleSearch" || toolName == "web_search_20250305" {
				body, _ = sjson.SetRawBytes(body, fmt.Sprintf("tools.%d", i), []byte(`{"type":"web_search"}`))
				continue
			}

			// 7. Shorten tool names if needed (MCP tools can have very long names)
			if len(toolName) > 64 {
				shortName := shortenToolName(toolName)
				body, _ = sjson.SetBytes(body, fmt.Sprintf("tools.%d.name", i), shortName)
			}
		}
	}

	return body
}

// shortenToolName shortens a tool name to fit within 64 character limit.
// For MCP tools (mcp__server__tool), it extracts just the tool name part.
func shortenToolName(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := codexCreds(auth)

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	upstreamModel := util.ResolveOriginalModel(req.Model, req.Metadata)

	from := opts.SourceFormat
	useNewTranslator := e.cfg != nil && e.cfg.UseCanonicalTranslator

	// Translate request: new translator if enabled, otherwise old translator (no fallback)
	var body []byte
	if useNewTranslator {
		var errTranslate error
		body, errTranslate = TranslateToOpenAI(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata, FormatResponsesAPI)
		if errTranslate != nil {
			return resp, fmt.Errorf("failed to translate request: %w", errTranslate)
		}
		// Apply Codex-specific fields for new translator
		body = applyCodexSpecificFields(body, upstreamModel)
	} else {
		to := sdktranslator.FromString("codex")
		body = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	}

	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning.effort", false)
	body = NormalizeThinkingConfig(body, upstreamModel, false)
	if errValidate := ValidateThinkingConfig(body, upstreamModel); errValidate != nil {
		return resp, errValidate
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.SetBytes(body, "model", upstreamModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey)
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
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		line = bytes.TrimSpace(line[5:])
		if gjson.GetBytes(line, "type").String() != "response.completed" {
			continue
		}

		if detail, ok := parseCodexUsage(line); ok {
			reporter.publish(ctx, detail)
		}

		// Use new or old translator based on config (no fallback)
		if useNewTranslator {
			translated, errTranslate := TranslateOpenAIResponseNonStream(e.cfg, from, line, req.Model)
			if errTranslate != nil {
				return resp, fmt.Errorf("failed to translate response: %w", errTranslate)
			}
			resp = cliproxyexecutor.Response{Payload: translated}
		} else {
			to := sdktranslator.FromString("codex")
			var param any
			out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, line, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
		}
		return resp, nil
	}
	err = statusErr{code: 408, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	return resp, err
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseURL := codexCreds(auth)

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	upstreamModel := util.ResolveOriginalModel(req.Model, req.Metadata)

	from := opts.SourceFormat
	useNewTranslator := e.cfg != nil && e.cfg.UseCanonicalTranslator

	// Translate request: new translator if enabled, otherwise old translator (no fallback)
	var body []byte
	if useNewTranslator {
		var errTranslate error
		body, errTranslate = TranslateToOpenAI(e.cfg, from, req.Model, bytes.Clone(req.Payload), true, req.Metadata, FormatResponsesAPI)
		if errTranslate != nil {
			return nil, fmt.Errorf("failed to translate request: %w", errTranslate)
		}
		// Apply Codex-specific fields for new translator
		body = applyCodexSpecificFields(body, upstreamModel)
	} else {
		to := sdktranslator.FromString("codex")
		body = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	}

	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning.effort", false)
	body = NormalizeThinkingConfig(body, upstreamModel, false)
	if errValidate := ValidateThinkingConfig(body, upstreamModel); errValidate != nil {
		return nil, errValidate
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.SetBytes(body, "model", upstreamModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey)
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
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB

		// State for new translator
		var streamState *OpenAIStreamState
		if useNewTranslator {
			streamState = NewOpenAIStreamState()
		}
		messageID := "resp-" + req.Model
		var param any

		// Buffer for SSE event lines - Codex API sends "event:" and "data:" on separate lines
		var pendingEventLine []byte

		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// SSE format: "event: xxx" followed by "data: {...}" on next line
			// We need to combine them for proper parsing
			if bytes.HasPrefix(line, []byte("event:")) {
				// Store event line and wait for data line
				pendingEventLine = bytes.Clone(line)
				continue
			}

			// Build complete SSE chunk by combining event + data lines
			var sseChunk []byte
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if gjson.GetBytes(data, "type").String() == "response.completed" {
					if detail, ok := parseCodexUsage(data); ok {
						reporter.publish(ctx, detail)
					}
				}

				// Combine pending event line with data line
				if len(pendingEventLine) > 0 {
					sseChunk = append(pendingEventLine, '\n')
					sseChunk = append(sseChunk, line...)
					pendingEventLine = nil
				} else {
					sseChunk = bytes.Clone(line)
				}
			} else if len(line) == 0 {
				// Empty line - skip (SSE event separator)
				continue
			} else {
				// Other content - pass through as-is
				sseChunk = bytes.Clone(line)
			}

			if len(sseChunk) == 0 {
				continue
			}

			// Use new or old translator based on config (no fallback)
			if useNewTranslator {
				// Convert Responses API format back to client's expected format.
				// If client sent request via /v1/chat/completions (from="openai"), convert to Chat Completions.
				// If client sent request via /v1/responses (from="codex"/"openai-response"), passthrough as-is.
				translatedChunks, errTranslate := TranslateOpenAIResponseStream(e.cfg, from, sseChunk, req.Model, messageID, streamState)
				if errTranslate != nil {
					out <- cliproxyexecutor.StreamChunk{Err: errTranslate}
					return
				}
				for _, chunk := range translatedChunks {
					out <- cliproxyexecutor.StreamChunk{Payload: chunk}
				}
			} else {
				to := sdktranslator.FromString("codex")
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, sseChunk, &param)
				for i := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
				}
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

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	upstreamModel := util.ResolveOriginalModel(req.Model, req.Metadata)

	from := opts.SourceFormat
	useNewTranslator := e.cfg != nil && e.cfg.UseCanonicalTranslator

	// Translate request: new translator if enabled, otherwise old translator (no fallback)
	var body []byte
	if useNewTranslator {
		var errTranslate error
		body, errTranslate = TranslateToOpenAI(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata, FormatResponsesAPI)
		if errTranslate != nil {
			return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: failed to translate request: %w", errTranslate)
		}
		// Apply Codex-specific fields for new translator
		body = applyCodexSpecificFields(body, upstreamModel)
	} else {
		to := sdktranslator.FromString("codex")
		body = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	}

	modelForCounting := req.Model

	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning.effort", false)
	body, _ = sjson.SetBytes(body, "model", upstreamModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.SetBytes(body, "stream", false)

	enc, err := tokenizerForCodexModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	to := sdktranslator.FromString("codex")
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuth(e.cfg)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
	var cache codexCache
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			var hasKey bool
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			if cache, hasKey = getCodexCache(key); !hasKey || cache.Expire.Before(time.Now()) {
				cache = codexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				setCodexCache(key, cache)
			}
		}
	} else if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Conversation_id", cache.ID)
	httpReq.Header.Set("Session_id", cache.ID)
	return httpReq, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "Version", "0.21.0")
	misc.EnsureHeader(r.Header, ginHeaders, "Openai-Beta", "responses=experimental")
	misc.EnsureHeader(r.Header, ginHeaders, "Session_id", uuid.NewString())
	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", "codex_cli_rs/0.50.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464")

	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if !isAPIKey {
		r.Header.Set("Originator", "codex_cli_rs")
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}
