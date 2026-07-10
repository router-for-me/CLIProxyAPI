package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_SetTemporaryCooldown(t *testing.T) {
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-1", Provider: "xai"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	until := time.Now().Add(time.Hour)
	manager.SetTemporaryCooldown("auth-1", until, "plugin cooldown")

	auth, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("auth was not found after cooldown")
	}
	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = %v, want true", auth.Unavailable)
	}
	if !auth.NextRetryAfter.Equal(until) {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, until)
	}
	if auth.StatusMessage != "plugin cooldown" {
		t.Fatalf("auth.StatusMessage = %q, want %q", auth.StatusMessage, "plugin cooldown")
	}

	now := time.Now()
	blocked, _, _ := isAuthBlockedForModel(auth, "", now)
	if !blocked {
		t.Fatalf("isAuthBlockedForModel = %v, want auth blocked during cooldown", blocked)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown persisted %d times, want 1", got)
	}
}

func TestManager_SetTemporaryCooldown_PastUntilStaysAvailable(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-1", Provider: "xai"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	until := time.Now().Add(-time.Hour)
	manager.SetTemporaryCooldown("auth-1", until, "expired cooldown")

	auth, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("auth was not found after cooldown")
	}
	now := time.Now()
	blocked, _, _ := isAuthBlockedForModel(auth, "", now)
	if blocked {
		t.Fatalf("isAuthBlockedForModel = %v, want auth available for past cooldown", blocked)
	}
}
