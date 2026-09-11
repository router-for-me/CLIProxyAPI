package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

type invalidRefreshTokenExecutor struct {
	schedulerProviderTestExecutor
}

func (e invalidRefreshTokenExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, errors.New(`token refresh failed with status 400: {"error":"invalid_refresh_token","error_description":"secret-refresh-detail-should-not-log"}`)
}

func TestManager_RefreshAuthLogsAuthFileBasenameOnInvalidRefreshToken(t *testing.T) {
	hook := setupTestLoggerHook(t)
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(invalidRefreshTokenExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
	})

	pastExpiry := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "should-not-appear-as-path",
		FileName: "account.json",
		Provider: "antigravity",
		Status:   StatusActive,
		Attributes: map[string]string{
			"path": "/hidden/auth-dir/account.json",
		},
		Metadata: map[string]any{
			"access_token": "expired-access-token",
			"expired":      pastExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	var saw bool
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel {
			continue
		}
		if !strings.Contains(entry.Message, "credential refresh failed") {
			continue
		}
		saw = true
		if got, _ := entry.Data["auth_file"].(string); got != "account.json" {
			t.Fatalf("refresh warn auth_file field = %v, want account.json (msg=%s data=%v)", entry.Data["auth_file"], entry.Message, entry.Data)
		}
		if got, _ := entry.Data["provider"].(string); got != "antigravity" {
			t.Fatalf("refresh warn provider field = %v, want antigravity (msg=%s data=%v)", entry.Data["provider"], entry.Message, entry.Data)
		}
		diagnostic, _ := entry.Data["diagnostic"].(string)
		if diagnostic == "" {
			t.Fatalf("refresh warn missing diagnostic field: msg=%s data=%v", entry.Message, entry.Data)
		}
		combined := entry.Message + " " + diagnostic
		if strings.Contains(combined, "hidden") || strings.Contains(combined, "auth-dir") {
			t.Fatalf("refresh warn leaked path: msg=%s diagnostic=%s", entry.Message, diagnostic)
		}
		if strings.Contains(combined, "expired-access-token") {
			t.Fatalf("refresh warn leaked token: msg=%s diagnostic=%s", entry.Message, diagnostic)
		}
		if !strings.Contains(diagnostic, "invalid_refresh_token") {
			t.Fatalf("refresh warn diagnostic missing invalid_refresh_token: %s", diagnostic)
		}
		if strings.Contains(entry.Message, "auth_file=") || strings.Contains(entry.Message, "antigravity") {
			t.Fatalf("refresh warn still interpolates structured values into message: %s", entry.Message)
		}
	}
	if !saw {
		t.Fatalf("expected credential refresh failed warn, got %#v", hook.AllEntries())
	}
}
