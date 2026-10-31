package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/storage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type rollbackFailureStorer struct {
	storage.Storer
	phase    string
	fault    error
	rollback bool
	injected bool
}

func (s *rollbackFailureStorer) SetReference(ref *plumbing.Reference) error {
	if s.rollback && s.phase == "branch" && ref.Name().IsBranch() {
		s.injected = true
		return s.fault
	}
	return s.Storer.SetReference(ref)
}

func (s *rollbackFailureStorer) SetIndex(index *gitindex.Index) error {
	if s.rollback && s.phase == "index" {
		s.injected = true
		return s.fault
	}
	return s.Storer.SetIndex(index)
}

func (s *rollbackFailureStorer) Close() error {
	if closer, ok := s.Storer.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func TestGitAcquisitionRollbackFailureRetainsRecoveryJournal(t *testing.T) {
	for _, phase := range []string{"branch", "index"} {
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
				original := []byte(`{"access_token":"original","disabled":false,"type":"codex"}`)
				if existing {
					if _, err := s.Save(context.Background(), &cliproxyauth.Auth{ID: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "original"}}); err != nil {
						t.Fatal(err)
					}
				}
				readme := filepath.Join(root, "workspace", "README.md")
				independent := []byte("independent edit\n")
				if err := os.WriteFile(readme, independent, 0o600); err != nil {
					t.Fatal(err)
				}
				pushErr := errors.New("push rejected")
				rollbackErr := errors.New("rollback " + phase + " unavailable")
				var injected *rollbackFailureStorer
				s.openFn = func(path string) (*git.Repository, error) {
					repo, err := git.PlainOpen(path)
					if err != nil {
						return nil, err
					}
					injected = &rollbackFailureStorer{Storer: repo.Storer, phase: phase, fault: rollbackErr}
					repo.Storer = injected
					return repo, nil
				}
				s.pushFn = func(context.Context, *git.Repository, *git.PushOptions) error {
					injected.rollback = true
					return pushErr
				}
				_, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "unpublished"}})
				if !errors.Is(err, pushErr) || !errors.Is(err, rollbackErr) || injected == nil || !injected.injected {
					t.Fatalf("push and rollback failures were not reached: %v", err)
				}
				marker := gitPublicationPath(s.repoDirSnapshot(), "auths/login.json")
				if _, err := readGitPublication(marker, s.repoDirSnapshot(), authDir); err != nil {
					t.Errorf("failed rollback lost its recovery journal: %v", err)
				}
				restarted := NewGitTokenStore(remote, "", "", "master")
				restarted.SetBaseDir(authDir)
				if err := restarted.EnsureRepository(); err != nil {
					t.Fatalf("restart stayed blocked after transient rollback failure: %v", err)
				}
				path := filepath.Join(authDir, "login.json")
				data, err := os.ReadFile(path)
				if existing {
					if err != nil || !bytes.Equal(data, original) {
						t.Fatalf("restart changed original credentials: %q, %v", data, err)
					}
					assertRemoteFileContents(t, remote, "master", "auths/login.json", string(original))
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("restart activated an unpublished credential: %q, %v", data, err)
				}
				if data, err := os.ReadFile(readme); err != nil || !bytes.Equal(data, independent) {
					t.Fatalf("restart lost unrelated work: %q, %v", data, err)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("successful recovery did not clear journal: %v", err)
				}
				if _, err := restarted.Save(context.Background(), &cliproxyauth.Auth{ID: "later.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "later"}}); err != nil {
					t.Fatalf("recovered store cannot save: %v", err)
				}
				assertRemoteTreePath(t, remote, "master", "auths/later.json", true)
				if existing {
					assertRemoteFileContents(t, remote, "master", "auths/login.json", string(original))
				} else {
					assertRemoteTreePath(t, remote, "master", "auths/login.json", false)
				}
			})
		}
	}
}
