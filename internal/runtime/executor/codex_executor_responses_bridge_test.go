package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorClaudeResponsesBridgeUsesOAuthToken(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotAccountID string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("Chatgpt-Account-Id")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token", "account_id": "oauth-account"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	opts := claudeResponsesBridgeOptions(requestBody, false)
	opts.Headers = http.Header{"Authorization": []string{"Bearer local-proxy-token"}, "X-Api-Key": []string{"local-proxy-key"}}
	response, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, opts)
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}

	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if gotAccountID != "oauth-account" {
		t.Fatalf("Chatgpt-Account-Id = %q, want OAuth account", gotAccountID)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("upstream model = %q, want gpt-5.6-sol; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.content.0.text").String(); got != "hello" {
		t.Fatalf("upstream input text = %q, want hello; body=%s", got, gotBody)
	}
	if gjson.GetBytes(gotBody, "context_management").Exists() {
		t.Fatalf("normal bridge injected context_management: %s", gotBody)
	}
	if got := gjson.GetBytes(response.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("translated response text = %q, want hello; response=%s", got, response.Payload)
	}
}

func TestCodexExecutorClaudeResponsesBridgeStreamUsesOAuthToken(t *testing.T) {
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range claudeBridgeCodexEvents(t) {
			chunk := append([]byte("data: "), event...)
			chunk = append(chunk, '\n', '\n')
			_, _ = w.Write(chunk)
		}
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	opts := claudeResponsesBridgeOptions(requestBody, true)
	opts.Headers = http.Header{"X-Api-Key": []string{"local-proxy-key"}, "Anthropic-Beta": []string{"thinking-token-count-2026-05-13"}}
	stream, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	var output strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if gjson.GetBytes(gotBody, "context_management").Exists() {
		t.Fatalf("normal stream bridge injected context_management: %s", gotBody)
	}
	assertClaudeBridgeUsageStream(t, output.String())
}

func TestCodexExecutorExecuteStreamCancellationClosesIdleStream(t *testing.T) {
	tests := []struct {
		name string
		alt  string
	}{
		{name: "Claude bridge usage ticks", alt: constant.ClaudeResponsesBridgeAlt},
		{name: "nil usage ticks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer upstream.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Attributes: map[string]string{"base_url": upstream.URL},
				Metadata:   map[string]any{"access_token": "oauth-token"},
			}
			requestBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
			ctx, cancel := context.WithCancel(context.Background())
			opts := claudeResponsesBridgeOptions(requestBody, true)
			opts.Alt = tt.alt
			stream, errExecute := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gpt-5.6-sol",
				Payload: requestBody,
			}, opts)
			if errExecute != nil {
				cancel()
				t.Fatalf("ExecuteStream error: %v", errExecute)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("timed out waiting for idle upstream stream")
			}

			cancel()
			closed := make(chan struct{})
			go func() {
				for range stream.Chunks {
				}
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("stream chunks did not close after context cancellation")
			}
		})
	}
}

func TestCodexAutoExecutorClaudeResponsesBridgeUsesHTTPWithoutWebsocketAuth(t *testing.T) {
	var gotMethod string
	var gotUpgrade string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotUpgrade = r.Header.Get("Upgrade")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	executor := NewCodexAutoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	stream, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, claudeResponsesBridgeOptions(requestBody, true))
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotUpgrade != "" {
		t.Fatalf("Upgrade = %q, want normal HTTP request", gotUpgrade)
	}
}

func TestCodexExecutorCountTokensReturnsExactInputCount(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	requestBody := []byte(`{"model":"gpt-5.6-sol","max_tokens":128,"messages":[{"role":"user","content":"count these exact tokens"}]}`)
	translatedBody := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FromString("codex"), "gpt-5.6-sol", requestBody, false)
	enc, errTokenizer := tokenizerForCodexModel("gpt-5.6-sol")
	if errTokenizer != nil {
		t.Fatalf("tokenizer: %v", errTokenizer)
	}
	want, errCount := countCodexInputTokens(enc, translatedBody)
	if errCount != nil {
		t.Fatalf("count exact tokens: %v", errCount)
	}

	response, errPublic := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errPublic != nil {
		t.Fatalf("CountTokens error: %v", errPublic)
	}
	if got := gjson.GetBytes(response.Payload, "input_tokens").Int(); got != want {
		t.Fatalf("input_tokens = %d, want exact %d; response=%s", got, want, response.Payload)
	}
}

func TestValidateClaudeBridgeContextWindowRejectsOversizedInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(mustJSONMarshalExecutorTest(t, strings.Repeat("x ", 200_500))) + `}]}]}`)
	count, errContext := validateClaudeBridgeContextWindow("gpt-5.6-luna", body, cliproxyexecutor.Options{Alt: constant.ClaudeResponsesBridgeAlt})
	if errContext == nil {
		t.Fatalf("context count = %d, want context_too_large error", count)
	}
	if count <= claudeBridgeContextWindow {
		t.Fatalf("context count = %d, want > %d", count, claudeBridgeContextWindow)
	}
	statusCoder, ok := errContext.(interface{ StatusCode() int })
	if !ok || statusCoder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("context error = %T %v, want status 400", errContext, errContext)
	}
	if got := gjson.Get(errContext.Error(), "error.code").String(); got != "context_too_large" {
		t.Fatalf("error code = %q, want context_too_large; error=%v", got, errContext)
	}
	if message := gjson.Get(errContext.Error(), "error.message").String(); !strings.Contains(message, "prompt is too long") {
		t.Fatalf("error message = %q, want prompt is too long", message)
	}
}

func TestValidateClaudeBridgeContextWindowAllowsCompactedReplayAboveSyntheticLimit(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"compaction","encrypted_content":"opaque-state"},{"type":"message","role":"user","content":[{"type":"input_text","text":` + string(mustJSONMarshalExecutorTest(t, strings.Repeat("x ", 200_500))) + `}]}]}`)
	count, errContext := validateClaudeBridgeContextWindow("gpt-5.6-luna", body, cliproxyexecutor.Options{Alt: constant.ClaudeResponsesBridgeAlt})
	if errContext != nil {
		t.Fatalf("compacted replay rejected at synthetic limit: %v", errContext)
	}
	if count <= claudeBridgeContextWindow {
		t.Fatalf("context count = %d, want > %d to exercise compacted replay exception", count, claudeBridgeContextWindow)
	}
}

func TestClaudeThinkingTokenCountRequested(t *testing.T) {
	if !claudeThinkingTokenCountRequested(http.Header{"Anthropic-Beta": []string{"thinking-token-count-2026-05-13"}}) {
		t.Fatal("explicit thinking-token-count beta was not detected")
	}
	if !claudeThinkingTokenCountRequested(http.Header{
		"X-App":                    []string{"cli"},
		"X-Claude-Code-Session-Id": []string{"session-1"},
	}) {
		t.Fatal("Claude workflow session was not enabled for thinking token progress")
	}
	if claudeThinkingTokenCountRequested(http.Header{"X-App": []string{"cli"}}) {
		t.Fatal("generic CLI request without a Claude session ID enabled thinking token progress")
	}
}

func claudeResponsesBridgeOptions(requestBody []byte, stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: requestBody,
		Stream:          stream,
	}
}

const claudeThinkingTokenQuantumForTest = int64(64)

func claudeSSEDataEvents(stream string) []gjson.Result {
	var events []gjson.Result
	for _, block := range strings.Split(stream, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				events = append(events, gjson.Parse(strings.TrimSpace(data)))
				break
			}
		}
	}
	return events
}

func mustJSONMarshalExecutorTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal test value: %v", errMarshal)
	}
	return encoded
}

func claudeBridgeCodexEvents(t *testing.T) [][]byte {
	t.Helper()
	reasoningCipher := base64.URLEncoding.EncodeToString(make([]byte, 1801))
	longOutput := strings.Repeat("visible output ", 100)
	return [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_bridge","model":"gpt-5.6-sol"}}`),
		[]byte(`{"type":"response.reasoning_summary_part.added"}`),
		[]byte(`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":` + string(mustJSONMarshalExecutorTest(t, reasoningCipher)) + `}}`),
		[]byte(`{"type":"response.output_text.delta","delta":` + string(mustJSONMarshalExecutorTest(t, longOutput)) + `}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_bridge","status":"completed","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":80000,"input_tokens_details":{"cache_write_tokens":1000,"cached_tokens":60000},"output_tokens":500,"output_tokens_details":{"reasoning_tokens":250},"total_tokens":80500}}}`),
	}
}

func assertClaudeBridgeUsageStream(t *testing.T, output string) {
	t.Helper()
	if !strings.Contains(output, "event: message_start") || !strings.Contains(output, "visible output") {
		t.Fatalf("unexpected Claude stream: %s", output)
	}
	var inputTokens int64
	var usage []gjson.Result
	var thinkingIncrements []int64
	for _, event := range claudeSSEDataEvents(output) {
		switch event.Get("type").String() {
		case "message_start":
			inputTokens = event.Get("message.usage.input_tokens").Int()
		case "message_delta":
			if event.Get("usage").Exists() {
				usage = append(usage, event.Get("usage"))
			}
		case "content_block_delta":
			if event.Get("delta.type").String() == "thinking_delta" {
				if estimated := event.Get("delta.estimated_tokens").Int(); estimated > 0 {
					thinkingIncrements = append(thinkingIncrements, estimated)
				}
			}
		}
	}
	if inputTokens <= 0 {
		t.Fatalf("message_start input tokens = %d, want positive estimate; stream=%s", inputTokens, output)
	}
	if len(usage) < 2 {
		t.Fatalf("message_delta usage events = %d, want live output and terminal; stream=%s", len(usage), output)
	}
	live := usage[len(usage)-2]
	if outputTokens := live.Get("output_tokens").Int(); outputTokens <= 0 || outputTokens >= 500 || live.Get("output_tokens_details.thinking_tokens").Int() <= 0 || live.Get("input_tokens").Int() != 0 {
		t.Fatalf("live output usage = %s, want output progress with nullable input", live.Raw)
	}
	terminal := usage[len(usage)-1]
	if terminal.Get("input_tokens").Int() != 80000 || terminal.Get("cache_creation_input_tokens").Int() != 0 || terminal.Get("cache_read_input_tokens").Int() != 0 || terminal.Get("output_tokens").Int() != 500 || terminal.Get("output_tokens_details.thinking_tokens").Int() != 250 {
		t.Fatalf("terminal usage = %s, want full Codex input without false Claude cache attribution", terminal.Raw)
	}
	if len(thinkingIncrements) == 0 || thinkingIncrements[0] <= 0 || thinkingIncrements[0]%claudeThinkingTokenQuantumForTest != 0 {
		t.Fatalf("thinking token increments = %v, want positive quantized beta progress; stream=%s", thinkingIncrements, output)
	}
}

func TestCodexExecutorClaudeResponsesCompactBridgeUsesOAuthToken(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"compaction","encrypted_content":"encrypted"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`))
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`)
	response, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesCompactBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: requestBody,
		Headers:         http.Header{"Authorization": []string{"Bearer local-proxy-token"}},
	})
	if errExecute != nil {
		t.Fatalf("Execute compact error: %v", errExecute)
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", gotPath)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("compact upstream model = %q; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.content.0.text").String(); !strings.Contains(got, "detailed summary") {
		t.Fatalf("compact upstream input = %q; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(response.Payload, "object").String(); got != "response.compaction" {
		t.Fatalf("compact response object = %q; response=%s", got, response.Payload)
	}
}

func TestApplyClaudeResponsesCompactionReplayPrependsOpaqueItems(t *testing.T) {
	source := []byte(`{"cpa_responses_compaction":{"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"compaction","encrypted_content":"encrypted"}]}}`)
	translated := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"new"}]}]}`)
	got := applyClaudeResponsesCompactionReplay(translated, source, cliproxyexecutor.Options{Alt: constant.ClaudeResponsesBridgeAlt})
	if itemType := gjson.GetBytes(got, "input.1.type").String(); itemType != "compaction" {
		t.Fatalf("input.1.type = %q, want compaction; body=%s", itemType, got)
	}
	if text := gjson.GetBytes(got, "input.2.content.0.text").String(); text != "new" {
		t.Fatalf("new input text = %q, want new; body=%s", text, got)
	}
	if gjson.GetBytes(got, constant.ClaudeResponsesCompactionField).Exists() {
		t.Fatalf("internal compaction field leaked upstream: %s", got)
	}
}
