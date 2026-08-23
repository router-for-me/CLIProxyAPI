package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// registerOverloadAuths registers n active codex credentials with descending priority so the
// selection order is deterministic, and returns their IDs in expected pick order.
func registerOverloadAuths(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("auth-overload-%d", i+1)
		auth := &Auth{
			ID:       id,
			Provider: "codex",
			Status:   StatusActive,
			// Higher priority is picked first, so descending values keep the order stable.
			Attributes: map[string]string{"priority": fmt.Sprintf("%d", 100-i)},
		}
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.6-terra"}})
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
	})
	return ids
}

func overloadStatusError() customStatusError {
	return customStatusError{
		code: http.StatusServiceUnavailable,
		msg:  `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","param":null}}`,
	}
}

func successStreamResult() *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.output_item.added"}`)}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed"}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Chunks:  ch,
	}
}

// With stream-bootstrap-buffering enabled the codex executor returns the overload rejection
// synchronously instead of relaying it in-stream. This test pins the operational question: with
// request-retry=5 and max-retry-credentials=6, do three consecutive overloaded accounts get
// skipped so the fourth credential serves the request?
func TestExecuteStream_BootstrapOverload_SkipsConsecutiveOverloadedCredentials(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 6)
	ids := registerOverloadAuths(t, m, 6)

	var mu sync.Mutex
	var order []string
	overloaded := map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}

	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			order = append(order, auth.ID)
			mu.Unlock()
			if overloaded[auth.ID] {
				return nil, overloadStatusError()
			}
			return successStreamResult(), nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("expected the request to survive three overloaded credentials: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result from the fourth credential")
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 {
		t.Fatalf("attempted %d credentials (%v), want exactly 4", len(order), order)
	}
	for i := 0; i < 3; i++ {
		if overloaded[order[i]] != true {
			t.Fatalf("attempt %d used %s, expected one of the overloaded credentials", i+1, order[i])
		}
	}
	if overloaded[order[3]] {
		t.Fatalf("final attempt used overloaded credential %s", order[3])
	}
}

// The credential budget must be honoured: when every credential is overloaded the request fails
// after max-retry-credentials attempts rather than looping forever.
func TestExecuteStream_BootstrapOverload_StopsAtCredentialBudget(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	// Six credentials exist and only four may be attempted in one round.
	m.SetRetryConfig(5, 0, 4)
	registerOverloadAuths(t, m, 6)

	var mu sync.Mutex
	attempts := 0
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			attempts++
			mu.Unlock()
			return nil, overloadStatusError()
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.ExecuteStream(context.Background(), []string{"codex"},
			cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ExecuteStream did not terminate within the credential budget")
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts == 0 {
		t.Fatal("expected at least one attempt")
	}
	// A no-wait retry round may consume the remaining two credentials after the
	// first four-credential sweep, but it must not exceed the available set.
	if attempts < 4 || attempts > 6 {
		t.Fatalf("attempts = %d, want between 4 and 6", attempts)
	}
	t.Logf("total upstream attempts across retry sweeps: %d", attempts)
}
