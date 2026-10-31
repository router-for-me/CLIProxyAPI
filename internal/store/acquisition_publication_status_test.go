package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRemoteStoresDisablePendingPublication(t *testing.T) {
	for _, backend := range []string{"git", "object", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "workspace", "auths")
			path := filepath.Join(dir, "account.json")
			first := true
			loseFirstMirror := func() error {
				if !first {
					return nil
				}
				first = false
				staged, err := filepath.Glob(filepath.Join(dir, ".auth-acquisition-*", "credential"))
				if err != nil || len(staged) != 1 {
					t.Fatalf("staged credential: %v, %v", staged, err)
				}
				return os.Remove(staged[0])
			}
			var s cliproxyauth.Store
			var remote []byte
			var gitRemote string
			switch backend {
			case "git":
				gitRemote = filepath.Join(t.TempDir(), "remote.git")
				if _, err := git.PlainInit(gitRemote, true); err != nil {
					t.Fatal(err)
				}
				gs := NewGitTokenStore(gitRemote, "", "", "")
				gs.SetBaseDir(dir)
				if err := gs.EnsureRepository(); err != nil {
					t.Fatal(err)
				}
				gs.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
					if err := repo.PushContext(ctx, opts); err != nil {
						return err
					}
					return loseFirstMirror()
				}
				s = gs
			case "object":
				s = &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, _ string, data []byte, _ string) error {
					remote = bytes.Clone(data)
					return loseFirstMirror()
				}}
			case "postgres":
				s = &PostgresStore{authDir: dir, persistAuthFn: func(_ context.Context, _ string, data []byte) error {
					remote = bytes.Clone(data)
					return loseFirstMirror()
				}}
			}
			auth := &cliproxyauth.Auth{ID: "account.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}}
			if _, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), auth); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) || !authpublication.HasPending(path) {
				t.Fatalf("fixture did not leave a missing committed mirror: %v", err)
			}
			auth.Disabled = true
			auth.Metadata["disabled"] = true
			if saved, err := s.Save(context.Background(), auth); err != nil || saved != path {
				t.Fatalf("disable skipped the committed publication: %q, %v", saved, err)
			}
			mirror, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(mirror, []byte(`"disabled":true`)) || !bytes.Contains(mirror, []byte(`"access_token":"committed"`)) || authpublication.HasPending(path) {
				t.Fatalf("disable lost credential or publication repair: %q, %v", mirror, err)
			}
			if gitRemote != "" {
				assertRemoteFileContents(t, gitRemote, "master", "auths/account.json", string(mirror))
			} else if !bytes.Equal(remote, mirror) {
				t.Fatalf("disable did not reach backend: %q, want %q", remote, mirror)
			}
			if errRemove := os.Remove(path); errRemove != nil {
				t.Fatal(errRemove)
			}
			if saved, errSave := s.Save(context.Background(), auth); errSave != nil || saved != "" {
				t.Fatalf("runtime save recreated a deliberately removed credential: %q, %v", saved, errSave)
			}
			if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
				t.Fatalf("genuine deletion fence lost: %v", errStat)
			}
		})
	}
}
