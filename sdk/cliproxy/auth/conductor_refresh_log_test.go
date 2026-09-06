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
	return nil, errors.New(`token refresh failed with status 400: {"error":"invalid_refresh_token"}`)
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
		if !strings.Contains(entry.Message, "auth_file=account.json") {
			t.Fatalf("refresh warn missing auth file basename: %s", entry.Message)
		}
		if strings.Contains(entry.Message, "hidden") || strings.Contains(entry.Message, "auth-dir") {
			t.Fatalf("refresh warn leaked path: %s", entry.Message)
		}
		if strings.Contains(entry.Message, "expired-access-token") {
			t.Fatalf("refresh warn leaked token: %s", entry.Message)
		}
		if !strings.Contains(entry.Message, "invalid_refresh_token") {
			t.Fatalf("refresh warn missing invalid_refresh_token: %s", entry.Message)
		}
	}
	if !saw {
		t.Fatalf("expected credential refresh failed warn, got %#v", hook.AllEntries())
	}
}
