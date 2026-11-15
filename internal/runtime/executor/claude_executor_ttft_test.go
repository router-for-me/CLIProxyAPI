package executor

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorStreamTTFTWaitsForFirstToken(t *testing.T) {
	const messageStart = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_ttft","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: ping\n" + `data: {"type":"ping"}` + "\n\n"
	const textStart = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"
	const textDelta = "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n"
	const thinkingStart = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n"
	const thinkingDelta = "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}` + "\n\n"
	const completion = "event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

	tests := []struct {
		name       string
		blockStart string
		token      string
		format     sdktranslator.Format
		httpStatus int
	}{
		{name: "text", blockStart: textStart, token: textDelta, format: sdktranslator.FormatClaude},
		{name: "thinking", blockStart: thinkingStart, token: thinkingDelta, format: sdktranslator.FormatClaude},
		{name: "chat_downstream", blockStart: textStart, token: textDelta, format: sdktranslator.FormatOpenAI},
		{name: "http_error_fallback", format: sdktranslator.FormatClaude, httpStatus: http.StatusTooManyRequests},
	}
	// Keep the global usage dispatcher outside the fake-clock bubble.
	coreusage.StartDefault(context.Background())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias := t.Name()
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(alias, capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(alias, multiProviderNoopUsagePlugin{})
			})

			synctest.Test(t, func(t *testing.T) {
				transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
					// time.Sleep advances synctest's virtual clock, not wall time.
					time.Sleep(10 * time.Millisecond)
					reader, writer := io.Pipe()
					go func() {
						defer func() { _ = writer.Close() }()
						write := func(s string) bool {
							_, errWrite := io.WriteString(writer, s)
							return errWrite == nil
						}
						time.Sleep(90 * time.Millisecond)
						if tt.httpStatus != 0 {
							write(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
							return
						}
						if !write(messageStart + tt.blockStart) {
							return
						}
						time.Sleep(900 * time.Millisecond)
						if !write(tt.token) {
							return
						}
						time.Sleep(100 * time.Millisecond)
						write(completion)
					}()
					status := tt.httpStatus
					if status == 0 {
						status = http.StatusOK
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": {"text/event-stream"}},
						Body:       reader,
						Request:    req,
					}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				ctx = coreusage.WithRequestedModelAlias(ctx, alias)
				auth := &cliproxyauth.Auth{ID: "claude-ttft", Attributes: map[string]string{
					"api_key": "sk-ant-api03-ttft", "base_url": "http://example.invalid",
				}}
				payload := []byte(`{"model":"claude-opus-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
				if tt.format == sdktranslator.FormatOpenAI {
					payload = []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
				}
				result, errExecute := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth,
					cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
					cliproxyexecutor.Options{SourceFormat: tt.format, Stream: true})
				if tt.httpStatus != 0 {
					if errExecute == nil {
						t.Fatal("expected HTTP error")
					}
					return
				}
				if errExecute != nil {
					t.Fatalf("ExecuteStream: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream: %v", chunk.Err)
					}
				}
			})

			record := capture.await(t)
			wantTTFT, wantLatency := time.Second, 1100*time.Millisecond
			if tt.httpStatus != 0 {
				wantTTFT, wantLatency = 100*time.Millisecond, 100*time.Millisecond
			}
			if record.TTFT != wantTTFT {
				t.Fatalf("TTFT = %v, want %v (first token frame, not message_start or ping)", record.TTFT, wantTTFT)
			}
			if record.Latency != wantLatency {
				t.Fatalf("Latency = %v, want %v", record.Latency, wantLatency)
			}
			if record.Failed != (tt.httpStatus != 0) {
				t.Fatalf("Failed = %v, want %v", record.Failed, tt.httpStatus != 0)
			}
		})
	}
}
