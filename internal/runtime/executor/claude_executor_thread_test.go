package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const missingClaudeThreadBody = `{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested ` + "`previous_message_id`" + `. Replay the full conversation with thread create."}}`

func TestClaudeThreadNotFoundRecoveryWithoutCredentialCooldown(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "stream"}[stream], func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if gjson.GetBytes(body, "thread.type").String() == "continue" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, missingClaudeThreadBody)
					return
				}
				if !strings.Contains(string(body), "original context") {
					t.Error("recovery omitted the original conversation")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-recovered\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg-recovered","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"recovered"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
				}
			}))
			defer server.Close()

			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
			ids := []string{uuid.NewString(), uuid.NewString()}
			for _, id := range ids {
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				_, err := manager.Register(t.Context(), &cliproxyauth.Auth{ID: id, Provider: "claude", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}})
				if err != nil {
					t.Fatal(err)
				}
			}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			run := func(payload []byte) error {
				request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
				if !stream {
					_, err := manager.Execute(context.Background(), []string{"claude"}, request, options)
					return err
				}
				result, err := manager.ExecuteStream(context.Background(), []string{"claude"}, request, options)
				if err != nil {
					return err
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			}
			err := run([]byte(`{"model":"claude-opus-5-5","max_tokens":16,"thread":{"type":"continue","previous_message_id":"msg-stale"},"messages":[{"role":"user","content":"new turn only"}]}`))
			var status cliproxyexecutor.StatusError
			if !errors.As(err, &status) || status.StatusCode() != http.StatusNotFound {
				t.Fatalf("missing thread error = %v, want original HTTP 404", err)
			}
			if !strings.Contains(err.Error(), "thread_not_found") {
				t.Fatalf("missing Claude Code recovery marker: %v", err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("missing thread rotated credentials: attempts = %d", attempts.Load())
			}
			for _, id := range ids {
				auth, _ := manager.GetByID(id)
				if auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.Quota.Exceeded {
					t.Fatalf("thread error penalized credential %s", id)
				}
				if state := auth.ModelStates["claude-opus-5-5"]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
					t.Fatalf("thread error disabled model for credential %s", id)
				}
			}
			if err := run([]byte(`{"model":"claude-opus-5-5","max_tokens":16,"thread":{"type":"create"},"messages":[{"role":"user","content":"original context"},{"role":"assistant","content":"earlier answer"},{"role":"user","content":"new turn"}]}`)); err != nil {
				t.Fatalf("full conversation replay failed: %v", err)
			}
			if attempts.Load() != 2 {
				t.Fatalf("unexpected replay attempts: %d", attempts.Load())
			}
		})
	}
}

func TestClaudeThreadNotFoundClassification(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		body        string
		recoverable bool
	}{
		{"upstream wording", 404, missingClaudeThreadBody, true},
		{"explicit code", 404, `{"error":{"type":"not_found_error","message":"Thread expired","details":{"error_code":"thread_not_found"}}}`, true},
		{"legacy type", 404, `{"error":{"type":"thread_not_found","message":"Thread expired"}}`, true},
		{"model missing", 404, `{"error":{"type":"not_found_error","message":"model claude-opus-5-5 was not found"}}`, false},
		{"route missing", 404, `{"error":{"type":"not_found_error","message":"Not Found"}}`, false},
		{"server failure", 500, missingClaudeThreadBody, false},
		{"auth failure", 401, missingClaudeThreadBody, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyClaudeUpstreamError(test.status, nil, []byte(test.body))
			var scoped cliproxyexecutor.RequestScopedError
			got := errors.As(err, &scoped) && scoped.IsRequestScoped()
			if got != test.recoverable {
				t.Fatalf("request scoped = %v, want %v: %v", got, test.recoverable, err)
			}
			if !test.recoverable && err.Error() != test.body {
				t.Fatal("unrelated failure body changed")
			}
			if test.recoverable && gjson.Get(err.Error(), "error.details.error_code").String() != "thread_not_found" {
				t.Fatal("missing structured thread recovery code")
			}
		})
	}
}
