package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGeminiVertexGenerateContentInteractionsSSE(t *testing.T) {
	for _, provider := range []string{"vertex_api_key", "vertex_service_account"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if issue6258TokenResponse(w, r) {
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": keepalive\nevent: message\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]}}]}\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":4,\"totalTokenCount\":15}}\n\n")
			}))
			defer server.Close()

			ctx, exec, auth := issue6258Executor(t, provider, server.URL)
			req := cliproxyexecutor.Request{Model: "gemini-3.8-flash", Payload: []byte(`{"model":"gemini-3.8-flash","input":"hello"}`)}
			stream, errStream := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions, Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream: %v", errStream)
			}
			var events []string
			var text string
			var total int64
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error: %v", chunk.Err)
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					if after, ok := strings.CutPrefix(line, "event: "); ok {
						events = append(events, after)
					}
					if after, ok := strings.CutPrefix(line, "data: "); ok {
						payload := gjson.Parse(after)
						if value := payload.Get("delta.text"); value.Exists() {
							text += value.String()
						}
						if value := payload.Get("interaction.usage.total_tokens"); value.Exists() {
							total = value.Int()
						}
					}
				}
			}
			if got, want := strings.Join(events, ","), "interaction.created,interaction.status_update,step.start,step.delta,step.stop,interaction.completed,done"; got != want {
				t.Fatalf("events = %s, want %s", got, want)
			}
			if text != "answer" || total != 15 {
				t.Fatalf("text = %q, total tokens = %d; want answer and 15", text, total)
			}
		})
	}
}
