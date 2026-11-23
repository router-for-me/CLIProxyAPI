package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func nativeStoreGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	gitPath, errLookup := exec.LookPath("git")
	if errLookup != nil {
		t.Skip("native Git is required")
	}
	args = append([]string{"-C", dir, "-c", "user.name=Git Store Test", "-c", "user.email=git-store@example.test", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command(gitPath, args...)
	out, errRun := cmd.CombinedOutput()
	if errRun != nil {
		t.Fatalf("git %v: %v\n%s", args, errRun, out)
	}
	return strings.TrimSpace(string(out))
}

func gitWorkspacePair(t *testing.T) (string, *GitTokenStore, *GitTokenStore) {
	t.Helper()
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "base\n"})
	a := NewGitTokenStore(remote, "", "", "master")
	b := NewGitTokenStore(remote, "", "", "master")
	for dir, store := range map[string]*GitTokenStore{"a": a, "b": b} {
		store.SetBaseDir(filepath.Join(root, dir, "auths"))
		if errSync := store.EnsureRepository(); errSync != nil {
			t.Fatal(errSync)
		}
	}
	return remote, a, b
}

func TestGitSyncPreservesLocalAheadCommitAndStaging(t *testing.T) {
	remote, a, _ := gitWorkspacePair(t)
	dir := a.repoDirSnapshot()
	writeSeedTestFile(t, filepath.Join(dir, "local.txt"), "committed\n")
	nativeStoreGit(t, dir, "add", "local.txt")
	nativeStoreGit(t, dir, "commit", "-m", "User commit")
	oid := nativeStoreGit(t, dir, "rev-parse", "HEAD")
	writeSeedTestFile(t, filepath.Join(dir, "branch.txt"), "staged\n")
	nativeStoreGit(t, dir, "add", "branch.txt")
	writeSeedTestFile(t, filepath.Join(dir, "branch.txt"), "unstaged\n")
	// Use a fresh store to ensure ownership does not depend on session state.
	reopened := NewGitTokenStore(remote, "", "", "master")
	reopened.SetBaseDir(a.AuthDir())
	if errSync := reopened.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	if got := nativeStoreGit(t, dir, "rev-parse", "HEAD"); got != oid {
		t.Fatalf("HEAD = %s, want local user commit %s", got, oid)
	}
	if got := nativeStoreGit(t, dir, "show", ":branch.txt"); got != "staged" {
		t.Fatalf("staged contents = %q", got)
	}
	assertLocalFileContents(t, filepath.Join(dir, "branch.txt"), "unstaged\n")
	assertRemoteTreePath(t, remote, "master", "local.txt", false)
}

func TestGitRejectedPushLeavesUserCommitAndIndexThroughNextSync(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	dir := a.repoDirSnapshot()
	writeSeedTestFile(t, a.ConfigPath(), "port: 8317\n")
	writeSeedTestFile(t, b.ConfigPath(), "port: 8317\n")
	var localOID string
	a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		if _, errBootstrap := b.BootstrapConfig(ctx, ""); errBootstrap != nil {
			t.Fatal(errBootstrap)
		}
		if _, errSave := b.Save(ctx, &cliproxyauth.Auth{ID: "peer.json", Metadata: map[string]any{"type": "codex"}}); errSave != nil {
			t.Fatal(errSave)
		}
		writeSeedTestFile(t, filepath.Join(dir, "user.txt"), "my commit\n")
		nativeStoreGit(t, dir, "add", "user.txt")
		nativeStoreGit(t, dir, "commit", "-m", "Commit during store push")
		localOID = nativeStoreGit(t, dir, "rev-parse", "HEAD")
		writeSeedTestFile(t, a.ConfigPath(), "port: 8888\n")
		nativeStoreGit(t, dir, "add", gitConfigPath)
		writeSeedTestFile(t, a.ConfigPath(), "port: 9999\n")
		return repo.PushContext(ctx, opts)
	}
	if errPersist := a.PersistConfig(context.Background()); !errors.Is(errPersist, errGitRemoteAdvanced) || !errors.Is(errPersist, errGitLocalAdvanced) {
		t.Fatalf("PersistConfig error = %v, want remote and local advancement", errPersist)
	}
	reopened := NewGitTokenStore(remote, "", "", "master")
	reopened.SetBaseDir(a.AuthDir())
	if errSync := reopened.EnsureRepository(); !errors.Is(errSync, errGitLocalAdvanced) {
		t.Fatalf("next sync error = %v, want divergence", errSync)
	}
	if got := nativeStoreGit(t, dir, "rev-parse", "HEAD"); got != localOID {
		t.Fatalf("HEAD = %s, want local user commit %s", got, localOID)
	}
	if got := nativeStoreGit(t, dir, "show", ":"+gitConfigPath); got != "port: 8888" {
		t.Fatalf("staged config = %q", got)
	}
	assertLocalFileContents(t, a.ConfigPath(), "port: 9999\n")
	assertRemoteTreePath(t, remote, "master", "user.txt", false)
	assertRemoteTreePath(t, remote, "master", "auths/peer.json", true)
}

func TestGitPushLeaseIsPinnedBeforeIndependentFetch(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	dir := a.repoDirSnapshot()
	base := nativeStoreGit(t, dir, "rev-parse", "HEAD")
	auth := &cliproxyauth.Auth{ID: "local.json", Metadata: map[string]any{"type": "codex"}}
	auth.Storage = &callbackTokenStorage{save: func(path string) error {
		if _, errSave := b.Save(context.Background(), &cliproxyauth.Auth{ID: "peer.json", Metadata: map[string]any{"type": "codex"}}); errSave != nil {
			t.Fatal(errSave)
		}
		nativeStoreGit(t, dir, "fetch", "origin")
		if tracked := nativeStoreGit(t, dir, "rev-parse", "origin/master"); tracked == base {
			t.Fatal("independent fetch did not advance the tracking ref")
		}
		return os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600)
	}}
	if _, errSave := a.Save(context.Background(), auth); !errors.Is(errSave, errGitRemoteAdvanced) {
		t.Fatalf("Save error = %v, want rejection with the original lease", errSave)
	}
	assertRemoteTreePath(t, remote, "master", "auths/peer.json", true)
	assertRemoteTreePath(t, remote, "master", "auths/local.json", false)
	auth.Storage = nil
	if _, errSave := a.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("Save after sync: %v", errSave)
	}
	assertRemoteTreePath(t, remote, "master", "auths/peer.json", true)
	assertRemoteTreePath(t, remote, "master", "auths/local.json", true)
}

func TestGitFastForwardPreservesModesLinksAndUnrelatedEdits(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	adir, bdir := a.repoDirSnapshot(), b.repoDirSnapshot()
	writeSeedTestFile(t, filepath.Join(bdir, "script"), "#!/bin/sh\n")
	if errMode := os.Chmod(filepath.Join(bdir, "script"), 0o755); errMode != nil {
		t.Fatal(errMode)
	}
	if errLink := os.Symlink("script", filepath.Join(bdir, "shortcut")); errLink != nil {
		t.Skipf("symlinks unavailable: %v", errLink)
	}
	writeSeedTestFile(t, filepath.Join(bdir, "regular"), "read me\n")
	nativeStoreGit(t, bdir, "add", "script", "shortcut", "regular")
	nativeStoreGit(t, bdir, "commit", "-m", "Add modes")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	writeSeedTestFile(t, filepath.Join(adir, "branch.txt"), "staged locally\n")
	nativeStoreGit(t, adir, "add", "branch.txt")
	writeSeedTestFile(t, filepath.Join(adir, "branch.txt"), "unstaged locally\n")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	for name, executable := range map[string]bool{"script": true, "regular": false} {
		info, errStat := os.Lstat(filepath.Join(adir, name))
		if errStat != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0o111 != 0) != executable {
			t.Fatalf("%s: %v, %v", name, info, errStat)
		}
	}
	if target, errLink := os.Readlink(filepath.Join(adir, "shortcut")); errLink != nil || target != "script" {
		t.Fatalf("shortcut = %q, %v", target, errLink)
	}
	if status := nativeStoreGit(t, adir, "status", "--porcelain"); status != "MM branch.txt" {
		t.Fatalf("status = %q", status)
	}
	assertLocalFileContents(t, filepath.Join(adir, "branch.txt"), "unstaged locally\n")
	assertRemoteFileContents(t, remote, "master", "branch.txt", "base\n")
}

func TestGitFastForwardPreflightsDirectoryConflictBeforeChangingConfig(t *testing.T) {
	for _, withIgnoredFile := range []bool{false, true} {
		name := "empty directory"
		if withIgnoredFile {
			name = "directory with ignored file"
		}
		t.Run(name, func(t *testing.T) {
			_, a, b := gitWorkspacePair(t)
			adir, bdir := a.repoDirSnapshot(), b.repoDirSnapshot()
			writeSeedTestFile(t, b.ConfigPath(), "port: 8317\n")
			if _, errBootstrap := b.BootstrapConfig(context.Background(), ""); errBootstrap != nil {
				t.Fatal(errBootstrap)
			}
			if errSync := a.EnsureRepository(); errSync != nil {
				t.Fatal(errSync)
			}
			before := string(readSeedTestFile(t, a.ConfigPath()))
			base := nativeStoreGit(t, adir, "rev-parse", "HEAD")
			after := strings.Replace(before, "port: 8317", "port: 8765", 1)
			if before == after {
				t.Fatal("test did not change config")
			}
			writeSeedTestFile(t, b.ConfigPath(), after)
			writeSeedTestFile(t, filepath.Join(bdir, "branch.txt"), "remote change\n")
			writeSeedTestFile(t, filepath.Join(bdir, "remote.txt"), "remote addition\n")
			nativeStoreGit(t, bdir, "add", gitConfigPath, "branch.txt", "remote.txt")
			nativeStoreGit(t, bdir, "commit", "-m", "Update configuration and worktree")
			nativeStoreGit(t, bdir, "push", "origin", "master")
			want := nativeStoreGit(t, bdir, "rev-parse", "HEAD")

			conflict := filepath.Join(adir, "remote.txt")
			if errMkdir := os.Mkdir(conflict, 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			if withIgnoredFile {
				writeSeedTestFile(t, filepath.Join(adir, ".git", "info", "exclude"), "remote.txt/\n")
				writeSeedTestFile(t, filepath.Join(conflict, "user.txt"), "keep me\n")
			}
			if errSync := a.EnsureRepository(); errSync == nil || !strings.Contains(errSync.Error(), "remote.txt") {
				t.Fatalf("first sync error = %v, want a path conflict", errSync)
			}
			assertLocalFileContents(t, a.ConfigPath(), before)
			assertLocalFileContents(t, filepath.Join(adir, "branch.txt"), "base\n")
			if got := nativeStoreGit(t, adir, "rev-parse", "HEAD"); got != base {
				t.Fatalf("HEAD = %s after failed sync, want %s", got, base)
			}
			if got := nativeStoreGit(t, adir, "diff", "--cached", "--name-only"); got != "" {
				t.Fatalf("index changed after failed sync: %q", got)
			}
			if info, errStat := os.Lstat(conflict); errStat != nil || !info.IsDir() {
				t.Fatalf("user directory was not kept: %v, %v", info, errStat)
			}
			if withIgnoredFile {
				assertLocalFileContents(t, filepath.Join(conflict, "user.txt"), "keep me\n")
			}
			if errMove := os.Rename(conflict, filepath.Join(adir, "saved-user-directory")); errMove != nil {
				t.Fatal(errMove)
			}
			if errSync := a.EnsureRepository(); errSync != nil {
				t.Fatalf("sync after removing conflict: %v", errSync)
			}
			if got := nativeStoreGit(t, adir, "rev-parse", "HEAD"); got != want {
				t.Fatalf("HEAD = %s after retry, want %s", got, want)
			}
			assertLocalFileContents(t, a.ConfigPath(), after)
			assertLocalFileContents(t, filepath.Join(adir, "branch.txt"), "remote change\n")
			assertLocalFileContents(t, conflict, "remote addition\n")
		})
	}
}

func TestGitFastForwardHandlesTrackedFileDirectoryTransitions(t *testing.T) {
	_, a, b := gitWorkspacePair(t)
	adir, bdir := a.repoDirSnapshot(), b.repoDirSnapshot()
	bpath := filepath.Join(bdir, "branch.txt")
	if errRemove := os.Remove(bpath); errRemove != nil {
		t.Fatal(errRemove)
	}
	writeSeedTestFile(t, filepath.Join(bpath, "nested", "file"), "tracked child\n")
	nativeStoreGit(t, bdir, "add", "-A", "branch.txt")
	nativeStoreGit(t, bdir, "commit", "-m", "Change file to directory")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	apath := filepath.Join(adir, "branch.txt")
	assertLocalFileContents(t, filepath.Join(apath, "nested", "file"), "tracked child\n")

	if errRemove := os.RemoveAll(bpath); errRemove != nil {
		t.Fatal(errRemove)
	}
	writeSeedTestFile(t, bpath, "remote file\n")
	nativeStoreGit(t, bdir, "add", "-A", "branch.txt")
	nativeStoreGit(t, bdir, "commit", "-m", "Change directory back to file")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	userDir := filepath.Join(apath, "untracked")
	if errMkdir := os.Mkdir(userDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errSync := a.EnsureRepository(); errSync == nil || !strings.Contains(errSync.Error(), "untracked") {
		t.Fatalf("first sync error = %v, want conflict with untracked directory", errSync)
	}
	assertLocalFileContents(t, filepath.Join(apath, "nested", "file"), "tracked child\n")
	if errRemove := os.Remove(userDir); errRemove != nil {
		t.Fatal(errRemove)
	}
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	assertLocalFileContents(t, apath, "remote file\n")
	if status := nativeStoreGit(t, adir, "status", "--porcelain"); status != "" {
		t.Fatalf("unexpected worktree changes: %q", status)
	}
}

func TestGitFastForwardPrunesParentsBeforeLaterFileAddition(t *testing.T) {
	_, a, b := gitWorkspacePair(t)
	adir, bdir := a.repoDirSnapshot(), b.repoDirSnapshot()
	for _, path := range []string{"transition/deep/leaf", "transition/other/leaf", "kept-empty/leaf", "kept-file/leaf", "reused/leaf"} {
		writeSeedTestFile(t, filepath.Join(bdir, filepath.FromSlash(path)), "tracked\n")
	}
	nativeStoreGit(t, bdir, "add", "transition", "kept-empty", "kept-file", "reused")
	nativeStoreGit(t, bdir, "commit", "-m", "Add tracked directories")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	localEmpty := filepath.Join(adir, "kept-empty", "owned")
	if errMkdir := os.Mkdir(localEmpty, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	localFile := filepath.Join(adir, "kept-file", "owned")
	writeSeedTestFile(t, localFile, "local\n")

	nativeStoreGit(t, bdir, "rm", "-r", "transition", "kept-empty", "kept-file", "reused")
	writeSeedTestFile(t, filepath.Join(bdir, "reused", "replacement"), "remote child\n")
	nativeStoreGit(t, bdir, "add", "reused")
	nativeStoreGit(t, bdir, "commit", "-m", "Remove tracked directory contents")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatalf("first fast-forward: %v", errSync)
	}
	if _, errStat := os.Lstat(filepath.Join(adir, "transition")); !errors.Is(errStat, os.ErrNotExist) {
		t.Errorf("first fast-forward did not prune the emptied directory: %v", errStat)
	}
	if info, errStat := os.Lstat(localEmpty); errStat != nil || !info.IsDir() {
		t.Fatalf("user's empty directory was removed: %v", errStat)
	}
	assertLocalFileContents(t, localFile, "local\n")
	assertLocalFileContents(t, filepath.Join(adir, "reused", "replacement"), "remote child\n")

	writeSeedTestFile(t, filepath.Join(bdir, "transition"), "remote file\n")
	nativeStoreGit(t, bdir, "add", "transition")
	nativeStoreGit(t, bdir, "commit", "-m", "Add file at former directory path")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatalf("second fast-forward: %v", errSync)
	}
	assertLocalFileContents(t, filepath.Join(adir, "transition"), "remote file\n")
	if status := nativeStoreGit(t, adir, "status", "--porcelain"); status != "?? kept-file/" {
		t.Fatalf("unexpected worktree changes: %q", status)
	}
}

func TestGitFastForwardRestoresPrunedParentsOnConfigFailure(t *testing.T) {
	_, a, b := gitWorkspacePair(t)
	adir, bdir := a.repoDirSnapshot(), b.repoDirSnapshot()
	const leaf = "transition/deep/leaf"
	const original = "management: {secret-key: git-rollback-plaintext}\nport: 8317\n"
	writeSeedTestFile(t, filepath.Join(bdir, filepath.FromSlash(leaf)), "tracked\n")
	writeSeedTestFile(t, b.ConfigPath(), original)
	nativeStoreGit(t, bdir, "add", leaf, gitConfigPath)
	nativeStoreGit(t, bdir, "commit", "-m", "Add tracked directory and config")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	base := nativeStoreGit(t, adir, "rev-parse", "HEAD")
	parent := filepath.Join(adir, "transition", "deep")
	if runtime.GOOS != "windows" {
		if errChmod := os.Chmod(parent, 0o775); errChmod != nil {
			t.Fatal(errChmod)
		}
		// Keep the old inode alive so a recreated directory gets a new identity.
		dir, errOpen := os.Open(parent)
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		t.Cleanup(func() {
			if errClose := dir.Close(); errClose != nil {
				t.Error(errClose)
			}
		})
	}
	before, errBefore := os.Stat(parent)
	if errBefore != nil {
		t.Fatal(errBefore)
	}

	// The config CAS fails after the ordinary checkout updates have run.
	nativeStoreGit(t, bdir, "rm", leaf)
	writeSeedTestFile(t, b.ConfigPath(), "port: 8317\n# git-rollback-plaintext\n")
	nativeStoreGit(t, bdir, "add", gitConfigPath)
	nativeStoreGit(t, bdir, "commit", "-m", "Delete tracked directory and orphan config secret")
	nativeStoreGit(t, bdir, "push", "origin", "master")
	if errSync := a.EnsureRepository(); errSync == nil || !strings.Contains(errSync.Error(), "without its identifying setting") {
		t.Fatalf("fast-forward error = %v, want config CAS rejection", errSync)
	}
	after, errAfter := os.Stat(parent)
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if before.Mode() != after.Mode() || (runtime.GOOS != "windows" && os.SameFile(before, after)) {
		t.Fatalf("pruned directory was not recreated with mode %v; got %v", before.Mode(), after.Mode())
	}
	assertLocalFileContents(t, filepath.Join(adir, filepath.FromSlash(leaf)), "tracked\n")
	assertLocalFileContents(t, a.ConfigPath(), original)
	if head := nativeStoreGit(t, adir, "rev-parse", "HEAD"); head != base {
		t.Fatalf("HEAD changed after failed fast-forward: got %s, want %s", head, base)
	}
	if status := nativeStoreGit(t, adir, "status", "--porcelain"); status != "" {
		t.Fatalf("rollback changed the index or worktree: %q", status)
	}
}

func TestGitRejectsConfigLinksInWorktreeAndOriginalTree(t *testing.T) {
	for _, where := range []string{"local", "tree"} {
		for _, path := range []string{gitConfigPath, "config"} {
			t.Run(where+"/"+path, func(t *testing.T) {
				remote, a, b := gitWorkspacePair(t)
				outside := t.TempDir()
				outsideFile := filepath.Join(outside, "config.yaml")
				const original = "port: 9999\n"
				writeSeedTestFile(t, outsideFile, original)
				store := a
				if where == "tree" {
					store = b
				}
				dir := store.repoDirSnapshot()
				link := filepath.Join(dir, filepath.FromSlash(path))
				target := outsideFile
				if path == "config" {
					target = outside
					if errRemove := os.Remove(link); errRemove != nil {
						t.Fatal(errRemove)
					}
				}
				if errLink := os.Symlink(target, link); errLink != nil {
					t.Skipf("symlinks unavailable: %v", errLink)
				}
				if where == "tree" {
					nativeStoreGit(t, dir, "add", path)
					nativeStoreGit(t, dir, "commit", "-m", "Commit config symlink")
					nativeStoreGit(t, dir, "push", "origin", "master")
				}
				if _, errBootstrap := a.BootstrapConfig(context.Background(), ""); errBootstrap == nil {
					t.Fatal("BootstrapConfig accepted a config symlink")
				}
				if _, errPersist := a.PersistPreparedConfig(context.Background(), []byte("port: 8317\n")); errPersist == nil {
					t.Fatal("PersistPreparedConfig accepted a config symlink")
				}
				assertLocalFileContents(t, outsideFile, original)
				if where == "local" || path == "config" {
					assertRemoteTreePath(t, remote, "master", gitConfigPath, false)
				} else {
					assertRemoteFileContents(t, remote, "master", gitConfigPath, target)
				}
			})
		}
	}
}

func TestGitRecoveryKeepsConfigEditedDuringInstallation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "created", true: "edited"}[existing], func(t *testing.T) {
			_, a, _ := gitWorkspacePair(t)
			if existing {
				writeSeedTestFile(t, a.ConfigPath(), "port: 8317\n")
				if errPersist := a.PersistConfig(context.Background()); errPersist != nil {
					t.Fatal(errPersist)
				}
			}
			const duringInstall = "port: 9999\n"
			edited := false
			rename := func(src, dst string) error {
				if !edited && filepath.Dir(src) == a.repoDirSnapshot() {
					edited = true
					if existing {
						snapshot, errRead := config.ReadConfigFileSnapshot(a.ConfigPath())
						if errRead != nil {
							return errRead
						}
						if _, errWrite := config.WriteConfigFileCAS(a.ConfigPath(), snapshot, []byte(duringInstall)); errWrite != nil {
							return errWrite
						}
					} else if errCreate := config.CreateConfigFile(a.ConfigPath(), []byte(duringInstall)); errCreate != nil {
						return errCreate
					}
				}
				return os.Rename(src, dst)
			}
			if errRecover := a.recoverRepositoryLocked(a.repoDirSnapshot(), a.gitClientOptions(), nil, nil, nil, (*git.Repository).Close, rename); errRecover != nil {
				t.Fatal(errRecover)
			}
			if !edited {
				t.Fatal("did not exercise the installation window")
			}
			assertLocalFileContents(t, a.ConfigPath(), duringInstall)
		})
	}
}

func TestGitRejectedPushPreservesStageWithoutBranchChange(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	dir := a.repoDirSnapshot()
	base := nativeStoreGit(t, dir, "rev-parse", "HEAD")
	writeSeedTestFile(t, a.ConfigPath(), "port: 8317\n")
	a.pushFn = func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		if _, errSave := b.Save(ctx, &cliproxyauth.Auth{ID: "peer.json", Metadata: map[string]any{"type": "codex"}}); errSave != nil {
			t.Fatal(errSave)
		}
		writeSeedTestFile(t, a.ConfigPath(), "port: 8888\n")
		nativeStoreGit(t, dir, "add", gitConfigPath)
		writeSeedTestFile(t, a.ConfigPath(), "port: 9999\n")
		return repo.PushContext(ctx, opts)
	}
	if errPersist := a.PersistConfig(context.Background()); !errors.Is(errPersist, errGitRemoteAdvanced) {
		t.Fatalf("PersistConfig error = %v, want lease conflict", errPersist)
	}
	if got := nativeStoreGit(t, dir, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD changed despite rejected push: %s, want %s", got, base)
	}
	if errSync := a.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	if got := nativeStoreGit(t, dir, "show", ":"+gitConfigPath); got != "port: 8888" {
		t.Fatalf("staged config = %q", got)
	}
	assertLocalFileContents(t, a.ConfigPath(), "port: 9999\n")
	assertRemoteTreePath(t, remote, "master", gitConfigPath, false)
	assertRemoteTreePath(t, remote, "master", "auths/peer.json", true)
}
