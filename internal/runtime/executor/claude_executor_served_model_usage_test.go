package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// captureClaudeUsagePlugin captures usage records emitted for claude-provider
// requests so executor-level tests can assert the served-model capture without
// a full Postgres wiring (mirrors captureOpenAICompatUsagePlugin).
type captureClaudeUsagePlugin struct {
	records chan usage.Record
}

func (p *captureClaudeUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.Provider != "claude" {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForClaudeUsageRecord(t *testing.T, plugin *captureClaudeUsagePlugin) usage.Record {
	t.Helper()
	select {
	case record := <-plugin.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for claude usage record")
		return usage.Record{}
	}
}

// TestClaudeExecutorNonStreamRecordsServedModel pins the ordering contract:
// SetServedModel must run BEFORE reporter.Publish, because Publish is
// once-guarded and buildRecord snapshots the reporter by value. A standard
// Claude non-stream body carries top-level "model" and "usage" in the same
// object, so publishing first would permanently lock ServedModel to "".
func TestClaudeExecutorNonStreamRecordsServedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()

	plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-served-model",
		"base_url": server.URL,
	}}
	_, errExecute := executorObj.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	record := waitForClaudeUsageRecord(t, plugin)
	if record.ServedModel != "claude-haiku-4-5" {
		t.Fatalf("ServedModel = %q, want %q: capture must happen before the once-guarded Publish snapshots the record: %+v",
			record.ServedModel, "claude-haiku-4-5", record)
	}
}

// TestClaudeExecutorStreamRecordsServedModelFromCombinedEvent guards the
// streaming capture order with the payload shape that exposes it: a
// Claude-compatible upstream (a core use case of this fork) emitting "model"
// and top-level "usage" in ONE data event. If Publish runs before
// SetServedModel on that line, the record snapshots an empty served model and
// no later line can fix it. Standard Anthropic SSE happens to dodge this
// (message_start has nested usage only), so the combined event is deliberate.
func TestClaudeExecutorStreamRecordsServedModelFromCombinedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"model\":\"claude-haiku-4-5\",\"usage\":{\"output_tokens\":7},\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-served-model-stream",
		"base_url": server.URL,
	}}
	result, errStream := executorObj.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	record := waitForClaudeUsageRecord(t, plugin)
	if record.ServedModel != "claude-haiku-4-5" {
		t.Fatalf("ServedModel = %q, want %q: the line that publishes usage must set the served model first: %+v",
			record.ServedModel, "claude-haiku-4-5", record)
	}
}

// TestClaudeExecutorBufferedUpstreamStreamRecordsServedModel covers the
// upstreamStream branch of Execute (client asked for a translated response
// format, so the executor buffers the SSE body and replays it line by line).
// The capture lives in the FIRST loop (on the truly pre-restore line) and must
// still precede the usage Publish in the second loop; the upstream here puts
// model + top-level usage on the same event to stay mutation-sensitive.
func TestClaudeExecutorBufferedUpstreamStreamRecordsServedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5\",\"content\":[]},\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-served-model-buffered",
		"base_url": server.URL,
	}}
	_, errExecute := executorObj.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("claude"),
		ResponseFormat: sdktranslator.FromString("openai"),
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	record := waitForClaudeUsageRecord(t, plugin)
	if record.ServedModel != "claude-haiku-4-5" {
		t.Fatalf("ServedModel = %q, want %q: buffered upstream-stream capture must precede the usage Publish loop: %+v",
			record.ServedModel, "claude-haiku-4-5", record)
	}
}
