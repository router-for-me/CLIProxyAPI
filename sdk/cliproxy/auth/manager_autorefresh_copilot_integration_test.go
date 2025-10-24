package auth

import (
    "context"
    "errors"
    "sync/atomic"
    "testing"
    "time"

    cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// spyCopilotExec is a test double that records Refresh invocations.
type spyCopilotExec struct{ calls int32; signaled chan struct{} }

func (s *spyCopilotExec) Identifier() string { return "copilot" }
func (s *spyCopilotExec) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
    return cliproxyexecutor.Response{}, errors.New("not used")
}
func (s *spyCopilotExec) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
    return nil, errors.New("not used")
}
func (s *spyCopilotExec) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
    atomic.AddInt32(&s.calls, 1)
    // signal once without blocking if channel exists
    if s.signaled != nil {
        select { case s.signaled <- struct{}{}: default: }
    }
    // simulate token update from upstream
    if a != nil {
        if a.Metadata == nil { a.Metadata = make(map[string]any) }
        a.Metadata["access_token"] = "updated-token"
    }
    // return same auth (manager will set LastRefreshedAt, clear NextRefreshAfter)
    return a, nil
}
func (s *spyCopilotExec) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
    return cliproxyexecutor.Response{}, errors.New("not used")
}

// Test that manager.checkRefreshes schedules a preemptive refresh for provider=copilot
// when refresh_in is present and the preemptive window has arrived.
func TestAutoRefresh_Copilot_Preemptive_InvokesExecutor(t *testing.T) {
    t.Parallel()

    m := NewManager(nil, nil, nil)
    spy := &spyCopilotExec{signaled: make(chan struct{}, 1)}
    m.RegisterExecutor(spy)

    now := time.Now().UTC()
    m.timeNow = func() time.Time { return now }

    // Arrange: Copilot auth with last refresh 2 minutes ago and refresh_in=60s → should refresh now
    a := &Auth{
        ID:              "copilot-int-1",
        Provider:        "copilot",
        LastRefreshedAt: now.Add(-120 * time.Second),
        Metadata:        map[string]any{"refresh_in": 60},
        Status:          StatusActive,
        CreatedAt:       now.Add(-5 * time.Minute),
        UpdatedAt:       now.Add(-2 * time.Minute),
    }
    if _, err := m.Register(context.Background(), a); err != nil {
        t.Fatalf("register: %v", err)
    }

    // Act: trigger a single refresh evaluation tick (spawns a goroutine to refresh)
    m.checkRefreshes(context.Background())

    // Assert: spy executor was invoked
    select {
    case <-spy.signaled:
        // ok
    case <-time.After(2 * time.Second):
        t.Fatalf("expected refresh to be invoked within 2s, but it did not")
    }

    // And manager updated the auth state
    got, ok := m.GetByID(a.ID)
    if !ok || got == nil {
        t.Fatalf("auth not found after refresh")
    }
    if got.LastRefreshedAt.IsZero() {
        t.Fatalf("LastRefreshedAt not updated")
    }
    if !got.NextRefreshAfter.IsZero() {
        t.Fatalf("NextRefreshAfter should be cleared after successful refresh; got %v", got.NextRefreshAfter)
    }
    if got.Metadata == nil || got.Metadata["access_token"] != "updated-token" {
        t.Fatalf("expected access_token to be updated by refresh")
    }
}
