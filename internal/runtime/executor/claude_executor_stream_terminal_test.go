package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const claudeStreamTerminalHangTimeout = 2 * time.Second

func TestClaudeExecutor_ExecuteStreamStopsAtProtocolTerminalWithoutBodyEOF(t *testing.T) {
	completeMessage := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_hang","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")
	fatalError := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		``,
		``,
	}, "\n")

	tests := []struct {
		name           string
		stream         string
		sourceFormat   sdktranslator.Format
		responseFormat sdktranslator.Format
		wantContains   []string
	}{
		{
			name:         "native Claude forward stops at message_stop",
			stream:       completeMessage,
			sourceFormat: sdktranslator.FormatClaude,
			wantContains: []string{`"type":"message_stop"`, "msg_hang"},
		},
		{
			name:           "openai translate stops at message_stop",
			stream:         completeMessage,
			sourceFormat:   sdktranslator.FormatOpenAI,
			responseFormat: sdktranslator.FormatOpenAI,
			wantContains:   []string{`"object":"chat.completion.chunk"`},
		},
		{
			name:         "native Claude forward stops at stream error",
			stream:       fatalError,
			sourceFormat: sdktranslator.FormatClaude,
			wantContains: []string{`"type":"error"`, "Overloaded"},
		},
		{
			name:           "openai translate stops at stream error",
			stream:         fatalError,
			sourceFormat:   sdktranslator.FormatOpenAI,
			responseFormat: sdktranslator.FormatOpenAI,
			wantContains:   []string{"Overloaded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newClaudeHangingSSEServer(t, tt.stream)
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}
			opts := cliproxyexecutor.Options{
				SourceFormat: tt.sourceFormat,
				Stream:       true,
			}
			if tt.responseFormat != "" {
				opts.ResponseFormat = tt.responseFormat
			}

			result, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-5",
				Payload: []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}

			payloads, streamErr := drainClaudeStreamChunks(t, result.Chunks, claudeStreamTerminalHangTimeout)
			if streamErr != nil {
				t.Fatalf("unexpected stream error after protocol terminal: %v", streamErr)
			}
			joined := strings.Join(payloads, "")
			for _, want := range tt.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("stream payloads = %q, want substring %q", joined, want)
				}
			}
		})
	}
}

func newClaudeHangingSSEServer(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, stream)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server
}

func drainClaudeStreamChunks(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk, timeout time.Duration) (payloads []string, streamErr error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return payloads, streamErr
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if len(chunk.Payload) > 0 {
				payloads = append(payloads, string(chunk.Payload))
			}
		case <-deadline.C:
			t.Fatal("stream did not finish after protocol terminal without upstream body EOF")
		}
	}
}
