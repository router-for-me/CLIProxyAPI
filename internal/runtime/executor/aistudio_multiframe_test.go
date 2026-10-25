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
	// unique per call: the usage record below is matched on it, so a late record of an earlier call cannot satisfy the check
	authID := "aistudio-multiframe-" + strconv.FormatInt(multiFrameCalls.Add(1), 10)
	connected := make(chan struct{})
	relay := wsrelay.NewManager(wsrelay.Options{ProviderFactory: func(*http.Request) (string, error) { return authID, nil }, OnConnected: func(string) { close(connected) }})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Error(errStop)
		}
	}()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer conn.Close()
	<-connected
	clientDone := make(chan error, 1)
	go func() {
		var msg wsrelay.Message
		if err := conn.ReadJSON(&msg); err != nil {
			clientDone <- err
			return
		}
		responses := []wsrelay.Message{{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}}}
		for _, chunk := range chunks {
			responses = append(responses, wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": chunk}})
		}
		responses = append(responses, wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd})
		for _, response := range responses {
			if err := conn.WriteJSON(response); err != nil {
				clientDone <- err
				return
			}
		}
		clientDone <- nil
	}()

	plugin := &captureAIStudioUsagePlugin{records: make(chan usage.Record, 16)}
	usage.RegisterNamedPlugin("aistudio-multiframe-"+authID, plugin)
	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"model":"gemini-3.1-pro-preview","messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	if source == sdktranslator.FormatOpenAIResponse {
		req.Payload = []byte(`{"model":"gemini-3.1-pro-preview","input":"hi","stream":true}`)
	}
	if source == sdktranslator.FormatClaude {
		req.Payload = []byte(`{"model":"gemini-3.1-pro-preview","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	}
	opts := cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: req.Payload}
	stream, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, req, opts)
	if err != nil {
		t.Fatal(err)
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
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	// the usage the executor published for the request: the terminal frame's usage must be in it
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
// dropped, the stream never completed. Every response format gets both frames, whether they come in one chunk, in two chunks, or
// as CRLF frames, and the output is the same.
func TestAIStudioStreamTranslatesEveryFrameOfAChunk(t *testing.T) {
	formats := map[string]sdktranslator.Format{"openai": sdktranslator.FormatOpenAI, "openai-response": sdktranslator.FormatOpenAIResponse, "claude": sdktranslator.FormatClaude}
	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			separate, err := streamThroughRelay(t, format, []string{"data: " + multiFrameA + "\n\n", "data: " + multiFrameB + "\n\n"})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"AAA", "BBB"} {
				if !strings.Contains(separate, text) {
					t.Fatalf("baseline (one frame per chunk) lacks %q: %s", text, separate)
				}
			}
			for label, chunks := range map[string][]string{
				"one chunk":      {"data: " + multiFrameA + "\n\ndata: " + multiFrameB + "\n\n"},
				"crlf":           {"data: " + multiFrameA + "\r\n\r\ndata: " + multiFrameB + "\r\n\r\n"},
				"event lines":    {"event: message\ndata: " + multiFrameA + "\n\nevent: message\ndata: " + multiFrameB + "\n\n"},
				"split mid-pair": {"data: " + multiFrameA + "\n\ndata: " + multiFrameB + "\n\n", ": keep-alive\n\n"},
			} {
				got, err := streamThroughRelay(t, format, chunks)
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				for _, text := range []string{"AAA", "BBB"} {
					if !strings.Contains(got, text) {
						t.Errorf("%s: lost %q (the second frame): %s", label, text, got)
					}
				}
				if strings.Contains(got, `"finish_reason":"stop"`) != strings.Contains(separate, `"finish_reason":"stop"`) ||
					strings.Contains(got, "response.completed") != strings.Contains(separate, "response.completed") ||
					strings.Contains(got, "message_stop") != strings.Contains(separate, "message_stop") {
					t.Errorf("%s: the terminal event differs from the one-frame-per-chunk stream:\n got %s\nwant %s", label, got, separate)
				}
			}
		})
	}
}

// A pretty-printed JSON chunk (blank lines inside) is one frame and translates as before.
func TestAIStudioStreamKeepsPrettyJSONChunkWhole(t *testing.T) {
	pretty := "{\n  \"candidates\": [\n    {\"content\": {\"role\": \"model\", \"parts\": [{\"text\": \"PPP\"}]},\n\n     \"finishReason\": \"STOP\"}\n  ],\n  \"usageMetadata\": {\"promptTokenCount\": 11, \"candidatesTokenCount\": 4, \"totalTokenCount\": 15}\n}\n"
	got, err := streamThroughRelay(t, sdktranslator.FormatOpenAI, []string{pretty})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "PPP") {
		t.Fatalf("a pretty-printed chunk was cut or lost: %s", got)
	}
}
