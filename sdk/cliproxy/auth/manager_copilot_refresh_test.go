package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Test preemptive refresh when provider=copilot and refresh_in is present.
func TestShouldRefresh_Copilot_Preemptive(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, nil, nil)
	now := time.Now().UTC()
	last := now.Add(-61 * time.Second)
	a := &Auth{
		ID:              "copilot-1",
		Provider:        "copilot",
		LastRefreshedAt: last,
		Metadata:        map[string]any{"refresh_in": 120}, // seconds
	}
	m.timeNow = func() time.Time { return now }
	if got := m.shouldRefresh(a, now); !got {
		t.Fatalf("expected shouldRefresh=true with preemptive window; got false")
	}
}

// Test fallback path when refresh_in is missing (should not force refresh here).
func TestShouldRefresh_Copilot_Fallback_NoRefreshIn(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, nil, nil)
	now := time.Now().UTC()
	a := &Auth{
		ID:       "copilot-2",
		Provider: "copilot",
		Metadata: map[string]any{"expires_at": now.Add(1 * time.Hour).UnixMilli()},
	}
	m.timeNow = func() time.Time { return now }
	if got := m.shouldRefresh(a, now); got {
		t.Fatalf("expected shouldRefresh=false on fallback with far expiry; got true")
	}
}

// fake executor that always fails refresh
type failingCopilotExec struct{}

func (f failingCopilotExec) Identifier() string { return "copilot" }
func (f failingCopilotExec) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not used")
}
func (f failingCopilotExec) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return nil, errors.New("not used")
}
func (f failingCopilotExec) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	return nil, errors.New("refresh failed")
}
func (f failingCopilotExec) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not used")
}

// Test refresh failure advances NextRefreshAfter (backoff applied)
func TestRefreshAuth_FailureBackoff(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(failingCopilotExec{})
	a := &Auth{ID: "copilot-3", Provider: "copilot", Metadata: map[string]any{"refresh_in": 120}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatalf("register: %v", err)
	}
	before := time.Now()
	m.refreshAuth(context.Background(), a.ID)
	// read snapshot
	got, ok := m.GetByID(a.ID)
	if !ok || got == nil {
		t.Fatalf("auth not found after refreshAuth")
	}
	if got.NextRefreshAfter.IsZero() || !got.NextRefreshAfter.After(before) {
		t.Fatalf("expected NextRefreshAfter advanced after failure, got %v", got.NextRefreshAfter)
	}
}
