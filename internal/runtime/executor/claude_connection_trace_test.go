package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const claudeConnectionTraceMessage = "claude upstream connection acquired"

func captureDebugLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	oldOutput := logger.Out
	logger.SetLevel(log.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.ReplaceHooks(oldHooks)
		logger.SetOutput(oldOutput)
	})
	return hook
}

func claudeConnectionTraceEntries(hook *logtest.Hook) []*log.Entry {
	var entries []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == claudeConnectionTraceMessage {
			entries = append(entries, entry)
		}
	}
	return entries
}

// TestClaudeExecutor_ExecuteStream_LogsConnectionReuse checks that debug logs show
// whether a pooled Claude connection was reused and how long it sat idle, which is
// what diagnosing silent idle drops (#7357, #7465) needs.
func TestClaudeExecutor_ExecuteStream_LogsConnectionReuse(t *testing.T) {
	hook := captureDebugLogs(t)
	release := make(chan struct{}, 4)
	h := newKeepaliveHarness(t, release, false)
	h.transport = keepaliveReleaseRoundTripper{base: h.transport, total: len(keepaliveStreamData), release: release}
	h.auth.Index = "claude-index"

	ctx, cancel := context.WithCancel(logging.WithRequestID(context.Background(), "claude-request-id"))
	defer cancel()
	h.drainChunks(h.start(ctx))
	h.waitSettled()
	h.drainChunks(h.start(ctx))
	h.assertSingleConnection()

	entries := claudeConnectionTraceEntries(hook)
	if len(entries) != 2 {
		t.Fatalf("connection trace entries = %d, want 2", len(entries))
	}
	for i, wantReused := range []bool{false, true} {
		fields := entries[i].Data
		if entries[i].Level != log.DebugLevel || fields["provider"] != "claude" || fields["operation"] != "stream" ||
			fields["auth_index"] != "claude-index" || fields["request_id"] != "claude-request-id" ||
			fields["upstream_host"] != "127.0.0.1" || fields["reused"] != wantReused {
			t.Fatalf("entry %d: unexpected trace fields: %v", i, fields)
		}
		if _, ok := fields["idle_time"].(string); !ok {
			t.Fatalf("entry %d: idle_time missing: %v", i, fields)
		}
		for _, value := range fields {
			if s, ok := value.(string); ok && strings.Contains(s, "key-keepalive") {
				t.Fatalf("entry %d leaks the API key: %v", i, fields)
			}
		}
	}
}

func TestClaudeExecutor_NonStreamRequestsLogConnection(t *testing.T) {
	cases := []struct {
		operation string
		baseURL   string
		body      string
		run       func(ctx context.Context, exec *ClaudeExecutor, auth *cliproxyauth.Auth) error
	}{
		{
			operation: "execute",
			baseURL:   "https://claude.example.test",
			body:      `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			run: func(ctx context.Context, exec *ClaudeExecutor, auth *cliproxyauth.Auth) error {
				_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
					Model:   "claude-opus-5",
					Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude})
				return err
			},
		},
		{
			operation: "count_tokens",
			// Only the first-party origin counts tokens upstream.
			baseURL: "https://api.anthropic.com",
			body:    `{"input_tokens":3}`,
			run: func(ctx context.Context, exec *ClaudeExecutor, auth *cliproxyauth.Auth) error {
				_, err := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
					Model:   "claude-opus-5",
					Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude})
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			hook := captureDebugLogs(t)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				if trace == nil || trace.GotConn == nil {
					t.Fatal("request missing connection trace")
				}
				trace.GotConn(httptrace.GotConnInfo{Reused: true, WasIdle: true})
				header := make(http.Header)
				header.Set("Content-Type", "application/json")
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			exec := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Provider: "claude", Index: "claude-index", Attributes: map[string]string{
				"api_key":  "key-trace",
				"base_url": tc.baseURL,
			}}
			if err := tc.run(ctx, exec, auth); err != nil {
				t.Fatalf("%s error = %v", tc.operation, err)
			}
			entries := claudeConnectionTraceEntries(hook)
			if len(entries) != 1 {
				t.Fatalf("connection trace entries = %d, want 1", len(entries))
			}
			fields := entries[0].Data
			if fields["operation"] != tc.operation || fields["reused"] != true || fields["was_idle"] != true ||
				"https://"+fields["upstream_host"].(string) != tc.baseURL || fields["auth_index"] != "claude-index" {
				t.Fatalf("unexpected trace fields: %v", fields)
			}
		})
	}
}
