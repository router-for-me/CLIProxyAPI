package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFileTokenStoreSaveMetadataRestrictsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix file permissions")
	}

	for _, name := range []string{"new", "unchanged", "changed"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "antigravity.json")
			var before os.FileInfo
			if name != "new" {
				token := "new-token"
				if name == "changed" {
					token = "old-token"
				}
				if err := os.WriteFile(path, []byte(`{"type":"antigravity","access_token":"`+token+`","disabled":false}`), 0o600); err != nil {
					t.Fatal(err)
				}
				// Set the insecure mode explicitly so the process umask cannot hide the regression.
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}

			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			record := &cliproxyauth.Auth{
				ID: "antigravity.json",
				Metadata: map[string]any{
					"type": "antigravity", "access_token": "new-token",
				},
			}
			if _, err := store.Save(context.Background(), record); err != nil {
				t.Fatalf("Save: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := after.Mode().Perm(); got != 0o600 {
				t.Errorf("file permissions = %04o, want 0600", got)
			}
			if before != nil && !os.SameFile(before, after) {
				t.Error("Save replaced the existing credential inode")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["access_token"] != "new-token" || saved["disabled"] != false {
				t.Fatalf("unexpected saved metadata: %v", saved)
			}
		})
	}
}
