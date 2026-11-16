package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRemoteStoresDisabledLoginReachesTokenStorage(t *testing.T) {
	tests := []struct {
		name  string
		store cliproxyauth.Store
	}{
		{
			name: "postgres",
			store: &PostgresStore{
				authDir: filepath.Join(t.TempDir(), "auths"),
			},
		},
		{
			name: "object",
			store: &ObjectTokenStore{
				authDir: filepath.Join(t.TempDir(), "auths"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errReachedStorage := errors.New("reached token storage")
			storageReached := false
			auth := &cliproxyauth.Auth{
				ID:       "canonical-disabled.json",
				FileName: "canonical-disabled.json",
				Provider: "claude",
				Disabled: true,
				Storage: &callbackTokenStorage{save: func(string) error {
					storageReached = true
					return errReachedStorage
				}},
				Metadata: map[string]any{"type": "claude"},
			}

			savedPath, errSave := tt.store.Save(context.Background(), auth)
			if errSave != nil {
				t.Fatalf("runtime Save() error = %v", errSave)
			}
			if savedPath != "" {
				t.Fatalf("runtime Save() path = %q, want empty", savedPath)
			}
			if storageReached {
				t.Fatal("runtime Save() reached token storage for a missing disabled credential")
			}

			ctx := cliproxyauth.WithAuthCreationIntent(context.Background())
			_, errSave = tt.store.Save(ctx, auth)
			if !errors.Is(errSave, errReachedStorage) {
				t.Fatalf("Save() error = %v, want token storage sentinel", errSave)
			}
		})
	}
}

func TestRemoteStoresSaveDoesNotShortCircuitWhenLocalFileMatches(t *testing.T) {
	errRemoteDown := errors.New("simulated remote write error")
	tests := []struct {
		name  string
		build func(authDir string, onRemoteWrite func(id string, data []byte) error) cliproxyauth.Store
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authDir := filepath.Join(t.TempDir(), "auths")
			if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			failRemote := true
			remoteSaved := make(map[string]string)
			store := tt.build(authDir, func(id string, data []byte) error {
				if failRemote {
					return errRemoteDown
				}
				remoteSaved[id] = string(data)
				return nil
			})
			auth := &cliproxyauth.Auth{
				ID:       "partial.json",
				FileName: "partial.json",
				Provider: "antigravity",
				Metadata: map[string]any{"type": "antigravity"},
			}

			// First Save writes the local file, then fails on the remote backend.
			if _, errSave := store.Save(context.Background(), auth); !errors.Is(errSave, errRemoteDown) {
				t.Fatalf("first Save() error = %v, want %v", errSave, errRemoteDown)
			}
			if len(remoteSaved) != 0 {
				t.Fatalf("expected no remote records after failed Save, got %v", remoteSaved)
			}
			localBytes, errRead := os.ReadFile(filepath.Join(authDir, "partial.json"))
			if errRead != nil || string(localBytes) != `{"disabled":false,"type":"antigravity"}` {
				t.Fatalf("unexpected local mirror file after failed remote Save: %q (%v)", string(localBytes), errRead)
			}

			// Second Save with identical metadata must still sync to the remote backend.
			failRemote = false
			if _, errSave := store.Save(context.Background(), auth); errSave != nil {
				t.Fatalf("second Save() after remote recovery failed: %v", errSave)
			}
			if len(remoteSaved) != 1 {
				t.Fatalf("expected 1 remote record after retry, got %v", remoteSaved)
			}
			for _, payload := range remoteSaved {
				if payload != `{"disabled":false,"type":"antigravity"}` {
					t.Fatalf("remote payload = %q, want %q", payload, `{"disabled":false,"type":"antigravity"}`)
				}
			}
		})
	}
}

func TestGitTokenStoreSaveHonorsContextCancellationDuringPullAndPush(t *testing.T) {
	for _, stage := range []string{"pull", "push"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			remoteDir := filepath.Join(root, "remote.git")
			if _, errInit := git.PlainInit(remoteDir, true); errInit != nil {
				t.Fatal(errInit)
			}
			workDir := filepath.Join(root, "work")
			store := NewGitTokenStore(remoteDir, "", "", "")
			store.SetBaseDir(filepath.Join(workDir, "auths"))
			if errEnsure := store.EnsureRepository(); errEnsure != nil {
				t.Fatal(errEnsure)
			}

			stageEntered := make(chan struct{})
			if stage == "pull" {
				store.pullFn = func(ctx context.Context, _ *git.Worktree, _ *git.PullOptions) error {
					close(stageEntered)
					<-ctx.Done()
					return ctx.Err()
				}
			} else {
				store.pushFn = func(ctx context.Context, _ *git.Repository, _ *git.PushOptions) error {
					close(stageEntered)
					<-ctx.Done()
					return ctx.Err()
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			saveDone := make(chan error, 1)
			go func() {
				_, errSave := store.Save(ctx, &cliproxyauth.Auth{
					ID:       "git-cancel.json",
					FileName: "git-cancel.json",
					Provider: "antigravity",
					Metadata: map[string]any{"type": "antigravity", "access_token": "tok"},
				})
				saveDone <- errSave
			}()
			<-stageEntered
			cancel()
			if errSave := <-saveDone; !errors.Is(errSave, context.Canceled) {
				t.Fatalf("Save() during %s error = %v, want context.Canceled", stage, errSave)
			}
		})
	}
}
