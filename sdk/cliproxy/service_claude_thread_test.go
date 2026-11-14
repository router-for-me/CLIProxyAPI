package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeThreadSurvivesExecutorRebinding(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "credential update", true: "config reload"}[force], func(t *testing.T) {
			service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
			auth := &coreauth.Auth{ID: t.Name(), Provider: "claude", Attributes: map[string]string{"api_key": "key-service-thread"}}
			service.registerExecutorForAuth(auth, false)
			first, found := service.coreManager.Executor("claude")
			if !found {
				t.Fatal("Claude executor was not registered")
			}
			calls := 0
			transport := claudeThreadServiceRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if got := strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","); got != "message-threads-2026-08-12,effort-2025-11-24" {
					t.Errorf("wire beta = %q, want original Thread profile", got)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_service_thread","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12,effort-2025-11-24"}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			if _, errExecute := first.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			service.cfg = &config.Config{}
			service.registerExecutorForAuth(&coreauth.Auth{ID: "unrelated-credential", Provider: "claude"}, force)
			second, foundSecond := service.coreManager.Executor("claude")
			if !foundSecond || second == first {
				t.Fatal("test requires a freshly rebound executor")
			}
			request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_service_thread"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
			if _, errExecute := second.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatalf("active Thread lost after unrelated executor rebinding: %v", errExecute)
			}
			if calls != 2 {
				t.Fatalf("upstream requests = %d, want 2", calls)
			}
		})
	}
}

type claudeThreadServiceRoundTripper func(*http.Request) (*http.Response, error)

func (f claudeThreadServiceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
