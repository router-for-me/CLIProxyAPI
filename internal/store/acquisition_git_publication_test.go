package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type publicationCrashStorer struct {
	storage.Storer
	branch plumbing.ReferenceName
	base   plumbing.Hash
}

func (s *publicationCrashStorer) SetReference(ref *plumbing.Reference) error {
	if err := s.Storer.SetReference(ref); err != nil {
		return err
	}
	if ref.Name() == s.branch && ref.Hash() != s.base {
		os.Exit(83)
	}
	return nil
}

func TestGitAcquisitionPublicationCrashHelper(t *testing.T) {
	if os.Getenv("CPA_PUBLICATION_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	s := NewGitTokenStore(os.Getenv("CPA_PUBLICATION_REMOTE"), "", "", "master")
	s.SetBaseDir(os.Getenv("CPA_PUBLICATION_AUTH_DIR"))
	if os.Getenv("CPA_PUBLICATION_PHASE") == "before-commit" {
		s.openFn = func(string) (*git.Repository, error) { os.Exit(83); return nil, nil }
	}
	if os.Getenv("CPA_PUBLICATION_PHASE") == "after-commit" {
		s.openFn = func(path string) (*git.Repository, error) {
			repo, err := git.PlainOpen(path)
			if err != nil {
				return nil, err
			}
			head, err := repo.Head()
			if err != nil {
				_ = repo.Close()
				return nil, err
			}
			repo.Storer = &publicationCrashStorer{Storer: repo.Storer, branch: head.Name(), base: head.Hash()}
			return repo, nil
		}
	}
	s.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		if os.Getenv("CPA_PUBLICATION_PHASE") == "before-push" {
			os.Exit(83)
		}
		if err := repo.PushContext(ctx, opts); err != nil {
			return err
		}
		os.Exit(83)
		return nil
	}
	_, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}})
	t.Fatalf("process did not exit after push: %v", err)
}

func TestGitAcquisitionPublicationRecoversAfterProcessExit(t *testing.T) {
	for _, phase := range []string{"after-push", "before-commit", "after-commit", "before-push"} {
		for _, existing := range []bool{false, true} {
			name := "new"
			if existing {
				name = "replacement"
			}
			t.Run(phase+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "baseline\n"})
				authDir := filepath.Join(root, "workspace", "auths")
				s := NewGitTokenStore(remote, "", "", "master")
				s.SetBaseDir(authDir)
				if err := s.EnsureRepository(); err != nil {
					t.Fatal(err)
				}
				if existing {
					if _, err := s.Save(context.Background(), &cliproxyauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "original"}}); err != nil {
						t.Fatal(err)
					}
				}
				unrelated := filepath.Join(root, "workspace", "README.md")
				unrelatedBytes := []byte("independent local edit\n")
				if err := os.WriteFile(unrelated, unrelatedBytes, 0o600); err != nil {
					t.Fatal(err)
				}
				untracked := filepath.Join(authDir, "untracked.json")
				if err := os.WriteFile(untracked, []byte(`{"type":"codex","access_token":"independent"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				child := exec.Command(os.Args[0], "-test.run=^TestGitAcquisitionPublicationCrashHelper$")
				child.Env = append(os.Environ(), "CPA_PUBLICATION_CRASH_HELPER=1", "CPA_PUBLICATION_PHASE="+phase, "CPA_PUBLICATION_REMOTE="+remote, "CPA_PUBLICATION_AUTH_DIR="+authDir)
				output, err := child.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 83 {
					t.Fatalf("crash helper = %v, output=%s", err, output)
				}
				wantToken := "committed"
				wantCredential := phase == "after-push" || existing
				if phase != "after-push" {
					wantToken = "original"
				}
				expected := `{"access_token":"` + wantToken + `","disabled":false,"type":"codex"}`
				if wantCredential {
					assertRemoteFileContents(t, remote, "master", "auths/device.json", expected)
				} else {
					assertRemoteTreePath(t, remote, "master", "auths/device.json", false)
				}
				path := filepath.Join(authDir, "device.json")
				before, err := os.ReadFile(path)
				if existing {
					if err != nil || !bytes.Contains(before, []byte(`"original"`)) {
						t.Fatalf("helper published before exit: %q, %v", before, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("helper published before exit: %q, %v", before, err)
				}
				restarted := NewGitTokenStore(remote, "", "", "master")
				restarted.SetBaseDir(authDir)
				if err := restarted.EnsureRepository(); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if wantCredential {
					if err != nil || string(got) != expected {
						t.Errorf("restart loaded wrong mirror: %q, %v", got, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("restart published an uncommitted credential: %q, %v", got, err)
				}
				records, err := restarted.List(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, record := range records {
					if record.FileName == "device.json" {
						found = true
						if record.Metadata["access_token"] != wantToken {
							t.Errorf("runtime loaded old credential: %v", record.Metadata["access_token"])
						}
					}
				}
				if found != wantCredential {
					t.Errorf("credential loaded=%v, want=%v", found, wantCredential)
				}
				if got, err := os.ReadFile(unrelated); err != nil || !bytes.Equal(got, unrelatedBytes) {
					t.Errorf("restart overwrote unrelated edit: %q, %v", got, err)
				}
				if got, err := os.ReadFile(untracked); err != nil || !bytes.Contains(got, []byte(`"independent"`)) {
					t.Errorf("restart overwrote unrelated credential: %q, %v", got, err)
				}
				if err := restarted.EnsureRepository(); err != nil {
					t.Errorf("replay not idempotent: %v", err)
				}
				pending, err := filepath.Glob(filepath.Join(root, "workspace", ".git", "auth-publications", "*.json"))
				if err != nil || len(pending) != 0 {
					t.Errorf("completed replay left pending records: %v, %v", pending, err)
				}
				staging, err := filepath.Glob(filepath.Join(authDir, ".auth-acquisition-*"))
				if err != nil || len(staging) != 0 {
					t.Errorf("completed replay left private staging files: %v, %v", staging, err)
				}
				if _, err := restarted.Save(context.Background(), &cliproxyauth.Auth{ID: "later.json", FileName: "later.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "later"}}); err != nil {
					t.Fatalf("recovered store cannot save: %v", err)
				}
				assertRemoteTreePath(t, remote, "master", "auths/later.json", true)
				if wantCredential {
					assertRemoteFileContents(t, remote, "master", "auths/device.json", expected)
				} else {
					assertRemoteTreePath(t, remote, "master", "auths/device.json", false)
				}
			})
		}
	}
}
