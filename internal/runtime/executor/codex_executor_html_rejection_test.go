package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexHTMLForbiddenClassification(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"observed edge page", 403, "<html>\n<head><style global>body{font-family:Arial}</style></head></html>", 502},
		{"doctype and case", 403, " \n<!DOCTYPE HTML><HTML>Forbidden</HTML>", 502},
		{"byte order mark", 403, "\xef\xbb\xbf<html lang=\"en\">Forbidden</html>", 502},
		{"structured permission denial", 403, `{"error":{"type":"permission_error","message":"Access denied"}}`, 403},
		{"html inside structured denial", 403, `{"error":{"message":"<html>Access denied</html>"}}`, 403},
		{"plain denial", 403, "Forbidden", 403},
		{"similar prefix", 403, "<htmlish>Forbidden</htmlish>", 403},
		{"unauthorized unchanged", 401, "<html>Unauthorized</html>", 401},
		{"quota unchanged", 429, `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`, 429},
		{"gateway error unchanged", 502, "<html>Bad gateway</html>", 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := newCodexStatusErr(tt.status, []byte(tt.body))
			if err.StatusCode() != tt.want {
				t.Fatalf("status = %d, want %d", err.StatusCode(), tt.want)
			}
			if tt.status == 403 && tt.want == 502 {
				if err.RetryAfter() == nil || *err.RetryAfter() != 5*time.Second || err.IsCredentialScoped() {
					t.Fatalf("HTML rejection must have short, non-quota retry: %#v", err)
				}
				if strings.Contains(err.Error(), "<html") || !strings.Contains(err.Error(), "upstream_html_rejection") {
					t.Fatalf("expected sanitized edge error, got %q", err.Error())
				}
			}
		})
	}
	err := newCodexStatusErrWithHeaders(403, http.Header{"Retry-After": {"45"}}, []byte("<html>Forbidden</html>"))
	if err.RetryAfter() == nil || *err.RetryAfter() != 45*time.Second {
		t.Fatalf("upstream retry hint was not honoured: %#v", err)
	}
}

func TestCodexHTMLForbiddenRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) == 1 {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, "<html><head><style global>body{font-family:Arial}</style></head><body>Unavailable</body></html>")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
			}))
			t.Cleanup(server.Close)
			executor := NewCodexExecutor(&config.Config{})
			auth := codexOAuthTestAuth(server.URL)
			auth.ID = "html-recovery-" + name
			model := "gpt-6.1-sol"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(executor)
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":256}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
			before := time.Now()
			var firstErr error
			if stream {
				_, firstErr = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			} else {
				_, firstErr = manager.Execute(context.Background(), []string{"codex"}, req, opts)
			}
			if firstErr == nil || !strings.Contains(firstErr.Error(), "upstream_html_rejection") {
				t.Fatalf("first request should report transient HTML rejection: %v", firstErr)
			}
			updated, _ := manager.GetByID(auth.ID)
			state := updated.ModelStates[model]
			if state == nil || !state.NextRetryAfter.After(before) || state.NextRetryAfter.After(time.Now().Add(6*time.Second)) {
				t.Fatalf("expected short retry instead of thirty-minute auth cooldown: %#v", state)
			}
			if updated.Quota.Exceeded || state.Quota.Exceeded {
				t.Fatal("HTML rejection was incorrectly recorded as quota exhaustion")
			}
			// Exercise the recovered upstream with the exact same credential and
			// executor, without sleeps, token refresh, or a process restart.
			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 2 {
				t.Fatalf("unexpected upstream attempts: %d", attempts.Load())
			}
		})
	}
}
