package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// A partial response must fail visibly, without replaying inside that stream.
// A separate client request must then reach upstream using the same credential.
func TestCodexRestartRecovery_FreshRequestAfterMidstream1012(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(2, 15*time.Second, 1)
	id, model := uuid.NewString(), "gpt-restart-test"
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		calls++
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.output_text.delta","delta":"partial"}`)}
		if calls == 1 {
			ch <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("upstream read: %w", &websocket.CloseError{Code: websocket.CloseServiceRestart})}
		} else {
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"status":"completed"}}`)}
		}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}})
	req := cliproxyexecutor.Request{Model: model}
	first, err := m.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for chunk := range first.Chunks {
		failed = failed || chunk.Err != nil
	}
	if !failed || calls != 1 {
		t.Fatalf("partial stream: failed=%t calls=%d; want visible error and no replay", failed, calls)
	}
	second, err := m.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("fresh client request rejected after 1012: %v", err)
	}
	for chunk := range second.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if calls != 2 {
		t.Fatalf("upstream calls=%d, want 2", calls)
	}
	assertNoCooldown(t, m, id, model)
}

func TestCodexRestartRecovery_StatusProtection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			err := &Error{HTTPStatus: status, Message: "websocket: close 1012"}
			if shouldSkipCredentialCooldown(resultErrorFromError(err)) {
				t.Fatal("HTTP error lost cooldown protection")
			}
		})
	}
}

func TestCodexRestartRecovery_PreservesExistingQuotaCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	id, model := uuid.NewString(), "gpt-restart-quota"
	if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 429, Message: "quota"}})
	before, _ := m.GetByID(id)
	if before.ModelStates[model] == nil || before.ModelStates[model].NextRetryAfter.IsZero() {
		t.Fatal("quota test did not establish a cooldown")
	}
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: resultErrorFromError(&websocket.CloseError{Code: 1012})})
	after, _ := m.GetByID(id)
	if !after.ModelStates[model].NextRetryAfter.Equal(before.ModelStates[model].NextRetryAfter) {
		t.Fatal("restart removed the existing quota cooldown")
	}
}
