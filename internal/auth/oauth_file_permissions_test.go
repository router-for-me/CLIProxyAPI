package auth_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/vertex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
)

type tokenFileSaver interface {
	SaveTokenToFile(string) error
}

func TestTokenStorageSaveRestrictsFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix file permissions")
	}

	tests := []struct {
		name    string
		storage tokenFileSaver
	}{
		{
			name: "claude",
			storage: &claude.ClaudeTokenStorage{
				AccessToken: "access-token", RefreshToken: "refresh-token",
			},
		},
		{
			name: "codex",
			storage: &codex.CodexTokenStorage{
				AccessToken: "access-token", RefreshToken: "refresh-token",
			},
		},
		{
			name: "kimi",
			storage: &kimi.KimiTokenStorage{
				AccessToken: "access-token", RefreshToken: "refresh-token",
			},
		},
		{
			name: "vertex",
			storage: &vertex.VertexCredentialStorage{
				ServiceAccount: map[string]any{"type": "service_account"},
			},
		},
		{
			name: "xai",
			storage: &xai.TokenStorage{
				AccessToken: "access-token", RefreshToken: "refresh-token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.name+".json")
			if errWrite := os.WriteFile(path, []byte(`{"insecure":true}`), 0o644); errWrite != nil {
				t.Fatalf("os.WriteFile() error = %v", errWrite)
			}
			if errSave := tt.storage.SaveTokenToFile(path); errSave != nil {
				t.Fatalf("SaveTokenToFile() error = %v", errSave)
			}

			info, errStat := os.Stat(path)
			if errStat != nil {
				t.Fatalf("os.Stat() error = %v", errStat)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("file permissions = %o, want 600", got)
			}
		})
	}
}
