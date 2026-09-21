package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexDuplexInitialFailure(t *testing.T) {
	for _, tc := range []struct {
		name, payload  string
		status         int
		quota, headers bool
	}{
		{"response_failed_auth", `{"type":"response.failed","response":{"error":{"type":"authentication_error","message":"expired credential"}}}`, 401, false, false},
		{"response_failed_quota", `{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":3600}}}`, 429, true, false},
		// The top-level status is authoritative even when the error type is generic.
		{"error_auth", `{"type":"error","status":401,"headers":{"X-Request-Id":"initial-rejection"},"error":{"type":"server_error","message":"expired credential"}}`, 401, false, true},
		{"error_quota", `{"type":"error","status_code":429,"headers":{"X-Request-Id":"initial-rejection"},"error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":3600}}`, 429, true, true},
	} {
		for _, failover := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failover=%t", tc.name, failover), func(t *testing.T) {
				var rejected, succeeded atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = c.Close() }()
					// Bound a broken test without making server closure drive client recovery.
					_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
					if _, _, err = c.ReadMessage(); err != nil {
						t.Error(err)
						return
					}
					payloads := []string{tc.payload}
					if r.Header.Get("Authorization") == "Bearer bad-key" {
						rejected.Add(1)
					} else {
						succeeded.Add(1)
						payloads = []string{
							`{"type":"response.created","response":{"id":"healthy-response","output":[]}}`,
							`{"type":"response.completed","response":{"id":"healthy-response","output":[]}}`,
						}
					}
					for _, payload := range payloads {
						if err = c.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
							t.Error(err)
							return
						}
					}
					// Keep the rejected socket open: the executor must terminate it itself.
					_, _, _ = c.ReadMessage()
				}))
				defer upstream.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), make(chan core.WebsocketInput))
				cfg := &config.Config{}
				cfg.Codex.ResponseSteering = true
				cfg.CodexResponseSteering = true
				exec := NewCodexWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				model := "duplex-initial-failure-model"
				bad := &auth.Auth{ID: "duplex-initial-bad", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{
					"api_key": "bad-key", "base_url": upstream.URL, "websockets": "true", "priority": "4",
				}}
				req := core.Request{Model: model, Payload: []byte(`{"model":"duplex-initial-failure-model","input":[]}`)}
				opts := core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}}
				if !failover {
					result, err := exec.ExecuteStream(ctx, bad, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					chunk, ok := <-result.Chunks
					if !ok || chunk.Err == nil || len(chunk.Payload) != 0 {
						t.Fatalf("initial rejection must be an error chunk, got %+v (open=%v)", chunk, ok)
					}
					var status interface{ StatusCode() int }
					if !errors.As(chunk.Err, &status) || status.StatusCode() != tc.status {
						t.Fatalf("status lost: %v", chunk.Err)
					}
					var scoped interface{ IsRequestScoped() bool }
					if errors.As(chunk.Err, &scoped) && scoped.IsRequestScoped() {
						t.Fatal("upstream rejection became a connection-only error")
					}
					if tc.quota {
						var quota interface{ IsCredentialScoped() bool }
						var retry interface{ RetryAfter() *time.Duration }
						if !errors.As(chunk.Err, &quota) || !quota.IsCredentialScoped() {
							t.Fatal("credential quota scope lost")
						}
						if !errors.As(chunk.Err, &retry) || retry.RetryAfter() == nil || *retry.RetryAfter() != time.Hour {
							t.Fatal("upstream retry delay lost")
						}
					}
					if tc.headers {
						var headers interface{ Headers() http.Header }
						if !errors.As(chunk.Err, &headers) || headers.Headers().Get("X-Request-Id") != "initial-rejection" {
							t.Fatal("upstream error headers lost")
						}
					}
					select {
					case _, ok := <-result.Chunks:
						if ok {
							t.Fatal("rejected stream remained open")
						}
					case <-ctx.Done():
						t.Fatal("rejected stream did not terminate")
					}
				} else {
					manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
					manager.SetConfig(cfg)
					manager.SetRetryConfig(3, 30*time.Second, 0)
					manager.RegisterExecutor(exec)
					good := &auth.Auth{ID: "duplex-initial-good", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{
						"api_key": "good-key", "base_url": upstream.URL, "websockets": "true", "priority": "3",
					}}
					for _, candidate := range []*auth.Auth{bad, good} {
						registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
						if _, err := manager.Register(ctx, candidate); err != nil {
							t.Fatal(err)
						}
					}
					before := time.Now()
					result, err := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					completed := false
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
							completed = gjson.GetBytes(chunk.Payload, "response.id").String() == "healthy-response"
							cancel()
						} else if gjson.GetBytes(chunk.Payload, "type").String() != "response.created" {
							t.Fatalf("rejected account payload escaped bootstrap: %s", chunk.Payload)
						}
					}
					if !completed {
						t.Fatal("healthy account never completed")
					}
					current, _ := manager.GetByID(bad.ID)
					state := current.ModelStates[model]
					if state == nil || state.LastError == nil || state.LastError.HTTPStatus != tc.status || !state.Unavailable {
						t.Fatalf("initial failure was not recorded: %+v", state)
					}
					if tc.quota && (current.Quota.Reason != "credential_quota" || current.Quota.NextRecoverAt.Before(before.Add(time.Hour))) {
						t.Fatalf("credential quota cooldown lost: %+v", current.Quota)
					}
				}
				wantSucceeded := int32(0)
				if failover {
					wantSucceeded = 1
				}
				if rejected.Load() != 1 || succeeded.Load() != wantSucceeded {
					t.Fatalf("attempts: rejected=%d healthy=%d", rejected.Load(), succeeded.Load())
				}
			})
		}
	}
}
