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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest is a no-op for now (credentials are added via headers at execution time).
func (e *OpenAICompatExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	// Translate inbound request to OpenAI format
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), opts.Stream)
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
	}
	translated = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", translated)

	// Check if this is a web search request (has special marker we added in translator)
	isWebSearch := isWebSearchRequest(translated)

	// Store the marker flag but clean the payload before sending
	sendPayload := translated
	if isWebSearch {
		sendPayload = pickWebSearchFields(sendPayload)
	}

	var url string
	if isWebSearch {
		url = strings.TrimSuffix(baseURL, "/") + "/chat/retrieve"
	} else {
		url = strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(sendPayload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	log.Debugf("OpenAICompatExecutor Execute: HTTP Response status: %d, headers: %v", httpResp.StatusCode, httpResp.Header)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("OpenAICompatExecutor Execute: request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)

	// Handle web search responses differently from standard OpenAI responses
	var out string
	var param any
	if isWebSearch {
		log.Debugf("OpenAICompatExecutor Execute: Web search response received, request model: %s, raw response: %s", req.Model, string(body))
		// For web search responses, we need to format them properly for Claude
		// The /chat/retrieve endpoint returns a different format than OpenAI
		translatedOut := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, body, &param)
		log.Debugf("OpenAICompatExecutor Execute: Web search response translated to: %s", translatedOut)
		out = translatedOut
	} else {
		// Standard OpenAI response handling
		reporter.publish(ctx, parseOpenAIUsage(body))
		// Ensure we at least record the request even if upstream doesn't return usage
		reporter.ensurePublished(ctx)
		// Translate response back to source format when needed
		out = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, body, &param)
	}
	log.Debugf("OpenAICompatExecutor Execute: Response translated to: %s", out)

	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
	}
	translated = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", translated)

	// Check if this is a web search request (has special marker we added in translator)
	isWebSearch := isWebSearchRequest(translated)

	// Store the marker flag but clean the payload before sending
	sendPayload := translated
	if isWebSearch {
		sendPayload = pickWebSearchFields(sendPayload)
	}

	var url string
	if isWebSearch {
		url = strings.TrimSuffix(baseURL, "/") + "/chat/retrieve"
	} else {
		url = strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(sendPayload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	// For web search, we don't want stream headers as it returns a complete response
	if !isWebSearch {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
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
	log.Debugf("OpenAICompatExecutor ExecuteStream: HTTP Response status: %d, headers: %v", httpResp.StatusCode, httpResp.Header)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("OpenAICompatExecutor ExecuteStream: request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()

		// For web search requests, the response is a single JSON rather than an SSE stream
		if isWebSearch {
			// Read the complete response body at once, since /chat/retrieve returns complete JSON
			body, err := io.ReadAll(httpResp.Body)
			if err != nil {
				recordAPIResponseError(ctx, e.cfg, err)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: err}
				return
			}

			log.Debugf("OpenAICompatExecutor ExecuteStream: Web search response received, raw response: %s", string(body))
			appendAPIResponseChunk(ctx, e.cfg, body)

			// Translate the single web search response to SSE events
			// The response translator should handle web search response format and generate SSE events
			var param any
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, body, &param)
			for i := range chunks {
				log.Debugf("OpenAICompatExecutor ExecuteStream: Web search SSE event chunk: %s", chunks[i])
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		} else {
			// For regular OpenAI-compatible streaming responses
			scanner := bufio.NewScanner(httpResp.Body)
			buf := make([]byte, 20_971_520)
			scanner.Buffer(buf, 20_971_520)
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
				// OpenAI-compatible streams are SSE: lines typically prefixed with "data: ".
				// Pass through translator; it yields one or more chunks for the target schema.
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bytes.Clone(line), &param)
				for i := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				recordAPIResponseError(ctx, e.cfg, errScan)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errScan}
			}
			// Ensure we record the request if no usage chunk was ever seen
			reporter.ensurePublished(ctx)
		}
	}()
	return stream, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	modelForCounting := req.Model
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
		modelForCounting = modelOverride
	}

	enc, err := tokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: []byte(translatedUsage)}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveUpstreamModel(alias string, auth *cliproxyauth.Auth) string {
	if alias == "" || auth == nil || e.cfg == nil {
		return ""
	}
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		return ""
	}
	for i := range compat.Models {
		model := compat.Models[i]
		if model.Alias != "" {
			if strings.EqualFold(model.Alias, alias) {
				if model.Name != "" {
					return model.Name
				}
				return alias
			}
			continue
		}
		if strings.EqualFold(model.Name, alias) {
			return model.Name
		}
	}
	return ""
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
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
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }

// isWebSearchRequest checks if the translated request is a web search request
// by checking if it has exactly one tool that matches /^web_search/ or if it has the special marker
func isWebSearchRequest(translated []byte) bool {
	// First check for the special marker that the translator adds
	if bytes.Contains(translated, []byte("\"_web_search_request\":true")) {
		return true
	}

	var req map[string]interface{}
	if err := json.Unmarshal(translated, &req); err != nil {
		return false
	}

	// Check if tools exist and is an array
	tools, ok := req["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		return false
	}

	// Check if the single tool has a type that matches /^web_search/
	if tool, ok := tools[0].(map[string]interface{}); ok {
		if toolType, ok := tool["type"].(string); ok {
			return strings.HasPrefix(toolType, "web_search")
		}
	}

	return false
}

// pickWebSearchFields extracts only the required fields for /chat/retrieve endpoint
func pickWebSearchFields(payload []byte) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return payload
	}

	// Create new map with only the 6 required fields for /chat/retrieve
	cleaned := make(map[string]interface{})

	// Only extract these specific fields  (model is required, enableIntention and enableQueryRewrite should be false)
	if model, ok := data["model"].(string); ok {
		cleaned["model"] = model
	}
	if phase, ok := data["phase"].(string); ok {
		cleaned["phase"] = phase
	}
	if query, ok := data["query"].(string); ok {
		cleaned["query"] = query
	}
	if enableIntention, ok := data["enableIntention"].(bool); ok {
		cleaned["enableIntention"] = enableIntention
	}
	if appCode, ok := data["appCode"].(string); ok {
		cleaned["appCode"] = appCode
	}
	if enableQueryRewrite, ok := data["enableQueryRewrite"].(bool); ok {
		cleaned["enableQueryRewrite"] = enableQueryRewrite
	}

	// Re-encode with only the required fields
	result, err := json.Marshal(cleaned)
	if err != nil {
		return payload
	}

	return result
}
