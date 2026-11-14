package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type metaTTFTRoundTripFunc func(*http.Request) (*http.Response, error)

func (f metaTTFTRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMetaExecutorTTFT(t *testing.T) {
	const metadata = ": keepalive\n\n" +
		"event: response.subscription_usage\n" +
		"data: {\"type\":\"response.subscription_usage\",\"model\":\"subscription-telemetry\",\"subscription\":{\"tier\":\"test\"}}\n\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ttft\",\"model\":\"muse-spark-1.3\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\",\"summary\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n"
	const textDelta = "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	const completed = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ttft\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-spark-1.3\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"total_tokens\":13}}}\n\n"
	const emptyCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ttft\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"
	const emptyIncomplete = "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_ttft\",\"status\":\"incomplete\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"
	const reasoningSummaryDone = `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"reasoning_done","type":"reasoning","summary":[{"type":"summary_text","text":"done-only-summary"}]}}` + "\n\n"
	const reasoningContentDone = `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"reasoning_done","type":"reasoning","content":[{"type":"reasoning_text","text":"done-only-content"}]}}` + "\n\n"

	tests := []struct {
		name       string
		event      string
		content    string
		format     sdktranslator.Format
		terminal   bool
		nonstream  bool
		httpStatus int
		readError  error
		wantFailed bool
		wantTTFT   time.Duration
	}{
		{name: "text", event: textDelta, content: "hello"},
		{name: "reasoning_summary", event: "data: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"summary_index\":0,\"delta\":\"thinking\"}\n\n", content: "thinking"},
		{name: "done_reasoning_summary_responses", event: reasoningSummaryDone, content: "done-only-summary"},
		{name: "done_reasoning_summary_claude", event: reasoningSummaryDone, content: "done-only-summary", format: sdktranslator.FormatClaude},
		{name: "done_reasoning_content_responses", event: reasoningContentDone, content: "done-only-content"},
		{name: "done_reasoning_content_claude", event: reasoningContentDone, content: "done-only-content", format: sdktranslator.FormatClaude},
		{
			name:     "done_reasoning_empty",
			event:    `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":""}],"content":[]}}` + "\n\n",
			wantTTFT: 1100 * time.Millisecond,
		},
		{
			name:     "done_reasoning_encrypted_only",
			event:    `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[],"encrypted_content":"opaque"}}` + "\n\n",
			wantTTFT: 1100 * time.Millisecond,
		},
		{name: "function_arguments", event: "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"query\\\":\\\"hello\\\"}\"}\n\n", content: "query"},
		{name: "chat_downstream", event: textDelta, content: "hello", format: sdktranslator.FormatOpenAI},
		{name: "completed_without_delta", event: completed, content: "hello", terminal: true},
		{name: "empty_completed", event: emptyCompleted, terminal: true},
		{name: "empty_incomplete", event: emptyIncomplete, terminal: true},
		{name: "failed_event", event: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"upstream failed\"}}}\n\n", terminal: true, wantFailed: true},
		{name: "error_event", event: "data: {\"type\":\"error\",\"error\":{\"code\":502,\"message\":\"upstream failed\"}}\n\n", terminal: true, wantFailed: true},
		{name: "body_error_fallback", readError: io.ErrUnexpectedEOF, wantFailed: true},
		{name: "http_error_fallback", httpStatus: http.StatusTooManyRequests, wantFailed: true},
		{name: "nonstream_keeps_first_byte_timing", event: textDelta, content: "hello", nonstream: true},
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

			var forwarded strings.Builder
			var streamFailed bool
			var firstContentAt time.Duration
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				transport := metaTTFTRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					// All delays use synctest's virtual clock. Headers precede the first body byte.
					time.Sleep(10 * time.Millisecond)
					reader, writer := io.Pipe()
					go func() {
						defer func() {
							if errClose := writer.Close(); errClose != nil {
								t.Errorf("close upstream writer: %v", errClose)
							}
						}()
						write := func(s string) bool {
							_, errWrite := io.WriteString(writer, s)
							return errWrite == nil
						}
						time.Sleep(90 * time.Millisecond)
						if tt.httpStatus != 0 {
							write(`{"error":{"message":"rate limited"}}`)
							return
						}
						if !write(metadata) {
							return
						}
						time.Sleep(900 * time.Millisecond)
						if tt.readError != nil {
							if errClose := writer.CloseWithError(tt.readError); errClose != nil {
								t.Errorf("close upstream with error: %v", errClose)
							}
							return
						}
						if !write(tt.event) || tt.terminal {
							return
						}
						// A later completion must not replace the first effective-token time.
						time.Sleep(100 * time.Millisecond)
						write(completed)
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
				executor := NewMetaExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "meta", Attributes: map[string]string{
					"api_key": "test-token", "base_url": "http://example.invalid/v1",
				}}
				format := tt.format
				if format == "" {
					format = sdktranslator.FormatOpenAIResponse
				}
				payload := []byte(`{"model":"muse-spark-1.3","input":"hello"}`)
				if format == sdktranslator.FormatOpenAI {
					payload = []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`)
				}
				if format == sdktranslator.FormatClaude {
					payload = []byte(`{"model":"muse-spark-1.3","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
				}
				req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: format, Stream: !tt.nonstream}
				if tt.nonstream {
					resp, errExecute := executor.Execute(ctx, auth, req, opts)
					if errExecute != nil {
						t.Fatalf("Execute: %v", errExecute)
					}
					forwarded.Write(resp.Payload)
					return
				}
				result, errExecute := executor.ExecuteStream(ctx, auth, req, opts)
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
					if firstContentAt == 0 && tt.content != "" && strings.Contains(string(chunk.Payload), tt.content) {
						firstContentAt = time.Since(started)
					}
					forwarded.Write(chunk.Payload)
					if chunk.Err != nil {
						streamFailed = true
					}
				}
			})

			record := capture.await(t)
			wantTTFT := time.Second
			wantLatency := 1100 * time.Millisecond
			if tt.terminal || tt.readError != nil {
				wantLatency = time.Second
			}
			if tt.httpStatus != 0 {
				wantLatency = 100 * time.Millisecond
			}
			if tt.nonstream || tt.readError != nil || tt.httpStatus != 0 {
				wantTTFT = 100 * time.Millisecond
			}
			if tt.wantTTFT != 0 {
				wantTTFT = tt.wantTTFT
			}
			if record.TTFT != wantTTFT || record.Latency != wantLatency {
				t.Errorf("TTFT/latency = %v/%v, want %v/%v", record.TTFT, record.Latency, wantTTFT, wantLatency)
			}
			if record.Failed != tt.wantFailed {
				t.Errorf("Failed = %v, want %v", record.Failed, tt.wantFailed)
			}
			if tt.httpStatus == 0 && streamFailed != tt.wantFailed {
				t.Errorf("stream error = %v, want %v", streamFailed, tt.wantFailed)
			}
			if tt.httpStatus != 0 && record.Fail.StatusCode != tt.httpStatus {
				t.Errorf("failure status = %d, want %d", record.Fail.StatusCode, tt.httpStatus)
			}
			if tt.httpStatus == 0 && record.ResponseModel != "muse-spark-1.3" {
				t.Errorf("ResponseModel = %q, want muse-spark-1.3", record.ResponseModel)
			}
			if tt.content != "" {
				if !strings.Contains(forwarded.String(), tt.content) {
					t.Errorf("response lost content %q: %s", tt.content, forwarded.String())
				}
				if !tt.nonstream && firstContentAt != time.Second {
					t.Errorf("first downstream content = %v, want 1s", firstContentAt)
				}
				if got := record.Detail; got.InputTokens != 10 || got.OutputTokens != 3 || got.TotalTokens != 13 {
					t.Errorf("usage = %+v, want input/output/total = 10/3/13", got)
				}
			}
			if strings.Contains(forwarded.String(), "subscription") {
				t.Errorf("subscription telemetry leaked into the response: %s", forwarded.String())
			}
		})
	}
}
