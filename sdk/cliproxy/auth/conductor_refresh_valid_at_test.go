package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_ProactiveRefreshUnauthorizedKeepsValidAccessToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	now := time.Now()
	auth := &Auth{
		ID:       "codex-valid-at",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":         "x@example.com",
			"access_token":  testAccessTokenJWT(now.Add(38 * time.Hour)),
			"refresh_token": "rt-reused",
			"expired":       now.Add(-time.Hour).Format(time.RFC3339),
		},
	}
	registerSchedulerModels(t, "codex", "codex-keep-at-model", auth.ID)
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	if updated.Unavailable {
		t.Fatal("expected still-valid access token to remain available")
	}
	if updated.Status != StatusActive {
		t.Fatalf("Status = %q, want %q", updated.Status, StatusActive)
	}
	if updated.StatusMessage == "unauthorized" {
		t.Fatal("StatusMessage = unauthorized, want to keep the credential active")
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero so unauthorized refresh stops retrying", updated.NextRefreshAfter)
	}
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected unauthorized refresh failure to stop further proactive refresh")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected unauthorized refresh failure to leave the auto-refresh schedule")
	}

	got, errPick := manager.scheduler.pickSingle(ctx, "codex", "codex-keep-at-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v, want still-valid credential to remain selectable", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() auth = %v, want %q", got, auth.ID)
	}
}

func TestManager_ProactiveRefreshUnauthorizedEvictsExpiredAccessToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	now := time.Now()
	auth := &Auth{
		ID:       "codex-expired-at",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":         "x@example.com",
			"access_token":  testAccessTokenJWT(now.Add(-time.Minute)),
			"refresh_token": "rt-reused",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable || updated.Status != StatusError || updated.StatusMessage != "unauthorized" {
		t.Fatalf("expired AT eviction = unavailable=%v status=%q message=%q, want unavailable error/unauthorized", updated.Unavailable, updated.Status, updated.StatusMessage)
	}
}

func TestManager_ReactiveRefreshUnauthorizedEvictsValidAccessToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	now := time.Now()
	token := testAccessTokenJWT(now.Add(38 * time.Hour))
	auth := &Auth{
		ID:       "codex-reactive-at",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":         "x@example.com",
			"access_token":  token,
			"refresh_token": "rt-reused",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, token); errRefresh == nil {
		t.Fatal("expected reactive refresh to fail")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable || updated.Status != StatusError || updated.StatusMessage != "unauthorized" {
		t.Fatalf("reactive eviction = unavailable=%v status=%q message=%q, want unavailable error/unauthorized", updated.Unavailable, updated.Status, updated.StatusMessage)
	}
}
