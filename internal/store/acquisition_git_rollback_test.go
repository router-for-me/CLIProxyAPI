package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/storage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type prePushFailureStorer struct {
	storage.Storer
	phase     string
	fault     error
	baseline  plumbing.Hash
	committed bool
	injected  bool
	headReads int
}

func (s *prePushFailureStorer) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if s.committed && !s.injected && name == plumbing.HEAD {
		s.headReads++
		if (s.phase == "head" && s.headReads == 1) || ((s.phase == "push-head" || s.phase == "push-head-missing") && s.headReads == 2) {
			s.injected = true
			return nil, s.fault
		}
	}
	return s.Storer.Reference(name)
}

func (s *prePushFailureStorer) SetReference(ref *plumbing.Reference) error {
	if s.committed && !s.injected && s.phase == "rewrite-ref" && ref.Name().IsBranch() {
		s.injected = true
		return s.fault
	}
	if err := s.Storer.SetReference(ref); err != nil {
		return err
	}
	if ref.Name().IsBranch() && ref.Hash() != s.baseline {
		s.committed = true
	}
	return nil
}

func (s *prePushFailureStorer) SetEncodedObject(obj plumbing.EncodedObject) (plumbing.Hash, error) {
	if s.committed && !s.injected && s.phase == "rewrite-object" && obj.Type() == plumbing.CommitObject {
		s.injected = true
		return plumbing.ZeroHash, s.fault
	}
	return s.Storer.SetEncodedObject(obj)
}

func (s *prePushFailureStorer) Close() error {
	if closer, ok := s.Storer.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func readGitAcquisitionState(t *testing.T, dir string) (plumbing.Hash, *gitindex.Index) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	head, errHead := repo.Head()
	if errHead != nil && !errors.Is(errHead, plumbing.ErrReferenceNotFound) {
		t.Fatal(errHead)
	}
	index, errIndex := repo.Storer.Index()
	if errIndex != nil {
		t.Fatal(errIndex)
	}
	if head == nil {
		return plumbing.ZeroHash, index
	}
	return head.Hash(), index
}

func TestGitAcquisitionPrePushFailureRestoresCommitState(t *testing.T) {
	for _, phase := range []string{"head", "rewrite-object", "rewrite-ref", "push-head", "push-head-missing"} {
		for _, existing := range []bool{false, true} {
			name := "new"
			if existing {
				name = "replacement"
			}
			t.Run(phase+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "baseline\n"})
				s := NewGitTokenStore(remote, "", "", "master")
				s.SetBaseDir(filepath.Join(root, "workspace", "auths"))
				if err := s.EnsureRepository(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.AuthDir(), "login.json")
				var original []byte
				if existing {
					if _, err := s.Save(context.Background(), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "original"}}); err != nil {
						t.Fatal(err)
					}
					var err error
					original, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				before, _ := readGitAcquisitionState(t, s.repoDirSnapshot())
				fault := errors.New("post-commit " + phase + " failed")
				if phase == "push-head-missing" {
					fault = plumbing.ErrReferenceNotFound
				}
				var injected *prePushFailureStorer
				s.openFn = func(dir string) (*git.Repository, error) {
					repo, err := git.PlainOpen(dir)
					if err != nil {
						return nil, err
					}
					injected = &prePushFailureStorer{Storer: repo.Storer, phase: phase, fault: fault, baseline: before}
					repo.Storer = injected
					return repo, nil
				}
				saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "failed-login"}})
				if !errors.Is(err, fault) || saved != "" || injected == nil || !injected.injected {
					t.Fatalf("failure not reached: path=%q err=%v", saved, err)
				}
				got, errRead := os.ReadFile(path)
				if existing {
					if errRead != nil || string(got) != string(original) {
						t.Errorf("mirror not restored: %q, %v", got, errRead)
					}
				} else if !errors.Is(errRead, os.ErrNotExist) {
					t.Errorf("failed acquisition still readable: %q, %v", got, errRead)
				}
				after, index := readGitAcquisitionState(t, s.repoDirSnapshot())
				if after != before {
					t.Errorf("unpublished commit remains: before=%s after=%s", before, after)
				}
				entry, errEntry := index.Entry("auths/login.json")
				if existing {
					repo, err := git.PlainOpen(s.repoDirSnapshot())
					if err != nil {
						t.Fatal(err)
					}
					commit, err := repo.CommitObject(before)
					if err != nil {
						t.Fatal(err)
					}
					file, err := commit.File("auths/login.json")
					if errClose := repo.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					if err != nil || errEntry != nil || entry.Hash != file.Hash {
						t.Errorf("index not restored: entry=%v err=%v fileErr=%v", entry, errEntry, err)
					}
					assertRemoteFileContents(t, remote, "master", "auths/login.json", string(original))
				} else {
					if errEntry == nil {
						t.Error("index retained failed acquisition")
					}
					assertRemoteTreePath(t, remote, "master", "auths/login.json", false)
				}
				s.openFn = nil
				if _, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "retry"}}); err != nil {
					t.Fatalf("retry stayed wedged after transient failure: %v", err)
				}
				latest, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				assertRemoteFileContents(t, remote, "master", "auths/login.json", string(latest))
			})
		}
	}
}

func TestGitInitialCommitPrePushFailureRestoresUnbornBranch(t *testing.T) {
	for _, phase := range []string{"head", "rewrite-object", "rewrite-ref", "push-head", "push-head-missing"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			bare, err := git.PlainInit(remote, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := bare.Close(); err != nil {
				t.Fatal(err)
			}
			s := NewGitTokenStore(remote, "", "", "master")
			s.SetBaseDir(filepath.Join(root, "workspace", "auths"))
			repo, err := git.PlainInit(s.repoDirSnapshot(), false)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
				t.Fatal(err)
			}
			if err := repo.Close(); err != nil {
				t.Fatal(err)
			}
			if err := disableGitCommitSigning(s.repoDirSnapshot()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.repoDirSnapshot(), "seed")
			if err := os.WriteFile(path, []byte("seed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("initial post-commit " + phase + " failed")
			if phase == "push-head-missing" {
				fault = plumbing.ErrReferenceNotFound
			}
			var injected *prePushFailureStorer
			s.openFn = func(dir string) (*git.Repository, error) {
				repo, err := git.PlainOpen(dir)
				if err != nil {
					return nil, err
				}
				injected = &prePushFailureStorer{Storer: repo.Storer, phase: phase, fault: fault}
				repo.Storer = injected
				return repo, nil
			}
			s.mu.Lock()
			err = s.commitAndPushInitialLocked("Initialize store", "seed")
			s.mu.Unlock()
			if !errors.Is(err, fault) || injected == nil || !injected.injected {
				t.Fatalf("failure not reached: %v", err)
			}
			head, index := readGitAcquisitionState(t, s.repoDirSnapshot())
			if head != plumbing.ZeroHash || len(index.Entries) != 0 {
				t.Errorf("unborn state not restored: HEAD=%s entries=%d", head, len(index.Entries))
			}
			s.openFn = nil
			s.mu.Lock()
			err = s.commitAndPushInitialLocked("Initialize store", "seed")
			s.mu.Unlock()
			if err != nil {
				t.Fatalf("retry initialization failed: %v", err)
			}
			assertRemoteFileContents(t, remote, "master", "seed", "seed\n")
		})
	}
}
