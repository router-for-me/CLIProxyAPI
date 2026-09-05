package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func nativeResponsesExecutor(serverURL string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "deepseek-native",
		BaseURL: serverURL,
		WireAPI: "responses",
	}}}
	return NewOpenAICompatExecutor("openai-compatible-deepseek-native", cfg), &cliproxyauth.Auth{
		Provider: "deepseek-native",
		Attributes: map[string]string{
			"base_url": serverURL,
			"api_key":  "test-key",
		},
	}
}

func nativeResponsesRequest(stream bool) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"model":"deepseek-v4-flash","input":"search","tools":[{"type":"web_search"}],"stream":` + map[bool]string{true: "true", false: "false"}[stream] + `}`)
	return cliproxyexecutor.Request{
			Model:   "deepseek-v4-flash",
			Payload: payload,
		}, cliproxyexecutor.Options{
			Stream:          stream,
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: payload,
		}
}

func TestOpenAICompatNativeResponsesPreservesHostedTools(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","object":"response","status":"completed","model":"deepseek-v4-flash","output":[{"id":"ws-1","type":"web_search_call","status":"completed","action":{"type":"search","query":"search"}},{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"found"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()

	executor, auth := nativeResponsesExecutor(server.URL)
	req, opts := nativeResponsesRequest(false)
	response, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("hosted tool = %q, body=%s", got, upstreamBody)
	}
	if got := gjson.GetBytes(response.Payload, "output.0.type").String(); got != "web_search_call" {
		t.Fatalf("response output = %q, body=%s", got, response.Payload)
	}
}

func TestOpenAICompatNativeResponsesStreamsSemanticEventsWithoutDoneMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if got := gjson.GetBytes(body, "tools.0.type").String(); got != "web_search" {
			t.Fatalf("hosted tool = %q, body=%s", got, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws-1","type":"web_search_call","status":"completed","action":{"type":"search","query":"search"}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"found"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","model":"deepseek-v4-flash","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"))
	}))
	defer server.Close()

	executor, auth := nativeResponsesExecutor(server.URL)
	req, opts := nativeResponsesRequest(true)
	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"web_search_call"`) {
		t.Fatalf("web_search_call missing: %s", output.String())
	}
	if !strings.Contains(output.String(), `"type":"response.completed"`) {
		t.Fatalf("response.completed missing: %s", output.String())
	}
	if strings.Contains(output.String(), "[DONE]") {
		t.Fatalf("native Responses stream synthesized a Chat Completions marker: %s", output.String())
	}
}

func TestOpenAICompatNativeResponsesRejectsEOFBeforeTerminalEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"))
	}))
	defer server.Close()

	executor, auth := nativeResponsesExecutor(server.URL)
	req, opts := nativeResponsesRequest(true)
	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "terminal event") {
		t.Fatalf("stream error = %v, want missing terminal event", streamErr)
	}
}
