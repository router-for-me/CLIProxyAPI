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

type openAICompatTTFTRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openAICompatTTFTRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOpenAICompatExecutorStreamTTFTWaitsForEffectiveToken(t *testing.T) {
	tests := []struct {
		name   string
		format sdktranslator.Format
		token  string
		finish string
	}{
		{
			name:   "text",
			format: sdktranslator.FormatOpenAI,
			token:  "data: " + `{"choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n",
			finish: "stop",
		},
		{
			name:   "reasoning_content",
			format: sdktranslator.FormatOpenAI,
			token:  "data: " + `{"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}` + "\n\n",
			finish: "stop",
		},
		{
			name:   "reasoning",
			format: sdktranslator.FormatOpenAI,
			token:  "data: " + `{"choices":[{"index":0,"delta":{"reasoning":"thinking"}}]}` + "\n\n",
			finish: "stop",
		},
		{
			name:   "tool_call_responses_downstream",
			format: sdktranslator.FormatOpenAIResponse,
			token:  "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"hello\"}"}}]}}]}` + "\n\n",
			finish: "tool_calls",
		},
		{
			name:   "multiline_sse_frame",
			format: sdktranslator.FormatOpenAI,
			token:  "data: {\"choices\":[\ndata: {\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n",
			finish: "stop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias := t.Name()
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})
			// Keep the global usage dispatcher outside the fake-clock bubble.
			coreusage.StartDefault(context.Background())

			synctest.Test(t, func(t *testing.T) {
				transport := openAICompatTTFTRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					// All sleeps use synctest's virtual clock, not wall-clock delays.
					time.Sleep(250 * time.Millisecond)
					reader, writer := io.Pipe()
					go func() {
						defer func() { _ = writer.Close() }()
						write := func(s string) bool {
							_, errWrite := io.WriteString(writer, s)
							return errWrite == nil
						}
						// Early role/empty frames and keepalives must not lock TTFT.
						if !write(": keepalive\n\ndata: {\"model\":\"glm-5.3-flash-unc\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":0}}\n\n") {
							return
						}
						time.Sleep(84 * time.Second)
						if !write(tt.token) {
							return
						}
						// Later packets and the terminal event must not replace TTFT.
						time.Sleep(2 * time.Second)
						write("data: {\"model\":\"glm-5.3-flash-unc\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"" + tt.finish + "\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":1,\"total_tokens\":101}}\n\ndata: [DONE]\n\n")
					}()
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"text/event-stream"}},
						Body:       reader,
						Request:    req,
					}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				ctx = coreusage.WithRequestedModelAlias(ctx, alias)
				executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "http://example.invalid/v1"}}
				payload := []byte(`{"model":"glm-5.3-flash-unc","messages":[{"role":"user","content":"hello"}]}`)
				if tt.format == sdktranslator.FormatOpenAIResponse {
					payload = []byte(`{"model":"glm-5.3-flash-unc","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
				}
				result, errExecute := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
					Model: "glm-5.3-flash-unc", Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: tt.format, Stream: true})
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
			if want := 84*time.Second + 250*time.Millisecond; record.TTFT != want {
				t.Fatalf("TTFT = %v, want %v (first effective token, not role/empty/keepalive frames)", record.TTFT, want)
			}
			if want := 86*time.Second + 250*time.Millisecond; record.Latency != want {
				t.Fatalf("Latency = %v, want %v", record.Latency, want)
			}
			if record.Failed {
				t.Fatal("successful stream reported as failed")
			}
			if record.ResponseModel != "glm-5.3-flash-unc" || record.Detail.OutputTokens != 1 {
				t.Fatalf("unexpected response model or usage: %+v", record)
			}
		})
	}
}

func TestOpenAICompatExecutorStreamTTFTErrorFallback(t *testing.T) {
	alias := t.Name()
	capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
	})
	coreusage.StartDefault(context.Background())
	synctest.Test(t, func(t *testing.T) {
		transport := openAICompatTTFTRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(250 * time.Millisecond)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit"}}`)),
				Request:    req,
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		ctx = coreusage.WithRequestedModelAlias(ctx, alias)
		executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "http://example.invalid/v1"}}
		_, errExecute := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model: "glm-5.3-flash-unc", Payload: []byte(`{"model":"glm-5.3-flash-unc","messages":[{"role":"user","content":"hello"}]}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
		if errExecute == nil {
			t.Fatal("expected rate limit error")
		}
	})
	record := capture.await(t)
	if !record.Failed || record.TTFT != 250*time.Millisecond {
		t.Fatalf("failed request lost first-packet fallback: failed=%v TTFT=%v", record.Failed, record.TTFT)
	}
}
