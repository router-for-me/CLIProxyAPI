package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileRefreshClaimRequiresParentSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is unavailable on Windows")
	}
	for _, journalExists := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "auths")
		if errMkdir := os.Mkdir(dir, 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		if journalExists {
			if errMkdir := os.Mkdir(filepath.Join(dir, ".codex-refresh"), 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
		}
		lockRefreshTestDirectory(t, dir, 0o300)
		journal := &FileRefreshJournal{Dir: dir}
		claimed, errClaim := journal.RefreshClaim(context.Background(), strings.Repeat("e", 64))
		if claimed || errClaim == nil {
			t.Fatalf("claim allowed OAuth without synchronizing the journal directory (journal existed: %v): claimed=%v, error=%v", journalExists, claimed, errClaim)
		}
	}
}

func TestFileRefreshClaimCreatesDurableAncestorDirectories(t *testing.T) {
	journal := &FileRefreshJournal{Dir: filepath.Join(t.TempDir(), "new", "nested", "auths")}
	key := strings.Repeat("f", 64)
	claimed, errClaim := journal.RefreshClaim(context.Background(), key)
	if !claimed || errClaim != nil {
		t.Fatalf("create journal hierarchy: %v, %v", claimed, errClaim)
	}
	claimed, errClaim = journal.RefreshClaim(context.Background(), key)
	if claimed || errClaim != nil {
		t.Fatalf("claim was recreated: %v, %v", claimed, errClaim)
	}
	if errRelease := journal.RefreshRelease(context.Background(), key); errRelease != nil {
		t.Fatalf("release pending file claim: %v", errRelease)
	}
	reclaimed, errReclaim := journal.RefreshClaim(context.Background(), key)
	if !reclaimed || errReclaim != nil {
		t.Fatalf("reclaim after release failed: %v, %v", reclaimed, errReclaim)
	}
	if errComplete := journal.RefreshComplete(context.Background(), key, []byte(`{"stage":"transport"}`)); errComplete != nil {
		t.Fatalf("complete receipt: %v", errComplete)
	}
	if errReleaseCompleted := journal.RefreshRelease(context.Background(), key); errReleaseCompleted == nil {
		t.Fatal("completed file receipt must never be released")
	}
}

func lockRefreshTestDirectory(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if errChmod := os.Chmod(dir, mode); errChmod != nil {
		t.Fatal(errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
			t.Error(errChmod)
		}
	})
	if handle, errOpen := os.Open(dir); errOpen == nil {
		if errClose := handle.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		t.Skip("test requires directory read permissions to be enforced")
	}
}

func TestFileRefreshClaimSkipsUnchangedAncestors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is unavailable on Windows")
	}
	outer := filepath.Join(t.TempDir(), "outer")
	dir := filepath.Join(outer, "auths")
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	// Sandboxed services can traverse, but not open, directories above the auth dir.
	lockRefreshTestDirectory(t, outer, 0o100)
	journal := &FileRefreshJournal{Dir: dir}
	claimed, errClaim := journal.RefreshClaim(context.Background(), strings.Repeat("a", 64))
	if !claimed || errClaim != nil {
		t.Fatalf("claim synchronized an unchanged ancestor: claimed=%v, error=%v", claimed, errClaim)
	}
}

func TestFileRefreshClaimSyncsDeepestExistingAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is unavailable on Windows")
	}
	outer := filepath.Join(t.TempDir(), "outer")
	if errMkdir := os.Mkdir(outer, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	lockRefreshTestDirectory(t, outer, 0o300)
	journal := &FileRefreshJournal{Dir: filepath.Join(outer, "new", "auths")}
	for attempt := 1; attempt <= 2; attempt++ {
		claimed, errClaim := journal.RefreshClaim(context.Background(), strings.Repeat("b", 64))
		if claimed || errClaim == nil {
			t.Fatalf("claim attempt %d allowed OAuth without synchronizing the directory that gained an entry: claimed=%v, error=%v", attempt, claimed, errClaim)
		}
	}
	if _, errStat := os.Stat(filepath.Join(outer, "new")); !os.IsNotExist(errStat) {
		t.Fatalf("failed claim left an unsynced directory behind: %v", errStat)
	}
}

func TestFileRefreshClaimKeepsDanglingJournalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	dir := filepath.Join(t.TempDir(), "auths")
	if errMkdir := os.Mkdir(dir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	link := filepath.Join(dir, ".codex-refresh")
	if errLink := os.Symlink(filepath.Join(dir, "missing", "journal"), link); errLink != nil {
		t.Fatal(errLink)
	}
	journal := &FileRefreshJournal{Dir: dir}
	if claimed, errClaim := journal.RefreshClaim(context.Background(), strings.Repeat("c", 64)); claimed || errClaim == nil {
		t.Fatalf("claim through a dangling journal symlink: claimed=%v, error=%v", claimed, errClaim)
	}
	if _, errLstat := os.Lstat(link); errLstat != nil {
		t.Fatalf("failed claim removed the journal symlink: %v", errLstat)
	}
}
