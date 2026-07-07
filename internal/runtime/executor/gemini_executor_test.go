package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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
	if got := gjson.GetBytes(upstreamBody, "generation_config.thinking_summaries").String(); got != "auto" {
		t.Fatalf("thinking_summaries = %q, want auto. Body: %s", got, string(upstreamBody))
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

	done := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if bytes.Equal(bytes.TrimSpace(chunk.Payload), []byte("data: [DONE]")) {
			done = true
		}
	}
	if !done {
		t.Fatal("Responses [DONE] chunk not found")
	}
}
