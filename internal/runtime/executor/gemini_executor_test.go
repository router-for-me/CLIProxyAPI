package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCapGeminiMaxOutputTokensUsesOutputTokenLimit(t *testing.T) {
	body := []byte(`{"generationConfig":{"maxOutputTokens":500000,"temperature":0.2},"contents":[]}`)

	out := capGeminiMaxOutputTokens(body, "gemini-3.1-pro-preview")

	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 65536 {
		t.Fatalf("maxOutputTokens = %d, want 65536", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", got)
	}
}

func TestCapGeminiMaxOutputTokensLeavesAllowedOrUnknown(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  []byte
		want  int64
	}{
		{
			name:  "allowed value",
			model: "gemini-3.1-pro-preview",
			body:  []byte(`{"generationConfig":{"maxOutputTokens":64000}}`),
			want:  64000,
		},
		{
			name:  "unknown model",
			model: "custom-gemini-model",
			body:  []byte(`{"generationConfig":{"maxOutputTokens":500000}}`),
			want:  500000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := capGeminiMaxOutputTokens(tt.body, tt.model)
			if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != tt.want {
				t.Fatalf("maxOutputTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGeminiExecutorExecuteCapsMaxOutputTokensBeforeUpstream(t *testing.T) {
	var upstreamMaxOutputTokens int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		upstreamMaxOutputTokens = gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":500000}}`),
	}

	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamMaxOutputTokens != 65536 {
		t.Fatalf("upstream maxOutputTokens = %d, want 65536", upstreamMaxOutputTokens)
	}
}

func TestGeminiExecutorExecutePrependsLeadingUser(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	request := cliproxyexecutor.Request{
		Model: "gemini-3.7-flash",
		Payload: []byte(`{"contents":[` +
			`{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"key":"value"}}}]},` +
			`{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"ok"}}}]}` +
			`]}`),
	}

	if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	if len(contents) != 3 || contents[0].Get("role").String() != "user" || contents[1].Get("role").String() != "model" || contents[2].Get("role").String() != "user" {
		t.Fatalf("upstream roles malformed: %s", upstreamBody)
	}
	if got := contents[0].Get("parts.0.text").String(); got != "" {
		t.Fatalf("leading user prompt = %q, want empty string; body=%s", got, upstreamBody)
	}
}

func TestGeminiExecutorExecutePrependsLeadingUserForIssue4959ResponsesHistory(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	if _, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: issue4959ResponsesModelFirstPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertIssue4959LeadingUserContents(t, gjson.GetBytes(upstreamBody, "contents").Array())
}

func TestGeminiExecutorExecuteAppendsTrailingUserForTrailingModelTurn(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	request := cliproxyexecutor.Request{
		Model: "gemini-3.7-flash",
		Payload: []byte(`{"contents":[` +
			`{"role":"user","parts":[{"text":"hello"}]},` +
			`{"role":"model","parts":[{"text":"answer"}]}` +
			`]}`),
	}

	if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	if len(contents) != 3 || contents[0].Get("role").String() != "user" || contents[1].Get("role").String() != "model" || contents[2].Get("role").String() != "user" {
		t.Fatalf("upstream roles malformed: %s", upstreamBody)
	}
	if got := contents[2].Get("parts.0.text").String(); got != "" {
		t.Fatalf("trailing user prompt = %q, want empty string; body=%s", got, upstreamBody)
	}
}

func TestGeminiExecutorCountTokensPrependsLeadingUser(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":7}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	request := cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"contents":[{"role":"model","parts":[{"text":"prior output"}]}]}`),
	}

	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	if _, errCount := executor.CountTokens(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if !cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatal("CountTokens() did not mark the HTTP request as an upstream attempt")
	}
	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	if len(contents) != 2 || contents[0].Get("role").String() != "user" || contents[1].Get("role").String() != "model" {
		t.Fatalf("countTokens roles malformed: %s", upstreamBody)
	}
	if text := contents[0].Get("parts.0.text"); !text.Exists() || text.String() != "" {
		t.Fatalf("countTokens synthetic user missing: %s", upstreamBody)
	}
	if got := contents[1].Get("parts.0.text").String(); got != "prior output" {
		t.Fatalf("countTokens model text = %q, want prior output; body=%s", got, upstreamBody)
	}

	request.Metadata = map[string]any{"action": "countTokens"}
	if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errExecute != nil {
		t.Fatalf("Execute(countTokens) error = %v", errExecute)
	}
	contents = gjson.GetBytes(upstreamBody, "contents").Array()
	if len(contents) != 2 || contents[0].Get("role").String() != "user" || contents[1].Get("role").String() != "model" {
		t.Fatalf("Execute(countTokens) roles malformed: %s", upstreamBody)
	}
}

func TestGeminiExecutorAppliesPayloadRulesAfterLeadingUserNormalization(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gemini-3.7-flash", Protocol: "gemini"}},
		Params: map[string]any{"contents.0.parts.0.text": "payload override"},
	}}}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	request := cliproxyexecutor.Request{
		Model: "gemini-3.7-flash",
		Payload: []byte(`{"contents":[` +
			`{"role":"model","parts":[{"text":"prior output"}]},` +
			`{"role":"user","parts":[{"text":"continue"}]}` +
			`]}`),
	}

	if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	if len(contents) != 3 || contents[0].Get("role").String() != "user" || contents[1].Get("role").String() != "model" {
		t.Fatalf("upstream roles malformed: %s", upstreamBody)
	}
	if text := contents[0].Get("parts.0.text"); !text.Exists() || text.String() != "payload override" {
		t.Fatalf("payload override did not target final leading user: %s", upstreamBody)
	}
	if got := contents[1].Get("parts.0.text").String(); got != "prior output" {
		t.Fatalf("payload rule applied to %q, want unmodified original first model turn; body=%s", got, upstreamBody)
	}
}

func TestGeminiExecutorInteractionsWithGeminiAPIKeyUsesGeminiEndpoint(t *testing.T) {
	var gotPath string
	var gotRevision string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRevision = r.Header.Get("Api-Revision")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash",
		Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi"}`),
	}

	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotPath != "/v1beta/models/gemini-3.5-flash:generateContent" {
		t.Fatalf("path = %q, want Gemini generateContent endpoint", gotPath)
	}
	if gotRevision != "" {
		t.Fatalf("Api-Revision = %q, want empty for Gemini protocol request", gotRevision)
	}
	if !gjson.GetBytes(upstreamBody, "contents.0.parts.0.text").Exists() {
		t.Fatalf("contents text missing from translated Gemini body: %s", string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "input").Exists() {
		t.Fatalf("raw interactions input exists in translated Gemini body: %s", string(upstreamBody))
	}
}

func TestGeminiExecutorNativeInteractionsUsesInteractionsEndpoint(t *testing.T) {
	var gotPath string
	var gotRevision string
	var gotModelExists bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRevision = r.Header.Get("Api-Revision")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotModelExists = gjson.GetBytes(body, "model").Exists()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "agents/test-agent",
		Payload: []byte(`{"agent":"agents/test-agent","input":"hi"}`),
	}

	resp, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if gotRevision != "2026-05-20" {
		t.Fatalf("Api-Revision = %q, want 2026-05-20", gotRevision)
	}
	if gotModelExists {
		t.Fatal("model field exists for agent-only request, want absent")
	}
	if got := gjson.GetBytes(resp.Payload, "id").String(); got != "interaction_1" {
		t.Fatalf("response id = %q, want interaction_1", got)
	}
}

func TestGeminiExecutorNativeInteractionsTranslatesOpenAIResponsesRequest(t *testing.T) {
	var gotPath string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"model":"gemini-3.1-flash-lite",
			"instructions":"be brief",
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
			"reasoning":{"effort":"high","summary":"auto"}
		}`),
	}

	resp, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if got := gjson.GetBytes(upstreamBody, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_level").String(); got != "high" {
		t.Fatalf("thinking_level = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "ok" {
		t.Fatalf("response text = %q, want ok. Payload: %s", got, string(resp.Payload))
	}
}

func TestGeminiExecutorNativeInteractionsPayloadRulesUseResponsesFromProtocol(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}]}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-3.1-flash-lite", Protocol: "interactions", FromProtocol: "openai"},
					},
					Params: map[string]any{
						"generation_config.thinking_summaries": "wrong",
					},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-3.1-flash-lite", Protocol: "interactions", FromProtocol: "responses"},
					},
					Params: map[string]any{
						"generation_config.thinking_summaries": "detailed",
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"model":"gemini-3.1-flash-lite",
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_summaries").String(); got != "detailed" {
		t.Fatalf("thinking_summaries = %q, want detailed. Body: %s", got, string(upstreamBody))
	}
}

func TestGeminiExecutorNativeInteractionsTranslatesOpenAIChatRequest(t *testing.T) {
	var gotPath string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\",\"model\":\"gemini-3.1-flash-lite\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":{}}}\n\n"))
		_, _ = w.Write([]byte("event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"location\\\":\\\"北京\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"requires_action\",\"usage\":{\"total_input_tokens\":2,\"total_output_tokens\":3,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"model":"gemini-3.1-flash-lite",
			"stream":true,
			"messages":[{"role":"user","content":"今天北京的天气怎么样？"}],
			"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}],
			"tool_choice":"auto"
		}`),
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var toolStart []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if gjson.GetBytes(chunk.Payload, "choices.0.delta.tool_calls.0.function.name").String() == "get_weather" {
			toolStart = chunk.Payload
		}
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if got := gjson.GetBytes(upstreamBody, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("translated request text = %q. Body: %s", got, string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "messages").Exists() {
		t.Fatalf("raw OpenAI messages should not be sent upstream: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.type").String(); got != "function" {
		t.Fatalf("translated tool type = %q, want function. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.tool_choice").String(); got != "auto" {
		t.Fatalf("translated tool choice = %q, want auto. Body: %s", got, string(upstreamBody))
	}
	if toolStart == nil {
		t.Fatal("OpenAI tool call chunk not found")
	}
	if got := gjson.GetBytes(toolStart, "choices.0.delta.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("tool call id = %q, want call_1. Payload: %s", got, string(toolStart))
	}
}

func TestGeminiExecutorNativeInteractionsPayloadDefaultsUseTranslatedOpenAIChatSource(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}]}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-3.1-flash-lite", Protocol: "interactions", FromProtocol: "openai"},
					},
					Params: map[string]any{
						"generation_config.temperature": 0.9,
						"generation_config.top_p":       0.8,
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"model":"gemini-3.1-flash-lite",
			"messages":[{"role":"user","content":"hi"}],
			"temperature":0.2
		}`),
	}

	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want default 0.8. Body: %s", got, string(upstreamBody))
	}
}

func TestGeminiExecutorNativeInteractionsTranslatesGeminiStreamResponse(t *testing.T) {
	var gotPath string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\",\"model\":\"gemini-3.1-flash-lite\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call_1\",\"signature\":\"sig_1\",\"name\":\"get_weather\",\"arguments\":{}}}\n\n"))
		_, _ = w.Write([]byte("event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"location\\\":\\\"北京\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"requires_action\",\"usage\":{\"total_input_tokens\":2,\"total_output_tokens\":3,\"total_tokens\":5,\"total_cached_tokens\":1},\"service_tier\":\"standard\",\"model\":\"gemini-3.1-flash-lite\"}}\n\n"))
		_, _ = w.Write([]byte("event: done\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"contents":[{"role":"user","parts":[{"text":"今天北京的天气怎么样？"}]}],
			"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"OBJECT","properties":{"location":{"type":"STRING"}},"required":["location"]}}]}]
		}`),
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var callChunk []byte
	var finishChunk []byte
	chunkCount := 0
	for chunk := range result.Chunks {
		chunkCount++
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if gjson.GetBytes(chunk.Payload, "event_type").Exists() {
			t.Fatalf("interactions payload leaked to Gemini response: %s", string(chunk.Payload))
		}
		if gjson.GetBytes(chunk.Payload, "candidates.0.content.parts.0.functionCall").Exists() {
			callChunk = chunk.Payload
		}
		if gjson.GetBytes(chunk.Payload, "candidates.0.finishReason").Exists() {
			finishChunk = chunk.Payload
		}
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if gjson.GetBytes(upstreamBody, "contents").Exists() {
		t.Fatalf("raw Gemini contents should not be sent upstream: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("translated request text = %q. Body: %s", got, string(upstreamBody))
	}
	if chunkCount != 2 {
		t.Fatalf("stream chunk count = %d, want 2", chunkCount)
	}
	if callChunk == nil {
		t.Fatal("Gemini functionCall chunk not found")
	}
	if got := gjson.GetBytes(callChunk, "candidates.0.content.parts.0.functionCall.name").String(); got != "get_weather" {
		t.Fatalf("functionCall.name = %q, want get_weather. Payload: %s", got, string(callChunk))
	}
	if got := gjson.GetBytes(callChunk, "candidates.0.content.parts.0.functionCall.args.location").String(); got != "北京" {
		t.Fatalf("functionCall.args.location = %q, want 北京. Payload: %s", got, string(callChunk))
	}
	if got := gjson.GetBytes(callChunk, "candidates.0.content.parts.0.thoughtSignature").String(); got != "sig_1" {
		t.Fatalf("thoughtSignature = %q, want sig_1. Payload: %s", got, string(callChunk))
	}
	if finishChunk == nil {
		t.Fatal("Gemini finish chunk not found")
	}
	if got := gjson.GetBytes(finishChunk, "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP. Payload: %s", got, string(finishChunk))
	}
	if got := gjson.GetBytes(finishChunk, "usageMetadata.promptTokenCount").Int(); got != 2 {
		t.Fatalf("promptTokenCount = %d, want 2. Payload: %s", got, string(finishChunk))
	}
	if got := gjson.GetBytes(finishChunk, "usageMetadata.candidatesTokenCount").Int(); got != 3 {
		t.Fatalf("candidatesTokenCount = %d, want 3. Payload: %s", got, string(finishChunk))
	}
	if got := gjson.GetBytes(finishChunk, "usageMetadata.totalTokenCount").Int(); got != 5 {
		t.Fatalf("totalTokenCount = %d, want 5. Payload: %s", got, string(finishChunk))
	}
}

func TestNativeInteractionsSourceFormatAllowsSupportedEntryProtocols(t *testing.T) {
	supported := []sdktranslator.Format{
		sdktranslator.FormatInteractions,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		sdktranslator.FormatGemini,
	}
	for _, format := range supported {
		if !nativeInteractionsSourceFormat(format) {
			t.Fatalf("nativeInteractionsSourceFormat(%q) = false, want true", format)
		}
	}
	for _, format := range []sdktranslator.Format{sdktranslator.FormatCodex, sdktranslator.FormatAntigravity} {
		if nativeInteractionsSourceFormat(format) {
			t.Fatalf("nativeInteractionsSourceFormat(%q) = true, want false", format)
		}
	}
}

func TestGeminiExecutorNativeInteractionsTranslatesClaudeRequest(t *testing.T) {
	var gotPath string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","model":"gemini-3.1-flash-lite","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}],"usage":{"total_input_tokens":1,"total_output_tokens":1}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.1-flash-lite",
		Payload: []byte(`{
			"model":"gemini-3.1-flash-lite",
			"max_tokens":1024,
			"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}}}}],
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}`),
	}

	resp, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if got := gjson.GetBytes(upstreamBody, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("translated request text = %q, want hi. Body: %s", got, string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "messages").Exists() {
		t.Fatalf("raw Claude messages should not be sent upstream: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.type").String(); got != "function" {
		t.Fatalf("translated tool type = %q, want function. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "ok" {
		t.Fatalf("response text = %q, want ok. Payload: %s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "usage.output_tokens").Int(); got != 1 {
		t.Fatalf("response output tokens = %d, want 1. Payload: %s", got, string(resp.Payload))
	}
}

func TestGeminiExecutorNativeInteractionsStripsClaudeToolIDsBeforeUpstream(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","model":"gemini-3.6-flash","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}],"usage":{"total_input_tokens":1,"total_output_tokens":1}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.6-flash",
		Payload: []byte(`{
			"model":"gemini-3.6-flash",
			"messages":[
				{"role":"user","content":[{"type":"text","text":"weather?","id":"txt_user"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"北京"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"晴"}]}
			]
		}`),
	}

	if _, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	inputs := gjson.GetBytes(upstreamBody, "input").Array()
	if len(inputs) == 0 {
		t.Fatalf("upstream input missing: %s", upstreamBody)
	}
	foundCall := false
	foundResult := false
	for i, item := range inputs {
		for j, part := range item.Get("content").Array() {
			if part.Get("id").Exists() {
				t.Fatalf("input[%d].content[%d].id = %q, want omitted. Body: %s", i, j, part.Get("id").String(), upstreamBody)
			}
		}
		switch item.Get("type").String() {
		case "function_call":
			foundCall = true
			if got := item.Get("id").String(); got != "toolu_1" {
				t.Fatalf("function_call id = %q, want toolu_1. Body: %s", got, upstreamBody)
			}
			if item.Get("call_id").Exists() {
				t.Fatalf("function_call call_id must be omitted. Body: %s", upstreamBody)
			}
		case "function_result":
			foundResult = true
			if got := item.Get("call_id").String(); got != "toolu_1" {
				t.Fatalf("function_result call_id = %q, want toolu_1. Body: %s", got, upstreamBody)
			}
			if item.Get("id").Exists() {
				t.Fatalf("function_result id must be omitted. Body: %s", upstreamBody)
			}
		default:
			if item.Get("id").Exists() {
				t.Fatalf("input[%d].id = %q, want omitted. Body: %s", i, item.Get("id").String(), upstreamBody)
			}
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("missing function_call/function_result in upstream input: %s", upstreamBody)
	}
}

func TestGeminiExecutorNativeInteractionsStripsPassthroughInputIDsBeforeUpstream(t *testing.T) {
	payload := []byte(`{
		"model":"gemini-3.6-flash",
		"input":[
			{"type":"user_input","id":"msg_1","content":[{"type":"text","id":"txt_1","text":"hi"}]},
			{"type":"function_call","id":"toolu_1","call_id":"toolu_1","name":"lookup","arguments":{"q":"x"}},
			{"type":"function_result","id":"toolu_1","call_id":"toolu_1","result":"ok"}
		]
	}`)
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var upstreamBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Fatalf("read request body: %v", errRead)
				}
				upstreamBody = append([]byte(nil), body...)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"completed\"}}\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","model":"gemini-3.6-flash","steps":[],"usage":{"total_input_tokens":1,"total_output_tokens":1}}`))
			}))
			defer server.Close()

			exec := NewGeminiInteractionsExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "gemini-interactions",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			req := cliproxyexecutor.Request{Model: "gemini-3.6-flash", Payload: payload}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatInteractions,
				ResponseFormat: sdktranslator.FormatInteractions,
			}
			if stream {
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}

			inputs := gjson.GetBytes(upstreamBody, "input").Array()
			if len(inputs) != 3 {
				t.Fatalf("upstream input count = %d, want 3. Body: %s", len(inputs), upstreamBody)
			}
			for i, item := range inputs {
				if item.Get("type").String() != "function_call" && item.Get("id").Exists() {
					t.Fatalf("input[%d].id = %q, want omitted. Body: %s", i, item.Get("id").String(), upstreamBody)
				}
				for j, part := range item.Get("content").Array() {
					if part.Get("id").Exists() {
						t.Fatalf("input[%d].content[%d].id = %q, want omitted. Body: %s", i, j, part.Get("id").String(), upstreamBody)
					}
				}
			}
			if got := gjson.GetBytes(upstreamBody, "input.0.content.0.text").String(); got != "hi" {
				t.Fatalf("user text = %q, want hi. Body: %s", got, upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "input.1.id").String(); got != "toolu_1" {
				t.Fatalf("function_call id = %q, want toolu_1. Body: %s", got, upstreamBody)
			}
			if gjson.GetBytes(upstreamBody, "input.1.call_id").Exists() {
				t.Fatalf("function_call call_id must be omitted. Body: %s", upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "input.2.call_id").String(); got != "toolu_1" {
				t.Fatalf("function_result call_id = %q, want toolu_1. Body: %s", got, upstreamBody)
			}
			if gjson.GetBytes(upstreamBody, "input.2.id").Exists() {
				t.Fatalf("function_result id must be omitted. Body: %s", upstreamBody)
			}
		})
	}
}

func TestGeminiExecutorNativeInteractionsAppliesThinkingSuffix(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite(high)",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite(high)","generation_config":{"max_output_tokens":32},"input":"hi"}`),
	}
	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Body: %s", got, string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "generationConfig").Exists() {
		t.Fatalf("generationConfig exists, want Interactions snake_case only. Body: %s", string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "generation_config.thinking_config").Exists() {
		t.Fatalf("thinking_config exists, want native Interactions fields. Body: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_level").String(); got != "high" {
		t.Fatalf("thinking_level = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "generation_config.thinking_summaries").Exists() {
		t.Fatalf("thinking_summaries should be absent without explicit summary intent. Body: %s", string(upstreamBody))
	}
}

func TestGeminiExecutorNativeInteractionsPreservesThinkingProtocolFields(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite","generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"input":"hi"}`),
	}
	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gjson.GetBytes(upstreamBody, "generationConfig").Exists() {
		t.Fatalf("generationConfig exists, want Interactions snake_case only. Body: %s", string(upstreamBody))
	}
	if gjson.GetBytes(upstreamBody, "generation_config.thinking_config").Exists() {
		t.Fatalf("thinking_config exists, want native Interactions fields. Body: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_level").String(); got != "high" {
		t.Fatalf("thinking_level = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_summaries").String(); got != "auto" {
		t.Fatalf("thinking_summaries = %q, want auto. Body: %s", got, string(upstreamBody))
	}
}

func TestGeminiExecutorNativeInteractionsPreservesApiRevision(t *testing.T) {
	var gotRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRevision = r.Header.Get("Api-Revision")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	auth.Attributes["header:Api-Revision"] = "2026-06-01"
	req := cliproxyexecutor.Request{
		Model:   "agents/test-agent",
		Payload: []byte(`{"agent":"agents/test-agent","input":"hi"}`),
	}
	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotRevision != "2026-06-01" {
		t.Fatalf("Api-Revision = %q, want 2026-06-01", gotRevision)
	}
}

func TestGeminiExecutorNativeInteractionsUsesRequestApiRevision(t *testing.T) {
	var gotRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRevision = r.Header.Get("Api-Revision")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "agents/test-agent",
		Payload: []byte(`{"agent":"agents/test-agent","input":"hi"}`),
	}
	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Headers:        http.Header{"Api-Revision": []string{"2026-06-01"}},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotRevision != "2026-06-01" {
		t.Fatalf("Api-Revision = %q, want 2026-06-01", gotRevision)
	}
}

func TestGeminiExecutorNativeInteractionsRequestApiRevisionDoesNotOverrideAuthHeader(t *testing.T) {
	var gotRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRevision = r.Header.Get("Api-Revision")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","steps":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":             "test-key",
		"base_url":            server.URL,
		"header:Api-Revision": "2026-06-01",
	}, Provider: "gemini-interactions"}
	req := cliproxyexecutor.Request{
		Model:   "agents/test-agent",
		Payload: []byte(`{"agent":"agents/test-agent","input":"hi"}`),
	}
	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Headers:        http.Header{"Api-Revision": []string{"2026-07-01"}},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotRevision != "2026-06-01" {
		t.Fatalf("Api-Revision = %q, want 2026-06-01", gotRevision)
	}
}

func TestGeminiExecutorNativeInteractionsStreamParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\n"))
		_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":2,\"total_output_tokens\":3,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash",
		Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi","stream":true}`),
	}
	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	count := 0
	var completed []byte
	for chunk := range result.Chunks {
		count++
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if !bytes.Contains(chunk.Payload, []byte("event:")) || !bytes.Contains(chunk.Payload, []byte("data:")) {
			t.Fatalf("chunk = %q, want complete SSE frame", string(chunk.Payload))
		}
		payload := geminiInteractionsSSEPayload(chunk.Payload)
		if gjson.GetBytes(payload, "event_type").String() == "interaction.completed" {
			completed = payload
		}
	}
	if count == 0 {
		t.Fatal("no stream chunks received")
	}
	if completed == nil {
		t.Fatal("interaction.completed chunk not found")
	}
	if got := gjson.GetBytes(completed, "interaction.usage.total_input_tokens").Int(); got != 2 {
		t.Fatalf("total_input_tokens = %d, want 2", got)
	}
	if got := gjson.GetBytes(completed, "interaction.usage.total_output_tokens").Int(); got != 3 {
		t.Fatalf("total_output_tokens = %d, want 3", got)
	}
	if got := gjson.GetBytes(completed, "interaction.usage.total_tokens").Int(); got != 5 {
		t.Fatalf("total_tokens = %d, want 5", got)
	}
}

func TestGeminiExecutorNativeInteractionsClaudeStreamPreservesToolSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\",\"model\":\"gemini-3.1-flash-lite\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"toolu_1\",\"signature\":\"sig_1\",\"name\":\"get_weather\",\"arguments\":{}}}\n\n"))
		_, _ = w.Write([]byte("event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"location\\\":\\\"北京\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"requires_action\",\"usage\":{\"total_input_tokens\":1,\"total_output_tokens\":2}}}\n\n"))
		_, _ = w.Write([]byte("event: done\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var toolStart []byte
	var toolDelta []byte
	var messageStop []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload := geminiInteractionsSSEPayload(chunk.Payload)
		switch gjson.GetBytes(payload, "type").String() {
		case "content_block_start":
			if gjson.GetBytes(payload, "content_block.type").String() == "tool_use" {
				toolStart = payload
			}
		case "content_block_delta":
			if gjson.GetBytes(payload, "delta.type").String() == "input_json_delta" {
				toolDelta = payload
			}
		case "message_stop":
			messageStop = payload
		}
	}
	if toolStart == nil {
		t.Fatal("tool content_block_start chunk not found")
	}
	if got := gjson.GetBytes(toolStart, "content_block.signature").String(); got != "sig_1" {
		t.Fatalf("tool signature = %q, want sig_1. Payload: %s", got, string(toolStart))
	}
	if got := gjson.GetBytes(toolDelta, "delta.partial_json").String(); got != `{"location":"北京"}` {
		t.Fatalf("tool partial_json = %q, want location payload. Payload: %s", got, string(toolDelta))
	}
	if messageStop == nil {
		t.Fatal("message_stop chunk not found")
	}
}

func TestGeminiExecutorNativeInteractionsResponsesStreamEmitsDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\",\"model\":\"gemini-3.1-flash-lite\"}}\n\n"))
		_, _ = w.Write([]byte("event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"completed\",\"usage\":{\"total_input_tokens\":1,\"total_output_tokens\":2}}}\n\n"))
		_, _ = w.Write([]byte("event: done\ndata: [DONE]\n\n"))
		_, _ = w.Write([]byte("event: done\ndata: [DONE]\n\n"))
		_, _ = w.Write([]byte("data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"late\"}}\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	done := 0
	completed := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if bytes.Contains(chunk.Payload, []byte(`"type":"response.completed"`)) {
			completed++
		}
		if bytes.Contains(chunk.Payload, []byte(`"late"`)) {
			t.Fatal("post-terminal content was forwarded")
		}
		if bytes.Equal(bytes.TrimSpace(chunk.Payload), []byte("data: [DONE]")) {
			done++
		}
	}
	if done != 1 || completed != 1 {
		t.Fatalf("Responses terminal counts: DONE=%d completed=%d, want one each", done, completed)
	}
}

func TestGeminiExecutor_PrepareRequest_EmptyAPIKey_OmitsAuthHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://custom-gemini.example.com/v1beta/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer preexisting-bearer")
	req.Header.Set("x-goog-api-key", "preexisting-key")

	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"auth_kind":           "apikey",
			"base_url":            "https://custom-gemini.example.com",
			"header:Custom-Token": "gemini-secret",
		},
	}
	exec := &GeminiExecutor{}
	if errPrep := exec.PrepareRequest(req, auth); errPrep != nil {
		t.Fatalf("PrepareRequest() error = %v", errPrep)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "" {
		t.Fatalf("x-goog-api-key = %q, want empty", got)
	}
	if got := req.Header.Get("Custom-Token"); got != "gemini-secret" {
		t.Fatalf("Custom-Token = %q, want gemini-secret", got)
	}
}

type malformedFunctionCallUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *malformedFunctionCallUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type malformedFunctionCallUsageNoop struct{}

func (malformedFunctionCallUsageNoop) HandleUsage(context.Context, usage.Record) {}

func assertMalformedFunctionCallFailedUsage(t *testing.T, records <-chan usage.Record) {
	t.Helper()
	var record usage.Record
	select {
	case record = <-records:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
	if !record.Failed {
		t.Fatalf("usage record Failed = false, want true; record=%+v", record)
	}
	if record.Detail.InputTokens != 120 || record.Detail.OutputTokens != 10 || record.Detail.ReasoningTokens != 313 || record.Detail.TotalTokens != 443 {
		t.Fatalf("usage detail = %+v, want Input=120 Output=10 Reasoning=313 Total=443", record.Detail)
	}
}

func TestGeminiExecutorClaudeMalformedFunctionCallReturnsRetryableError(t *testing.T) {
	malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + malformedBody + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(malformedBody))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	req := cliproxyexecutor.Request{
		Model:   "gemini-4-argon",
		Payload: []byte(`{"model":"gemini-4-argon","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	}

	_, errExec := exec.Execute(context.Background(), auth, req, opts)
	if errExec == nil {
		t.Fatal("Execute() error = nil, want retryable 502 error for MALFORMED_FUNCTION_CALL")
	}
	status, okStatus := errExec.(interface{ StatusCode() int })
	if !okStatus || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
	}
	if got := gjson.Get(errExec.Error(), "error.type").String(); got != "api_error" {
		t.Fatalf("Execute() error.type = %q, want api_error. Error: %s", got, errExec.Error())
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)

	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var combined bytes.Buffer
	var streamErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		combined.Write(chunk.Payload)
	}
	if streamErr == nil {
		t.Fatal("ExecuteStream() chunk.Err = nil, want 502 error propagated for conductor/handler accounting")
	}
	streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
	if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
		t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
	}
	streamText := combined.String()
	if strings.Contains(streamText, `"stop_reason":"refusal"`) {
		t.Fatalf("stream unexpectedly emitted stop_reason refusal:\n%s", streamText)
	}
	if !strings.Contains(streamText, "event: error\n") || !strings.Contains(streamText, `"type":"api_error"`) {
		t.Fatalf("stream missing retryable api_error event:\n%s", streamText)
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
}

func TestGeminiExecutorClaudeMalformedFunctionCallSplitUsageAndConductorFailure(t *testing.T) {
	finishChunk := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-argon"}`
	usageChunk := `{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + finishChunk + "\n\ndata: " + usageChunk + "\n\n"))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "gemini",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	req := cliproxyexecutor.Request{
		Model:   "gemini-4-argon",
		Payload: []byte(`{"model":"gemini-4-argon","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	}

	streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"gemini"}, req, opts)
	if errStream != nil {
		t.Fatalf("manager.ExecuteStream() error = %v", errStream)
	}
	var sawChunkErr bool
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			sawChunkErr = true
		}
	}
	if !sawChunkErr {
		t.Fatal("expected manager.ExecuteStream to receive terminal chunk.Err")
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
	updated, okAuth := manager.GetByID(auth.ID)
	if !okAuth || updated.ModelStates["gemini-4-argon"] == nil || updated.ModelStates["gemini-4-argon"].LastError == nil {
		t.Fatalf("expected conductor to record failure on model state, got %+v", updated)
	}
}

func TestGeminiExecutorClaudeMalformedFunctionCallCancelAfterErrorPreservesUsageAndConductorFailure(t *testing.T) {
	malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+malformedBody+"\n\n")
		if flusher, okFlush := w.(http.Flusher); okFlush {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "gemini",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
	})

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := cliproxyexecutor.Request{
		Model:   "gemini-4-argon",
		Payload: []byte(`{"model":"gemini-4-argon","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	}

	streamRes, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
	if errStream != nil {
		t.Fatalf("manager.ExecuteStream() error = %v", errStream)
	}
	for chunk := range streamRes.Chunks {
		if strings.Contains(string(chunk.Payload), "event: error\n") {
			cancel()
		}
	}
	assertMalformedFunctionCallFailedUsage(t, capture.records)
	updated, okAuth := manager.GetByID(auth.ID)
	if !okAuth || updated.ModelStates["gemini-4-argon"] == nil || updated.ModelStates["gemini-4-argon"].LastError == nil {
		t.Fatalf("expected conductor to record failure on model state after client cancel on error event, got %+v", updated)
	}
	if updated.ModelStates["gemini-4-argon"].LastError.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("LastError.HTTPStatus = %d, want 502; err=%+v", updated.ModelStates["gemini-4-argon"].LastError.HTTPStatus, updated.ModelStates["gemini-4-argon"].LastError)
	}
}

func TestGeminiExecutorPromptFeedbackBlockWithoutCandidatesReturnsError(t *testing.T) {
	cases := []struct {
		name           string
		upstreamBody   string
		format         sdktranslator.Format
		requestPayload []byte
		wantErrSubstr  string
	}{
		{
			name:           "openai_with_message",
			upstreamBody:   `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":413751,"totalTokenCount":413751},"modelVersion":"gemini-4-argon"}`,
			format:         sdktranslator.FormatOpenAI,
			requestPayload: []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}]}`),
			wantErrSubstr:  "upstream blocked the prompt: SAFETY - The prompt is blocked due to safety",
		},
		{
			name:           "claude_empty_candidates_without_message",
			upstreamBody:   `{"candidates":[],"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":413751,"totalTokenCount":413751},"modelVersion":"gemini-4-argon"}`,
			format:         sdktranslator.FormatClaude,
			requestPayload: []byte(`{"model":"gemini-4-argon","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
			wantErrSubstr:  "upstream blocked the prompt: PROHIBITED_CONTENT",
		},
		{
			name:           "openai_responses_without_message",
			upstreamBody:   `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":413751,"totalTokenCount":413751},"modelVersion":"gemini-4-argon"}`,
			format:         sdktranslator.FormatOpenAIResponse,
			requestPayload: []byte(`{"model":"gemini-4-argon","input":"hi"}`),
			wantErrSubstr:  "upstream blocked the prompt: SAFETY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, ":streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + tc.upstreamBody + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.upstreamBody))
			}))
			defer server.Close()

			exec := NewGeminiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:       t.Name(),
				Provider: "gemini",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
			})

			req := cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: tc.requestPayload,
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   tc.format,
				ResponseFormat: tc.format,
			}

			_, errExec := exec.Execute(context.Background(), auth, req, opts)
			if errExec == nil {
				t.Fatal("Execute() error = nil, want 502 prompt block error")
			}
			status, okStatus := errExec.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
			}
			if !strings.Contains(errExec.Error(), tc.wantErrSubstr) {
				t.Fatalf("Execute() error = %q, want %q", errExec.Error(), tc.wantErrSubstr)
			}

			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil {
				t.Fatal("ExecuteStream() chunk.Err = nil, want 502 prompt block error")
			}
			streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
			if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
			}
			if !strings.Contains(streamErr.Error(), tc.wantErrSubstr) {
				t.Fatalf("ExecuteStream() chunk.Err = %q, want %q", streamErr.Error(), tc.wantErrSubstr)
			}

			for i := 0; i < 2; i++ {
				record := <-capture.records
				if !record.Failed || record.Detail.InputTokens != 413751 {
					t.Fatalf("usage record[%d] = %+v, want Failed=true InputTokens=413751", i, record)
				}
			}
		})
	}

	t.Run("candidates_with_promptFeedback_not_blocked", func(t *testing.T) {
		bodyWithCandidate := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11},"modelVersion":"gemini-4-argon"}`
		if msg, blocked := geminiPromptBlockErrorMessage([]byte(bodyWithCandidate)); blocked {
			t.Fatalf("geminiPromptBlockErrorMessage() = (%q, true), want empty false when candidates exist", msg)
		}
		wrappedBlocked := `{"response":{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"blocked"}}}`
		if msg, blocked := geminiPromptBlockErrorMessage([]byte(wrappedBlocked)); !blocked || msg != "upstream blocked the prompt: SAFETY - blocked" {
			t.Fatalf("geminiPromptBlockErrorMessage(wrapped) = (%q, %v), wantSAFETY blocked true", msg, blocked)
		}
	})
}

func TestGeminiAndVertexOpenAIStreamTruncationReturnsError(t *testing.T) {
	truncatedChunk := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"totalTokenCount":25}}

`
	formats := []struct {
		name    string
		format  sdktranslator.Format
		payload []byte
	}{
		{
			name:    "openai_chat",
			format:  sdktranslator.FormatOpenAI,
			payload: []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		},
	}
	for _, provider := range []string{"gemini", "vertex_api_key", "vertex_service_account"} {
		for _, fc := range formats {
			t.Run(provider+"/"+fc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, truncatedChunk)
				}))
				defer server.Close()

				ctx, exec, auth := issue6258Executor(t, provider, server.URL)
				auth.ID = t.Name()
				capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
				})

				req := cliproxyexecutor.Request{
					Model:   "gemini-2.5-flash",
					Payload: fc.payload,
				}
				streamRes, errStream := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
					SourceFormat:   fc.format,
					ResponseFormat: fc.format,
					Stream:         true,
				})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				var streamErr error
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
				}
				if streamErr == nil {
					t.Fatal("ExecuteStream() chunk.Err = nil, want 502 truncation error")
				}
				if !strings.Contains(streamErr.Error(), "upstream stream closed before finishReason") {
					t.Fatalf("ExecuteStream() chunk.Err = %q, want upstream stream closed before finishReason", streamErr.Error())
				}
				record := <-capture.records
				if !record.Failed || record.Detail.InputTokens != 20 {
					t.Fatalf("usage record = %+v, want Failed=true InputTokens=20", record)
				}
			})
		}
	}
}
