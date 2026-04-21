package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/vertex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileTokenStoreSaveAndList_PreservesRuntimeState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	nextRetry := time.Date(2026, time.April, 23, 10, 0, 0, 0, time.UTC)
	nextRefresh := nextRetry.Add(-6 * time.Hour)
	lastRefresh := nextRetry.Add(-12 * time.Hour)
	modelUpdated := nextRetry.Add(-30 * time.Minute)
	path := filepath.Join(dir, "codex-user.json")

	auth := &cliproxyauth.Auth{
		ID:               "codex-user.json",
		Provider:         "codex",
		FileName:         "codex-user.json",
		Status:           cliproxyauth.StatusError,
		StatusMessage:    "quota exhausted",
		Unavailable:      true,
		Attributes:       map[string]string{"path": path},
		Metadata:         map[string]any{"type": "codex", "email": "user@example.com"},
		Quota:            cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRetry, BackoffLevel: 4},
		LastError:        &cliproxyauth.Error{Code: "quota_exhausted", Message: "quota exhausted", Retryable: true, HTTPStatus: 429},
		LastRefreshedAt:  lastRefresh,
		NextRefreshAfter: nextRefresh,
		NextRetryAfter:   nextRetry,
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gpt-5.4": {
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				LastError:      &cliproxyauth.Error{Code: "quota_exhausted", Message: "quota exhausted", Retryable: true, HTTPStatus: 429},
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRetry, BackoffLevel: 2},
				UpdatedAt:      modelUpdated,
			},
		},
	}

	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("List() count = %d, want 1", len(auths))
	}

	got := auths[0]
	if got.Status != cliproxyauth.StatusError {
		t.Fatalf("Status = %q, want %q", got.Status, cliproxyauth.StatusError)
	}
	if got.StatusMessage != "quota exhausted" {
		t.Fatalf("StatusMessage = %q, want %q", got.StatusMessage, "quota exhausted")
	}
	if !got.Unavailable {
		t.Fatalf("Unavailable = false, want true")
	}
	if !got.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("NextRetryAfter = %v, want %v", got.NextRetryAfter, nextRetry)
	}
	if !got.NextRefreshAfter.Equal(nextRefresh) {
		t.Fatalf("NextRefreshAfter = %v, want %v", got.NextRefreshAfter, nextRefresh)
	}
	if !got.LastRefreshedAt.Equal(lastRefresh) {
		t.Fatalf("LastRefreshedAt = %v, want %v", got.LastRefreshedAt, lastRefresh)
	}
	if !got.Quota.Exceeded || got.Quota.Reason != "quota" || !got.Quota.NextRecoverAt.Equal(nextRetry) || got.Quota.BackoffLevel != 4 {
		t.Fatalf("Quota = %+v, want exceeded quota until %v with backoff 4", got.Quota, nextRetry)
	}
	if got.LastError == nil || got.LastError.Code != "quota_exhausted" || got.LastError.HTTPStatus != 429 {
		t.Fatalf("LastError = %+v, want quota_exhausted/429", got.LastError)
	}
	state := got.ModelStates["gpt-5.4"]
	if state == nil {
		t.Fatalf("ModelStates[gpt-5.4] = nil")
	}
	if state.Status != cliproxyauth.StatusError || !state.Unavailable || !state.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("ModelState = %+v, want unavailable error until %v", state, nextRetry)
	}
	if state.LastError == nil || state.LastError.Code != "quota_exhausted" {
		t.Fatalf("ModelState.LastError = %+v, want quota_exhausted", state.LastError)
	}
}

func TestFileTokenStoreSave_ClearsStaleRuntimeKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	path := filepath.Join(dir, "healthy.json")
	auth := &cliproxyauth.Auth{
		ID:         "healthy.json",
		Provider:   "codex",
		FileName:   "healthy.json",
		Attributes: map[string]string{"path": path},
		Metadata: map[string]any{
			"type":             "codex",
			"email":            "user@example.com",
			"status":           string(cliproxyauth.StatusError),
			"status_message":   "stale",
			"unavailable":      true,
			"next_retry_after": "2026-04-23T10:00:00Z",
			"quota": map[string]any{
				"exceeded":        true,
				"reason":          "quota",
				"next_recover_at": "2026-04-23T10:00:00Z",
			},
			"model_states": map[string]any{
				"gpt-5.4": map[string]any{
					"status":           string(cliproxyauth.StatusError),
					"unavailable":      true,
					"next_retry_after": "2026-04-23T10:00:00Z",
				},
			},
		},
	}

	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var persisted map[string]any
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, key := range []string{"status", "status_message", "unavailable", "next_retry_after", "quota", "model_states"} {
		if _, ok := persisted[key]; ok {
			t.Fatalf("persisted stale key %q still present in %v", key, persisted)
		}
	}
}

func TestFileTokenStoreSave_InjectsRuntimeMetadataIntoTokenStorages(t *testing.T) {
	t.Parallel()

	nextRetry := time.Date(2026, time.April, 23, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		file    string
		storage any
	}{
		{
			name: "codebuddy",
			file: "codebuddy-user.json",
			storage: &codebuddy.CodeBuddyTokenStorage{
				AccessToken:  "access",
				RefreshToken: "refresh",
				Domain:       "global",
				UserID:       "user-1",
			},
		},
		{
			name: "copilot",
			file: "copilot-user.json",
			storage: &copilot.CopilotTokenStorage{
				AccessToken: "access",
				TokenType:   "bearer",
				Username:    "user-1",
				Email:       "user@example.com",
			},
		},
		{
			name: "kilo",
			file: "kilo-user.json",
			storage: &kilo.KiloTokenStorage{
				Token:          "access",
				OrganizationID: "org-1",
				Model:          "kilo-default",
				Email:          "user@example.com",
			},
		},
		{
			name: "kiro",
			file: "kiro-user.json",
			storage: &kiro.KiroTokenStorage{
				Type:         "kiro",
				AccessToken:  "access",
				RefreshToken: "refresh",
				ProfileArn:   "arn:aws:codewhisperer::profile/test",
				ExpiresAt:    "2026-04-23T12:00:00Z",
				AuthMethod:   "oauth",
				Provider:     "google",
				Email:        "user@example.com",
			},
		},
		{
			name: "vertex",
			file: "vertex-user.json",
			storage: &vertex.VertexCredentialStorage{
				ServiceAccount: map[string]any{
					"type":         "service_account",
					"project_id":   "proj-1",
					"client_email": "svc@example.com",
				},
				ProjectID: "proj-1",
				Email:     "svc@example.com",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			path := filepath.Join(dir, tc.file)

			auth := &cliproxyauth.Auth{
				ID:             tc.file,
				Provider:       tc.name,
				FileName:       tc.file,
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "refresh failed",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				Attributes:     map[string]string{"path": path},
				Metadata:       map[string]any{"label": "test-auth"},
				Storage:        tc.storage.(interface{ SaveTokenToFile(string) error }),
			}

			if _, err := store.Save(context.Background(), auth); err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}

			var persisted map[string]any
			if err = json.Unmarshal(raw, &persisted); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			if got, ok := persisted["status"].(string); !ok || got != string(cliproxyauth.StatusError) {
				t.Fatalf("status = %#v, want %q", persisted["status"], cliproxyauth.StatusError)
			}
			if got, ok := persisted["status_message"].(string); !ok || got != "refresh failed" {
				t.Fatalf("status_message = %#v, want %q", persisted["status_message"], "refresh failed")
			}
			if got, ok := persisted["unavailable"].(bool); !ok || !got {
				t.Fatalf("unavailable = %#v, want true", persisted["unavailable"])
			}
			if got, ok := persisted["next_retry_after"].(string); !ok || got != nextRetry.Format(time.RFC3339) {
				t.Fatalf("next_retry_after = %#v, want %q", persisted["next_retry_after"], nextRetry.Format(time.RFC3339))
			}
			if got, ok := persisted["label"].(string); !ok || got != "test-auth" {
				t.Fatalf("label = %#v, want %q", persisted["label"], "test-auth")
			}
		})
	}
}
