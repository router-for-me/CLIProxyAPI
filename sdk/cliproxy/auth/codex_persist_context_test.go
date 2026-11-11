package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestPersistCodexAuthFileCanceledContextDoesNotWrite(t *testing.T) {
	for _, mode := range []string{"metadata", "storage", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "codex.json")
			original := []byte(`{"type":"codex","access_token":"original"}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			record := &Auth{Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement", "note": "updated"}}
			switch mode {
			case "storage":
				record.Storage = &codex.CodexTokenStorage{AccessToken: "replacement", ReplaceCredentials: true}
			case "refresh":
				record.CodexRefreshPatch = &codex.CredentialRefreshPatch{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			handled, err := PersistCodexAuthFile(ctx, path, record)
			if !handled || !errors.Is(err, context.Canceled) {
				t.Errorf("handled=%t, error=%v; want handled cancellation", handled, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(original) {
				t.Errorf("canceled persistence changed credentials: %s", got)
			}
		})
	}
}
