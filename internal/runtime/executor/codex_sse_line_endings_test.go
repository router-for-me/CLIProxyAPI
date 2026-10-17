package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexSSESocketLineEndings(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deltaReceived := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for index, event := range []string{`{"type":"response.output_text.delta","delta":"socket-marker"}`, `{"type":"response.completed","response":{"id":"resp_socket","status":"completed","output":[]}}`} {
					_, _ = w.Write([]byte("data: " + event + ending + ending))
					w.(http.Flusher).Flush()
					if index == 0 {
						select {
						case <-deltaReceived:
						case <-r.Context().Done():
							return
						}
					}
				}
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = false
			executor := NewCodexExecutor(cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL}}
			result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output.Write(chunk.Payload)
				if strings.Contains(string(chunk.Payload), "socket-marker") {
					select {
					case <-deltaReceived:
					default:
						close(deltaReceived)
					}
				}
			}
			if !strings.Contains(output.String(), "socket-marker") || !strings.Contains(output.String(), "response.completed") {
				t.Fatalf("missing streamed events: %s", output.String())
			}
		})
	}
}
