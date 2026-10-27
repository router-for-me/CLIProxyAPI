package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAcquisitionPublishesOnlyAfterRemoteCommit(t *testing.T) {
	for _, backend := range []string{"git", "object", "postgres"} {
		for _, existing := range []bool{false, true} {
			for _, succeeds := range []bool{false, true} {
				name := backend + "/new/failure"
				if existing {
					name = backend + "/replacement/failure"
				}
				if succeeds {
					name = strings.TrimSuffix(name, "failure") + "success"
				}
				t.Run(name, func(t *testing.T) {
					dir := filepath.Join(t.TempDir(), "workspace", "auths")
					path := filepath.Join(dir, "device.json")
					original := []byte(`{"type":"codex","access_token":"original"}`)
					remoteErr := errors.New("remote rejected acquisition")
					reached := false
					beforeCommit := func() error {
						reached = true
						data, err := os.ReadFile(path)
						if existing {
							if err != nil || string(data) != string(original) {
								t.Errorf("uncommitted credential replaced visible mirror: %q, %v", data, err)
							}
						} else if !errors.Is(err, os.ErrNotExist) {
							t.Errorf("uncommitted credential is visible: %q, %v", data, err)
						}
						if !succeeds {
							return remoteErr
						}
						return nil
					}
					var s cliproxyauth.Store
					var gitRemote string
					switch backend {
					case "git":
						remote := filepath.Join(t.TempDir(), "remote.git")
						gitRemote = remote
						if _, err := git.PlainInit(remote, true); err != nil {
							t.Fatal(err)
						}
						gs := NewGitTokenStore(remote, "", "", "")
						gs.SetBaseDir(dir)
						if err := gs.EnsureRepository(); err != nil {
							t.Fatal(err)
						}
						gs.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
							if err := beforeCommit(); err != nil {
								return err
							}
							return repo.PushContext(ctx, opts)
						}
						s = gs
					case "object":
						s = &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, key string, data []byte, _ string) error {
							if !bytes.Contains(data, []byte(`"replacement"`)) {
								t.Errorf("remote did not receive acquired credential: %q", data)
							}
							if key != "auths/device.json" {
								t.Errorf("remote key = %q", key)
							}
							return beforeCommit()
						}}
					case "postgres":
						s = &PostgresStore{authDir: dir, persistAuthFn: func(_ context.Context, id string, data []byte) error {
							if !bytes.Contains(data, []byte(`"replacement"`)) {
								t.Errorf("remote did not receive acquired credential: %q", data)
							}
							if id != "device.json" {
								t.Errorf("remote id = %q", id)
							}
							return beforeCommit()
						}}
					}
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
					if existing {
						if err := os.WriteFile(path, original, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement"}})
					if !reached {
						t.Fatal("remote commit was not attempted")
					}
					if succeeds {
						if err != nil || saved != path {
							t.Fatalf("Save = %q, %v", saved, err)
						}
						data, err := os.ReadFile(path)
						if err != nil || !bytes.Contains(data, []byte(`"replacement"`)) {
							t.Fatalf("committed credential not published: %q, %v", data, err)
						}
						if gitRemote != "" {
							assertRemoteFileContents(t, gitRemote, "master", "auths/device.json", string(data))
						}
					} else if !errors.Is(err, remoteErr) || saved != "" {
						t.Fatalf("Save = %q, %v", saved, err)
					}
				})
			}
		}
	}
}
