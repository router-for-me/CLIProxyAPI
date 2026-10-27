package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type postPushFailureStorer struct {
	storage.Storer
	trackingRef    plumbing.ReferenceName
	errTracking    error
	errClose       error
	trackingFailed bool
	closeFailed    bool
}

func (s *postPushFailureStorer) SetReference(ref *plumbing.Reference) error {
	if ref.Name() == s.trackingRef && s.errTracking != nil {
		s.trackingFailed = true
		return s.errTracking
	}
	return s.Storer.SetReference(ref)
}

func (s *postPushFailureStorer) Close() error {
	var errClose error
	if closer, ok := s.Storer.(interface{ Close() error }); ok {
		errClose = closer.Close()
	}
	if s.errClose != nil {
		s.closeFailed = true
	}
	return errors.Join(errClose, s.errClose)
}

func TestGitAcquisitionKeepsCommittedMirrorAfterLocalFailure(t *testing.T) {
	for _, failure := range []string{"tracking-ref", "close"} {
		for _, existing := range []bool{false, true} {
			name := "new"
			if existing {
				name = "replacement"
			}
			t.Run(failure+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "baseline\n"})
				s := NewGitTokenStore(remote, "", "", "master")
				s.SetBaseDir(filepath.Join(root, "workspace", "auths"))
				if err := s.EnsureRepository(); err != nil {
					t.Fatal(err)
				}
				if existing {
					_, err := s.Save(context.Background(), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "original"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(s.AuthDir(), "login.json")
				var committed []byte
				var injected *postPushFailureStorer
				s.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
					if err := repo.PushContext(ctx, opts); err != nil {
						return err
					}
					var errRead error
					committed, errRead = os.ReadFile(path)
					if errRead != nil {
						return errRead
					}
					injected = &postPushFailureStorer{Storer: repo.Storer, trackingRef: plumbing.NewRemoteReferenceName("origin", "master")}
					if failure == "tracking-ref" {
						injected.errTracking = errors.New("tracking ref write failed after successful push")
					} else {
						injected.errClose = errors.New("repository close failed after successful push")
					}
					repo.Storer = injected
					return nil
				}
				saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "replacement"}})
				if injected == nil || (failure == "tracking-ref" && !injected.trackingFailed) || (failure == "close" && !injected.closeFailed) {
					t.Fatal("post-push failure was not reached")
				}
				if err != nil || saved != path {
					t.Errorf("committed acquisition reported failure: saved=%q, err=%v", saved, err)
				}
				got, errRead := os.ReadFile(path)
				if errRead != nil || string(got) != string(committed) {
					t.Errorf("successful push was rolled back locally: got %q, want %q, err=%v", got, committed, errRead)
				}
				assertRemoteFileContents(t, remote, "master", "auths/login.json", string(committed))
				readHead := func(dir string) plumbing.Hash {
					t.Helper()
					repo, err := git.PlainOpen(dir)
					if err != nil {
						t.Fatal(err)
					}
					head, errHead := repo.Head()
					errClose := repo.Close()
					if errHead != nil || errClose != nil {
						t.Fatalf("read repository head: %v, %v", errHead, errClose)
					}
					return head.Hash()
				}
				if local, remoteHead := readHead(s.repoDirSnapshot()), readHead(remote); local != remoteHead {
					t.Errorf("successful push rolled back local HEAD: local=%s remote=%s", local, remoteHead)
				}
				s.pushFn = nil
				if _, err := s.Save(context.Background(), &cliproxyauth.Auth{ID: "later.json", FileName: "later.json", Metadata: map[string]any{"type": "codex", "access_token": "later"}}); err != nil {
					t.Fatalf("next save could not recover local tracking: %v", err)
				}
				assertRemoteFileContents(t, remote, "master", "auths/login.json", string(committed))
			})
		}
	}
}
