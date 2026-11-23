//go:build aix || solaris

package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"

	"golang.org/x/sys/unix"
)

func setGitTestConfigLock(file *os.File, exclusive bool) error {
	mode := int16(unix.F_UNLCK)
	if exclusive {
		mode = unix.F_WRLCK
	}
	lock := unix.Flock_t{Type: mode, Whence: 0, Start: 0, Len: 0}
	return unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
}

// Even a shared read through a second descriptor would release the parent
// process's exclusive fcntl lock when go-git closes that descriptor.
func TestGitStatusSnapshotDoesNotDropProcessConfigLock(t *testing.T) {
	_, a, _ := gitWorkspacePair(t)
	writeSeedTestFile(t, a.ConfigPath(), "port: 8317\n")
	if errPersist := a.PersistConfig(context.Background()); errPersist != nil {
		t.Fatal(errPersist)
	}
	writeSeedTestFile(t, a.ConfigPath(), "port: 9999\n# an edited size forces content hashing\n")
	snapshot, errSnapshot := config.ReadConfigFileSnapshot(a.ConfigPath())
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	repo, errOpen := git.PlainOpen(a.repoDirSnapshot())
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() {
		if errClose := repo.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	worktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		t.Fatal(errWorktree)
	}
	snapshotFS, errFS := gitSnapshotFS(worktree.Filesystem(), snapshot)
	if errFS != nil {
		t.Fatal(errFS)
	}
	scan, errScan := git.Open(repo.Storer, snapshotFS)
	if errScan != nil {
		t.Fatal(errScan)
	}
	worktree, errWorktree = scan.Worktree()
	if errWorktree != nil {
		t.Fatal(errWorktree)
	}
	file, errFile := os.OpenFile(a.ConfigPath(), os.O_RDWR, 0)
	if errFile != nil {
		t.Fatal(errFile)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if errLock := setGitTestConfigLock(file, true); errLock != nil {
		t.Fatal(errLock)
	}
	status, errStatus := worktree.Status()
	if errStatus != nil || status.File(gitConfigPath).Worktree != git.Modified {
		t.Fatalf("config status = %v, %v", status, errStatus)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestGitProcessConfigLockProbe$")
	child.Env = append(os.Environ(), "CPA_GIT_TEST_CONFIG_PROBE="+a.ConfigPath())
	if out, errProbe := child.CombinedOutput(); errProbe != nil {
		t.Fatalf("process probe: %v\n%s", errProbe, out)
	}
}

func TestGitProcessConfigLockProbe(t *testing.T) {
	path := os.Getenv("CPA_GIT_TEST_CONFIG_PROBE")
	if path == "" {
		return
	}
	file, errOpen := os.OpenFile(path, os.O_RDWR, 0)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if errLock := setGitTestConfigLock(file, true); !errors.Is(errLock, unix.EACCES) && !errors.Is(errLock, unix.EAGAIN) {
		t.Fatalf("expected parent process to still hold the lock, got %v", errLock)
	}
}
