package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestFileTokenStoreListReturnsAuthFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), []byte(`{"type":"custom"}`), 0o600); err != nil {
		t.Fatalf("write valid auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"type":`), 0o600); err != nil {
		t.Fatalf("write broken auth: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	entries, err := store.List(context.Background())
	if err == nil {
		t.Fatal("List succeeded, want error for broken auth file")
	}
	if entries != nil {
		t.Fatalf("entries = %#v, want nil on error", entries)
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("error = %q, want broken file path", err.Error())
	}
}
