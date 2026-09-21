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

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	kiroeventstream "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	kirotranslator "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	KiroStreamingUserAgent  = "aws-sdk-js/1.0.34 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.34 m/E KiroIDE-0.10.32-kiro-anonymous"
	KiroStreamingAmzUA      = "aws-sdk-js/1.0.34 KiroIDE-0.10.32-kiro-anonymous"
	KiroRuntimeUserAgent    = "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/codewhispererruntime#1.0.0 m/N,E KiroIDE-0.10.32-kiro-anonymous"
	KiroRuntimeAmzUA        = "aws-sdk-js/1.0.0 KiroIDE-0.10.32-kiro-anonymous"
	KiroCodeWhispererTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	KiroAmazonQTarget       = "AmazonQDeveloperStreamingService.SendMessage"
)

type kiroEndpoint struct {
	URL       string
	Origin    string
	AmzTarget string
	Name      string
}

var kiroStreamingEndpoints = []kiroEndpoint{
	{URL: "https://q.us-east-1.amazonaws.com/generateAssistantResponse", Origin: "AI_EDITOR", Name: "Kiro IDE"},
	{URL: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse", Origin: "AI_EDITOR", AmzTarget: KiroCodeWhispererTarget, Name: "CodeWhisperer"},
	{URL: "https://q.us-east-1.amazonaws.com/generateAssistantResponse", Origin: "AI_EDITOR", AmzTarget: KiroAmazonQTarget, Name: "AmazonQ"},
}

var kiroRuntimeEndpoint = kiroEndpoint{
	URL:       "https://runtime.us-east-1.kiro.dev/",
	Origin:    "KIRO_CLI",
	AmzTarget: KiroCodeWhispererTarget,
	Name:      "Kiro CLI",
}

// KiroExecutor handles requests for Kiro AI (AWS CodeWhisperer/Amazon Q).
type KiroExecutor struct {
	cfg        *config.Config
	httpClient *http.Client
	authSvc    *kiroauth.KiroAuth
}

// NewKiroExecutor creates a new KiroExecutor instance.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	client := &http.Client{}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroExecutor{
		cfg:        cfg,
		httpClient: client,
		authSvc:    kiroauth.NewKiroAuth(cfg, client),
	}
}

// Identifier returns the unique executor identifier.
func (e *KiroExecutor) Identifier() string {
	return "kiro"
}

// PrepareRequest prepares the HTTP request with Kiro headers.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	return nil
}

// BuildHeaders constructs the required headers for Kiro API call.
func (e *KiroExecutor) BuildHeaders(targetURL string, accessToken string, authMethod string) http.Header {
	ep := kiroEndpoint{URL: targetURL}
	if strings.Contains(targetURL, "://codewhisperer.") {
		ep.AmzTarget = KiroCodeWhispererTarget
	}
	return e.buildHeadersForEndpoint(ep, accessToken, authMethod, 1, 3)
}

func (e *KiroExecutor) buildHeadersForEndpoint(ep kiroEndpoint, accessToken string, authMethod string, attempt int, maxAttempts int) http.Header {
	authMethod = strings.ToLower(strings.TrimSpace(authMethod))
	isAPIKey := authMethod == "api_key"
	headers := make(http.Header)
	if isAPIKey {
		headers.Set("Content-Type", "application/x-amz-json-1.0")
		headers.Set("User-Agent", KiroRuntimeUserAgent)
		headers.Set("X-Amz-User-Agent", KiroRuntimeAmzUA)
		headers.Set("x-amzn-codewhisperer-optout", "false")
		headers.Set("tokentype", "API_KEY")
	} else {
		headers.Set("Content-Type", "application/json")
		headers.Set("User-Agent", KiroStreamingUserAgent)
		headers.Set("X-Amz-User-Agent", KiroStreamingAmzUA)
		headers.Set("x-amzn-kiro-agent-mode", "vibe")
		headers.Set("x-amzn-codewhisperer-optout", "true")
	}
	headers.Set("Accept", "*/*")
	headers.Set("Amz-Sdk-Request", fmt.Sprintf("attempt=%d; max=%d", attempt, maxAttempts))
	headers.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
	if ep.AmzTarget != "" {
		headers.Set("X-Amz-Target", ep.AmzTarget)
	}
	if accessToken != "" {
		headers.Set("Authorization", "Bearer "+accessToken)
	}
	if authMethod == "external_idp" {
		headers.Set("TokenType", "EXTERNAL_IDP")
	}
	return headers
}

// GetOrderedBaseURLs orders the base URLs based on auth method.
func (e *KiroExecutor) GetOrderedBaseURLs(authMethod string, region string) []string {
	endpoints := e.getOrderedEndpoints(authMethod, region)
	urls := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		urls = append(urls, ep.URL)
	}
	return urls
}

func (e *KiroExecutor) getOrderedEndpoints(authMethod string, region string) []kiroEndpoint {
	safeRegion := kiroauth.AssertValidAwsRegion(region)
	if strings.EqualFold(strings.TrimSpace(authMethod), "api_key") {
		ep := kiroRuntimeEndpoint
		ep.URL = fmt.Sprintf("https://runtime.%s.kiro.dev/", safeRegion)
		return []kiroEndpoint{ep}
	}
	endpoints := make([]kiroEndpoint, 0, len(kiroStreamingEndpoints))
	for _, ep := range kiroStreamingEndpoints {
		if safeRegion != "us-east-1" && strings.Contains(ep.URL, "amazonaws.com") {
			ep.URL = strings.Replace(ep.URL, ".us-east-1.amazonaws.com", "."+safeRegion+".amazonaws.com", 1)
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints
}

// ExecuteKiroStream sends the translated request to Kiro and streams SSE back to the client.
func (e *KiroExecutor) ExecuteKiroStream(ctx context.Context, model string, reqBody []byte, accessToken string, profileArn string, authMethod string, region string, w http.ResponseWriter) error {
	// 1. Translate request payload to Kiro payload format
	kiroPayload, cleanModel, err := kirotranslator.TranslateToKiroRequest(reqBody, profileArn)
	if err != nil {
		return fmt.Errorf("kiro translation failed: %w", err)
	}
	log.Debugf("Kiro request translated for model %s: %s", cleanModel, string(kiroPayload))

	// 2. Select ordered URLs
	urls := e.GetOrderedBaseURLs(authMethod, region)

	var lastErr error
	var resp *http.Response

	for _, targetURL := range urls {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(kiroPayload))
		if err != nil {
			lastErr = err
			continue
		}

		httpReq.Header = e.BuildHeaders(targetURL, accessToken, authMethod)

		log.Infof("Sending request to Kiro endpoint: %s", targetURL)
		resp, err = e.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			log.Warnf("Kiro request to %s failed: %v", targetURL, err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		respBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("Kiro API %s returned status %d: %s", targetURL, resp.StatusCode, string(respBytes))
		log.Warnf("%v", lastErr)
	}

	if resp == nil || resp.StatusCode != http.StatusOK {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("failed to execute request across all Kiro endpoints")
	}
	defer resp.Body.Close()

	// Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)

	// 3. Read and decode AWS EventStream frames
	messageID := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()

	for {
		frame, err := kiroeventstream.ReadEventStreamFrame(resp.Body)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Debugf("EventStream decode finished or stopped: %v", err)
			break
		}

		eventType := frame.EventType()
		content, reasoning, parseErr := frame.ParseAssistantEvent()
		if parseErr != nil {
			continue
		}

		if content != "" || reasoning != "" {
			chunk := map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []map[string]any{
					{
						"index": 0,
						"delta": map[string]any{
							"content":           content,
							"reasoning_content": reasoning,
						},
						"finish_reason": nil,
					},
				},
			}
			chunkBytes, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
			if ok {
				flusher.Flush()
			}
		}

		if eventType == "messageStopEvent" {
			stopChunk := map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []map[string]any{
					{
						"index":         0,
						"delta":         map[string]any{},
						"finish_reason": "stop",
					},
				},
			}
			stopBytes, _ := json.Marshal(stopChunk)
			fmt.Fprintf(w, "data: %s\n\n", string(stopBytes))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if ok {
				flusher.Flush()
			}
			break
		}
	}

	return nil
}

func setKiroPayloadOrigin(payload []byte, origin string) ([]byte, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return payload, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	conversationState, ok := raw["conversationState"].(map[string]any)
	if !ok {
		return payload, nil
	}
	currentMessage, ok := conversationState["currentMessage"].(map[string]any)
	if !ok {
		return payload, nil
	}
	userInputMessage, ok := currentMessage["userInputMessage"].(map[string]any)
	if !ok {
		return payload, nil
	}
	userInputMessage["origin"] = origin
	return json.Marshal(raw)
}

func (e *KiroExecutor) executeRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, reporter *helps.UsageReporter) (*http.Response, http.Header, string, string, error) {
	if e == nil {
		return nil, nil, "", "", fmt.Errorf("kiro executor: executor is nil")
	}
	if auth == nil {
		return nil, nil, "", "", fmt.Errorf("kiro executor: auth is nil")
	}
	creds := kiroauth.CredentialsFromAuth(auth)
	if creds == nil {
		return nil, nil, "", "", fmt.Errorf("kiro executor: missing credentials")
	}
	profileArn := strings.TrimSpace(creds.ProfileArn)
	if profileArn == "" {
		if resolved, errResolve := e.authSvc.ResolveProfileArn(ctx, creds); errResolve == nil {
			profileArn = strings.TrimSpace(resolved)
		}
	}
	payload, cleanModel, err := kirotranslator.TranslateToKiroRequest(req.Payload, profileArn)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("kiro translation failed: %w", err)
	}
	endpoints := e.getOrderedEndpoints(strings.ToLower(strings.TrimSpace(creds.AuthMethod)), creds.Region)
	if len(endpoints) == 0 {
		return nil, nil, "", "", fmt.Errorf("kiro executor: no upstream endpoints available")
	}

	client := e.httpClient
	if auth.ProxyURL != "" {
		client = helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	}
	if client == nil {
		client = http.DefaultClient
	}
	client = reporter.TrackHTTPClient(client)

	var lastErr error
	for _, endpoint := range endpoints {
		requestPayload, errOrigin := setKiroPayloadOrigin(payload, endpoint.Origin)
		if errOrigin != nil {
			lastErr = errOrigin
			continue
		}
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(requestPayload))
		if errReq != nil {
			lastErr = errReq
			continue
		}
		httpReq.Header = e.buildHeadersForEndpoint(endpoint, strings.TrimSpace(creds.AccessToken), strings.ToLower(strings.TrimSpace(creds.AuthMethod)), 1, 1)
		resp, errReq := client.Do(httpReq)
		if errReq != nil {
			lastErr = errReq
			helps.RecordAPIResponseError(ctx, e.cfg, errReq)
			continue
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
		if resp.StatusCode == http.StatusOK {
			return resp, resp.Header.Clone(), cleanModel, endpoint.URL, nil
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		lastErr = statusErr{code: resp.StatusCode, msg: fmt.Sprintf("kiro api %s returned status %d: %s", endpoint.Name, resp.StatusCode, string(body))}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusPaymentRequired {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kiro executor: request failed")
	}
	return nil, nil, "", "", lastErr
}

// kiroStreamUsage holds the usage counters reported by a Kiro event stream.
// Kiro bills per credit and usually omits token counters entirely, so Credits
// is the authoritative cost signal.
type kiroStreamUsage struct {
	InputTokens  int
	OutputTokens int
	Credits      float64
}

func (e *KiroExecutor) parseEventStream(resp *http.Response, model string) ([]byte, []byte, []kiroeventstream.ToolUse, kiroStreamUsage, string, error) {
	var usageTotals kiroStreamUsage
	if resp == nil || resp.Body == nil {
		return nil, nil, nil, usageTotals, "", fmt.Errorf("kiro executor: response body is nil")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
	}()
	var content strings.Builder
	var reasoning strings.Builder
	var toolUses []kiroeventstream.ToolUse
	var stopReason string
	err := kiroeventstream.ParseEventStream(resp.Body, &kiroeventstream.StreamCallback{
		OnText: func(text string, isThinking bool) {
			if isThinking {
				reasoning.WriteString(text)
				return
			}
			content.WriteString(text)
		},
		OnToolUse: func(toolUse kiroeventstream.ToolUse) {
			toolUses = append(toolUses, toolUse)
		},
		OnComplete: func(inTokens, outTokens int) {
			usageTotals.InputTokens = inTokens
			usageTotals.OutputTokens = outTokens
		},
		OnCredits: func(credits float64) {
			usageTotals.Credits = credits
		},
		OnStopReason: func(reason string) {
			stopReason = reason
		},
	})
	if err != nil {
		return nil, nil, nil, kiroStreamUsage{}, "", err
	}
	return []byte(content.String()), []byte(reasoning.String()), toolUses, usageTotals, stopReason, nil
}

func openAIToolCallsFromKiro(toolUses []kiroeventstream.ToolUse) []map[string]any {
	calls := make([]map[string]any, 0, len(toolUses))
	for _, toolUse := range toolUses {
		arguments, err := json.Marshal(toolUse.Input)
		if err != nil {
			arguments = []byte("{}")
		}
		id := strings.TrimSpace(toolUse.ToolUseID)
		if id == "" {
			id = "call_" + uuid.New().String()
		}
		calls = append(calls, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      toolUse.Name,
				"arguments": string(arguments),
			},
		})
	}
	return calls
}

func responseFormatOrOpenAI(opts cliproxyexecutor.Options) sdktranslator.Format {
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	if format == "" {
		return sdktranslator.FormatOpenAI
	}
	return format
}

func mapOpenAIFinishReason(reason string, toolCount int) string {
	if toolCount > 0 {
		return "tool_calls"
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "max_output_tokens", "length", "model_context_window_exceeded", "context_window_exceeded":
		return "length"
	case "refusal", "content_filter", "content_filtered", "guardrail_intervened":
		return "content_filter"
	default:
		return "stop"
	}
}

func mapClaudeStopReason(reason string, toolCount int) string {
	if toolCount > 0 {
		return "tool_use"
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "max_output_tokens", "length":
		return "max_tokens"
	case "model_context_window_exceeded", "context_window_exceeded":
		return "model_context_window_exceeded"
	case "refusal", "content_filter", "content_filtered", "guardrail_intervened":
		return "refusal"
	case "stop_sequence":
		return "stop_sequence"
	default:
		return "end_turn"
	}
}

func openAIChatPayloadFromKiro(content, reasoning []byte, toolUses []kiroeventstream.ToolUse, inputTokens, outputTokens int, model, stopReason string) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": string(content),
	}
	if len(reasoning) > 0 {
		message["reasoning_content"] = string(reasoning)
	}
	if len(toolUses) > 0 {
		message["content"] = nil
		message["tool_calls"] = openAIToolCallsFromKiro(toolUses)
	}
	return map[string]any{
		"id":      "chatcmpl-" + uuid.New().String(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": mapOpenAIFinishReason(stopReason, len(toolUses)),
		}},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}
}

func claudeMessagePayloadFromKiro(content, reasoning []byte, toolUses []kiroeventstream.ToolUse, inputTokens, outputTokens int, model, stopReason string) map[string]any {
	blocks := make([]map[string]any, 0, 2+len(toolUses))
	if len(reasoning) > 0 {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": string(reasoning)})
	}
	if len(content) > 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": string(content)})
	}
	for _, toolUse := range toolUses {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": toolUse.ToolUseID, "name": toolUse.Name, "input": toolUse.Input})
	}
	return map[string]any{
		"id":            "msg_" + uuid.New().String(),
		"type":          "message",
		"role":          "assistant",
		"content":       blocks,
		"model":         model,
		"stop_reason":   mapClaudeStopReason(stopReason, len(toolUses)),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
}

// Execute implements ProviderExecutor interface.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	respUpstream, headers, _, _, errReq := e.executeRequest(ctx, auth, req, reporter)
	if errReq != nil {
		return resp, errReq
	}
	if respUpstream == nil {
		return resp, fmt.Errorf("kiro executor: missing upstream response")
	}
	content, reasoning, toolUses, usageTotals, stopReason, errParse := e.parseEventStream(respUpstream, req.Model)
	if errParse != nil {
		return resp, errParse
	}
	inputTokens, outputTokens := usageTotals.InputTokens, usageTotals.OutputTokens
	reporter.Publish(ctx, helps.ParseKiroUsage(inputTokens, outputTokens, usageTotals.Credits))
	responseFormat := responseFormatOrOpenAI(opts)
	var payloadOut map[string]any
	switch responseFormat {
	case sdktranslator.FormatClaude:
		payloadOut = claudeMessagePayloadFromKiro(content, reasoning, toolUses, inputTokens, outputTokens, req.Model, stopReason)
	default:
		payloadOut = openAIChatPayloadFromKiro(content, reasoning, toolUses, inputTokens, outputTokens, req.Model, stopReason)
	}
	if headers != nil {
		resp.Headers = headers
	}
	bodyOut, _ := json.Marshal(payloadOut)
	resp.Payload = bodyOut
	return resp, nil
}

func sendKiroStreamPayload(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, payload []byte) bool {
	select {
	case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
		return true
	case <-ctx.Done():
		return false
	}
}

func sendKiroStreamError(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, err error) {
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: err}:
	case <-ctx.Done():
	}
}

func sendKiroStreamJSON(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, v any) bool {
	body, err := json.Marshal(v)
	if err != nil {
		sendKiroStreamError(ctx, out, err)
		return false
	}
	return sendKiroStreamPayload(ctx, out, body)
}

func ssePayload(event string, data any) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, body)), nil
}

func sendClaudeSSE(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, event string, data any) bool {
	payload, err := ssePayload(event, data)
	if err != nil {
		sendKiroStreamError(ctx, out, err)
		return false
	}
	return sendKiroStreamPayload(ctx, out, payload)
}

func streamOpenAIFromKiro(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, model string, reporter *helps.UsageReporter) {
	messageID := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()
	finishReason := "stop"
	toolIndex := 0
	inputTokens := 0
	outputTokens := 0
	credits := 0.0
	errParse := kiroeventstream.ParseEventStream(body, &kiroeventstream.StreamCallback{
		OnText: func(text string, isThinking bool) {
			delta := map[string]any{}
			if isThinking {
				delta["reasoning_content"] = text
			} else {
				delta["content"] = text
			}
			sendKiroStreamJSON(ctx, out, map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
			})
		},
		OnToolUse: func(toolUse kiroeventstream.ToolUse) {
			finishReason = "tool_calls"
			toolCalls := openAIToolCallsFromKiro([]kiroeventstream.ToolUse{toolUse})
			if len(toolCalls) == 0 {
				return
			}
			toolCalls[0]["index"] = toolIndex
			toolIndex++
			sendKiroStreamJSON(ctx, out, map[string]any{
				"id":      messageID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"tool_calls": toolCalls}, "finish_reason": nil}},
			})
		},
		OnComplete: func(inTokens, outTokens int) {
			inputTokens = inTokens
			outputTokens = outTokens
		},
		OnCredits: func(value float64) {
			credits = value
		},
	})
	if errParse != nil {
		reporter.PublishFailure(ctx, errParse)
		sendKiroStreamError(ctx, out, errParse)
		return
	}
	sendKiroStreamJSON(ctx, out, map[string]any{
		"id":      messageID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason}},
	})
	reporter.Publish(ctx, helps.ParseKiroUsage(inputTokens, outputTokens, credits))
}

func streamClaudeFromKiro(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, model string, reporter *helps.UsageReporter) {
	messageID := "msg_" + uuid.New().String()
	if !sendClaudeSSE(ctx, out, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}) {
		return
	}
	blockIndex := 0
	activeTextIndex := -1
	activeThinkingIndex := -1
	toolCount := 0
	inputTokens := 0
	outputTokens := 0
	credits := 0.0
	upstreamStopReason := ""
	stopBlock := func(index int) {
		if index >= 0 {
			sendClaudeSSE(ctx, out, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		}
	}
	errParse := kiroeventstream.ParseEventStream(body, &kiroeventstream.StreamCallback{
		OnText: func(text string, isThinking bool) {
			if isThinking {
				if activeThinkingIndex < 0 {
					activeThinkingIndex = blockIndex
					blockIndex++
					sendClaudeSSE(ctx, out, "content_block_start", map[string]any{"type": "content_block_start", "index": activeThinkingIndex, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
				}
				sendClaudeSSE(ctx, out, "content_block_delta", map[string]any{"type": "content_block_delta", "index": activeThinkingIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": text}})
				return
			}
			if activeTextIndex < 0 {
				activeTextIndex = blockIndex
				blockIndex++
				sendClaudeSSE(ctx, out, "content_block_start", map[string]any{"type": "content_block_start", "index": activeTextIndex, "content_block": map[string]any{"type": "text", "text": ""}})
			}
			sendClaudeSSE(ctx, out, "content_block_delta", map[string]any{"type": "content_block_delta", "index": activeTextIndex, "delta": map[string]any{"type": "text_delta", "text": text}})
		},
		OnToolUse: func(toolUse kiroeventstream.ToolUse) {
			stopBlock(activeThinkingIndex)
			activeThinkingIndex = -1
			stopBlock(activeTextIndex)
			activeTextIndex = -1
			idx := blockIndex
			blockIndex++
			toolCount++
			sendClaudeSSE(ctx, out, "content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "tool_use", "id": toolUse.ToolUseID, "name": toolUse.Name, "input": map[string]any{}}})
			inputJSON, err := json.Marshal(toolUse.Input)
			if err != nil {
				inputJSON = []byte("{}")
			}
			sendClaudeSSE(ctx, out, "content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)}})
			sendClaudeSSE(ctx, out, "content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		},
		OnComplete: func(inTokens, outTokens int) {
			inputTokens = inTokens
			outputTokens = outTokens
		},
		OnCredits: func(value float64) {
			credits = value
		},
		OnStopReason: func(reason string) {
			upstreamStopReason = reason
		},
	})
	if errParse != nil {
		reporter.PublishFailure(ctx, errParse)
		sendKiroStreamError(ctx, out, errParse)
		return
	}
	stopBlock(activeThinkingIndex)
	stopBlock(activeTextIndex)
	sendClaudeSSE(ctx, out, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": mapClaudeStopReason(upstreamStopReason, toolCount), "stop_sequence": nil}, "usage": map[string]any{"output_tokens": outputTokens}})
	sendClaudeSSE(ctx, out, "message_stop", map[string]any{"type": "message_stop"})
	reporter.Publish(ctx, helps.ParseKiroUsage(inputTokens, outputTokens, credits))
}

// ExecuteStream implements ProviderExecutor interface.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (res *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	respUpstream, headers, _, _, errReq := e.executeRequest(ctx, auth, req, reporter)
	if errReq != nil {
		return nil, errReq
	}
	if respUpstream == nil {
		return nil, fmt.Errorf("kiro executor: missing upstream response")
	}
	if headers == nil {
		headers = make(http.Header)
	}
	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		// Guarantee the request is counted even when the upstream stream never
		// reports token usage.
		defer reporter.EnsurePublished(ctx)
		defer func() {
			if errClose := respUpstream.Body.Close(); errClose != nil {
				log.Errorf("kiro executor: close response body error: %v", errClose)
			}
		}()
		switch responseFormatOrOpenAI(opts) {
		case sdktranslator.FormatClaude:
			streamClaudeFromKiro(ctx, respUpstream.Body, out, req.Model, reporter)
		default:
			streamOpenAIFromKiro(ctx, respUpstream.Body, out, req.Model, reporter)
		}
	}()
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

// Refresh implements ProviderExecutor interface.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, nil
	}
	creds := kiroauth.CredentialsFromAuth(auth)
	if creds == nil {
		return auth.Clone(), nil
	}
	if strings.EqualFold(strings.TrimSpace(creds.AuthMethod), "api_key") {
		return auth.Clone(), nil
	}
	refreshed, err := e.authSvc.RefreshToken(ctx, creds)
	if err != nil {
		return nil, err
	}
	if refreshed == nil {
		return auth.Clone(), nil
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = creds.RefreshToken
	}
	if refreshed.ProfileArn == "" {
		refreshed.ProfileArn = creds.ProfileArn
	}
	if refreshed.ExpiresAt == 0 && creds.ExpiresAt > 0 {
		refreshed.ExpiresAt = creds.ExpiresAt
	}
	if refreshed.LastRefreshed == 0 {
		refreshed.LastRefreshed = time.Now().Unix()
	}
	updated := auth.Clone()
	kiroauth.ApplyCredentialsToAuth(updated, refreshed)
	kiroauth.SetRefreshFingerprints(updated, refreshed.RefreshToken, creds.RefreshToken)
	return updated, nil
}

// CountTokens implements ProviderExecutor interface.
func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	return resp, fmt.Errorf("CountTokens not supported for Kiro")
}

// HttpRequest implements ProviderExecutor interface.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	client := e.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(httpReq)
}
