package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const clineTestOpenAIResponse = `{"id":"chatcmpl-1","object":"chat.completion","model":"cline-test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"cost":0.000001}}`

// newClineTestUpstream starts a fake Cline upstream and captures the last
// request. When the request body asks for a stream it replies with a minimal
// OpenAI SSE sequence, otherwise with a JSON chat completion.
func newClineTestUpstream(t *testing.T) (*httptest.Server, func() (path string, headers http.Header, body []byte)) {
	t.Helper()
	var seenPath string
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		if gjson.GetBytes(seenBody, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"cline-test-model","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"cline-test-model","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n" +
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"cline-test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"cost":0.000001}}` + "\n\n" +
				"data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(clineTestOpenAIResponse))
	}))
	t.Cleanup(server.Close)
	return server, func() (string, http.Header, []byte) {
		return seenPath, seenHeaders, seenBody
	}
}

func clineTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "cline",
		Attributes: map[string]string{
			"api_key":  "cline-test-token",
			"base_url": baseURL,
		},
	}
}

func assertClineRequestShape(t *testing.T, path string, headers http.Header, body []byte) {
	t.Helper()
	if path != "/api/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /api/v1/chat/completions", path)
	}
	if got := headers.Get("Authorization"); got != "Bearer cline-test-token" {
		t.Fatalf("Authorization = %q, want Bearer cline-test-token", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty for cline upstream", got)
	}
	message := gjson.GetBytes(body, "messages")
	if !message.IsArray() || len(message.Array()) == 0 {
		t.Fatalf("upstream body is not an OpenAI chat request: %s", body)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "cline-test-model" {
		t.Fatalf("upstream body model = %q, want cline-test-model: %s", got, body)
	}
}

func TestClineExecutor_OpenAISourceNonStream(t *testing.T) {
	server, captured := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	auth := clineTestAuth(server.URL + "/api/v1")
	payload := []byte(`{"model":"cline-test-model","messages":[{"role":"user","content":"hi"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	path, headers, body := captured()
	assertClineRequestShape(t, path, headers, body)
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok: %s", got, resp.Payload)
	}
}

func TestClineExecutor_ClaudeSourceNonStream(t *testing.T) {
	server, captured := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	auth := clineTestAuth(server.URL + "/api/v1")
	payload := []byte(`{"model":"cline-test-model","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	path, headers, body := captured()
	assertClineRequestShape(t, path, headers, body)
	// The inbound Anthropic Messages payload must round-trip to an Anthropic
	// Messages response even though the upstream spoke OpenAI chat completions.
	if got := gjson.GetBytes(resp.Payload, "content").String(); got == "" && !gjson.GetBytes(resp.Payload, "content").IsArray() {
		t.Fatalf("response missing Anthropic content blocks: %s", resp.Payload)
	}
}

func TestClineExecutor_ClaudeSourceStream(t *testing.T) {
	server, captured := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	auth := clineTestAuth(server.URL + "/api/v1")
	payload := []byte(`{"model":"cline-test-model","max_tokens":128,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	chunks := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		chunks++
	}
	if chunks == 0 {
		t.Fatal("expected at least one translated SSE chunk")
	}

	path, headers, body := captured()
	assertClineRequestShape(t, path, headers, body)
	if got := headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", got)
	}
	if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("stream_options.include_usage not set: %s", body)
	}
}

func TestClineExecutor_TokenFromMetadata(t *testing.T) {
	server, captured := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	// File-based OAuth records carry the access token in metadata, not attributes.
	auth := &cliproxyauth.Auth{
		Provider: "cline",
		Attributes: map[string]string{
			"base_url": server.URL + "/api/v1",
		},
		Metadata: map[string]any{
			"access_token": "cline-meta-token",
		},
	}
	payload := []byte(`{"model":"cline-test-model","messages":[{"role":"user","content":"hi"}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	_, headers, _ := captured()
	if got := headers.Get("Authorization"); got != "Bearer cline-meta-token" {
		t.Fatalf("Authorization = %q, want Bearer cline-meta-token", got)
	}
}

func TestClineExecutor_DefaultBaseURL(t *testing.T) {
	executor := NewClineExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "cline", Metadata: map[string]any{}}
	_, baseURL := clineCreds(auth)
	if baseURL != "https://api.cline.bot/api/v1" {
		t.Fatalf("default base URL = %q", baseURL)
	}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: []byte(`{"model":"cline-test-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want missing access token error")
	}
}

func TestClineExecutor_Identifier(t *testing.T) {
	if got := NewClineExecutor(&config.Config{}).Identifier(); got != "cline" {
		t.Fatalf("Identifier() = %q, want cline", got)
	}
}

type captureClineUsagePlugin struct {
	records chan usage.Record
}

func (p *captureClineUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForClineUsageRecord(t *testing.T, records <-chan usage.Record) usage.Record {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case record := <-records:
			if record.Provider == "cline" {
				return record
			}
		case <-timeout:
			t.Fatal("timed out waiting for cline usage record")
		}
	}
}

func almostEqualFloat(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-12
}

func TestClineExecutor_UsageRecordsCostUSDNonStream(t *testing.T) {
	plugin := &captureClineUsagePlugin{records: make(chan usage.Record, 16)}
	usage.RegisterPlugin(plugin)

	server, _ := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	auth := clineTestAuth(server.URL + "/api/v1")
	payload := []byte(`{"model":"cline-test-model","messages":[{"role":"user","content":"hi"}]}`)

	if _, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	record := waitForClineUsageRecord(t, plugin.records)
	if record.Detail.InputTokens != 2 || record.Detail.OutputTokens != 1 || record.Detail.TotalTokens != 3 {
		t.Fatalf("tokens = %+v, want 2/1/3", record.Detail)
	}
	if !almostEqualFloat(record.Detail.CostUSD, 0.000001) {
		t.Fatalf("CostUSD = %v, want 0.000001", record.Detail.CostUSD)
	}
}

func TestClineExecutor_UsageRecordsCostUSDStream(t *testing.T) {
	plugin := &captureClineUsagePlugin{records: make(chan usage.Record, 16)}
	usage.RegisterPlugin(plugin)

	server, _ := newClineTestUpstream(t)
	executor := NewClineExecutor(&config.Config{})
	auth := clineTestAuth(server.URL + "/api/v1")
	payload := []byte(`{"model":"cline-test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "cline-test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	record := waitForClineUsageRecord(t, plugin.records)
	if record.Detail.InputTokens != 2 || record.Detail.OutputTokens != 1 || record.Detail.TotalTokens != 3 {
		t.Fatalf("stream tokens = %+v, want 2/1/3", record.Detail)
	}
	if !almostEqualFloat(record.Detail.CostUSD, 0.000001) {
		t.Fatalf("stream CostUSD = %v, want 0.000001", record.Detail.CostUSD)
	}
}
