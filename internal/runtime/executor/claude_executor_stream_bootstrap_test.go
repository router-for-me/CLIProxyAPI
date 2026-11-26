package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	claudeBootstrapTestStart = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: ping\n" + `data: {"type":"ping"}` + "\n\n"
	claudeBootstrapTestRateLimit = "event: error\n" +
		`data: {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}` + "\n\n"
	claudeBootstrapTestCompletion = "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
)

func executeClaudeBootstrapTestStream(t *testing.T, buffering bool, format sdktranslator.Format, upstream string) (*cliproxyexecutor.StreamResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(upstream))
	}))
	t.Cleanup(server.Close)
	exec := NewClaudeExecutor(&config.Config{Claude: config.ClaudeConfig{StreamBootstrapBuffering: buffering}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if format != sdktranslator.FormatClaude {
		payload = []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	}
	return exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: format, ResponseFormat: format, Stream: true})
}

func drainClaudeBootstrapTestStream(result *cliproxyexecutor.StreamResult) ([]byte, error) {
	var payload bytes.Buffer
	var errStream error
	for chunk := range result.Chunks {
		payload.Write(chunk.Payload)
		if chunk.Err != nil && errStream == nil {
			errStream = chunk.Err
		}
	}
	return payload.Bytes(), errStream
}

func TestClaudeStreamBootstrapBufferingFailsOverInStreamRateLimit(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		t.Run(format.String(), func(t *testing.T) {
			result, err := executeClaudeBootstrapTestStream(t, true, format, claudeBootstrapTestStart+claudeBootstrapTestRateLimit)
			if result != nil {
				t.Fatal("ExecuteStream returned a stream, want the rate limit before any byte")
			}
			var limit claudeRateLimitError
			if !errors.As(err, &limit) || limit.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("ExecuteStream() error = %#v, want a 429 claudeRateLimitError", err)
			}

			// With buffering off the same stream commits and the limit stays in-stream.
			result, err = executeClaudeBootstrapTestStream(t, false, format, claudeBootstrapTestStart+claudeBootstrapTestRateLimit)
			if err != nil {
				t.Fatalf("ExecuteStream() with buffering off error = %v", err)
			}
			if _, errStream := drainClaudeBootstrapTestStream(result); errStream == nil {
				t.Fatal("buffering off: want the rate limit as an in-stream error")
			}
		})
	}
}

func TestClaudeStreamBootstrapBufferingKeepsStreamsIdentical(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		for name, upstream := range map[string]string{
			"completion":        claudeBootstrapTestStart + claudeBootstrapTestCompletion,
			"limit after text":  claudeBootstrapTestStart + claudeBootstrapTestCompletion[:bytes.Index([]byte(claudeBootstrapTestCompletion), []byte("event: content_block_stop"))] + claudeBootstrapTestRateLimit,
			"end while holding": claudeBootstrapTestStart,
		} {
			t.Run(format.String()+"/"+name, func(t *testing.T) {
				var outputs [2][]byte
				var errs [2]error
				for i, buffering := range []bool{false, true} {
					result, err := executeClaudeBootstrapTestStream(t, buffering, format, upstream)
					if err != nil {
						t.Fatalf("ExecuteStream(buffering=%t) error = %v", buffering, err)
					}
					outputs[i], errs[i] = drainClaudeBootstrapTestStream(result)
				}
				if !bytes.Equal(outputs[0], outputs[1]) {
					t.Fatalf("buffering changed the stream:\noff: %q\non:  %q", outputs[0], outputs[1])
				}
				if (errs[0] == nil) != (errs[1] == nil) {
					t.Fatalf("buffering changed the stream error: off %v, on %v", errs[0], errs[1])
				}
				if format == sdktranslator.FormatClaude && errs[1] == nil && string(outputs[1]) != upstream {
					t.Fatalf("passthrough output = %q, want the upstream bytes", outputs[1])
				}
			})
		}
	}
}
