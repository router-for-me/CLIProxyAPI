package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestApplyCoreAuthAddOrUpdate_PreservesRuntimeStateWhenRuntimeMetadataMissing(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{coreManager: manager}
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	existing := &coreauth.Auth{
		ID:               "a1",
		Provider:         "claude",
		Status:           coreauth.StatusError,
		StatusMessage:    "cooling",
		Unavailable:      true,
		LastRefreshedAt:  now.Add(-2 * time.Hour),
		NextRefreshAfter: now.Add(30 * time.Minute),
		NextRetryAfter:   now.Add(10 * time.Minute),
		Quota: coreauth.QuotaState{
			Exceeded: true,
			Reason:   "rate_limited",
		},
		LastError: &coreauth.Error{
			Code:       "rate_limit",
			Message:    "too many requests",
			HTTPStatus: 429,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-3-7-sonnet": {
				Status:      coreauth.StatusError,
				Unavailable: true,
			},
		},
		Metadata: map[string]any{"type": "claude", "email": "u@example.com"},
	}
	if _, err := manager.Register(context.Background(), existing); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	incoming := &coreauth.Auth{
		ID:       "a1",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "claude", "email": "u@example.com"},
	}
	service.applyCoreAuthAddOrUpdate(context.Background(), incoming)

	got, ok := manager.GetByID("a1")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if got.Status != coreauth.StatusError {
		t.Fatalf("status = %s, want %s", got.Status, coreauth.StatusError)
	}
	if got.StatusMessage != "cooling" {
		t.Fatalf("status message = %q, want %q", got.StatusMessage, "cooling")
	}
	if !got.Unavailable {
		t.Fatal("unavailable = false, want true")
	}
	if got.NextRetryAfter.IsZero() {
		t.Fatal("next retry cleared, want preserved")
	}
	if got.LastError == nil || got.LastError.HTTPStatus != 429 {
		t.Fatalf("last error = %#v, want HTTPStatus=429", got.LastError)
	}
	if len(got.ModelStates) != 1 {
		t.Fatalf("model states len = %d, want 1", len(got.ModelStates))
	}
	if !got.LastRefreshedAt.Equal(existing.LastRefreshedAt) {
		t.Fatalf("last refreshed = %v, want %v", got.LastRefreshedAt, existing.LastRefreshedAt)
	}
	if !got.NextRefreshAfter.Equal(existing.NextRefreshAfter) {
		t.Fatalf("next refresh after = %v, want %v", got.NextRefreshAfter, existing.NextRefreshAfter)
	}
}

func TestApplyCoreAuthAddOrUpdate_RuntimeMetadataTakesPrecedence(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{coreManager: manager}
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	existing := &coreauth.Auth{
		ID:            "a2",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: "old-state",
		Unavailable:   true,
		Metadata:      map[string]any{"type": "claude", "email": "u@example.com"},
	}
	if _, err := manager.Register(context.Background(), existing); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	incoming := &coreauth.Auth{
		ID:       "a2",
		Provider: "claude",
		Metadata: map[string]any{
			"type":                                 "claude",
			"email":                                "u@example.com",
			coreauth.MetadataKeyRuntimeStatus:      string(coreauth.StatusActive),
			coreauth.MetadataKeyRuntimeStatusMsg:   "",
			coreauth.MetadataKeyRuntimeUnavailable: false,
			coreauth.MetadataKeyRuntimeNextRetry:   now.Format(time.RFC3339Nano),
		},
	}
	coreauth.RestoreRuntimeStateFromMetadata(incoming, incoming.Metadata)
	service.applyCoreAuthAddOrUpdate(context.Background(), incoming)

	got, ok := manager.GetByID("a2")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if got.Status != coreauth.StatusActive {
		t.Fatalf("status = %s, want %s", got.Status, coreauth.StatusActive)
	}
	if got.Unavailable {
		t.Fatal("unavailable = true, want false")
	}
}
