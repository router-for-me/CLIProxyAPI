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
	for _, scenario := range []string{"http", "eof", "buffered_eof"} {
		t.Run(scenario, func(t *testing.T) {
			raw := ` {"error":{"code":"context_length_exceeded","message":"context rejected","opaque":"` + strings.Repeat("retained", 1500) + `tail"}} `
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Request-Id", "req_usage_observer")
				if scenario == "http" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, raw)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", "99999")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
			}))
			defer server.Close()
			capture := &codexFailureUsageCapture{authID: t.Name(), records: make(chan usage.Record, 2)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &codexFailureUsageCapture{}) })
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = scenario == "buffered_eof"
			executor := NewCodexExecutor(cfg)
			ctx := logging.WithResponseHeadersHolder(context.Background())
			result, err := executor.ExecuteStream(ctx, &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hi"}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
			if err == nil {
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						err = chunk.Err
					}
				}
			}
			if err == nil {
				t.Fatal("failed execution succeeded")
			}
			select {
			case record := <-capture.records:
				if !record.Failed || record.ResponseHeaders.Get("X-Request-Id") != "req_usage_observer" {
					t.Fatalf("failure identity/headers lost: %+v", record)
				}
				if scenario == "http" {
					if record.Fail.Body != raw || record.Fail.StatusCode != http.StatusBadRequest {
						t.Fatal("usage observer did not receive exact raw HTTP failure")
					}
				} else {
					if err.Error() != codexIncompleteStreamMessage || errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatal("incomplete error's recovery-facing representation changed")
					}
					for key, want := range map[string]string{"upstream_http_status": "200", "last_event": "response.output_text.delta", "cause": "unexpected EOF"} {
						if got := ojson.Get(record.Fail.Body, key).String(); got != want {
							t.Errorf("%s=%q, want %q", key, got, want)
						}
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("usage observer received no %s failure", scenario)
			}
		})
	}
}
