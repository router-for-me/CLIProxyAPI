package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
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

func TestFileTokenStoreSaveSyncsDisabledIntoStorageMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	path := filepath.Join(dir, "codex-test.json")
	record := &cliproxyauth.Auth{
		ID:       "codex-test.json",
		FileName: "codex-test.json",
		Disabled: false,
		Attributes: map[string]string{
			"path": path,
		},
		Metadata: map[string]any{
			"type":     "codex",
			"email":    "u@example.com",
			"disabled": true,
		},
		Storage: &codexauth.CodexTokenStorage{
			Email: "u@example.com",
		},
	}

	if _, err := store.Save(context.Background(), record); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	disabled, _ := payload["disabled"].(bool)
	if disabled {
		t.Fatalf("expected disabled=false in persisted payload, got true: %s", string(raw))
	}
}
