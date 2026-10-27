package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAcquisitionRemoteCommitSurvivesMirrorConflict(t *testing.T) {
	for _, backend := range []string{"git", "object", "postgres"} {
		for _, mutation := range []string{"rewrite", "replace", "remove", "directory", "create", "staging-lost"} {
			t.Run(backend+"/"+mutation, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "workspace", "auths")
				path := filepath.Join(dir, "device.json")
				original := []byte(`{"type":"codex","access_token":"original"}`)
				var remoteBytes []byte
				var gitRemote string
				mutate := func() error {
					switch mutation {
					case "staging-lost":
						staged, err := filepath.Glob(filepath.Join(dir, ".auth-acquisition-*", "credential"))
						if err != nil || len(staged) != 1 {
							t.Fatalf("staged credential: %v, %v", staged, err)
						}
						return os.Remove(staged[0])
					case "rewrite", "create":
						return os.WriteFile(path, []byte(`{"type":"codex","access_token":"independent"}`), 0o600)
					case "replace":
						replacement := filepath.Join(dir, "replacement")
						if err := os.WriteFile(replacement, []byte(`{"type":"codex","access_token":"independent"}`), 0o600); err != nil {
							return err
						}
						return os.Rename(replacement, path)
					case "remove":
						return os.Remove(path)
					case "directory":
						if err := os.Remove(path); err != nil {
							return err
						}
						return os.Mkdir(path, 0o700)
					}
					return nil
				}
				var s cliproxyauth.Store
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
						return mutate()
					}
					s = gs
				case "object":
					s = &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, _ string, data []byte, _ string) error {
						remoteBytes = append([]byte(nil), data...)
						return mutate()
					}}
				case "postgres":
					s = &PostgresStore{authDir: dir, persistAuthFn: func(_ context.Context, _ string, data []byte) error {
						remoteBytes = append([]byte(nil), data...)
						return mutate()
					}}
				}
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if mutation != "create" {
					if err := os.WriteFile(path, original, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				record := &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}}
				saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record)
				if err != nil || saved != path {
					t.Errorf("successful remote commit reported save failure: %q, %v", saved, err)
				}
				if mutation == "directory" {
					info, err := os.Stat(path)
					if err != nil || !info.IsDir() {
						t.Fatalf("publication removed independent directory: %v, %v", info, err)
					}
				} else if mutation == "staging-lost" {
					data, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(data, original) {
						t.Fatalf("old mirror should remain readable: %q, %v", data, err)
					}
				} else {
					data, err := os.ReadFile(path)
					if err != nil || !bytes.Contains(data, []byte(`"committed"`)) {
						t.Errorf("mirror was not reconciled to committed credential: %q, %v", data, err)
					}
				}
				expected := `{"access_token":"committed","disabled":false,"type":"codex"}`
				if mutation == "directory" || mutation == "staging-lost" {
					if got := record.CommittedAuthFile(saved); string(got) != expected {
						t.Errorf("notification snapshot = %q, want remotely committed bytes %q", got, expected)
					}
				} else if got := record.CommittedAuthFile(saved); len(got) != 0 {
					t.Errorf("successful publication retained a notification snapshot: %q", got)
				}
				if gitRemote != "" {
					assertRemoteFileContents(t, gitRemote, "master", "auths/device.json", expected)
				} else if string(remoteBytes) != expected {
					t.Errorf("remote retained %q, want %q", remoteBytes, expected)
				}
			})
		}
	}
}
