package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatNativeResponsesRejectsMissingTranslators(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tt := range []struct {
			name           string
			from, response sdktranslator.Format
		}{
			{"chat request", sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse},
			{"claude request", sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse},
			{"gemini request", sdktranslator.FormatGemini, sdktranslator.FormatOpenAIResponse},
			{"chat response", sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI},
			{"unknown response", sdktranslator.FormatOpenAIResponse, sdktranslator.Format("unknown-native-client")},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, tt.name), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"r1","object":"response","output":[]}`)
				}))
				defer server.Close()
				executor, auth := nativeResponsesExecutor(server.URL)
				req, opts := nativeResponsesRequest(streaming)
				opts.SourceFormat, opts.ResponseFormat = tt.from, tt.response
				var err error
				if streaming {
					result, executeErr := executor.ExecuteStream(t.Context(), auth, req, opts)
					err = executeErr
					if result != nil {
						for range result.Chunks {
						}
					}
				} else {
					_, err = executor.Execute(t.Context(), auth, req, opts)
				}
				if err == nil {
					t.Fatal("missing translator was accepted")
				}
				status, ok := err.(interface{ StatusCode() int })
				if !ok || status.StatusCode() != http.StatusBadRequest {
					t.Fatalf("error = %v, want HTTP 400", err)
				}
				scope, ok := err.(interface{ IsRequestScoped() bool })
				if !ok || !scope.IsRequestScoped() {
					t.Fatalf("error = %v, want request-scoped failure", err)
				}
				if got := calls.Load(); got != 0 {
					t.Fatalf("upstream requests = %d, want 0", got)
				}
			})
		}
	}
}

func TestOpenAICompatNativeResponsesPreservesOpaqueHistory(t *testing.T) {
	const input = `[{"type":"reasoning","id":"provider-reasoning-id","encrypted_content":"native-provider:opaque+/=","summary":[]},{"type":"compaction","encrypted_content":"provider-compaction"},{"role":"user","content":"continue"}]`
	for _, mode := range []string{"non-streaming", "streaming", "compact"} {
		for _, store := range []string{"", `,"store":false`, `,"store":true`} {
			t.Run(mode+"/"+store, func(t *testing.T) {
				streaming := mode == "streaming"
				var upstreamBody []byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamBody, _ = io.ReadAll(r.Body)
					if mode == "compact" && r.URL.Path != "/responses/compact" {
						t.Errorf("path = %s, want /responses/compact", r.URL.Path)
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"r1","object":"response","output":[]}`)
					}
				}))
				defer server.Close()
				executor, auth := nativeResponsesExecutor(server.URL)
				req, opts := nativeResponsesRequest(streaming)
				req.Payload = []byte(`{"model":"deepseek-v4-flash","input":` + input + store + `}`)
				opts.OriginalRequest = req.Payload
				original := string(req.Payload)
				if mode == "compact" {
					opts.Alt = "responses/compact"
				}
				if streaming {
					result, err := executor.ExecuteStream(t.Context(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(t.Context(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
				if got := gjson.GetBytes(upstreamBody, "input").Raw; got != input {
					t.Fatalf("opaque history changed:\ngot  %s\nwant %s", got, input)
				}
				if string(req.Payload) != original {
					t.Fatal("caller payload was mutated")
				}
			})
		}
	}
}

func TestOpenAICompatNativeResponsesTerminalOutcomes(t *testing.T) {
	const failed = `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"provider quota detail","status_code":429}}}`
	const completed = `{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`
	for _, tt := range []struct {
		name, event, data string
		wantCode          int
		wantError         string
	}{
		{name: "completed", data: completed},
		{name: "named completed", event: "response.completed", data: completed},
		{name: "incomplete", data: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`},
		{name: "failed data only", data: failed, wantCode: 429, wantError: failed},
		{name: "failed named", event: "response.failed", data: failed, wantCode: 429, wantError: failed},
		{name: "failed without details", data: `{"type":"response.failed","response":{"status":"failed"}}`, wantCode: 502, wantError: `"status":"failed"`},
		{name: "truncated completed", data: `{"type":"response.completed","response":`, wantCode: 502, wantError: "incomplete SSE data frame"},
		{name: "truncated incomplete", data: `{"type":"response.incomplete","response":`, wantCode: 502, wantError: "incomplete SSE data frame"},
		{name: "truncated failed", data: `{"type":"response.failed","response":`, wantCode: 502, wantError: "incomplete SSE data frame"},
		{name: "truncated named failed", event: "response.failed", data: `{"type":"response.failed","response":`, wantCode: 502, wantError: "incomplete SSE data frame"},
		{name: "done is not a Responses terminal", data: "[DONE]", wantCode: 502, wantError: "terminal event"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tt.event != "" {
					_, _ = fmt.Fprintf(w, "event: %s\n", tt.event)
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", tt.data)
			}))
			defer server.Close()
			executor, auth := nativeResponsesExecutor(server.URL)
			auth.ID = t.Name()
			capture := &nativeResponsesUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
			usage.RegisterPlugin(capture)
			req, opts := nativeResponsesRequest(true)
			result, err := executor.ExecuteStream(t.Context(), auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				output.Write(chunk.Payload)
			}
			if tt.wantCode == 0 {
				var dataLines []string
				for _, line := range strings.Split(output.String(), "\n") {
					if strings.HasPrefix(line, "data:") {
						dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					}
				}
				payload := strings.Join(dataLines, "\n")
				if streamErr != nil || !gjson.Valid(payload) {
					t.Fatalf("error = %v, output = %s", streamErr, output.String())
				}
				for _, path := range []string{"type", "response.status", "response.usage.total_tokens", "response.incomplete_details.reason"} {
					if got, want := gjson.Get(payload, path).Raw, gjson.Get(tt.data, path).Raw; got != want {
						t.Errorf("%s = %s, want %s", path, got, want)
					}
				}
			} else {
				status, ok := streamErr.(interface{ StatusCode() int })
				if !ok || status.StatusCode() != tt.wantCode || !strings.Contains(streamErr.Error(), tt.wantError) {
					t.Fatalf("error = %v, want status %d containing %q", streamErr, tt.wantCode, tt.wantError)
				}
				if output.Len() != 0 {
					t.Fatalf("invalid/failed terminal payload forwarded as success: %s", output.String())
				}
			}
			select {
			case record := <-capture.records:
				if record.Failed != (tt.wantCode != 0) {
					t.Fatalf("usage Failed = %t, want %t", record.Failed, tt.wantCode != 0)
				}
				if tt.wantCode != 0 && record.Fail.StatusCode != tt.wantCode {
					t.Fatalf("usage failure status = %d, want %d", record.Fail.StatusCode, tt.wantCode)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("usage record was not published")
			}
		})
	}
}

type nativeResponsesUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (p *nativeResponsesUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID == p.authID {
		select {
		case p.records <- record:
		default:
		}
	}
}
