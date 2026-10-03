package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/refreshdiagnostic"
	log "github.com/sirupsen/logrus"
)

func TestTerminalRefreshDiagnosticDedupAndRecovery(t *testing.T) {
	hook := setupTestLoggerHook(t)
	log.SetLevel(log.InfoLevel)
	manager := NewManager(nil, nil, nil)
	executor := &mockOAuthErrorExecutor{id: "codex", errToReturn: refreshdiagnostic.FromResponse(401, []byte(`{"error":{"code":"refresh_token_expired","message":"secret"}}`))}
	manager.RegisterExecutor(executor)
	ctx := context.Background()
	_, err := manager.Register(ctx, &Auth{ID: "private@example.com.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"refresh_token": "fixture", "access_token": "fixture", "expired": time.Now().Add(-time.Hour).Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, _ = manager.ForceRefreshAuth(ctx, "private@example.com.json")
	}
	failures := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == "OAuth refresh requires sign-in; use OAuth Login" {
			failures++
			if strings.Contains(fmt.Sprint(entry.Data), "private") || strings.Contains(fmt.Sprint(entry.Data), "secret") {
				t.Fatal("identity or token logged")
			}
		}
	}
	if failures != 1 {
		t.Fatalf("warning count %d", failures)
	}
	results := manager.ForceRefreshAll(ctx)
	if len(results) != 1 || !results[0].ReauthRequired || results[0].Reason != "refresh_token_expired" {
		t.Fatalf("results %+v", results)
	}
	auth, ok := manager.GetByID("private@example.com.json")
	if !ok || !hasUnauthorizedAuthFailure(auth) || manager.shouldRefresh(auth, time.Now()) {
		t.Fatal("terminal credential keeps refreshing")
	}
	executor.errToReturn = nil
	_, err = manager.ForceRefreshAuth(ctx, "private@example.com.json")
	if err != nil {
		t.Fatal(err)
	}
	recovered := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == "OAuth refresh recovered" {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatalf("recovery count %d", recovered)
	}
}

func TestTerminalRefreshRetainsUsableAccessTokenUntilExpiry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &mockOAuthErrorExecutor{id: "codex", errToReturn: refreshdiagnostic.FromResponse(400, []byte(`{"code":"refresh_token_reused"}`))}
	manager.RegisterExecutor(executor)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	ctx := context.Background()
	_, err := manager.Register(ctx, &Auth{ID: "fixture.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "fixture", "refresh_token": "fixture", "expired": expiry.Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.ForceRefreshAuth(ctx, "fixture.json")
	auth, _ := manager.GetByID("fixture.json")
	if auth.Unavailable || auth.Status != StatusActive || !auth.NextRefreshAfter.Equal(expiry) {
		t.Fatalf("usable token disabled or refresh scheduled early: %+v", auth)
	}
}

func TestTerminalRefreshStopsWhenAccessTokenWasRejected(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&mockOAuthErrorExecutor{id: "codex", errToReturn: refreshdiagnostic.FromResponse(400, []byte(`{"code":"refresh_token_reused","message":"secret"}`))})
	ctx := context.Background()
	_, err := manager.Register(ctx, &Auth{ID: "fixture.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "rejected", "refresh_token": "fixture", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.refreshAuthForRequest(ctx, "fixture.json", "rejected")
	auth, _ := manager.GetByID("fixture.json")
	if !hasUnauthorizedAuthFailure(auth) || auth.LastError.Code != "refresh_token_reused" || strings.Contains(auth.StatusMessage, "secret") {
		t.Fatalf("terminal diagnostic lost: %+v", auth)
	}
}
