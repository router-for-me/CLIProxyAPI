package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type vertexUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *vertexUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "vertex" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type vertexUsageNoop struct{}

func (vertexUsageNoop) HandleUsage(context.Context, usage.Record) {}

// chunkCarriesResponsesCompleted reports whether a translated stream chunk is
// the OpenAI Responses terminal event, tolerating both bare JSON payloads and
// SSE-framed payloads.
func chunkCarriesResponsesCompleted(payload []byte) bool {
	if gjson.GetBytes(payload, "type").String() == "response.completed" {
		return true
	}
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if gjson.Parse(line).Get("type").String() == "response.completed" {
			return true
		}
	}
	return false
}

// TestGeminiVertexStream_ClientDisconnectAfterTerminalChunkDoesNotReportFailure
// reproduces Issue #6421 where the downstream client disconnects right after
// the terminal chunk (response.completed) has been delivered, cancelling the
// upstream scanner context before upstream EOF. The settlement tail must treat
// this as success and publish the observed usage instead of a failure record.
func TestGeminiVertexStream_ClientDisconnectAfterTerminalChunkDoesNotReportFailure(t *testing.T) {
	const authID = "vertex-term-disconnect-test"
	capture := &vertexUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), vertexUsageNoop{})
	})

	// Real Vertex streamGenerateContent SSE shape: the terminal candidate frame
	// carries finishReason together with usageMetadata.
	const upstreamSSE = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello, world!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.1-pro-preview"}` + "\n\n"

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the upstream body open until the downstream disconnect or test
		// end, reproducing upstream lag where EOF happens after the terminal
		// event and the scanner dies on context cancellation.
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: authID, Provider: "vertex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"model":"gemini-3.1-pro-preview","messages":[{"role":"user","content":"hello"}],"stream":true}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := exec.ExecuteStream(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	// Consume chunks until the terminal response.completed event arrives, then
	// terminate the connection like a client that got everything it needed.
	terminalReceived := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			break
		}
		if chunkCarriesResponsesCompleted(chunk.Payload) {
			terminalReceived = true
			cancel()
			break
		}
	}
	if !terminalReceived {
		t.Fatal("expected to receive response.completed chunk before cancellation")
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d, body=%q", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 || record.Detail.TotalTokens != 15 {
			t.Fatalf("reported usage = %+v, want input 10 output 5 total 15", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}
