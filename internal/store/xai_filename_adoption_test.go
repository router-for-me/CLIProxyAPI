package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestFileNameStoresExposeBaseDirCapability pins the capability marker the
// login flows use to decide whether renaming onto a richer credential file
// is safe: every built-in backend resolves the save path from the record
// name and must keep exposing SetBaseDir.
func TestFileNameStoresExposeBaseDirCapability(t *testing.T) {
	stores := map[string]cliproxyauth.Store{
		"git":      &GitTokenStore{},
		"postgres": &PostgresStore{},
		"object":   &ObjectTokenStore{},
	}
	for name, store := range stores {
		if _, ok := store.(interface{ SetBaseDir(string) }); !ok {
			t.Errorf("%s store does not expose SetBaseDir; xAI filename adoption would be disabled for it", name)
		}
	}
}

// TestGitTokenStoreSaveResolvesFromRecordFileName proves the behavior the
// adoption rename relies on outside FileTokenStore: when a record's name is
// changed before Save, the new file lands at the adopted (email) name and
// not at the superseded (subject) name it carried before the rename.
func TestGitTokenStoreSaveResolvesFromRecordFileName(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	if _, errInit := git.PlainInit(remoteDir, true); errInit != nil {
		t.Fatalf("PlainInit error: %v", errInit)
	}
	store := NewGitTokenStore(remoteDir, "", "", "")
	authDir := filepath.Join(root, "work", "auths")
	store.SetBaseDir(authDir)
	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository error: %v", errEnsure)
	}

	const emailFileName = "xai-user@example.com-0123456789abcdef.json"
	const subjectFileName = "xai-sub-1-fedcba9876543210.json"
	record := &cliproxyauth.Auth{
		ID:       subjectFileName,
		FileName: subjectFileName,
		Provider: "xai",
		Metadata: map[string]any{
			"type":         "xai",
			"sub":          "sub-1",
			"access_token": "token-1",
		},
	}
	// Simulate the login-flow adoption rename onto the richer email file.
	record.ID = emailFileName
	record.FileName = emailFileName
	record.Metadata["email"] = "user@example.com"

	savedPath, errSave := store.Save(context.Background(), record)
	if errSave != nil {
		t.Fatalf("Save error: %v", errSave)
	}
	if want := filepath.Join(authDir, emailFileName); savedPath != want {
		t.Fatalf("savedPath = %s, want adopted email path %s", savedPath, want)
	}
	if _, errStat := os.Stat(savedPath); errStat != nil {
		t.Fatalf("saved credential missing at %s: %v", savedPath, errStat)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, subjectFileName)); !os.IsNotExist(errStat) {
		t.Fatalf("superseded subject file should not exist: %v", errStat)
	}
}

// TestRemoteStoresSaveResolvesFromRecordFileName proves the same rename
// behavior for the Postgres and object backends: the remote key follows the
// resolved local path, so an adopted email filename is what reaches the
// remote store.
func TestRemoteStoresSaveResolvesFromRecordFileName(t *testing.T) {
	const emailFileName = "xai-user@example.com-0123456789abcdef.json"
	const subjectFileName = "xai-sub-1-fedcba9876543210.json"

	tests := []struct {
		name         string
		build        func(authDir string, onRemoteWrite func(id string, data []byte) error) cliproxyauth.Store
		wantRemoteID string
	}{
		{
			name: "postgres",
			build: func(authDir string, onRemoteWrite func(id string, data []byte) error) cliproxyauth.Store {
				return &PostgresStore{
					authDir: authDir,
					cfg:     PostgresStoreConfig{AuthTable: "auth_tokens"},
					persistAuthFn: func(_ context.Context, relID string, data []byte) error {
						return onRemoteWrite(relID, data)
					},
				}
			},
			wantRemoteID: emailFileName,
		},
		{
			name: "object",
			build: func(authDir string, onRemoteWrite func(id string, data []byte) error) cliproxyauth.Store {
				return &ObjectTokenStore{
					authDir: authDir,
					cfg:     ObjectStoreConfig{Bucket: "bucket"},
					putObjectFn: func(_ context.Context, key string, data []byte, _ string) error {
						return onRemoteWrite(key, data)
					},
				}
			},
			wantRemoteID: "auths/" + emailFileName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authDir := filepath.Join(t.TempDir(), "auths")
			if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			remoteSaved := make(map[string]string)
			store := tt.build(authDir, func(id string, data []byte) error {
				remoteSaved[id] = string(data)
				return nil
			})

			record := &cliproxyauth.Auth{
				ID:       subjectFileName,
				FileName: subjectFileName,
				Provider: "xai",
				Metadata: map[string]any{
					"type":         "xai",
					"sub":          "sub-1",
					"access_token": "token-1",
				},
			}
			// Simulate the login-flow adoption rename onto the email file.
			record.ID = emailFileName
			record.FileName = emailFileName
			record.Metadata["email"] = "user@example.com"

			savedPath, errSave := store.Save(context.Background(), record)
			if errSave != nil {
				t.Fatalf("Save error: %v", errSave)
			}
			if want := filepath.Join(authDir, emailFileName); savedPath != want {
				t.Fatalf("savedPath = %s, want adopted email path %s", savedPath, want)
			}
			remotePayload, ok := remoteSaved[tt.wantRemoteID]
			if !ok {
				t.Fatalf("remote records = %v, want key %q", remoteSaved, tt.wantRemoteID)
			}
			if !strings.Contains(remotePayload, `"access_token":"token-1"`) {
				t.Fatalf("remote payload = %s, want refreshed token", remotePayload)
			}
			if _, errStat := os.Stat(filepath.Join(authDir, subjectFileName)); !os.IsNotExist(errStat) {
				t.Fatalf("superseded subject file should not exist: %v", errStat)
			}
		})
	}
}
