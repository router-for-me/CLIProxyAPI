package executor

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexTemporaryIPRestrictionDoesNotLookLikeTokenFailure(t *testing.T) {
	body := []byte(`{"error":{"message":"Your IP is not authorized to make this request.","type":"authentication_error"}}`)
	err := newCodexStatusErr(http.StatusUnauthorized, body)
	if got := err.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("temporary IP restriction status = %d, want 503 to avoid OAuth refresh", got)
	}
	if !strings.Contains(err.Error(), `"code":"ip_temporarily_restricted"`) {
		t.Fatalf("temporary IP restriction code missing: %s", err.Error())
	}
	if err.RetryAfter() == nil || *err.RetryAfter() <= 0 || *err.RetryAfter() > time.Minute {
		t.Fatalf("temporary IP restriction retry delay = %v, want a short cooldown", err.RetryAfter())
	}

	plain := newCodexStatusErr(http.StatusUnauthorized, []byte("Your IP is not authorized to make this request."))
	if got := plain.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("plain text IP restriction status = %d, want 503", got)
	}

	ordinary := newCodexStatusErr(http.StatusUnauthorized, []byte(`{"error":{"message":"invalid or expired token","type":"authentication_error"}}`))
	if got := ordinary.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("ordinary token failure status = %d, want 401", got)
	}
}

type codexIPRestrictionTestExecutor struct {
	*CodexExecutor
	primaryID    string
	failure      error
	refreshCalls atomic.Int64
}

func (e *codexIPRestrictionTestExecutor) Execute(_ context.Context, auth *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth.ID == e.primaryID && e.refreshCalls.Load() == 0 {
		return cliproxyexecutor.Response{}, e.failure
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *codexIPRestrictionTestExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	response, errExecute := e.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		return nil, errExecute
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *codexIPRestrictionTestExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	e.refreshCalls.Add(1)
	updated := auth.Clone()
	updated.Metadata["access_token"] = "synthetic-refreshed-token"
	return updated, nil
}

func TestCodexIPRestrictionManagerRefresh(t *testing.T) {
	for _, source := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			for _, restricted := range []bool{false, true} {
				name := source + "/execute"
				if stream {
					name = source + "/stream"
				}
				message := "invalid or expired token"
				name += "/expired-token"
				if restricted {
					message = "Your IP is not authorized to make this request."
					name = strings.TrimSuffix(name, "expired-token") + "ip-restriction"
				}
				t.Run(name, func(t *testing.T) {
					body := []byte(`{"error":{"message":"` + message + `","type":"authentication_error"}}`)
					var failure error = newCodexStatusErr(http.StatusUnauthorized, body)
					if source == "websocket" {
						var ok bool
						failure, ok = parseCodexWebsocketError([]byte(`{"type":"error","status":401,"body":` + string(body) + `}`))
						if !ok {
							t.Fatal("websocket error not parsed")
						}
					}
					primaryID, backupID := "ip-test-aa-primary", "ip-test-bb-backup"
					model := "ip-test-model"
					reg := registry.GetGlobalRegistry()
					manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
					executor := &codexIPRestrictionTestExecutor{CodexExecutor: NewCodexExecutor(nil), primaryID: primaryID, failure: failure}
					manager.RegisterExecutor(executor)
					for _, id := range []string{primaryID, backupID} {
						reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { reg.UnregisterClient(id) })
						auth := &cliproxyauth.Auth{ID: id, Provider: "codex", Metadata: map[string]any{"access_token": "synthetic-token", "refresh_token": "synthetic-refresh-token"}}
						if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
							t.Fatal(errRegister)
						}
					}
					request := cliproxyexecutor.Request{Model: model}
					var payload string
					if stream {
						result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							payload += string(chunk.Payload)
						}
					} else {
						result, errExecute := manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						payload = string(result.Payload)
					}
					wantID, wantRefresh := primaryID, int64(1)
					if restricted {
						wantID, wantRefresh = backupID, 0
					}
					if got := executor.refreshCalls.Load(); got != wantRefresh {
						t.Fatalf("Refresh calls = %d, want %d", got, wantRefresh)
					}
					if payload != wantID {
						t.Fatalf("selected auth = %q, want %q", payload, wantID)
					}
				})
			}
		}
	}
}

func TestCodexWebsocketTemporaryIPRestriction(t *testing.T) {
	payload := []byte(`{"type":"error","status":401,"body":{"error":{"message":"Your IP is not authorized to make this request.","type":"authentication_error"}}}`)
	err, ok := parseCodexWebsocketError(payload)
	if !ok {
		t.Fatal("websocket IP restriction was not parsed")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("websocket IP restriction = %v, want 503", err)
	}
}
