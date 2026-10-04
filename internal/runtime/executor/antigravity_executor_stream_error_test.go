package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravityTerminalThenHoldSSE delivers a text frame followed by the terminal
// finishReason frame carrying the usage, then keeps the upstream connection
// open so the scanner never observes EOF on its own. The stream only ends when
// the client goes away.
const antigravityTerminalThenHoldSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-hold"},"traceId":"trace-hold"}

data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"modelVersion":"gemini-3.7-flash","responseId":"resp-hold"},"traceId":"trace-hold"}

`

// TestAntigravityStreamSettlesUsageAfterTerminalDisconnect covers issue #6376:
// a client that disconnects after receiving the terminal chunk but before the
// upstream EOF must get the cached usage published as a success record, not a
// PublishFailure(context.Canceled) that locks the once-reported failure in
// place.
func TestAntigravityStreamSettlesUsageAfterTerminalDisconnect(t *testing.T) {
	const authID = "antigravity-terminal-disconnect"
	capture := &antigravityUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, antigravityTerminalThenHoldSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the upstream open: EOF must not race the client disconnect.
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
		ID: authID,
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"model":"gemini-3.7-flash","instructions":"You are a helper.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	terminalSeen := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			// A producer-side cancellation after the disconnect surfaces as an
			// error chunk before settlement; the accounting outcome is what
			// this test locks.
			continue
		}
		for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
			if gjson.GetBytes(helps.JSONPayload(line), "type").String() != "response.completed" {
				continue
			}
			terminalSeen = true
			// Simulate the client dropping the connection right after the
			// terminal chunk was delivered, before the upstream EOF. The
			// grace period lets the executor finish forwarding the terminal
			// frame before cancellation lands.
			time.Sleep(100 * time.Millisecond)
			cancel()
		}
	}
	if !terminalSeen {
		t.Fatal("expected a response.completed terminal chunk before the disconnect")
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed, want success after terminal delivery: %+v", record)
		}
		if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 33 {
			t.Fatalf("reported usage = %+v, want input 11 output 22 total 33", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the usage record")
	}
}
