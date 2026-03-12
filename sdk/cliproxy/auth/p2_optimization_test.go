package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type refreshCountingExecutor struct {
	provider string
	calls    chan string
}

func (e *refreshCountingExecutor) Identifier() string { return e.provider }

func (e *refreshCountingExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshCountingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *refreshCountingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	select {
	case e.calls <- auth.ID:
	default:
	}
	return auth.Clone(), nil
}

func (e *refreshCountingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshCountingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerCheckRefreshesOnlyRefreshesDueAuths(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &refreshCountingExecutor{
		provider: "claude",
		calls:    make(chan string, 32),
	}
	manager.RegisterExecutor(executor)

	now := time.Now().UTC()
	dueIDs := make(map[string]struct{}, 10)
	for index := 0; index < 1000; index++ {
		authID := fmt.Sprintf("refresh-%04d", index)
		auth := &Auth{
			ID:       authID,
			Provider: "claude",
			Attributes: map[string]string{
				"refresh_interval": "1h",
			},
			LastRefreshedAt: now,
		}
		if index < 10 {
			auth.LastRefreshedAt = now.Add(-2 * time.Hour)
			dueIDs[authID] = struct{}{}
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", authID, errRegister)
		}
	}

	if got := len(manager.dueRefreshAuthIDs(now)); got != 10 {
		t.Fatalf("dueRefreshAuthIDs() count = %d, want 10", got)
	}

	manager.checkRefreshes(context.Background())

	gotIDs := make(map[string]struct{}, 10)
	deadline := time.After(2 * time.Second)
	for len(gotIDs) < 10 {
		select {
		case authID := <-executor.calls:
			gotIDs[authID] = struct{}{}
		case <-deadline:
			t.Fatalf("timed out waiting for refreshes, got %d", len(gotIDs))
		}
	}

	select {
	case extra := <-executor.calls:
		t.Fatalf("unexpected extra refresh for %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	if len(gotIDs) != len(dueIDs) {
		t.Fatalf("refresh count = %d, want %d", len(gotIDs), len(dueIDs))
	}
	for authID := range gotIDs {
		if _, ok := dueIDs[authID]; !ok {
			t.Fatalf("unexpected refreshed auth %s", authID)
		}
	}
}

func TestManagerMarkResultQuotaOnlyBlocksCurrentModelShard(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	authA := &Auth{ID: "quota-auth-a", Provider: "gemini"}
	authB := &Auth{ID: "quota-auth-b", Provider: "gemini"}
	if _, errRegister := manager.Register(ctx, authA); errRegister != nil {
		t.Fatalf("register authA: %v", errRegister)
	}
	if _, errRegister := manager.Register(ctx, authB); errRegister != nil {
		t.Fatalf("register authB: %v", errRegister)
	}

	registerSchedulerModels(t, "gemini", "model-a", authA.ID, authB.ID)
	registerSchedulerModels(t, "gemini", "model-b", authA.ID)
	manager.RefreshSchedulerEntry(authA.ID)
	manager.RefreshSchedulerEntry(authB.ID)

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "model-b", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(model-b) before quota error = %v", errPick)
	}
	if got == nil || got.ID != authA.ID {
		t.Fatalf("pickSingle(model-b) before quota auth = %v, want %s", got, authA.ID)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   authA.ID,
		Provider: "gemini",
		Model:    "model-a",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "model-a", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(model-a) after quota error = %v", errPick)
	}
	if got == nil || got.ID != authB.ID {
		t.Fatalf("pickSingle(model-a) after quota auth = %v, want %s", got, authB.ID)
	}

	got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "model-b", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(model-b) after quota error = %v", errPick)
	}
	if got == nil || got.ID != authA.ID {
		t.Fatalf("pickSingle(model-b) after quota auth = %v, want %s", got, authA.ID)
	}
}
