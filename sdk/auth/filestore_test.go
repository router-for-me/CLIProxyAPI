package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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

func TestFileTokenStoreSave_RejectsPathOutsideBaseDir(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	baseDir := t.TempDir()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:       "outside.json",
		FileName: "../../outside.json",
		Metadata: map[string]any{"type": "kiro"},
	}

	_, err := store.Save(context.Background(), auth)
	if err == nil {
		t.Fatalf("expected save to reject path traversal")
	}
<<<<<<< HEAD
	if !strings.Contains(err.Error(), "escapes base directory") && !strings.Contains(err.Error(), "path traversal") {
=======
	if !strings.Contains(err.Error(), "escapes base directory") {
>>>>>>> archive/pr-234-head-20260223
		t.Fatalf("unexpected error: %v", err)
	}
}

<<<<<<< HEAD
func TestFileTokenStoreSave_RejectsEncodedAndWindowsTraversalPath(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	baseDir := t.TempDir()
	store.SetBaseDir(baseDir)

	for _, path := range []string{"..\\\\outside.json", "..//..%2foutside.json"} {
		auth := &cliproxyauth.Auth{
			ID:       "x",
			FileName: path,
			Metadata: map[string]any{"type": "kiro"},
		}
		if _, err := store.Save(context.Background(), auth); err == nil {
			t.Fatalf("expected encoded/windows traversal path to be rejected: %s", path)
		}
	}
}

=======
>>>>>>> archive/pr-234-head-20260223
func TestFileTokenStoreDelete_RejectsAbsolutePathOutsideBaseDir(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	baseDir := t.TempDir()
	store.SetBaseDir(baseDir)

	outside := filepath.Join(filepath.Dir(baseDir), "outside.json")
	err := store.Delete(context.Background(), outside)
	if err == nil {
		t.Fatalf("expected delete to reject absolute path outside base dir")
	}
	if !strings.Contains(err.Error(), "escapes base directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}
