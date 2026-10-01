package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// runVertexResponsesTerminalStream drives GeminiVertexExecutor.ExecuteStream
// with API key auth against a local SSE server, mirroring the direct Gemini
// path so both executors keep the same terminal ordering.
func runVertexResponsesTerminalStream(t *testing.T, frames string, truncate bool) geminiResponsesTerminalStream {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if truncate {
			// Promise more bytes than the handler writes so the client observes a
			// deterministic read failure instead of a clean end of stream.
			w.Header().Set("Content-Length", strconv.Itoa(len(frames)+64))
		}
		_, _ = io.WriteString(w, frames)
	}))
	defer server.Close()

	request := []byte(`{"model":"gemini-3.8-flash-high","input":"synthetic local fixture","stream":true}`)
	result, errExecute := NewGeminiVertexExecutor(&config.Config{}).ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	observed := geminiResponsesTerminalStream{}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			observed.Errors = append(observed.Errors, chunk.Err)
			continue
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch data.Get("type").String() {
			case "response.completed", "response.incomplete":
				observed.Terminals = append(observed.Terminals, data)
			}
		}
	}
	return observed
}

func TestVertexStreamReadErrorDoesNotSynthesizeCompletion(t *testing.T) {
	observed := runVertexResponsesTerminalStream(t, geminiTerminalContentFrame+"\n\n", true)
	if len(observed.Errors) != 1 {
		t.Fatalf("stream errors = %d (%v), want 1", len(observed.Errors), observed.Errors)
	}
	if len(observed.Terminals) != 0 {
		t.Fatalf("read error produced %d terminal events: %s", len(observed.Terminals), observed.Terminals[0].Raw)
	}
}

func TestVertexStreamPendingTerminalIsNotCompletedOnReadError(t *testing.T) {
	frames := geminiTerminalContentFrame + "\n\n" + geminiTerminalFinishFrame + "\n\n"
	observed := runVertexResponsesTerminalStream(t, frames, true)
	if len(observed.Errors) != 1 {
		t.Fatalf("stream errors = %d (%v), want 1", len(observed.Errors), observed.Errors)
	}
	if len(observed.Terminals) != 0 {
		t.Fatalf("pending terminal was completed by a read error: %s", observed.Terminals[0].Raw)
	}
}

func TestVertexStreamCleanEndOfStreamFinalizesOnce(t *testing.T) {
	observed := runVertexResponsesTerminalStream(t, geminiTerminalContentFrame+"\n\n", false)
	if len(observed.Errors) != 0 {
		t.Fatalf("unexpected stream errors: %v", observed.Errors)
	}
	if len(observed.Terminals) != 1 || observed.Terminals[0].Get("type").String() != "response.completed" {
		t.Fatalf("clean EOF terminal events = %d, want one response.completed", len(observed.Terminals))
	}
}
