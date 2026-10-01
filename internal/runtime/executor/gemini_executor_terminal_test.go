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

const geminiTerminalContentFrame = `data: {"candidates":[{"content":{"parts":[{"text":"answer"}]}}],"responseId":"gemini-terminal"}`

const geminiTerminalFinishFrame = `data: {"candidates":[{"finishReason":"STOP"}],"responseId":"gemini-terminal"}`

const geminiTerminalUsageFrame = `data: {"candidates":[],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":50,"totalTokenCount":160},"responseId":"gemini-terminal"}`

// geminiResponsesTerminalStream drives GeminiExecutor.ExecuteStream against a
// local SSE server and reports the terminal events and stream errors observed
// by the client.
type geminiResponsesTerminalStream struct {
	Terminals []gjson.Result
	Errors    []error
}

func runGeminiResponsesTerminalStream(t *testing.T, frames string, truncate bool) geminiResponsesTerminalStream {
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
	result, errExecute := NewGeminiExecutor(&config.Config{}).ExecuteStream(context.Background(), &cliproxyauth.Auth{
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

func TestGeminiStreamReadErrorDoesNotSynthesizeCompletion(t *testing.T) {
	observed := runGeminiResponsesTerminalStream(t, geminiTerminalContentFrame+"\n\n", true)
	if len(observed.Errors) != 1 {
		t.Fatalf("stream errors = %d (%v), want 1", len(observed.Errors), observed.Errors)
	}
	if len(observed.Terminals) != 0 {
		t.Fatalf("read error produced %d terminal events: %s", len(observed.Terminals), observed.Terminals[0].Raw)
	}
}

func TestGeminiStreamPendingTerminalIsNotCompletedOnReadError(t *testing.T) {
	frames := geminiTerminalContentFrame + "\n\n" + geminiTerminalFinishFrame + "\n\n"
	observed := runGeminiResponsesTerminalStream(t, frames, true)
	if len(observed.Errors) != 1 {
		t.Fatalf("stream errors = %d (%v), want 1", len(observed.Errors), observed.Errors)
	}
	if len(observed.Terminals) != 0 {
		t.Fatalf("pending terminal was completed by a read error: %s", observed.Terminals[0].Raw)
	}
}

func TestGeminiStreamCleanEndOfStreamFinalizesOnce(t *testing.T) {
	observed := runGeminiResponsesTerminalStream(t, geminiTerminalContentFrame+"\n\n", false)
	if len(observed.Errors) != 0 {
		t.Fatalf("unexpected stream errors: %v", observed.Errors)
	}
	if len(observed.Terminals) != 1 || observed.Terminals[0].Get("type").String() != "response.completed" {
		t.Fatalf("clean EOF terminal events = %d, want one response.completed", len(observed.Terminals))
	}
}

func TestGeminiStreamSplitTerminalUsageIsPreserved(t *testing.T) {
	frames := geminiTerminalContentFrame + "\n\n" + geminiTerminalFinishFrame + "\n\n" + geminiTerminalUsageFrame + "\n\n"
	observed := runGeminiResponsesTerminalStream(t, frames, false)
	if len(observed.Errors) != 0 {
		t.Fatalf("unexpected stream errors: %v", observed.Errors)
	}
	if len(observed.Terminals) != 1 {
		t.Fatalf("terminal events = %d, want 1", len(observed.Terminals))
	}
	terminal := observed.Terminals[0]
	if terminal.Get("type").String() != "response.completed" || terminal.Get("response.status").String() != "completed" {
		t.Fatalf("terminal = %s", terminal.Raw)
	}
	usage := terminal.Get("response.usage")
	if usage.Get("input_tokens").Int() != 100 || usage.Get("output_tokens").Int() != 60 ||
		usage.Get("output_tokens_details.reasoning_tokens").Int() != 50 || usage.Get("total_tokens").Int() != 160 {
		t.Fatalf("split terminal usage = %s", usage.Raw)
	}
}

func TestGeminiStreamMaxTokensReportsIncomplete(t *testing.T) {
	frames := geminiTerminalContentFrame + "\n\n" +
		`data: {"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10},"responseId":"gemini-terminal"}` + "\n\n"
	observed := runGeminiResponsesTerminalStream(t, frames, false)
	if len(observed.Errors) != 0 {
		t.Fatalf("unexpected stream errors: %v", observed.Errors)
	}
	if len(observed.Terminals) != 1 {
		t.Fatalf("terminal events = %d, want 1", len(observed.Terminals))
	}
	terminal := observed.Terminals[0]
	if terminal.Get("type").String() != "response.incomplete" || terminal.Get("response.status").String() != "incomplete" {
		t.Fatalf("MAX_TOKENS terminal = %s", terminal.Raw)
	}
	if terminal.Get("response.incomplete_details.reason").String() != "max_output_tokens" {
		t.Fatalf("incomplete_details = %s", terminal.Get("response.incomplete_details").Raw)
	}
	if terminal.Get("response.usage.total_tokens").Int() != 10 {
		t.Fatalf("incomplete response lost usage: %s", terminal.Get("response.usage").Raw)
	}
	if got := terminal.Get(`response.output.#(type=="message").content.0.text`).String(); got != "answer" {
		t.Fatalf("incomplete response lost partial output: %s", terminal.Raw)
	}
	if got := terminal.Get(`response.output.#(type=="message").status`).String(); got != "incomplete" {
		t.Fatalf("truncated message status = %q, want incomplete", got)
	}
}
