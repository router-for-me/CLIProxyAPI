package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexUsageFailureBodyRetainsProviderFields(t *testing.T) {
	raw := []byte(` {"error":{"code":"context_length_exceeded","message":"context rejected","custom":[null,9007199254740993]},"opaque":"` + strings.Repeat("provider", 2000) + `tail"} `)
	err := newCodexStatusErr(http.StatusBadRequest, raw)
	if ojson.Get(err.Error(), "error.code").String() != "context_too_large" || err.StatusCode() != http.StatusBadRequest {
		t.Fatalf("classified error changed: %v", err)
	}
	if string(err.UsageFailureBody()) != string(raw) {
		t.Fatal("original provider body was lost")
	}
	rules := []config.RequestScopedErrorRule{{Status: 400, Match: []string{"context_too_large"}, Action: "stop"}}
	if action, ok := cliproxyauth.MatchRequestScopedErrorRules(rules, err); !ok || action != cliproxyauth.RequestScopedActionStop {
		t.Fatal("usage diagnostics changed request-scoped rule matching")
	}
	copyBody := err.UsageFailureBody()
	copyBody[0] = 'x'
	raw[0] = 'y'
	if err.UsageFailureBody()[0] != ' ' {
		t.Fatal("failure body shares mutable storage")
	}
	terminal := []byte(`{"type":"response.failed","vendor":{"trace":"retained"},"response":{"error":{"code":"insufficient_quota","message":"quota unavailable"}}}`)
	terminalErr, _, ok := codexTerminalFailureErr(terminal)
	if !ok || string(terminalErr.UsageFailureBody()) != string(terminal) {
		t.Fatal("terminal SSE envelope was lost")
	}
	incomplete := []byte(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"provider_reason"}}}`)
	if string(newCodexEmptyIncompleteStreamError(incomplete).UsageFailureBody()) != string(incomplete) {
		t.Fatal("incomplete reason was lost")
	}
}

type codexFailureUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *codexFailureUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c.records != nil && record.AuthID == c.authID {
		select {
		case c.records <- record:
		default:
		}
	}
}

func TestCodexUsageFailureBodyReachesObserver(t *testing.T) {
	const created = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"
	const delta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	const multilineCreated = "data: {\"type\":\n" + "data: \"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"
	const multilineDelta = "data: {\"type\":\n" + "data: \"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	const failed = `{"type":"response.failed","response":{"error":{"code":"invalid_value","message":"rejected"},"output":[{"type":"function_call","name":"collaboration-optimize.foo"}]}}`
	for _, tc := range []struct {
		name      string
		buffered  bool
		bootstrap bool
		nonstream bool
		optimized bool
		noError   bool
		frames    string
		lastEvent string
		wantBody  string
	}{
		{name: "http"},
		{name: "http_read_error"},
		{name: "eof", frames: created + delta, lastEvent: "response.output_text.delta"},
		{name: "buffered_eof", buffered: true, frames: created + delta, lastEvent: "response.output_text.delta"},
		{name: "bootstrap_eof", buffered: true, bootstrap: true, frames: created, lastEvent: "response.created"},
		{name: "multiline_eof", frames: created + multilineDelta, lastEvent: "response.output_text.delta"},
		{name: "bootstrap_multiline_eof", buffered: true, bootstrap: true, frames: multilineCreated, lastEvent: "response.created"},
		{name: "nonstream_done_eof", nonstream: true, frames: created + "data: [DONE]\n\n", lastEvent: "response.created"},
		{name: "empty_eof", noError: true, lastEvent: "none"},
		{name: "buffered_empty_eof", buffered: true, noError: true, lastEvent: "none"},
		{name: "optimized_failure", optimized: true, frames: created + "data: " + failed + "\n\n", wantBody: failed},
		{name: "buffered_optimized_failure", optimized: true, buffered: true, frames: created + "data: " + failed + "\n\n", wantBody: failed},
		{name: "nonstream_optimized_failure", optimized: true, nonstream: true, frames: created + "data: " + failed + "\n\n", wantBody: failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := ` {"error":{"code":"context_length_exceeded","message":"context rejected","opaque":"` + strings.Repeat("retained", 1500) + `tail"}} `
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.optimized {
					requestBody, _ := io.ReadAll(r.Body)
					if got := ojson.GetBytesNoCopy(requestBody, "input.0.tools.0.name").String(); got != "collaboration-optimize" {
						t.Errorf("test request did not enable collaboration restoration: %q", got)
					}
				}
				w.Header().Set("X-Request-Id", "req_usage_observer")
				if tc.name != "http" && !tc.noError && tc.wantBody == "" {
					w.Header().Set("Content-Length", "99999")
				}
				if strings.HasPrefix(tc.name, "http") {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, raw)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.frames)
			}))
			defer server.Close()
			capture := &codexFailureUsageCapture{authID: t.Name(), records: make(chan usage.Record, 2)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &codexFailureUsageCapture{}) })
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = tc.buffered
			cfg.Client.Codex.OptimizeMultiAgentV2 = tc.optimized
			executor := NewCodexExecutor(cfg)
			ctx := logging.WithResponseHeadersHolder(context.Background())
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hi"}`)}
			if tc.optimized {
				ctx = logging.WithResponseHeadersHolder(codexSpawnAgentTestContext())
				req.Payload = codexSpawnAgentTestPayload()
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: !tc.nonstream}
			var err error
			if tc.nonstream {
				_, err = executor.Execute(ctx, auth, req, opts)
			} else {
				var result *cliproxyexecutor.StreamResult
				result, err = executor.ExecuteStream(ctx, auth, req, opts)
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			}
			if err == nil && !tc.noError {
				t.Fatal("failed execution succeeded")
			}
			if tc.noError && err != nil {
				t.Fatal("empty-stream return behavior changed")
			}
			select {
			case record := <-capture.records:
				if !record.Failed || record.ResponseHeaders.Get("X-Request-Id") != "req_usage_observer" {
					t.Fatalf("failure identity/headers lost: %+v", record)
				}
				if tc.name == "http" {
					if record.Fail.Body != raw || record.Fail.StatusCode != http.StatusBadRequest {
						t.Fatal("usage observer did not receive exact raw HTTP failure")
					}
					return
				}
				if tc.wantBody != "" {
					if record.Fail.Body != tc.wantBody {
						t.Fatalf("usage observer received a restored failure instead of the assembled upstream event: %s", record.Fail.Body)
					}
					return
				}
				wantStatus := "200"
				if tc.name == "http_read_error" || tc.bootstrap {
					if err.Error() != io.ErrUnexpectedEOF.Error() || !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatal("read error's recovery-facing representation changed")
					}
					if tc.name == "http_read_error" {
						wantStatus = "400"
						if ojson.Get(record.Fail.Body, "upstream_body").String() != raw {
							t.Fatal("partial HTTP body was lost")
						}
					}
				} else if !tc.noError && (err.Error() != codexIncompleteStreamMessage || errors.Is(err, io.ErrUnexpectedEOF)) {
					t.Fatal("incomplete error's recovery-facing representation changed")
				}
				wantCause := "unexpected EOF"
				if tc.noError {
					wantCause = "EOF"
				}
				for key, want := range map[string]string{"upstream_http_status": wantStatus, "last_event": tc.lastEvent, "cause": wantCause} {
					if got := ojson.Get(record.Fail.Body, key).String(); got != want {
						t.Errorf("%s=%q, want %q", key, got, want)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("usage observer received no %s failure", tc.name)
			}
		})
	}
}
