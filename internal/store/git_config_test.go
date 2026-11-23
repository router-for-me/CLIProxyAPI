package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGitConfigBootstrapUsesWinningRemoteBlob(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	a := NewGitTokenStore(remote, "", "", "")
	a.SetBaseDir(filepath.Join(root, "a", "auths"))
	b := NewGitTokenStore(remote, "", "", "")
	b.SetBaseDir(filepath.Join(root, "b", "auths"))
	example := filepath.Join(root, "example.yaml")
	writeSeedTestFile(t, example, seedTestPlaintextConfig("management"))
	ctx := context.Background()
	var winner *config.Config
	pushes := 0
	a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		pushes++
		// Run B while A has staged its own generated config but has not published it.
		if pushes == 1 {
			var err error
			winner, err = b.BootstrapConfig(ctx, example)
			if err != nil {
				t.Fatalf("winner BootstrapConfig: %v", err)
			}
		}
		return repo.PushContext(ctx, opts)
	}
	got, err := a.BootstrapConfig(ctx, example)
	if err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || got.ServerID == "" || got.ServerID != winner.ServerID || got.SourceHash != winner.SourceHash {
		t.Fatalf("loser = %q/%q, winner = %q/%q, loser pushes = %d", got.ServerID, got.SourceHash, winner.ServerID, winner.SourceHash, pushes)
	}
	data := readSeedTestFile(t, a.ConfigPath())
	assertSeededWithHashedSecret(t, data, b.ConfigPath(), "management")
	assertRemoteFileContents(t, remote, "master", "config/config.yaml", string(data))
	if got.SourceHash != config.ComputeSourceHash(data) {
		t.Fatalf("SourceHash = %s, want hash of committed blob", got.SourceHash)
	}
}

func TestGitConfigBootstrapDoesNotSeedOverRemoteWhenLocalConfigWasDeleted(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	ctx := context.Background()
	writeSeedTestFile(t, b.ConfigPath(), "# remote-only configuration\nport: 8765\n")
	winner, errBootstrap := b.BootstrapConfig(ctx, "")
	if errBootstrap != nil {
		t.Fatal(errBootstrap)
	}
	remoteBytes := readSeedTestFile(t, b.ConfigPath())
	mirrored, errMirror := a.BootstrapConfig(ctx, "")
	if errMirror != nil || mirrored.ServerID == "" || mirrored.ServerID != winner.ServerID || mirrored.SourceHash != winner.SourceHash {
		t.Fatalf("initial BootstrapConfig did not mirror remote: %v", errMirror)
	}
	remoteOID := nativeStoreGit(t, remote, "rev-parse", "refs/heads/master")
	if errRemove := os.Remove(a.ConfigPath()); errRemove != nil {
		t.Fatal(errRemove)
	}
	example := filepath.Join(t.TempDir(), "example.yaml")
	writeSeedTestFile(t, example, "# template must not replace the remote\nport: 9999\n")
	reopened := NewGitTokenStore(remote, "", "", "master")
	reopened.SetBaseDir(a.AuthDir())
	if _, errRestart := reopened.BootstrapConfig(ctx, example); !errors.Is(errRestart, config.ErrConfigFileChanged) {
		t.Fatalf("BootstrapConfig error = %v, want config conflict", errRestart)
	}
	if got := nativeStoreGit(t, remote, "rev-parse", "refs/heads/master"); got != remoteOID {
		t.Fatalf("remote HEAD = %s, want %s", got, remoteOID)
	}
	assertRemoteFileContents(t, remote, "master", gitConfigPath, string(remoteBytes))
	if _, errStat := os.Lstat(a.ConfigPath()); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("local config unexpectedly recreated: %v", errStat)
	}
}

func TestGitConfigBootstrapDoesNotDiscardUserEditDuringRejectedPush(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	a := NewGitTokenStore(remote, "", "", "")
	a.SetBaseDir(filepath.Join(root, "a", "auths"))
	b := NewGitTokenStore(remote, "", "", "")
	b.SetBaseDir(filepath.Join(root, "b", "auths"))
	example := filepath.Join(root, "example.yaml")
	writeSeedTestFile(t, example, "port: 8317\n")
	const edit = "# edited during push\nport: 8888\n"
	a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		if _, err := b.BootstrapConfig(ctx, example); err != nil {
			t.Fatal(err)
		}
		if err := config.CreateConfigFile(a.ConfigPath(), []byte(edit)); err != nil {
			t.Fatal(err)
		}
		return repo.PushContext(ctx, opts)
	}
	if _, err := a.BootstrapConfig(context.Background(), example); err == nil {
		t.Fatal("BootstrapConfig overwrote an overlapping user edit")
	}
	if local := readSeedTestFile(t, a.ConfigPath()); string(local) != edit {
		t.Fatalf("user edit lost: %q", local)
	}
	assertRemoteFileContents(t, remote, "master", "config/config.yaml", string(readSeedTestFile(t, b.ConfigPath())))
}

func TestGitPersistPreparedConfigUploadsVerifiedSnapshot(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	s := NewGitTokenStore(remote, "", "", "")
	s.SetBaseDir(filepath.Join(root, "a", "auths"))
	if err := s.EnsureRepository(); err != nil {
		t.Fatal(err)
	}
	path := s.ConfigPath()
	writeSeedTestFile(t, path, seedTestPlaintextConfig("management"))
	prepared, err := config.PrepareConfigFileForUploadWithOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	const edited = "port: 8888\nmanagement:\n  secret-key: changed-plaintext\n"
	s.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		snapshot, errRead := config.ReadConfigFileSnapshot(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if _, errWrite := config.WriteConfigFileCAS(path, snapshot, []byte(edited)); errWrite != nil {
			t.Fatal(errWrite)
		}
		// Moving the local branch after verification must not change the
		// immutable commit selected as the upload source.
		cfg, errConfig := repo.Config()
		if errConfig != nil {
			t.Fatal(errConfig)
		}
		cfg.Commit.GpgSign = gitconfig.OptBoolFalse
		if errSet := repo.SetConfig(cfg); errSet != nil {
			t.Fatal(errSet)
		}
		worktree, errWorktree := repo.Worktree()
		if errWorktree != nil {
			t.Fatal(errWorktree)
		}
		if _, errAdd := worktree.Add(gitConfigPath); errAdd != nil {
			t.Fatal(errAdd)
		}
		if _, errCommit := worktree.Commit("Unverified local edit", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}}); errCommit != nil {
			t.Fatal(errCommit)
		}
		return repo.PushContext(ctx, opts)
	}
	hash, err := s.PersistPreparedConfig(context.Background(), prepared)
	if err != nil || hash != config.ComputeSourceHash(prepared) {
		t.Fatalf("PersistPreparedConfig = %q, %v", hash, err)
	}
	if !bytes.Equal(readSeedTestFile(t, path), []byte(edited)) {
		t.Fatal("local edit was lost")
	}
	assertRemoteFileContents(t, remote, "master", "config/config.yaml", string(prepared))
}

func TestGitConfigAuthPersistenceCannotUploadPlaintext(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	s := NewGitTokenStore(remote, "", "", "")
	s.SetBaseDir(filepath.Join(root, "a", "auths"))
	if err := s.EnsureRepository(); err != nil {
		t.Fatal(err)
	}
	path := s.ConfigPath()
	writeSeedTestFile(t, path, seedTestConfigsKeepingPlaintext["anchored secret"])
	if err := s.PersistAuthFiles(context.Background(), "Bypass", path); err == nil {
		t.Fatal("PersistAuthFiles uploaded config containing a plaintext secret")
	}
	assertRemoteTreePath(t, remote, "master", "config/config.yaml", false)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestGitConfigBootstrapConvergesWhenInitializingEmptyRemote(t *testing.T) {
	for _, branch := range []string{"", "trunk"} {
		t.Run("branch="+branch, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "empty.git")
			repo, errInit := git.PlainInit(remote, true)
			if errInit != nil {
				t.Fatal(errInit)
			}
			if errClose := repo.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			a := NewGitTokenStore(remote, "", "", branch)
			a.SetBaseDir(filepath.Join(root, "a", "auths"))
			b := NewGitTokenStore(remote, "", "", branch)
			b.SetBaseDir(filepath.Join(root, "b", "auths"))
			example := filepath.Join(root, "example.yaml")
			writeSeedTestFile(t, example, "port: 8317\n")
			var winner *config.Config
			a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
				if winner == nil {
					var errBootstrap error
					winner, errBootstrap = b.BootstrapConfig(ctx, example)
					if errBootstrap != nil {
						t.Fatal(errBootstrap)
					}
				}
				return repo.PushContext(ctx, opts)
			}
			cfg, errBootstrap := a.BootstrapConfig(context.Background(), example)
			if errBootstrap != nil || cfg.SourceHash == "" || cfg.SourceHash != winner.SourceHash || cfg.ServerID != winner.ServerID {
				t.Fatalf("initial branch creation did not converge: %v", errBootstrap)
			}
		})
	}
}

func TestGitOptionalConfigStandsByWithoutUploading(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	s := NewGitTokenStore(remote, "", "", "")
	s.SetBaseDir(filepath.Join(root, "a", "auths"))
	s.SetOptionalConfig(true)
	example := filepath.Join(root, "example.yaml")
	writeSeedTestFile(t, example, "port: [\n")
	cfg, errBootstrap := s.BootstrapConfig(context.Background(), example)
	if errBootstrap != nil || cfg.ServerID != "" || cfg.SourceHash != "" {
		t.Fatalf("malformed optional template did not enter standby: %v", errBootstrap)
	}
	if errPersist := s.PersistConfig(context.Background()); errPersist != nil {
		t.Fatal(errPersist)
	}
	assertRemoteTreePath(t, remote, "master", gitConfigPath, false)
}

func TestGitConfigBootstrapCompetesWithExistingUntrackedSeeds(t *testing.T) {
	for _, scenario := range []string{"same seed", "edited during push", "different seed", "empty remote", "winner already published"} {
		t.Run(scenario, func(t *testing.T) {
			var remote string
			var a, b *GitTokenStore
			if scenario == "empty remote" {
				root := t.TempDir()
				remote = filepath.Join(root, "remote.git")
				repo, errInit := git.PlainInit(remote, true)
				if errInit != nil {
					t.Fatal(errInit)
				}
				if errClose := repo.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				a = NewGitTokenStore(remote, "", "", "master")
				b = NewGitTokenStore(remote, "", "", "master")
				a.SetBaseDir(filepath.Join(root, "a", "auths"))
				b.SetBaseDir(filepath.Join(root, "b", "auths"))
			} else {
				remote, a, b = gitWorkspacePair(t)
			}
			seedA := seedTestPlaintextConfig("management")
			seedB := seedA
			if scenario == "different seed" {
				seedB = strings.Replace(seedA, "port: 8317", "port: 8888", 1)
			}
			writeSeedTestFile(t, a.ConfigPath(), seedA)
			writeSeedTestFile(t, b.ConfigPath(), seedB)
			const edited = "port: 9999\n"
			var winner *config.Config
			if scenario == "winner already published" {
				var errBootstrap error
				winner, errBootstrap = b.BootstrapConfig(context.Background(), "")
				if errBootstrap != nil {
					t.Fatal(errBootstrap)
				}
			}
			a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
				if winner != nil {
					t.Fatal("losing replica tried to publish a second candidate")
				}
				var errBootstrap error
				winner, errBootstrap = b.BootstrapConfig(ctx, "")
				if errBootstrap != nil {
					t.Fatal(errBootstrap)
				}
				if scenario == "edited during push" {
					snapshot, errRead := config.ReadConfigFileSnapshot(a.ConfigPath())
					if errRead != nil {
						t.Fatal(errRead)
					}
					if _, errWrite := config.WriteConfigFileCAS(a.ConfigPath(), snapshot, []byte(edited)); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				return repo.PushContext(ctx, opts)
			}
			got, errBootstrap := a.BootstrapConfig(context.Background(), "")
			switch scenario {
			case "edited during push", "different seed":
				if errBootstrap == nil {
					t.Fatal("BootstrapConfig overwrote user-owned config")
				}
				if scenario == "edited during push" {
					seedA = edited
				}
				assertLocalFileContents(t, a.ConfigPath(), seedA)
			default:
				if errBootstrap != nil || got.ServerID != winner.ServerID || got.SourceHash != winner.SourceHash {
					t.Fatalf("BootstrapConfig did not adopt the winning seed: %v", errBootstrap)
				}
				assertSeededWithHashedSecret(t, readSeedTestFile(t, a.ConfigPath()), b.ConfigPath(), "management")
			}
			assertRemoteFileContents(t, remote, "master", gitConfigPath, string(readSeedTestFile(t, b.ConfigPath())))
		})
	}
}
