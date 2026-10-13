package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func codexStreamChunks(t *testing.T, stream string, buffered bool) []string {
	t.Helper()
	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = buffered
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			chunks = append(chunks, string(chunk.Payload))
		}
	}
	return chunks
}

func TestCodexExecuteStreamTreatsAnEventSplitAcrossDataLinesAsOneEvent(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_split","model":"gpt-5.4","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`
	cut := strings.Index(completed, `"output":`)
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n"
	oneLine := created + "event: response.completed\ndata: " + completed + "\n\n"
	split := created + "event: response.completed\ndata: " + completed[:cut] + "\ndata: " + completed[cut:] + "\n\n"
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			want := codexStreamChunks(t, oneLine, buffered)
			got := codexStreamChunks(t, split, buffered)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
				t.Fatalf("split event chunks = %q\nwant the one-line chunks %q", got, want)
			}
			if terminal := want[len(want)-1]; !strings.Contains(terminal, `"response.completed"`) || !json.Valid([]byte(strings.TrimPrefix(terminal, "data: "))) {
				t.Fatalf("last chunk = %q, want a valid response.completed", terminal)
			}
		})
	}
}

func TestCodexExecuteStreamPassesADoneSentinelAfterTheCompletedEvent(t *testing.T) {
	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	stream := codexAllocationStream(2, 16) + "data: [DONE]\n\n"
	chunks := codexStreamChunks(t, stream, false)
	sawCompleted := false
	for _, chunk := range chunks {
		if strings.Contains(chunk, `"response.completed"`) {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatalf("chunks = %q, want the completed event", chunks)
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "SSE event data is not valid JSON") {
			t.Fatalf("unexpected framing warning: %s", entry.Message)
		}
	}
}

func TestCodexExecuteNonStreamReadsAnEventSplitAcrossDataLines(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_split","object":"response","model":"gpt-5.4","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`
	cut := strings.Index(completed, `"output":`)
	stream := "event: response.completed\r\ndata: " + completed[:cut] + "\r\ndata: " + completed[cut:] + "\r\n\r\n"
	executor := NewCodexExecutor(&config.Config{})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var got struct {
		ID     string            `json:"id"`
		Output []json.RawMessage `json:"output"`
	}
	if errDecode := json.Unmarshal(resp.Payload, &got); errDecode != nil || got.ID != "resp_split" || len(got.Output) != 1 {
		t.Fatalf("payload = %s (decode error %v), want the completed response", resp.Payload, errDecode)
	}
}

func TestCodexExecuteStreamKeepsAnInvalidSplitEventInvalid(t *testing.T) {
	// Split inside a string, so the SSE-joined data has a raw newline in it and is not JSON.
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bad\",\"model\":\"gpt-5.4\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bad\",\"status\":\"comp\ndata: leted\",\"output\":[]}}\n\n"
	chunks := codexStreamChunks(t, stream, false)
	last := chunks[len(chunks)-1]
	var values []string
	for _, line := range strings.Split(last, "\n") {
		values = append(values, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
	if !strings.HasPrefix(last, "data: ") || strings.Count(last, "\ndata: ") != 1 || json.Valid([]byte(strings.Join(values, "\n"))) {
		t.Fatalf("last chunk = %q, want the invalid event's two data lines unchanged", last)
	}
}

func TestCodexExecuteStreamKeepsBootstrapBudgetsForASplitEvent(t *testing.T) {
	// One handshake event split across more data lines than the bootstrap frame budget holds.
	var split strings.Builder
	split.WriteString("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_split\",\"output\":[\n")
	for range codexBootstrapMaxBufferedFrames + 10 {
		split.WriteString("data: {\"x\":1},\n")
	}
	split.WriteString("data: {\"x\":1}]}}\n\n")
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n" + split.String() +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_split\",\"status\":\"completed\",\"output\":[]}}\n\n"
	countFragments := func(chunks []string) int {
		fragments := 0
		for _, chunk := range chunks {
			if chunk == `data: {"x":1},` {
				fragments++
			}
		}
		return fragments
	}
	// Unbuffered, the event is assembled; while bootstrap buffering holds the stream, its lines
	// stay within the frame budget and are released one by one, as they were before.
	if fragments := countFragments(codexStreamChunks(t, stream, false)); fragments != 0 {
		t.Fatalf("unbuffered: %d fragments emitted, want the event assembled", fragments)
	}
	if fragments := countFragments(codexStreamChunks(t, stream, true)); fragments == 0 {
		t.Fatal("buffered: the event was assembled past the bootstrap frame budget")
	}
}
