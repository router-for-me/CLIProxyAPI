package auth

import (
	"context"
	"os"
	"path/filepath"
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

func TestFileTokenStoreListReadsProxyURL(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "proxy.json"), []byte(`{"type":"claude","proxy_url":" http://proxy.local:8080 "}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("len(auths) = %d, want 1", len(auths))
	}
	if got := auths[0].ProxyURL; got != "http://proxy.local:8080" {
		t.Fatalf("ProxyURL = %q, want %q", got, "http://proxy.local:8080")
	}
}
