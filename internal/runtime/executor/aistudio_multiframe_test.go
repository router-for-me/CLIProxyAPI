package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

var multiFrameCalls atomic.Int64

const (
	multiFrameA = `{"candidates":[{"content":{"role":"model","parts":[{"text":"AAA"}]}}],"modelVersion":"gemini-3.1-pro-preview"}`
	multiFrameB = `{"candidates":[{"content":{"role":"model","parts":[{"text":"BBB"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"totalTokenCount":15},"modelVersion":"gemini-3.1-pro-preview"}`
)

func TestSplitRelayFrames(t *testing.T) {
	pretty := "{\n  \"candidates\": [\n    {}\n  ]\n\n}\n"
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"one data frame", "data: A\n\n", []string{"data: A\n\n"}},
		{"two data frames", "data: A\n\ndata: B\n\n", []string{"data: A\n\n", "data: B\n\n"}},
		{"crlf frames", "data: A\r\n\r\ndata: B\r\n\r\n", []string{"data: A\n\n", "data: B\n\n"}},
		{"event line dropped, data kept", "event: message\ndata: A\n\nevent: message\ndata: B\n\n", []string{"data: A\n\n", "data: B\n\n"}},
		{"id and retry dropped", "id: 7\nretry: 100\ndata: A\n\n", []string{"data: A\n\n"}},
		{"no trailing blank line", "data: A\n\ndata: B", []string{"data: A\n\n", "data: B\n\n"}},
		{"extra blank lines", "data: A\n\n\n\ndata: B\n\n", []string{"data: A\n\n", "data: B\n\n"}},
		{"keep-alive comment dropped", ": keep-alive\n\ndata: A\n\n", []string{"data: A\n\n"}},
		{"only a comment", ": keep-alive\n\n", nil},
		{"leading whitespace", "\n data: A\n\ndata: B\n\n", []string{"data: A\n\n", "data: B\n\n"}},
		{"several data lines of one frame stay together", "data: A1\ndata: A2\n\ndata: B\n\n", []string{"data: A1\ndata: A2\n\n", "data: B\n\n"}},
		{"bare json is one frame, never cut", pretty, []string{pretty}},
		{"array is one frame", "[\n1\n\n,2]", []string{"[\n1\n\n,2]"}},
		{"done is one frame", "[DONE]", []string{"[DONE]"}},
	} {
		got := splitRelayFrames([]byte(c.in))
		if len(got) != len(c.want) {
			t.Errorf("%s: %d frames %q, want %d", c.name, len(got), got, len(c.want))
			continue
		}
		for i := range got {
			if string(got[i]) != c.want[i] {
				t.Errorf("%s: frame %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// streamThroughRelay runs ExecuteStream for the source format with the given relay chunks (the "data" strings of
// MessageTypeStreamChunk messages, in order) and returns the concatenated client-bound payload and the stream error, if any.
func streamThroughRelay(t *testing.T, source sdktranslator.Format, chunks []string) (string, error) {
	t.Helper()
	// Unique per call: the usage record below is matched on it, so a late record of an earlier call cannot satisfy the check.
	authID := "aistudio-multiframe-" + strconv.FormatInt(multiFrameCalls.Add(1), 10)
	connected := make(chan struct{})
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
		OnConnected:     func(string) { close(connected) },
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Errorf("relay stop error = %v", errStop)
		}
	}()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("websocket close error = %v", errClose)
		}
	}()
	<-connected
	clientDone := make(chan error, 1)
	go func() {
		var msg wsrelay.Message
		if errReadJSON := conn.ReadJSON(&msg); errReadJSON != nil {
			clientDone <- errReadJSON
			return
		}
		responses := []wsrelay.Message{{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}}}
		for _, chunk := range chunks {
			responses = append(responses, wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": chunk}})
		}
		responses = append(responses, wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd})
		for _, response := range responses {
			if errWriteJSON := conn.WriteJSON(response); errWriteJSON != nil {
				clientDone <- errWriteJSON
				return
			}
		}
		clientDone <- nil
	}()

	pluginName := "aistudio-multiframe-" + authID
	plugin := &captureAIStudioUsagePlugin{records: make(chan usage.Record, 16)}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, malformedFunctionCallUsageNoop{})
	})
	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"model":"gemini-3.1-pro-preview","messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	if source == sdktranslator.FormatOpenAIResponse {
		req.Payload = []byte(`{"model":"gemini-3.1-pro-preview","input":"hi","stream":true}`)
	}
	if source == sdktranslator.FormatClaude {
		req.Payload = []byte(`{"model":"gemini-3.1-pro-preview","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	}
	opts := cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: req.Payload}
	stream, errExecuteStream := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, req, opts)
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}
	var out bytes.Buffer
	var streamErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		out.Write(chunk.Payload)
		out.WriteByte('\n')
	}
	if errClient := <-clientDone; errClient != nil {
		t.Fatal(errClient)
	}
	// The usage the executor published for the request: the terminal frame's usage must be in it.
	var record usage.Record
	for found := false; !found; {
		select {
		case r := <-plugin.records:
			if r.AuthID == authID && r.Model == "gemini-3.1-pro-preview" {
				record, found = r, true
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no usage record for this call")
		}
	}
	if record.Failed {
		t.Errorf("the stream published failed usage: %+v", record.Fail)
	}
	if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 4 {
		t.Errorf("published usage %+v, want input 11 output 4 from the terminal frame", record.Detail)
	}
	return out.String(), streamErr
}

// A relay chunk with two SSE frames used to translate only the first: the frame with the finish reason and the usage was
// dropped, and the stream never completed. Every response format gets both frames, whether they come in one chunk, in two chunks,
// or as CRLF frames, and the output is the same.
func TestAIStudioStreamTranslatesEveryFrameOfAChunk(t *testing.T) {
	formats := []struct {
		name            string
		format          sdktranslator.Format
		requiredMarkers []string
	}{
		{
			name:            "openai",
			format:          sdktranslator.FormatOpenAI,
			requiredMarkers: []string{"AAA", "BBB", `"finish_reason": "stop"`, `"prompt_tokens": 11`, `"completion_tokens": 4`},
		},
		{
			name:            "openai-response",
			format:          sdktranslator.FormatOpenAIResponse,
			requiredMarkers: []string{"AAA", "BBB", "event: response.completed", `"input_tokens":11`, `"output_tokens":4`},
		},
		{
			name:            "claude",
			format:          sdktranslator.FormatClaude,
			requiredMarkers: []string{"AAA", "BBB", "event: message_stop", `"stop_reason":"end_turn"`, `"input_tokens":11`, `"output_tokens":4`},
		},
	}
	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			separate, errSeparate := streamThroughRelay(t, tc.format, []string{"data: " + multiFrameA + "\n\n", "data: " + multiFrameB + "\n\n"})
			if errSeparate != nil {
				t.Fatal(errSeparate)
			}
			for _, marker := range tc.requiredMarkers {
				if !strings.Contains(separate, marker) {
					t.Fatalf("baseline (one frame per chunk) lacks %q: %s", marker, separate)
				}
			}
			for label, chunks := range map[string][]string{
				"one chunk":      {"data: " + multiFrameA + "\n\ndata: " + multiFrameB + "\n\n"},
				"crlf":           {"data: " + multiFrameA + "\r\n\r\ndata: " + multiFrameB + "\r\n\r\n"},
				"event lines":    {"event: message\ndata: " + multiFrameA + "\n\nevent: message\ndata: " + multiFrameB + "\n\n"},
				"split mid-pair": {"data: " + multiFrameA + "\n\ndata: " + multiFrameB + "\n\n", ": keep-alive\n\n"},
			} {
				got, errGot := streamThroughRelay(t, tc.format, chunks)
				if errGot != nil {
					t.Fatalf("%s: %v", label, errGot)
				}
				for _, marker := range tc.requiredMarkers {
					if !strings.Contains(got, marker) {
						t.Errorf("%s: missing %q in output:\n got %s\nwant %s", label, marker, got, separate)
					}
				}
			}
		})
	}
}

// A pretty-printed JSON chunk (blank lines inside) is one frame and translates as before.
func TestAIStudioStreamKeepsPrettyJSONChunkWhole(t *testing.T) {
	pretty := "{\n  \"candidates\": [\n    {\"content\": {\"role\": \"model\", \"parts\": [{\"text\": \"PPP\"}]},\n\n     \"finishReason\": \"STOP\"}\n  ],\n  \"usageMetadata\": {\"promptTokenCount\": 11, \"candidatesTokenCount\": 4, \"totalTokenCount\": 15}\n}\n"
	got, errGot := streamThroughRelay(t, sdktranslator.FormatOpenAI, []string{pretty})
	if errGot != nil {
		t.Fatal(errGot)
	}
	if !strings.Contains(got, "PPP") {
		t.Fatalf("a pretty-printed chunk was cut or lost: %s", got)
	}
}
