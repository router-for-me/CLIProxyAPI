package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// responsesFormatOptions returns executor options matching a downstream
// /v1/responses client (OpenAI Responses entry and exit protocol).
func responsesFormatOptions(stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Stream:         stream,
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}
}

func TestCommandCodeExecutor_ResponsesFormat_BuildsHarnessEnvelope(t *testing.T) {
	var upstreamBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = body
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"ok"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{ID: "test-auth", Provider: "commandcode", Attributes: map[string]string{"api_key": "test-api-key"}}

	reqJSON := []byte(`{"model":"z-ai/glm-5.3-flash","instructions":"Be brief.","input":"Reply with one word: ok","max_output_tokens":16,"stream":false}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "z-ai/glm-5.3-flash",
		Payload: reqJSON,
	}, responsesFormatOptions(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(upstreamBody) == 0 {
		t.Fatal("upstream received no body")
	}

	root := gjson.ParseBytes(upstreamBody)
	if !root.Get("config").Exists() || !root.Get("memory").Exists() || !root.Get("params").Exists() {
		t.Fatalf("upstream body is not a harness envelope: %s", truncateForLog(upstreamBody))
	}
	if got := root.Get("params.model").String(); got != "z-ai/glm-5.3-flash" {
		t.Errorf("params.model = %q, want %q", got, "z-ai/glm-5.3-flash")
	}
	if got := root.Get("params.messages.0.role").String(); got != "user" {
		t.Errorf("params.messages.0.role = %q, want user", got)
	}
	if got := root.Get("params.messages.0.content.0.text").String(); !strings.Contains(got, "Reply with one word: ok") {
		t.Errorf("user text missing from params.messages, got: %s", got)
	}
	if got := root.Get("params.system").String(); got != "Be brief." {
		t.Errorf("params.system = %q, want instructions text", got)
	}
	if got := root.Get("params.max_tokens").Int(); got != 16 {
		t.Errorf("params.max_tokens = %d, want 16", got)
	}
	if !root.Get("params.stream").Bool() {
		t.Error("params.stream must be forced true for /alpha/generate")
	}
}

func TestCommandCodeExecutor_ResponsesFormat_RewritesOfficialSpelling(t *testing.T) {
	restore := registry.SetCommandCodeOfficialSpellingsForTest(map[string]string{
		"deepseek/deepseek-v4.1-flash": "DeepSeek/DeepSeek-V4.1-Flash",
	})
	defer restore()

	var upstreamBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = body
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop"}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-api-key"}}
	reqJSON := []byte(`{"model":"deepseek/deepseek-v4.1-flash","input":"hi"}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4.1-flash",
		Payload: reqJSON,
	}, responsesFormatOptions(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "params.model").String(); got != "DeepSeek/DeepSeek-V4.1-Flash" {
		t.Errorf("params.model = %q, want official spelling", got)
	}
}

func TestCommandCodeExecutor_ResponsesFormat_NonStreamResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"Go channels are "}`)
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"typed conduits."}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":12,"outputTokens":8,"totalTokens":20}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-api-key"}}
	reqJSON := []byte(`{"model":"z-ai/glm-5.3-flash","input":"Explain Go channels.","stream":false}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "z-ai/glm-5.3-flash",
		Payload: reqJSON,
	}, responsesFormatOptions(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	root := gjson.ParseBytes(resp.Payload)
	if got := root.Get("object").String(); got != "response" {
		t.Fatalf("object = %q, want response (payload: %s)", got, truncateForLog(resp.Payload))
	}
	if got := root.Get("status").String(); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	if got := root.Get("model").String(); got != "z-ai/glm-5.3-flash" {
		t.Errorf("model = %q, want requested model echoed", got)
	}
	var messageOutput gjson.Result
	for _, item := range root.Get("output").Array() {
		if item.Get("type").String() == "message" {
			messageOutput = item
			break
		}
	}
	if !messageOutput.Exists() {
		t.Fatalf("no message output item in payload: %s", truncateForLog(resp.Payload))
	}
	var text string
	for _, part := range messageOutput.Get("content").Array() {
		if part.Get("type").String() == "output_text" {
			text += part.Get("text").String()
		}
	}
	if text != "Go channels are typed conduits." {
		t.Errorf("output text = %q, want concatenated deltas", text)
	}
}

func TestCommandCodeExecutor_ResponsesFormat_StreamEvents(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"Hello "}`)
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"world"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":3,"outputTokens":2,"totalTokens":5}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-api-key"}}
	reqJSON := []byte(`{"model":"z-ai/glm-5.3-flash","input":"Say hello.","stream":true}`)
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "z-ai/glm-5.3-flash",
		Payload: reqJSON,
	}, responsesFormatOptions(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
		buf.WriteString("\n")
	}
	stream := buf.String()

	if !strings.Contains(stream, `"type":"response.created"`) {
		t.Error("missing response.created event")
	}
	if !strings.Contains(stream, `"type":"response.output_text.delta"`) {
		t.Error("missing response.output_text.delta event")
	}
	if !strings.Contains(stream, `"type":"response.completed"`) {
		t.Error("missing response.completed event")
	}
	if strings.Contains(stream, "chat.completion.chunk") {
		t.Error("chat.completion.chunk leaked into Responses stream")
	}
	if !strings.Contains(stream, `"delta":"Hello "`) || !strings.Contains(stream, `"delta":"world"`) {
		t.Errorf("delta texts missing, stream: %s", truncateForLog([]byte(stream)))
	}
	if !strings.HasSuffix(strings.TrimSpace(stream), "[DONE]") {
		t.Errorf("stream must end with [DONE] frame, tail: %q", stream[len(stream)-40:])
	}
}

func TestCommandCodeExecutor_OpenAIFormatUnchanged(t *testing.T) {
	// Guard: the Chat Completions path must keep producing chat chunks.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"hi"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-api-key"}}
	reqJSON := []byte(`{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "z-ai/glm-5.3-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sawChatChunk := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "chat.completion.chunk") {
			sawChatChunk = true
		}
	}
	if !sawChatChunk {
		t.Error("OpenAI format path no longer emits chat.completion.chunk stream")
	}
}

func truncateForLog(b []byte) string {
	const max = 6000
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

var _ = json.Marshal // keep encoding/json import if assertions evolve
