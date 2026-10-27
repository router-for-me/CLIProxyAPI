package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestObjectStoreListsNestedPendingPublication(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "team", "account.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var remote []byte
			s := &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, key string, data []byte, _ string) error {
				if key != "auths/team/account.json" {
					t.Fatalf("nested remote key = %q", key)
				}
				remote = append([]byte(nil), data...)
				staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".auth-acquisition-*", "credential"))
				if err != nil || len(staged) != 1 {
					t.Fatalf("staged nested credential: %v, %v", staged, err)
				}
				return os.Remove(staged[0])
			}}
			record := &cliproxyauth.Auth{ID: "team/account.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}}
			if _, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(remote, []byte(`"access_token":"committed"`)) {
				t.Fatalf("remote lost nested credential: %q", remote)
			}
			listed, err := s.List(context.Background())
			if err != nil || len(listed) != 1 || listed[0].ID != record.ID || listed[0].Metadata["access_token"] != "committed" {
				t.Fatalf("nested committed credential missing from listing: %#v, %v", listed, err)
			}
		})
	}
}
