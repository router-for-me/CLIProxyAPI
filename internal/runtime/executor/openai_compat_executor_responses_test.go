package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorNativeResponsesNonStream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	responseBody := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
		"wire_api": "responses",
	}}
	payload := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"custom","name":"exec","description":"run","format":{"type":"text"}}]}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("native Responses request was converted to Chat Completions: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want message; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "tools.0.type").String(); got != "custom" {
		t.Fatalf("tools.0.type = %q, want custom; body=%s", got, gotBody)
	}
	if string(resp.Payload) != string(responseBody) {
		t.Fatalf("response payload = %s, want unchanged %s", resp.Payload, responseBody)
	}
}

func TestOpenAICompatExecutorNativeResponsesRequiresMatchingWireAndSource(t *testing.T) {
	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	tests := []struct {
		name       string
		wireAPI    string
		source     sdktranslator.Format
		alt        string
		wantNative bool
	}{
		{name: "responses source and wire", wireAPI: "responses", source: sdktranslator.FormatOpenAIResponse, wantNative: true},
		{name: "trimmed case insensitive wire", wireAPI: " Responses ", source: sdktranslator.FormatOpenAIResponse, wantNative: true},
		{name: "empty wire", source: sdktranslator.FormatOpenAIResponse},
		{name: "unknown wire", wireAPI: "response", source: sdktranslator.FormatOpenAIResponse},
		{name: "chat source", wireAPI: "responses", source: sdktranslator.FormatOpenAI},
		{name: "claude source", wireAPI: "responses", source: sdktranslator.FormatClaude},
		{name: "compact keeps priority", wireAPI: "responses", source: sdktranslator.FormatOpenAIResponse, alt: "responses/compact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"wire_api": tt.wireAPI}}
			got := exec.useNativeResponsesWire(auth, cliproxyexecutor.Options{SourceFormat: tt.source, Alt: tt.alt})
			if got != tt.wantNative {
				t.Fatalf("useNativeResponsesWire() = %v, want %v", got, tt.wantNative)
			}
		})
	}
}

func TestOpenAICompatExecutorRequestToFormatUsesSelectedAuth(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "JiuRelay", WireAPI: "responses"},
	}}
	exec := NewOpenAICompatExecutor("openai-compatible-jiurelay", cfg)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	if got := exec.RequestToFormat(cliproxyexecutor.Request{}, opts); got != sdktranslator.FormatOpenAI {
		t.Fatalf("RequestToFormat() = %q, want stable fallback %q", got, sdktranslator.FormatOpenAI)
	}
	selectedOldAuth := &cliproxyauth.Auth{Attributes: map[string]string{"wire_api": ""}}
	if got := exec.RequestToFormatWithAuth(selectedOldAuth, cliproxyexecutor.Request{}, opts); got != sdktranslator.FormatOpenAI {
		t.Fatalf("new cfg with old selected auth = %q, want %q", got, sdktranslator.FormatOpenAI)
	}
	cfg.OpenAICompatibility[0].WireAPI = ""
	selectedNewAuth := &cliproxyauth.Auth{Attributes: map[string]string{"wire_api": "responses"}}
	if got := exec.RequestToFormatWithAuth(selectedNewAuth, cliproxyexecutor.Request{}, opts); got != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("old cfg with new selected auth = %q, want %q", got, sdktranslator.FormatOpenAIResponse)
	}

	tests := []struct {
		name    string
		wireAPI string
		opts    cliproxyexecutor.Options
		want    sdktranslator.Format
	}{
		{name: "native Responses", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, want: sdktranslator.FormatOpenAIResponse},
		{name: "unknown wire stays Chat", wireAPI: "response", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, want: sdktranslator.FormatOpenAI},
		{name: "Chat source stays Chat", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}, want: sdktranslator.FormatOpenAI},
		{name: "Claude source stays Chat", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, want: sdktranslator.FormatOpenAI},
		{name: "nonstream compact stays Responses", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"}, want: sdktranslator.FormatOpenAIResponse},
		{name: "stream compact keeps existing Chat behavior", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact", Stream: true}, want: sdktranslator.FormatOpenAI},
		{name: "images keep source format", wireAPI: "responses", opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}, want: sdktranslator.FromString("openai-image")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"wire_api": tt.wireAPI}}
			if got := exec.RequestToFormatWithAuth(auth, cliproxyexecutor.Request{}, tt.opts); got != tt.want {
				t.Fatalf("RequestToFormatWithAuth() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAICompatExecutorNonNativeRequestsKeepChatCompletionsWire(t *testing.T) {
	tests := []struct {
		name    string
		wireAPI string
		source  sdktranslator.Format
		payload []byte
	}{
		{name: "empty wire with Responses source", source: sdktranslator.FormatOpenAIResponse, payload: []byte(`{"model":"grok-4.5","input":"hi"}`)},
		{name: "unknown wire with Responses source", wireAPI: "response", source: sdktranslator.FormatOpenAIResponse, payload: []byte(`{"model":"grok-4.5","input":"hi"}`)},
		{name: "Responses wire with Chat source", wireAPI: "responses", source: sdktranslator.FormatOpenAI, payload: []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}]}`)},
		{name: "Responses wire with Claude source", wireAPI: "responses", source: sdktranslator.FormatClaude, payload: []byte(`{"model":"grok-4.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL + "/v1",
				"api_key":  "test",
				"wire_api": tt.wireAPI,
			}}
			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "grok-4.5",
				Payload: tt.payload,
			}, cliproxyexecutor.Options{
				SourceFormat:   tt.source,
				ResponseFormat: sdktranslator.FormatOpenAI,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotPath != "/v1/chat/completions" {
				t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
			}
			if !gjson.GetBytes(gotBody, "messages").Exists() {
				t.Fatalf("Chat Completions request has no messages: %s", gotBody)
			}
		})
	}
}

func TestOpenAICompatExecutorNativeResponsesStreamPreservesSSEFields(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("id: resp-event-1  \n"))
		_, _ = w.Write([]byte("retry: 1500\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
		"wire_api": "responses",
	}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gjson.GetBytes(gotBody, "stream_options").Exists() {
		t.Fatalf("native Responses request contains Chat stream_options: %s", gotBody)
	}
	wantChunks := []string{
		"event: response.created",
		"id: resp-event-1  ",
		"retry: 1500",
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
	}
	if !slices.Equal(chunks, wantChunks) {
		t.Fatalf("stream chunks = %#v, want %#v", chunks, wantChunks)
	}
	if strings.Contains(strings.Join(chunks, "\n"), "[DONE]") {
		t.Fatalf("native Responses stream received a synthetic [DONE]: %#v", chunks)
	}
}

func TestOpenAICompatExecutorNativeResponsesStreamAcceptsIncompleteTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "wire_api": "responses"}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5", Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("response.incomplete returned stream error: %v", chunk.Err)
		}
	}
}

func TestOpenAICompatExecutorNativeResponsesStreamClosesAfterTerminalWithoutEOF(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "wire_api": "responses"}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5", Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	type streamOutcome struct {
		chunks []string
		err    error
	}
	done := make(chan streamOutcome, 1)
	go func() {
		outcome := streamOutcome{}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				outcome.err = chunk.Err
				break
			}
			outcome.chunks = append(outcome.chunks, string(chunk.Payload))
		}
		done <- outcome
	}()

	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("terminal stream error: %v", outcome.err)
		}
		if len(outcome.chunks) != 1 || !strings.Contains(outcome.chunks[0], `"type":"response.completed"`) {
			t.Fatalf("terminal stream chunks = %#v", outcome.chunks)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native Responses stream did not close after terminal event")
	}
}

func TestOpenAICompatExecutorNativeResponsesStreamRejectsTruncatedEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "wire_api": "responses"}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5", Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("truncated native Responses stream error = nil")
	}
	if status, ok := streamErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("truncated stream status = %v, want %d", streamErr, http.StatusRequestTimeout)
	}
	if scoped, ok := streamErr.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
		t.Fatalf("truncated stream error is not request-scoped: %T %v", streamErr, streamErr)
	}
	if !strings.Contains(streamErr.Error(), "response.completed or response.incomplete") {
		t.Fatalf("truncated stream error = %q", streamErr.Error())
	}
}

func TestOpenAICompatExecutorNativeResponsesStreamSurfacesFailureEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"invalid_value\",\"message\":\"bad tools\"}}\n\n"))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "wire_api": "responses"}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "grok-4.5", Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "bad tools") {
		t.Fatalf("failure event stream error = %v", streamErr)
	}
	if status, ok := streamErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("failure event status = %v, want %d", streamErr, http.StatusBadRequest)
	}
}
