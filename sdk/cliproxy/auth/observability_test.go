package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestSessionAffinitySnapshot_WindowFilter(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &BalancedHashSelector{}, nil)
	manager.RegisterExecutor(&sessionAffinityExecutor{id: "claude"})
	_, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "auth-observe-1",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "a@example.com"},
	})
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-observe-1", "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4-6"}})
	t.Cleanup(func() { reg.UnregisterClient("auth-observe-1") })

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "sess-observe-1",
			"idempotency_key": "idemp-1",
		},
	}
	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"input":"ping"}`),
	}, opts)
	if errExec != nil {
		t.Fatalf("execute: %v", errExec)
	}

	got := manager.SessionAffinitySnapshot(5 * time.Minute)
	if len(got) != 1 {
		t.Fatalf("snapshot len=%d, want 1", len(got))
	}
	if got[0].SessionID != "sess-observe-1" || got[0].AuthID != "auth-observe-1" {
		t.Fatalf("unexpected snapshot item: %+v", got[0])
	}

	manager.mu.Lock()
	manager.sessionAffinitySeenAt["sess-observe-1"] = time.Now().UTC().Add(-10 * time.Minute)
	manager.mu.Unlock()
	got = manager.SessionAffinitySnapshot(5 * time.Minute)
	if len(got) != 0 {
		t.Fatalf("snapshot len=%d after stale cleanup, want 0", len(got))
	}
}

func TestBalancedHashPreview_ContainsScores(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &BalancedHashSelector{}, nil)
	_, errA := manager.Register(context.Background(), &Auth{
		ID:       "auth-preview-a",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "a@example.com"},
	})
	if errA != nil {
		t.Fatalf("register auth A: %v", errA)
	}
	_, errB := manager.Register(context.Background(), &Auth{
		ID:       "auth-preview-b",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "b@example.com"},
	})
	if errB != nil {
		t.Fatalf("register auth B: %v", errB)
	}

	got := manager.BalancedHashPreview("claude", "claude-sonnet-4-6", "same-key")
	if len(got) != 2 {
		t.Fatalf("preview len=%d, want 2", len(got))
	}
	if got[0].AuthID == "" || got[1].AuthID == "" {
		t.Fatalf("preview missing auth id: %+v", got)
	}
}
