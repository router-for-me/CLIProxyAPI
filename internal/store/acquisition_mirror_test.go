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

func TestAcquiredAuthRemoteFailurePreservesMirror(t *testing.T) {
	for _, backend := range []string{"git", "object", "postgres"} {
		for _, existing := range []bool{false, true} {
			name := "new"
			if existing {
				name = "replacement"
			}
			t.Run(backend+"/"+name, func(t *testing.T) {
				authDir := filepath.Join(t.TempDir(), "workspace", "auths")
				entered := make(chan struct{})
				remoteSave := func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}
				var tokenStore cliproxyauth.Store
				switch backend {
				case "git":
					remote := filepath.Join(t.TempDir(), "remote.git")
					if _, err := git.PlainInit(remote, true); err != nil {
						t.Fatal(err)
					}
					s := NewGitTokenStore(remote, "", "", "")
					s.SetBaseDir(authDir)
					if err := s.EnsureRepository(); err != nil {
						t.Fatal(err)
					}
					s.pushFn = func(ctx context.Context, _ *git.Repository, _ *git.PushOptions) error { return remoteSave(ctx) }
					tokenStore = s
				case "object":
					tokenStore = &ObjectTokenStore{authDir: authDir, putObjectFn: func(ctx context.Context, _ string, _ []byte, _ string) error { return remoteSave(ctx) }}
				case "postgres":
					tokenStore = &PostgresStore{authDir: authDir, persistAuthFn: func(ctx context.Context, _ string, _ []byte) error { return remoteSave(ctx) }}
				}
				if err := os.MkdirAll(authDir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(authDir, "device.json")
				original := []byte(`{"type":"codex","access_token":"original","note":"keep"}`)
				if existing {
					if err := os.WriteFile(path, original, 0o640); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(cliproxyauth.WithAuthCreationIntent(context.Background()))
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := tokenStore.Save(ctx, &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement"}})
					done <- err
				}()
				<-entered
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("Save error = %v, want cancellation", err)
				}
				got, err := os.ReadFile(path)
				if !existing {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed acquisition left a readable credential: %q, %v", got, err)
					}
				} else {
					if err != nil || string(got) != string(original) {
						t.Fatalf("failed acquisition replaced prior credentials: %q, %v", got, err)
					}
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o640 {
						t.Fatalf("old credential permissions were not restored: %v, %v", info, err)
					}
				}
			})
		}
	}
}

func TestGitAcquisitionLeaseFailureRestoresMirrorAndIndex(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "baseline\n"})
			storeA := NewGitTokenStore(remote, "", "", "")
			storeA.SetBaseDir(filepath.Join(root, "a", "auths"))
			if err := storeA.EnsureRepository(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(storeA.AuthDir(), "device.json")
			var original []byte
			if existing {
				if _, err := storeA.Save(context.Background(), &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "original"}}); err != nil {
					t.Fatal(err)
				}
				var err error
				original, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			storeB := NewGitTokenStore(remote, "", "", "")
			storeB.SetBaseDir(filepath.Join(root, "b", "auths"))
			if err := storeB.EnsureRepository(); err != nil {
				t.Fatal(err)
			}
			repo, err := git.PlainOpen(filepath.Dir(storeA.AuthDir()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := repo.Close(); err != nil {
					t.Error(err)
				}
			}()
			before, err := repo.Head()
			if err != nil {
				t.Fatal(err)
			}
			record := &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement"}}
			record.Storage = &callbackTokenStorage{save: func(path string) error {
				if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"replacement"}`), 0o600); err != nil {
					return err
				}
				_, err := storeB.Save(context.Background(), &cliproxyauth.Auth{ID: "concurrent.json", FileName: "concurrent.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "independent"}})
				return err
			}}
			if _, err := storeA.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record); err == nil {
				t.Fatal("Save succeeded despite the competing remote update")
			}
			if existing {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(original) {
					t.Fatalf("old mirror was not restored: %q, %v", got, err)
				}
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed new credential remains in mirror: %v", err)
			}
			after, err := repo.Head()
			if err != nil || after.Hash() != before.Hash() {
				t.Fatalf("local HEAD changed after rejected acquisition: %v, %v", after, err)
			}
			index, err := repo.Storer.Index()
			if err != nil {
				t.Fatal(err)
			}
			entry, err := index.Entry("auths/device.json")
			if existing {
				if err != nil {
					t.Fatal(err)
				}
				commit, err := repo.CommitObject(before.Hash())
				if err != nil {
					t.Fatal(err)
				}
				file, err := commit.File("auths/device.json")
				if err != nil || entry.Hash != file.Hash {
					t.Fatalf("index retained the failed credential: %v", err)
				}
			} else if err == nil {
				t.Fatal("index retained the failed new credential")
			}
			if _, err := storeA.Save(context.Background(), &cliproxyauth.Auth{ID: "later.json", FileName: "later.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "later"}}); err != nil {
				t.Fatal(err)
			}
			assertRemoteTreePath(t, remote, "master", "auths/concurrent.json", true)
			assertRemoteTreePath(t, remote, "master", "auths/later.json", true)
			if existing {
				assertRemoteFileContents(t, remote, "master", "auths/device.json", string(original))
			} else {
				assertRemoteTreePath(t, remote, "master", "auths/device.json", false)
			}
		})
	}
}

func TestAcquisitionMirrorDoesNotOverwriteIndependentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.json")
	original := []byte(`{"type":"codex","access_token":"original"}`)
	independent := []byte(`{"type":"codex","access_token":"independent"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	errRemote := errors.New("remote unavailable")
	s := &ObjectTokenStore{authDir: dir, putObjectFn: func(context.Context, string, []byte, string) error {
		if err := os.WriteFile(path, independent, 0o600); err != nil {
			return err
		}
		return errRemote
	}}
	ctx := cliproxyauth.WithAuthCreationIntent(context.Background())
	_, err := s.Save(ctx, &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement"}})
	if !errors.Is(err, errRemote) {
		t.Fatalf("Save error = %v, want remote error", err)
	}
	got, errRead := os.ReadFile(path)
	if errRead != nil || string(got) != string(independent) {
		t.Fatalf("rollback overwrote independent credentials: %q, %v", got, errRead)
	}
}

func TestAcquiredAuthSuccessfulSaveKeepsMirror(t *testing.T) {
	for _, backend := range []string{"object", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			var remote []byte
			var s cliproxyauth.Store
			if backend == "object" {
				s = &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, _ string, data []byte, _ string) error {
					remote = append([]byte(nil), data...)
					return nil
				}}
			} else {
				s = &PostgresStore{authDir: dir, persistAuthFn: func(_ context.Context, _ string, data []byte) error {
					remote = append([]byte(nil), data...)
					return nil
				}}
			}
			record := &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}}
			path, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(remote) || len(remote) == 0 || path != filepath.Join(dir, record.FileName) {
				t.Fatalf("committed mirror differs from remote: path=%q, %q != %q, %v", path, got, remote, err)
			}
		})
	}
}
