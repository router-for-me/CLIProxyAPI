package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type testBranchSpec struct {
	name     string
	contents string
}

type callbackTokenStorage struct {
	save func(string) error
}

func (s *callbackTokenStorage) SaveTokenToFile(path string) error {
	return s.save(path)
}

func TestEnsureRepositoryUsesRemoteDefaultBranchWhenBranchNotConfigured(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "trunk",
		testBranchSpec{name: "trunk", contents: "remote default branch\n"},
		testBranchSpec{name: "release/2026", contents: "release branch\n"},
	)

	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "trunk", "remote default branch\n")
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "trunk", "remote default branch updated\n", "advance trunk")
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "release/2026", "release branch updated\n", "advance release")

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository second call: %v", err)
	}

	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "trunk", "remote default branch updated\n")
	assertRemoteHeadBranch(t, remoteDir, "trunk")
}

func TestEnsureRepositoryUsesConfiguredBranchWhenExplicitlySet(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "trunk",
		testBranchSpec{name: "trunk", contents: "remote default branch\n"},
		testBranchSpec{name: "release/2026", contents: "release branch\n"},
	)

	store := NewGitTokenStore(remoteDir, "", "", "release/2026")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "release/2026", "release branch\n")
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "trunk", "remote default branch updated\n", "advance trunk")
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "release/2026", "release branch updated\n", "advance release")

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository second call: %v", err)
	}

	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "release/2026", "release branch updated\n")
	assertRemoteHeadBranch(t, remoteDir, "trunk")
}

func TestEnsureRepositoryReturnsErrorForMissingConfiguredBranch(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "trunk",
		testBranchSpec{name: "trunk", contents: "remote default branch\n"},
	)

	store := NewGitTokenStore(remoteDir, "", "", "missing-branch")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))

	err := store.EnsureRepository()
	if err == nil {
		t.Fatal("EnsureRepository succeeded, want error for nonexistent configured branch")
	}
	assertRemoteHeadBranch(t, remoteDir, "trunk")
}

func TestEnsureRepositoryReturnsErrorForMissingConfiguredBranchOnExistingRepositoryPull(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "trunk",
		testBranchSpec{name: "trunk", contents: "remote default branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(baseDir)

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository initial clone: %v", err)
	}

	reopened := NewGitTokenStore(remoteDir, "", "", "missing-branch")
	reopened.SetBaseDir(baseDir)

	err := reopened.EnsureRepository()
	if err == nil {
		t.Fatal("EnsureRepository succeeded on reopen, want error for nonexistent configured branch")
	}
	assertRepositoryHeadBranch(t, filepath.Join(root, "workspace"), "trunk")
	assertRemoteHeadBranch(t, remoteDir, "trunk")
}

func TestEnsureRepositoryInitializesEmptyRemoteUsingConfiguredBranch(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	branch := "feature/gemini-fix"
	store := NewGitTokenStore(remoteDir, "", "", branch)
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	assertRepositoryHeadBranch(t, filepath.Join(root, "workspace"), branch)
	assertRemoteBranchExistsWithCommit(t, remoteDir, branch)
	assertRemoteBranchDoesNotExist(t, remoteDir, "master")
}

func TestEnsureRepositoryExistingRepoSwitchesToConfiguredBranch(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
		testBranchSpec{name: "develop", contents: "remote develop branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(baseDir)

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository initial clone: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "master", "remote master branch\n")

	reopened := NewGitTokenStore(remoteDir, "", "", "develop")
	reopened.SetBaseDir(baseDir)

	if err := reopened.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository reopen: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "develop", "remote develop branch\n")

	workspaceDir := filepath.Join(root, "workspace")
	if err := os.WriteFile(filepath.Join(workspaceDir, "branch.txt"), []byte("local develop update\n"), 0o600); err != nil {
		t.Fatalf("write local branch marker: %v", err)
	}

	reopened.mu.Lock()
	err := reopened.commitAndPushLocked("Update develop branch marker", "branch.txt")
	reopened.mu.Unlock()
	if err != nil {
		t.Fatalf("commitAndPushLocked: %v", err)
	}

	assertRepositoryHeadBranch(t, workspaceDir, "develop")
	assertRemoteBranchContents(t, remoteDir, "develop", "local develop update\n")
	assertRemoteBranchContents(t, remoteDir, "master", "remote master branch\n")
}

func TestEnsureRepositoryExistingRepoSwitchesToConfiguredBranchCreatedAfterClone(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(baseDir)

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository initial clone: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "master", "remote master branch\n")

	advanceRemoteBranchFromNewBranch(t, filepath.Join(root, "seed"), remoteDir, "release/2026", "release branch\n", "create release")

	reopened := NewGitTokenStore(remoteDir, "", "", "release/2026")
	reopened.SetBaseDir(baseDir)

	if err := reopened.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository reopen: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "release/2026", "release branch\n")
}

func TestEnsureRepositoryResetsToRemoteDefaultWhenBranchUnset(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
		testBranchSpec{name: "develop", contents: "remote develop branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	// First store pins to develop and prepares local workspace
	storePinned := NewGitTokenStore(remoteDir, "", "", "develop")
	storePinned.SetBaseDir(baseDir)
	if err := storePinned.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository pinned: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "develop", "remote develop branch\n")

	// Second store has branch unset and should reset local workspace to remote default (master)
	storeDefault := NewGitTokenStore(remoteDir, "", "", "")
	storeDefault.SetBaseDir(baseDir)
	if err := storeDefault.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository default: %v", err)
	}
	// Local HEAD should now follow remote default (master)
	assertRepositoryHeadBranch(t, filepath.Join(root, "workspace"), "master")

	// Make a local change and push using the store with branch unset; push should update remote master
	workspaceDir := filepath.Join(root, "workspace")
	if err := os.WriteFile(filepath.Join(workspaceDir, "branch.txt"), []byte("local master update\n"), 0o600); err != nil {
		t.Fatalf("write local master marker: %v", err)
	}
	storeDefault.mu.Lock()
	if err := storeDefault.commitAndPushLocked("Update master marker", "branch.txt"); err != nil {
		storeDefault.mu.Unlock()
		t.Fatalf("commitAndPushLocked: %v", err)
	}
	storeDefault.mu.Unlock()

	assertRemoteBranchContents(t, remoteDir, "master", "local master update\n")
}

func TestGitTokenStoreRefusesWatcherOriginatedAuthDeletion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	auth := &cliproxyauth.Auth{
		ID:       "protected.json",
		FileName: "protected.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "token"},
	}
	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/protected.json", true)

	if err := os.Remove(path); err != nil {
		t.Fatalf("simulate unexpected local removal: %v", err)
	}
	err = store.PersistAuthFiles(context.Background(), "Remove auth protected.json", path)
	if err == nil {
		t.Fatal("PersistAuthFiles watcher removal error = nil, want fail-closed rejection")
	}
	if got := err.Error(); !strings.Contains(got, "refusing watcher-originated removal") {
		t.Fatalf("PersistAuthFiles error = %q, want watcher-removal rejection", got)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/protected.json", true)
}

func TestGitTokenStoreWatcherRemovalNoOpsAfterExplicitDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	auth := &cliproxyauth.Auth{
		ID:       "explicit.json",
		FileName: "explicit.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "token"},
	}
	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Management deletes unlink the file before invoking Store.Delete.
	if err := os.Remove(path); err != nil {
		t.Fatalf("pre-remove explicit auth: %v", err)
	}
	if err := store.Delete(context.Background(), path); err != nil {
		t.Fatalf("Delete after pre-remove: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/explicit.json", false)

	if err := store.Delete(context.Background(), path); err != nil {
		t.Fatalf("repeated Delete: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/explicit.json", false)

	if err := store.PersistAuthFiles(context.Background(), "Remove auth explicit.json", path); err != nil {
		t.Fatalf("watcher removal after explicit delete: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/explicit.json", false)
}

func TestGitTokenStoreRepeatedDeleteDoesNotOverwriteRemoteOnlyChanges(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	storeA := NewGitTokenStore(remoteDir, "", "", "")
	baseA := filepath.Join(root, "workspace-a", "auths")
	storeA.SetBaseDir(baseA)
	if err := storeA.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository A: %v", err)
	}
	authA := &cliproxyauth.Auth{
		ID:       "a.json",
		FileName: "a.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "a"},
	}
	pathA, err := storeA.Save(context.Background(), authA)
	if err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := storeA.Delete(context.Background(), pathA); err != nil {
		t.Fatalf("Delete A: %v", err)
	}

	storeB := NewGitTokenStore(remoteDir, "", "", "")
	baseB := filepath.Join(root, "workspace-b", "auths")
	storeB.SetBaseDir(baseB)
	if err := storeB.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository B: %v", err)
	}
	authB := &cliproxyauth.Auth{
		ID:       "b.json",
		FileName: "b.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "b"},
	}
	if _, err := storeB.Save(context.Background(), authB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/b.json", true)

	if err := storeA.Delete(context.Background(), pathA); err != nil {
		t.Fatalf("repeated Delete A: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/b.json", true)
}

func TestGitTokenStoreRejectsPathsOutsideRepositoryBeforeMutation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	outsidePath := filepath.Join(root, "outside.json")
	outsideContents := []byte("outside\n")
	if err := os.WriteFile(outsidePath, outsideContents, 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := store.Delete(context.Background(), outsidePath); err == nil {
		t.Fatal("Delete outside repository error = nil, want rejection")
	}
	if got, errRead := os.ReadFile(outsidePath); errRead != nil {
		t.Fatalf("read outside file after delete rejection: %v", errRead)
	} else if string(got) != string(outsideContents) {
		t.Fatalf("outside file contents = %q, want %q", got, outsideContents)
	}

	outsideSavePath := filepath.Join(root, "outside-save.json")
	auth := &cliproxyauth.Auth{
		ID:       "outside-save.json",
		FileName: "outside-save.json",
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributePath: outsideSavePath,
		},
		Metadata: map[string]any{"type": "codex", "access_token": "token"},
	}
	if _, err := store.Save(context.Background(), auth); err == nil {
		t.Fatal("Save outside repository error = nil, want rejection")
	}
	if _, errStat := os.Stat(outsideSavePath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("outside save path stat error = %v, want not exist", errStat)
	}
}

func TestGitTokenStorePersistConfigDropsUnrelatedStagedDeletions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	auth := &cliproxyauth.Auth{
		ID:       "protected.json",
		FileName: "protected.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "token"},
	}
	authPath, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	configPath := store.ConfigPath()
	if err := os.WriteFile(configPath, []byte("version: one\n"), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig initial: %v", err)
	}

	repo, err := git.PlainOpen(filepath.Join(root, "workspace"))
	if err != nil {
		t.Fatalf("open workspace repo: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open workspace worktree: %v", err)
	}
	if _, err := worktree.Remove("auths/protected.json"); err != nil {
		t.Fatalf("stage unexpected auth removal: %v", err)
	}
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed auth stat error = %v, want not exist", err)
	}
	if err := os.WriteFile(configPath, []byte("version: two\n"), 0o600); err != nil {
		t.Fatalf("write updated config: %v", err)
	}

	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig with corrupt index: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/protected.json", true)
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "version: two\n")
}

func TestGitTokenStorePersistConfigRepairsIndexAfterUnstagedPull(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	configPath := store.ConfigPath()
	if err := os.WriteFile(configPath, []byte("source: local-config\n"), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "master", "remote branch advanced\n", "advance remote")

	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig after unstaged pull: %v", err)
	}
	assertRemoteBranchContents(t, remoteDir, "master", "remote branch advanced\n")
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "source: local-config\n")
}

func TestGitTokenStorePersistConfigPreservesRemoteOnlyAuthAfterDivergence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if err := storeA.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository A: %v", err)
	}

	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if err := storeB.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository B: %v", err)
	}
	authB := &cliproxyauth.Auth{
		ID:       "remote-only.json",
		FileName: "remote-only.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "remote"},
	}
	if _, err := storeB.Save(context.Background(), authB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/remote-only.json", true)

	configPathA := storeA.ConfigPath()
	if err := os.WriteFile(configPathA, []byte("source: store-a\n"), 0o600); err != nil {
		t.Fatalf("write config A: %v", err)
	}
	if err := storeA.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig A after divergence: %v", err)
	}

	assertRemoteTreePath(t, remoteDir, "master", "auths/remote-only.json", true)
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "source: store-a\n")
}

func TestGitTokenStoreRejectsStaleForcePush(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if err := storeA.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository A: %v", err)
	}
	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if err := storeB.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository B: %v", err)
	}

	authB := &cliproxyauth.Auth{
		ID:       "concurrent.json",
		FileName: "concurrent.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "remote"},
	}
	if _, err := storeB.Save(context.Background(), authB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	configPathA := storeA.ConfigPath()
	if err := os.WriteFile(configPathA, []byte("source: stale-a\n"), 0o600); err != nil {
		t.Fatalf("write stale config A: %v", err)
	}

	storeA.mu.Lock()
	errPush := storeA.commitAndPushLocked("Update stale config", "config/config.yaml")
	storeA.mu.Unlock()
	if errPush == nil {
		t.Fatal("stale force push error = nil, want lease rejection")
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/concurrent.json", true)
	assertRemoteTreePath(t, remoteDir, "master", "config/config.yaml", false)

	if err := storeA.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig A after lease rejection: %v", err)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/concurrent.json", true)
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "source: stale-a\n")
}

func TestGitTokenStoreSaveRetryAfterLeaseConflictCommitsMatchingContent(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A: %v", errEnsure)
	}
	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if errEnsure := storeB.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository B: %v", errEnsure)
	}

	authA := &cliproxyauth.Auth{
		ID:       "local.json",
		FileName: "local.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "local"},
	}
	remoteAdvanced := false
	authA.Storage = &callbackTokenStorage{save: func(path string) error {
		raw, errMarshal := json.Marshal(authA.Metadata)
		if errMarshal != nil {
			return errMarshal
		}
		if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
			return errWrite
		}
		if remoteAdvanced {
			return nil
		}
		remoteAdvanced = true
		_, errSave := storeB.Save(context.Background(), &cliproxyauth.Auth{
			ID:       "concurrent.json",
			FileName: "concurrent.json",
			Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "remote"},
		})
		return errSave
	}}
	if _, errSave := storeA.Save(context.Background(), authA); errSave == nil {
		t.Fatal("first Save error = nil, want lease rejection")
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/local.json", false)
	assertRemoteTreePath(t, remoteDir, "master", "auths/concurrent.json", true)

	authA.Storage = nil
	if _, errSave := storeA.Save(context.Background(), authA); errSave != nil {
		t.Fatalf("second Save after lease rejection: %v", errSave)
	}
	assertRemoteFileContents(t, remoteDir, "master", "auths/local.json", `{"access_token":"local","disabled":false,"type":"codex"}`)
	assertRemoteTreePath(t, remoteDir, "master", "auths/concurrent.json", true)
}

func TestGitTokenStoreConcurrentInitializationDoesNotOverwriteCreatedBranch(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	remoteRepo, errInitRemote := git.PlainInit(remoteDir, true)
	if errInitRemote != nil {
		t.Fatalf("init bare remote: %v", errInitRemote)
	}
	if errHead := remoteRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))); errHead != nil {
		t.Fatalf("set remote HEAD: %v", errHead)
	}

	workspaceDir := filepath.Join(root, "workspace")
	localRepo, errInitLocal := git.PlainInit(workspaceDir, false)
	if errInitLocal != nil {
		t.Fatalf("init local repository: %v", errInitLocal)
	}
	if errSigning := disableGitCommitSigning(workspaceDir); errSigning != nil {
		t.Fatalf("disable local commit signing: %v", errSigning)
	}
	if _, errRemote := localRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remoteDir}}); errRemote != nil {
		t.Fatalf("create local origin: %v", errRemote)
	}
	for _, path := range []string{"auths/.gitkeep", "config/.gitkeep"} {
		fullPath := filepath.Join(workspaceDir, filepath.FromSlash(path))
		if errMkdir := os.MkdirAll(filepath.Dir(fullPath), 0o700); errMkdir != nil {
			t.Fatalf("create local placeholder parent: %v", errMkdir)
		}
		if errWrite := os.WriteFile(fullPath, nil, 0o600); errWrite != nil {
			t.Fatalf("write local placeholder: %v", errWrite)
		}
	}

	winnerDir := filepath.Join(root, "winner")
	winnerRepo, errInitWinner := git.PlainInit(winnerDir, false)
	if errInitWinner != nil {
		t.Fatalf("init winning repository: %v", errInitWinner)
	}
	if errSigning := disableGitCommitSigning(winnerDir); errSigning != nil {
		t.Fatalf("disable winner commit signing: %v", errSigning)
	}
	if errHead := winnerRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))); errHead != nil {
		t.Fatalf("set winner HEAD: %v", errHead)
	}
	winnerFiles := map[string]string{
		"auths/remote.json":  `{"type":"codex","access_token":"remote"}`,
		"config/config.yaml": "source: winner\n",
	}
	winnerWorktree, errWinnerWorktree := winnerRepo.Worktree()
	if errWinnerWorktree != nil {
		t.Fatalf("open winning worktree: %v", errWinnerWorktree)
	}
	for path, contents := range winnerFiles {
		fullPath := filepath.Join(winnerDir, filepath.FromSlash(path))
		if errMkdir := os.MkdirAll(filepath.Dir(fullPath), 0o700); errMkdir != nil {
			t.Fatalf("create winning file parent: %v", errMkdir)
		}
		if errWrite := os.WriteFile(fullPath, []byte(contents), 0o600); errWrite != nil {
			t.Fatalf("write winning file: %v", errWrite)
		}
		if _, errAdd := winnerWorktree.Add(path); errAdd != nil {
			t.Fatalf("add winning file: %v", errAdd)
		}
	}
	if _, errCommit := winnerWorktree.Commit("Initialize complete store", &git.CommitOptions{Author: &object.Signature{
		Name: "CLIProxyAPI", Email: "cliproxy@local", When: time.Unix(1711929600, 0),
	}}); errCommit != nil {
		t.Fatalf("commit winning repository: %v", errCommit)
	}
	if _, errRemote := winnerRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remoteDir}}); errRemote != nil {
		t.Fatalf("create winner origin: %v", errRemote)
	}
	if errPush := winnerRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []gitconfig.RefSpec{"refs/heads/master:refs/heads/master"}}); errPush != nil {
		t.Fatalf("push winning initialization: %v", errPush)
	}

	store := NewGitTokenStore(remoteDir, "", "", "master")
	store.SetBaseDir(filepath.Join(workspaceDir, "auths"))
	store.mu.Lock()
	errInitialize := store.commitAndPushInitialLocked("Initialize git token store", "auths/.gitkeep", "config/.gitkeep")
	store.mu.Unlock()
	if errInitialize == nil {
		t.Fatal("late initialization push error = nil, want branch-creation rejection")
	}
	assertRemoteFileContents(t, remoteDir, "master", "auths/remote.json", winnerFiles["auths/remote.json"])
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", winnerFiles["config/config.yaml"])

	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository after initialization race: %v", errEnsure)
	}
	assertLocalFileContents(t, filepath.Join(workspaceDir, "auths", "remote.json"), winnerFiles["auths/remote.json"])
	assertLocalFileContents(t, filepath.Join(workspaceDir, "config", "config.yaml"), winnerFiles["config/config.yaml"])
}

func TestEnsureRepositoryRetryRestoresTrackedAuthOnUpToDatePull(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository: %v", errEnsure)
	}
	authPath, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "retry.json",
		FileName: "retry.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "remote"},
	})
	if errSave != nil {
		t.Fatalf("Save: %v", errSave)
	}

	repo, errOpen := git.PlainOpen(filepath.Join(root, "workspace"))
	if errOpen != nil {
		t.Fatalf("open workspace repository: %v", errOpen)
	}
	worktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		t.Fatalf("open workspace worktree: %v", errWorktree)
	}
	if _, errRemove := worktree.Remove("auths/retry.json"); errRemove != nil {
		t.Fatalf("stage missing auth: %v", errRemove)
	}
	cfg, errConfig := repo.Config()
	if errConfig != nil {
		t.Fatalf("read workspace config: %v", errConfig)
	}
	cfg.Remotes["origin"].URLs = []string{filepath.Join(root, "missing.git")}
	if errSetConfig := repo.SetConfig(cfg); errSetConfig != nil {
		t.Fatalf("break workspace origin: %v", errSetConfig)
	}
	if errEnsure := store.EnsureRepository(); errEnsure == nil {
		t.Fatal("EnsureRepository with unavailable remote error = nil, want retryable failure")
	}
	if _, errStat := os.Stat(authPath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("missing auth stat error = %v, want not exist", errStat)
	}

	cfg.Remotes["origin"].URLs = []string{remoteDir}
	if errSetConfig := repo.SetConfig(cfg); errSetConfig != nil {
		t.Fatalf("restore workspace origin: %v", errSetConfig)
	}
	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository retry: %v", errEnsure)
	}
	assertLocalFileContents(t, authPath, `{"access_token":"remote","disabled":false,"type":"codex"}`)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List after retry: %v", errList)
	}
	if len(auths) != 1 || auths[0].ID != "retry.json" {
		t.Fatalf("List after retry = %#v, want retry.json", auths)
	}

	if errDelete := store.Delete(context.Background(), authPath); errDelete != nil {
		t.Fatalf("explicit Delete after retry: %v", errDelete)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/retry.json", false)
	auths, errList = store.List(context.Background())
	if errList != nil {
		t.Fatalf("List after explicit Delete: %v", errList)
	}
	if len(auths) != 0 {
		t.Fatalf("List after explicit Delete = %#v, want empty", auths)
	}
}

func TestEnsureRepositoryReconcilesRemoteAuthChangesAroundLocalConfig(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	owner := NewGitTokenStore(remoteDir, "", "", "")
	owner.SetBaseDir(filepath.Join(root, "owner", "auths"))
	if errEnsure := owner.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository owner: %v", errEnsure)
	}
	for _, id := range []string{"modified.json", "deleted.json"} {
		if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
			ID: id, FileName: id, Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "old"},
		}); errSave != nil {
			t.Fatalf("Save owner %s: %v", id, errSave)
		}
	}
	if errWrite := os.WriteFile(owner.ConfigPath(), []byte("source: original\n"), 0o600); errWrite != nil {
		t.Fatalf("write owner config: %v", errWrite)
	}
	if errPersist := owner.PersistConfig(context.Background()); errPersist != nil {
		t.Fatalf("PersistConfig owner: %v", errPersist)
	}

	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A: %v", errEnsure)
	}
	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if errEnsure := storeB.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository B: %v", errEnsure)
	}
	if errWrite := os.WriteFile(storeA.ConfigPath(), []byte("source: local-a\n"), 0o600); errWrite != nil {
		t.Fatalf("write local config A: %v", errWrite)
	}
	if _, errSave := storeB.Save(context.Background(), &cliproxyauth.Auth{
		ID: "modified.json", FileName: "modified.json", Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "new"},
	}); errSave != nil {
		t.Fatalf("Save remote auth update: %v", errSave)
	}
	if errDelete := storeB.Delete(context.Background(), filepath.Join(storeB.AuthDir(), "deleted.json")); errDelete != nil {
		t.Fatalf("Delete remote auth: %v", errDelete)
	}

	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A after remote auth changes: %v", errEnsure)
	}
	assertLocalFileContents(t, storeA.ConfigPath(), "source: local-a\n")
	assertLocalJSONValue(t, filepath.Join(storeA.AuthDir(), "modified.json"), "access_token", "new")
	if _, errStat := os.Stat(filepath.Join(storeA.AuthDir(), "deleted.json")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("deleted local auth stat error = %v, want not exist", errStat)
	}
}

func TestEnsureRepositoryReconcilesRemoteConfigChangesAroundLocalAuth(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	owner := NewGitTokenStore(remoteDir, "", "", "")
	owner.SetBaseDir(filepath.Join(root, "owner", "auths"))
	if errEnsure := owner.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository owner: %v", errEnsure)
	}
	if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
		ID: "local.json", FileName: "local.json", Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "old"},
	}); errSave != nil {
		t.Fatalf("Save owner auth: %v", errSave)
	}
	if errWrite := os.WriteFile(owner.ConfigPath(), []byte("source: original\n"), 0o600); errWrite != nil {
		t.Fatalf("write owner config: %v", errWrite)
	}
	if errPersist := owner.PersistConfig(context.Background()); errPersist != nil {
		t.Fatalf("PersistConfig owner: %v", errPersist)
	}

	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A: %v", errEnsure)
	}
	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if errEnsure := storeB.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository B: %v", errEnsure)
	}
	localAuthPath := filepath.Join(storeA.AuthDir(), "local.json")
	localAuthContents := `{"type":"codex","access_token":"local-dirty"}`
	if errWrite := os.WriteFile(localAuthPath, []byte(localAuthContents), 0o600); errWrite != nil {
		t.Fatalf("write local dirty auth: %v", errWrite)
	}
	if errWrite := os.WriteFile(storeB.ConfigPath(), []byte("source: remote-modified\n"), 0o600); errWrite != nil {
		t.Fatalf("write remote config update: %v", errWrite)
	}
	if errPersist := storeB.PersistConfig(context.Background()); errPersist != nil {
		t.Fatalf("PersistConfig B: %v", errPersist)
	}

	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A after remote config update: %v", errEnsure)
	}
	assertLocalFileContents(t, storeA.ConfigPath(), "source: remote-modified\n")
	assertLocalFileContents(t, localAuthPath, localAuthContents)

	if errRemove := os.Remove(storeB.ConfigPath()); errRemove != nil {
		t.Fatalf("remove config B: %v", errRemove)
	}
	storeB.mu.Lock()
	errDeleteConfig := storeB.commitAndPushLocked("Delete config", "config/config.yaml")
	storeB.mu.Unlock()
	if errDeleteConfig != nil {
		t.Fatalf("commit remote config deletion: %v", errDeleteConfig)
	}
	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A after remote config deletion: %v", errEnsure)
	}
	if _, errStat := os.Stat(storeA.ConfigPath()); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("deleted local config stat error = %v, want not exist", errStat)
	}
	assertLocalFileContents(t, localAuthPath, localAuthContents)
}

func TestEnsureRepositoryFailsClosedOnSamePathConflict(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	owner := NewGitTokenStore(remoteDir, "", "", "")
	owner.SetBaseDir(filepath.Join(root, "owner", "auths"))
	if errEnsure := owner.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository owner: %v", errEnsure)
	}
	if errWrite := os.WriteFile(owner.ConfigPath(), []byte("source: original\n"), 0o600); errWrite != nil {
		t.Fatalf("write owner config: %v", errWrite)
	}
	if errPersist := owner.PersistConfig(context.Background()); errPersist != nil {
		t.Fatalf("PersistConfig owner: %v", errPersist)
	}

	storeA := NewGitTokenStore(remoteDir, "", "", "")
	storeA.SetBaseDir(filepath.Join(root, "workspace-a", "auths"))
	if errEnsure := storeA.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository A: %v", errEnsure)
	}
	storeB := NewGitTokenStore(remoteDir, "", "", "")
	storeB.SetBaseDir(filepath.Join(root, "workspace-b", "auths"))
	if errEnsure := storeB.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository B: %v", errEnsure)
	}
	if errWrite := os.WriteFile(storeA.ConfigPath(), []byte("source: local\n"), 0o600); errWrite != nil {
		t.Fatalf("write local config: %v", errWrite)
	}
	if errWrite := os.WriteFile(storeB.ConfigPath(), []byte("source: remote\n"), 0o600); errWrite != nil {
		t.Fatalf("write remote config: %v", errWrite)
	}
	if errPersist := storeB.PersistConfig(context.Background()); errPersist != nil {
		t.Fatalf("PersistConfig B: %v", errPersist)
	}

	errEnsure := storeA.EnsureRepository()
	if errEnsure == nil || !strings.Contains(errEnsure.Error(), "conflicts with local change") {
		t.Fatalf("EnsureRepository conflict error = %v, want fail-closed conflict", errEnsure)
	}
	assertLocalFileContents(t, storeA.ConfigPath(), "source: local\n")
	assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "source: remote\n")
}

func TestInstallRecoveredGitDirectoryRetainsBackupWhenRestoreFails(t *testing.T) {
	backupPath := filepath.Join("recovery", "corrupt.git")
	installErr := errors.New("install failed")
	restoreErr := errors.New("restore failed")
	calls := 0
	rename := func(_, _ string) error {
		calls++
		switch calls {
		case 1:
			return nil
		case 2:
			return installErr
		default:
			return restoreErr
		}
	}

	retain, errInstall := installRecoveredGitDirectory("repo/.git", "clone/.git", backupPath, rename)
	if !retain {
		t.Fatal("retain recovery = false, want true after failed rollback")
	}
	if !errors.Is(errInstall, installErr) || !errors.Is(errInstall, restoreErr) {
		t.Fatalf("install error = %v, want install and restore failures", errInstall)
	}
	if !strings.Contains(errInstall.Error(), backupPath) {
		t.Fatalf("install error = %q, want retained backup path %q", errInstall, backupPath)
	}
}

func TestGitTokenStoreCorruptionRecoveryUsesLatestRemoteAuthTree(t *testing.T) {
	tests := []struct {
		name          string
		updateRemote  func(*testing.T, *GitTokenStore)
		wantExists    bool
		wantAuthToken string
	}{
		{
			name: "modification",
			updateRemote: func(t *testing.T, store *GitTokenStore) {
				t.Helper()
				if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
					ID: "victim.json", FileName: "victim.json", Provider: "codex",
					Metadata: map[string]any{"type": "codex", "access_token": "remote-new"},
				}); errSave != nil {
					t.Fatalf("update remote auth: %v", errSave)
				}
			},
			wantExists:    true,
			wantAuthToken: "remote-new",
		},
		{
			name: "deletion",
			updateRemote: func(t *testing.T, store *GitTokenStore) {
				t.Helper()
				if errDelete := store.Delete(context.Background(), filepath.Join(store.AuthDir(), "victim.json")); errDelete != nil {
					t.Fatalf("delete remote auth: %v", errDelete)
				}
			},
			wantExists: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			remoteDir := setupGitRemoteRepository(t, root, "master",
				testBranchSpec{name: "master", contents: "remote master branch\n"},
			)
			owner := NewGitTokenStore(remoteDir, "", "", "")
			owner.SetBaseDir(filepath.Join(root, "owner", "auths"))
			if errEnsure := owner.EnsureRepository(); errEnsure != nil {
				t.Fatalf("EnsureRepository owner: %v", errEnsure)
			}
			if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
				ID: "victim.json", FileName: "victim.json", Provider: "codex",
				Metadata: map[string]any{"type": "codex", "access_token": "remote-old"},
			}); errSave != nil {
				t.Fatalf("save initial auth: %v", errSave)
			}

			store := NewGitTokenStore(remoteDir, "", "", "")
			store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
			if errEnsure := store.EnsureRepository(); errEnsure != nil {
				t.Fatalf("EnsureRepository workspace: %v", errEnsure)
			}
			test.updateRemote(t, owner)
			removeHeadFileObject(t, filepath.Join(root, "workspace"), "corrupt-object.txt")

			if errEnsure := store.EnsureRepository(); errEnsure != nil {
				t.Fatalf("EnsureRepository recovery: %v", errEnsure)
			}
			victimPath := filepath.Join(store.AuthDir(), "victim.json")
			if test.wantExists {
				assertLocalJSONValue(t, victimPath, "access_token", test.wantAuthToken)
			} else if _, errStat := os.Stat(victimPath); !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("deleted local auth stat error = %v, want not exist", errStat)
			}

			if _, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
				ID: "unrelated.json", FileName: "unrelated.json", Provider: "codex",
				Metadata: map[string]any{"type": "codex", "access_token": "local"},
			}); errSave != nil {
				t.Fatalf("Save after recovery: %v", errSave)
			}
			assertRemoteTreePath(t, remoteDir, "master", "auths/victim.json", test.wantExists)
			if test.wantExists {
				assertRemoteFileContents(t, remoteDir, "master", "auths/victim.json", `{"access_token":"remote-new","disabled":false,"type":"codex"}`)
			}
		})
	}
}

func TestGitTokenStoreCorruptionRecoveryPreservesOnlyNonConflictingLocalChanges(t *testing.T) {
	setup := func(t *testing.T) (string, *GitTokenStore, *GitTokenStore) {
		t.Helper()
		root := t.TempDir()
		remoteDir := setupGitRemoteRepository(t, root, "master",
			testBranchSpec{name: "master", contents: "remote master branch\n"},
		)
		owner := NewGitTokenStore(remoteDir, "", "", "")
		owner.SetBaseDir(filepath.Join(root, "owner", "auths"))
		if errEnsure := owner.EnsureRepository(); errEnsure != nil {
			t.Fatalf("EnsureRepository owner: %v", errEnsure)
		}
		if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
			ID: "victim.json", FileName: "victim.json", Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "remote-old"},
		}); errSave != nil {
			t.Fatalf("save initial auth: %v", errSave)
		}
		store := NewGitTokenStore(remoteDir, "", "", "")
		store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
		if errEnsure := store.EnsureRepository(); errEnsure != nil {
			t.Fatalf("EnsureRepository workspace: %v", errEnsure)
		}
		return filepath.Join(root, "workspace"), owner, store
	}

	t.Run("non-conflicting change", func(t *testing.T) {
		workspaceDir, owner, store := setup(t)
		if errWrite := os.WriteFile(store.ConfigPath(), []byte("source: local\n"), 0o600); errWrite != nil {
			t.Fatalf("write local config: %v", errWrite)
		}
		if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
			ID: "victim.json", FileName: "victim.json", Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "remote-new"},
		}); errSave != nil {
			t.Fatalf("update remote auth: %v", errSave)
		}
		removeHeadFileObject(t, workspaceDir, "corrupt-object.txt")

		if errEnsure := store.EnsureRepository(); errEnsure != nil {
			t.Fatalf("EnsureRepository recovery: %v", errEnsure)
		}
		assertLocalFileContents(t, store.ConfigPath(), "source: local\n")
		assertLocalJSONValue(t, filepath.Join(store.AuthDir(), "victim.json"), "access_token", "remote-new")
	})

	t.Run("same-path conflict", func(t *testing.T) {
		workspaceDir, owner, store := setup(t)
		victimPath := filepath.Join(store.AuthDir(), "victim.json")
		localContents := `{"type":"codex","access_token":"local"}`
		if errWrite := os.WriteFile(victimPath, []byte(localContents), 0o600); errWrite != nil {
			t.Fatalf("write local auth: %v", errWrite)
		}
		if _, errSave := owner.Save(context.Background(), &cliproxyauth.Auth{
			ID: "victim.json", FileName: "victim.json", Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "remote-new"},
		}); errSave != nil {
			t.Fatalf("update remote auth: %v", errSave)
		}
		removeHeadFileObject(t, workspaceDir, "corrupt-object.txt")

		errEnsure := store.EnsureRepository()
		if errEnsure == nil || !strings.Contains(errEnsure.Error(), "conflicts with local change") {
			t.Fatalf("EnsureRepository conflict error = %v, want fail-closed conflict", errEnsure)
		}
		assertLocalFileContents(t, victimPath, localContents)
		assertRemoteFileContents(t, owner.remote, "master", "auths/victim.json", `{"access_token":"remote-new","disabled":false,"type":"codex"}`)
	})
}

func TestGitTokenStoreFullPackfileCorruptionFailsClosedWithDirtyManagedFile(t *testing.T) {
	setup := func(t *testing.T) (string, string, *GitTokenStore) {
		t.Helper()
		root := t.TempDir()
		remoteDir := setupGitRemoteRepository(t, root, "master",
			testBranchSpec{name: "master", contents: "remote master branch\n"},
		)
		workspaceDir := filepath.Join(root, "workspace")
		store := NewGitTokenStore(remoteDir, "", "", "")
		store.SetBaseDir(filepath.Join(workspaceDir, "auths"))
		if errEnsure := store.EnsureRepository(); errEnsure != nil {
			t.Fatalf("EnsureRepository: %v", errEnsure)
		}
		return remoteDir, workspaceDir, store
	}

	t.Run("config", func(t *testing.T) {
		remoteDir, workspaceDir, store := setup(t)
		configPath := store.ConfigPath()
		if errWrite := os.WriteFile(configPath, []byte("source: remote\n"), 0o600); errWrite != nil {
			t.Fatalf("write initial config: %v", errWrite)
		}
		if errPersist := store.PersistConfig(context.Background()); errPersist != nil {
			t.Fatalf("PersistConfig initial config: %v", errPersist)
		}

		localContents := "source: local-dirty\n"
		if errWrite := os.WriteFile(configPath, []byte(localContents), 0o600); errWrite != nil {
			t.Fatalf("write dirty config: %v", errWrite)
		}
		corruptGitRepository(t, workspaceDir)

		errPersist := store.PersistConfig(context.Background())
		if errPersist == nil || !strings.Contains(errPersist.Error(), "inspect recovery baseline") {
			t.Fatalf("PersistConfig error = %v, want fail-closed recovery baseline error", errPersist)
		}
		assertLocalFileContents(t, configPath, localContents)
		assertRemoteFileContents(t, remoteDir, "master", "config/config.yaml", "source: remote\n")
	})

	t.Run("auth", func(t *testing.T) {
		remoteDir, workspaceDir, store := setup(t)
		authPath, errSave := store.Save(context.Background(), &cliproxyauth.Auth{
			ID: "dirty.json", FileName: "dirty.json", Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "remote"},
		})
		if errSave != nil {
			t.Fatalf("Save initial auth: %v", errSave)
		}

		localContents := `{"type":"codex","access_token":"local-dirty"}`
		if errWrite := os.WriteFile(authPath, []byte(localContents), 0o600); errWrite != nil {
			t.Fatalf("write dirty auth: %v", errWrite)
		}
		corruptGitRepository(t, workspaceDir)

		_, errSave = store.Save(context.Background(), &cliproxyauth.Auth{
			ID: "unrelated.json", FileName: "unrelated.json", Provider: "codex",
			Metadata: map[string]any{"type": "codex", "access_token": "unrelated"},
		})
		if errSave == nil || !strings.Contains(errSave.Error(), "inspect recovery baseline") {
			t.Fatalf("Save error = %v, want fail-closed recovery baseline error", errSave)
		}
		assertLocalFileContents(t, authPath, localContents)
		assertRemoteFileContents(t, remoteDir, "master", "auths/dirty.json", `{"access_token":"remote","disabled":false,"type":"codex"}`)
		assertRemoteTreePath(t, remoteDir, "master", "auths/unrelated.json", false)
	})
}

func TestGitTokenStoreMissingPackfileRecoveryFailsClosedWithoutBaseline(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	baseDir := filepath.Join(root, "workspace", "auths")
	store.SetBaseDir(baseDir)
	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatalf("EnsureRepository: %v", errEnsure)
	}
	auth := &cliproxyauth.Auth{
		ID:       "recover.json",
		FileName: "recover.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "remote"},
	}
	authPath, errSave := store.Save(context.Background(), auth)
	if errSave != nil {
		t.Fatalf("Save: %v", errSave)
	}

	repo := corruptGitRepository(t, filepath.Join(root, "workspace"))
	if errRemove := os.Remove(authPath); errRemove != nil {
		t.Fatalf("remove local auth before recovery: %v", errRemove)
	}
	if errVerify := verifyRepositoryHead(repo); !isRepositoryCorruptionError(errVerify) {
		t.Fatalf("verifyRepositoryHead error = %v, want repository corruption", errVerify)
	}

	errEnsure := store.EnsureRepository()
	if errEnsure == nil || !strings.Contains(errEnsure.Error(), "inspect recovery baseline") {
		t.Fatalf("EnsureRepository error = %v, want fail-closed recovery baseline error", errEnsure)
	}
	if _, errStat := os.Stat(authPath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("local deleted auth stat error = %v, want not exist", errStat)
	}
	assertRemoteTreePath(t, remoteDir, "master", "auths/recover.json", true)
}

func TestCommitAndPushLockedPushesBeforeRunningGC(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)

	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}

	workspaceDir := filepath.Join(root, "workspace")
	updates := []string{
		"local master update one\n",
		"local master update two\n",
	}
	for _, contents := range updates {
		if err := os.WriteFile(filepath.Join(workspaceDir, "branch.txt"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write local master marker: %v", err)
		}

		store.lastGC = time.Now().Add(-gcInterval)
		store.mu.Lock()
		err := store.commitAndPushLocked("Update master marker", "branch.txt")
		store.mu.Unlock()
		if err != nil {
			t.Fatalf("commitAndPushLocked with forced GC: %v", err)
		}

		assertRemoteBranchContents(t, remoteDir, "master", contents)
	}
}

func TestEnsureRepositoryFollowsRenamedRemoteDefaultBranchWhenAvailable(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
		testBranchSpec{name: "main", contents: "remote main branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(baseDir)

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository initial clone: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "master", "remote master branch\n")

	setRemoteHeadBranch(t, remoteDir, "main")
	advanceRemoteBranch(t, filepath.Join(root, "seed"), remoteDir, "main", "remote main branch updated\n", "advance main")

	reopened := NewGitTokenStore(remoteDir, "", "", "")
	reopened.SetBaseDir(baseDir)

	if err := reopened.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository after remote default rename: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "main", "remote main branch updated\n")
	assertRemoteHeadBranch(t, remoteDir, "main")
}

func TestEnsureRepositoryKeepsCurrentBranchWhenRemoteDefaultCannotBeResolved(t *testing.T) {
	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
		testBranchSpec{name: "develop", contents: "remote develop branch\n"},
	)

	baseDir := filepath.Join(root, "workspace", "auths")
	pinned := NewGitTokenStore(remoteDir, "", "", "develop")
	pinned.SetBaseDir(baseDir)
	if err := pinned.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository pinned: %v", err)
	}
	assertRepositoryBranchAndContents(t, filepath.Join(root, "workspace"), "develop", "remote develop branch\n")

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	defer authServer.Close()

	repo, err := git.PlainOpen(filepath.Join(root, "workspace"))
	if err != nil {
		t.Fatalf("open workspace repo: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	cfg.Remotes["origin"].URLs = []string{authServer.URL}
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatalf("set repo config: %v", err)
	}

	reopened := NewGitTokenStore(remoteDir, "", "", "")
	reopened.SetBaseDir(baseDir)

	if err := reopened.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository default branch fallback: %v", err)
	}
	assertRepositoryHeadBranch(t, filepath.Join(root, "workspace"), "develop")
}

func removeHeadFileObject(t *testing.T, repoDir, path string) {
	t.Helper()

	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		t.Fatalf("open repository before object removal: %v", errOpen)
	}
	worktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		t.Fatalf("open worktree before object removal: %v", errWorktree)
	}
	fullPath := filepath.Join(repoDir, filepath.FromSlash(path))
	if errWrite := os.WriteFile(fullPath, []byte("corrupt me\n"), 0o600); errWrite != nil {
		t.Fatalf("write corruption marker: %v", errWrite)
	}
	if _, errAdd := worktree.Add(path); errAdd != nil {
		t.Fatalf("add corruption marker: %v", errAdd)
	}
	if _, errCommit := worktree.Commit("Add corruption marker", &git.CommitOptions{Author: &object.Signature{
		Name: "CLIProxyAPI", Email: "cliproxy@local", When: time.Unix(1711929600, 0),
	}}); errCommit != nil {
		t.Fatalf("commit corruption marker: %v", errCommit)
	}
	head, errHead := repo.Head()
	if errHead != nil {
		t.Fatalf("read repository head: %v", errHead)
	}
	commit, errCommit := repo.CommitObject(head.Hash())
	if errCommit != nil {
		t.Fatalf("read repository commit: %v", errCommit)
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		t.Fatalf("read repository tree: %v", errTree)
	}
	file, errFile := tree.File(path)
	if errFile != nil {
		t.Fatalf("read repository file %s: %v", path, errFile)
	}
	objectPath := filepath.Join(repoDir, ".git", "objects", file.Hash.String()[:2], file.Hash.String()[2:])
	if errRemove := os.Remove(objectPath); errRemove != nil {
		t.Fatalf("remove repository object for %s: %v", path, errRemove)
	}
	if errVerify := verifyRepositoryHead(repo); !isRepositoryCorruptionError(errVerify) {
		t.Fatalf("verifyRepositoryHead error = %v, want repository corruption", errVerify)
	}
}

func corruptGitRepository(t *testing.T, repoDir string) *git.Repository {
	t.Helper()

	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		t.Fatalf("open repository before corruption: %v", errOpen)
	}
	if errRepack := repo.RepackObjects(&git.RepackConfig{}); errRepack != nil {
		t.Fatalf("repack repository objects: %v", errRepack)
	}
	objectsDir := filepath.Join(repoDir, ".git", "objects")
	objectEntries, errReadDir := os.ReadDir(objectsDir)
	if errReadDir != nil {
		t.Fatalf("read object directory: %v", errReadDir)
	}
	for _, entry := range objectEntries {
		if entry.IsDir() && len(entry.Name()) == 2 {
			if errRemove := os.RemoveAll(filepath.Join(objectsDir, entry.Name())); errRemove != nil {
				t.Fatalf("remove loose object directory %s: %v", entry.Name(), errRemove)
			}
		}
	}
	packfiles, errGlob := filepath.Glob(filepath.Join(objectsDir, "pack", "*.pack"))
	if errGlob != nil {
		t.Fatalf("glob packfiles: %v", errGlob)
	}
	if len(packfiles) == 0 {
		t.Fatal("no packfiles found to corrupt")
	}
	for _, packfile := range packfiles {
		if errRemove := os.Remove(packfile); errRemove != nil {
			t.Fatalf("remove packfile %s: %v", filepath.Base(packfile), errRemove)
		}
	}
	return repo
}

func setupGitRemoteRepository(t *testing.T, root, defaultBranch string, branches ...testBranchSpec) string {
	t.Helper()

	remoteDir := filepath.Join(root, "remote.git")
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	seedDir := filepath.Join(root, "seed")
	seedRepo, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatalf("init seed repo: %v", err)
	}
	seedConfig, errConfig := seedRepo.Config()
	if errConfig != nil {
		t.Fatalf("get seed repo config: %v", errConfig)
	}
	seedConfig.Commit.GpgSign = gitconfig.OptBoolFalse
	if errSetConfig := seedRepo.SetConfig(seedConfig); errSetConfig != nil {
		t.Fatalf("disable seed repo commit signing: %v", errSetConfig)
	}
	if err := seedRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch))); err != nil {
		t.Fatalf("set seed HEAD: %v", err)
	}

	worktree, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("open seed worktree: %v", err)
	}

	defaultSpec, ok := findBranchSpec(branches, defaultBranch)
	if !ok {
		t.Fatalf("missing default branch spec for %q", defaultBranch)
	}
	commitBranchMarker(t, seedDir, worktree, defaultSpec, "seed default branch")

	for _, branch := range branches {
		if branch.name == defaultBranch {
			continue
		}
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(defaultBranch)}); err != nil {
			t.Fatalf("checkout default branch %s: %v", defaultBranch, err)
		}
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch.name), Create: true}); err != nil {
			t.Fatalf("create branch %s: %v", branch.name, err)
		}
		commitBranchMarker(t, seedDir, worktree, branch, "seed branch "+branch.name)
	}

	if _, err := seedRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remoteDir}}); err != nil {
		t.Fatalf("create origin remote: %v", err)
	}
	if err := seedRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("refs/heads/*:refs/heads/*")},
	}); err != nil {
		t.Fatalf("push seed branches: %v", err)
	}

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	if err := remoteRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch))); err != nil {
		t.Fatalf("set remote HEAD: %v", err)
	}

	return remoteDir
}

func commitBranchMarker(t *testing.T, seedDir string, worktree *git.Worktree, branch testBranchSpec, message string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(seedDir, "branch.txt"), []byte(branch.contents), 0o600); err != nil {
		t.Fatalf("write branch marker for %s: %v", branch.name, err)
	}
	if _, err := worktree.Add("branch.txt"); err != nil {
		t.Fatalf("add branch marker for %s: %v", branch.name, err)
	}
	if _, err := worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "CLIProxyAPI",
			Email: "cliproxy@local",
			When:  time.Unix(1711929600, 0),
		},
	}); err != nil {
		t.Fatalf("commit branch marker for %s: %v", branch.name, err)
	}
}

func advanceRemoteBranch(t *testing.T, seedDir, remoteDir, branch, contents, message string) {
	t.Helper()

	seedRepo, err := git.PlainOpen(seedDir)
	if err != nil {
		t.Fatalf("open seed repo: %v", err)
	}
	worktree, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("open seed worktree: %v", err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch)}); err != nil {
		t.Fatalf("checkout branch %s: %v", branch, err)
	}
	commitBranchMarker(t, seedDir, worktree, testBranchSpec{name: branch, contents: contents}, message)
	if err := seedRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec(plumbing.NewBranchReferenceName(branch).String() + ":" + plumbing.NewBranchReferenceName(branch).String()),
		},
	}); err != nil {
		t.Fatalf("push branch %s update to %s: %v", branch, remoteDir, err)
	}
}

func advanceRemoteBranchFromNewBranch(t *testing.T, seedDir, remoteDir, branch, contents, message string) {
	t.Helper()

	seedRepo, err := git.PlainOpen(seedDir)
	if err != nil {
		t.Fatalf("open seed repo: %v", err)
	}
	worktree, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("open seed worktree: %v", err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("master")}); err != nil {
		t.Fatalf("checkout master before creating %s: %v", branch, err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Create: true}); err != nil {
		t.Fatalf("create branch %s: %v", branch, err)
	}
	commitBranchMarker(t, seedDir, worktree, testBranchSpec{name: branch, contents: contents}, message)
	if err := seedRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec(plumbing.NewBranchReferenceName(branch).String() + ":" + plumbing.NewBranchReferenceName(branch).String()),
		},
	}); err != nil {
		t.Fatalf("push new branch %s update to %s: %v", branch, remoteDir, err)
	}
}

func findBranchSpec(branches []testBranchSpec, name string) (testBranchSpec, bool) {
	for _, branch := range branches {
		if branch.name == name {
			return branch, true
		}
	}
	return testBranchSpec{}, false
}

func assertLocalFileContents(t *testing.T, path, wantContents string) {
	t.Helper()

	contents, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read local file %s: %v", path, errRead)
	}
	if string(contents) != wantContents {
		t.Fatalf("local file %s contents = %q, want %q", path, contents, wantContents)
	}
}

func assertLocalJSONValue(t *testing.T, path, key, wantValue string) {
	t.Helper()

	contents, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read local JSON file %s: %v", path, errRead)
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(contents, &metadata); errUnmarshal != nil {
		t.Fatalf("unmarshal local JSON file %s: %v", path, errUnmarshal)
	}
	if gotValue, _ := metadata[key].(string); gotValue != wantValue {
		t.Fatalf("local JSON file %s value %s = %q, want %q", path, key, gotValue, wantValue)
	}
}

func assertRemoteTreePath(t *testing.T, remoteDir, branch, path string, want bool) {
	t.Helper()

	repo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		t.Fatalf("read remote branch %s: %v", branch, err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("read remote commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("read remote tree: %v", err)
	}
	_, err = tree.File(filepath.ToSlash(path))
	got := err == nil
	if err != nil && !errors.Is(err, object.ErrFileNotFound) {
		t.Fatalf("inspect remote path %s: %v", path, err)
	}
	if got != want {
		t.Fatalf("remote path %s exists = %v, want %v", path, got, want)
	}
}

func assertRemoteFileContents(t *testing.T, remoteDir, branch, path, wantContents string) {
	t.Helper()

	repo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		t.Fatalf("read remote branch %s: %v", branch, err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("read remote commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("read remote tree: %v", err)
	}
	file, err := tree.File(filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("read remote file %s: %v", path, err)
	}
	contents, err := file.Contents()
	if err != nil {
		t.Fatalf("read remote file %s contents: %v", path, err)
	}
	if contents != wantContents {
		t.Fatalf("remote file %s contents = %q, want %q", path, contents, wantContents)
	}
}

func assertRepositoryBranchAndContents(t *testing.T, repoDir, branch, wantContents string) {
	t.Helper()

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("open local repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("local repo head: %v", err)
	}
	if got, want := head.Name(), plumbing.NewBranchReferenceName(branch); got != want {
		t.Fatalf("local head branch = %s, want %s", got, want)
	}
	contents, err := os.ReadFile(filepath.Join(repoDir, "branch.txt"))
	if err != nil {
		t.Fatalf("read branch marker: %v", err)
	}
	if got := string(contents); got != wantContents {
		t.Fatalf("branch marker contents = %q, want %q", got, wantContents)
	}
}

func assertRepositoryHeadBranch(t *testing.T, repoDir, branch string) {
	t.Helper()

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("open local repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("local repo head: %v", err)
	}
	if got, want := head.Name(), plumbing.NewBranchReferenceName(branch); got != want {
		t.Fatalf("local head branch = %s, want %s", got, want)
	}
}

func assertRemoteHeadBranch(t *testing.T, remoteDir, branch string) {
	t.Helper()

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	head, err := remoteRepo.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read remote HEAD: %v", err)
	}
	if got, want := head.Target(), plumbing.NewBranchReferenceName(branch); got != want {
		t.Fatalf("remote HEAD target = %s, want %s", got, want)
	}
}

func setRemoteHeadBranch(t *testing.T, remoteDir, branch string) {
	t.Helper()

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	if err := remoteRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		t.Fatalf("set remote HEAD to %s: %v", branch, err)
	}
}

func assertRemoteBranchExistsWithCommit(t *testing.T, remoteDir, branch string) {
	t.Helper()

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	ref, err := remoteRepo.Reference(plumbing.NewBranchReferenceName(branch), false)
	if err != nil {
		t.Fatalf("read remote branch %s: %v", branch, err)
	}
	if got := ref.Hash(); got == plumbing.ZeroHash {
		t.Fatalf("remote branch %s hash = %s, want non-zero hash", branch, got)
	}
}

func assertRemoteBranchDoesNotExist(t *testing.T, remoteDir, branch string) {
	t.Helper()

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	if _, err := remoteRepo.Reference(plumbing.NewBranchReferenceName(branch), false); err == nil {
		t.Fatalf("remote branch %s exists, want missing", branch)
	} else if err != plumbing.ErrReferenceNotFound {
		t.Fatalf("read remote branch %s: %v", branch, err)
	}
}

func assertRemoteBranchContents(t *testing.T, remoteDir, branch, wantContents string) {
	t.Helper()

	remoteRepo, err := git.PlainOpen(remoteDir)
	if err != nil {
		t.Fatalf("open remote repo: %v", err)
	}
	ref, err := remoteRepo.Reference(plumbing.NewBranchReferenceName(branch), false)
	if err != nil {
		t.Fatalf("read remote branch %s: %v", branch, err)
	}
	commit, err := remoteRepo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("read remote branch %s commit: %v", branch, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("read remote branch %s tree: %v", branch, err)
	}
	file, err := tree.File("branch.txt")
	if err != nil {
		t.Fatalf("read remote branch %s file: %v", branch, err)
	}
	contents, err := file.Contents()
	if err != nil {
		t.Fatalf("read remote branch %s contents: %v", branch, err)
	}
	if contents != wantContents {
		t.Fatalf("remote branch %s contents = %q, want %q", branch, contents, wantContents)
	}
}
