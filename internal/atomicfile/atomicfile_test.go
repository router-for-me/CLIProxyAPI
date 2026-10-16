package atomicfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestWriteFileReplacesContentAndKeepsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, want the existing 0644 kept", info.Mode().Perm())
		}
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestWriteFileCreatesNewFileWithPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestWriteFileFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	info, _ := os.Lstat(link)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Fatalf("target content = %q, want new", got)
	}
}

func TestWriteFileFallsBackInPlaceWhenRenameFails(t *testing.T) {
	original := rename
	t.Cleanup(func() { rename = original })
	rename = func(string, string) error { return errors.New("device or resource busy") }

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// Readers polling during repeated saves must only ever see a complete version.
func TestWriteFileReadersNeverSeeTruncatedContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The reader below holds the file open without delete sharing, so Windows
		// refuses the rename and WriteFile falls back to its documented in-place
		// write, which this test would rightly flag.
		t.Skip("Windows cannot replace a file another handle has open")
	}
	for name, write := range map[string]func(string, []byte, os.FileMode) error{
		"WriteFile":   WriteFile,
		"ReplaceFile": ReplaceFile,
	} {
		t.Run(name, func(t *testing.T) { assertReadersNeverSeeTruncatedContent(t, write) })
	}
}

func assertReadersNeverSeeTruncatedContent(t *testing.T, write func(string, []byte, os.FileMode) error) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	versions := [][]byte{bytes.Repeat([]byte("a"), 64<<10), bytes.Repeat([]byte("b"), 64<<10)}
	if err := os.WriteFile(path, versions[0], 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stop atomic.Bool
	var bad atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			data, err := os.ReadFile(path)
			if err != nil || (!bytes.Equal(data, versions[0]) && !bytes.Equal(data, versions[1])) {
				bad.Add(1)
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if err := write(path, versions[i%2], 0o600); err != nil {
			t.Fatalf("write error = %v", err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("readers saw %d empty, partial or missing reads during saves", n)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

// ReplaceFile must never bring back a file that is gone: a runtime save racing a
// deletion has to lose, as the previous O_TRUNC write without O_CREATE did.
func TestReplaceFileDoesNotRecreateRemovedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	err := ReplaceFile(path, []byte("{}"), 0o600)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReplaceFile() error = %v, want os.ErrNotExist", err)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("ReplaceFile() recreated the removed file (stat err %v)", errStat)
	}
	assertNoTempFiles(t, dir)
}

func TestReplaceFileReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ReplaceFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("ReplaceFile() error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestReplaceFileFallbackDoesNotRecreateRemovedFile(t *testing.T) {
	original := rename
	t.Cleanup(func() { rename = original })
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The replacement fails (as on a bind mount) after the file was deleted; the
	// in-place fallback must not create it again.
	originalExchange := exchange
	t.Cleanup(func() { exchange = originalExchange })
	busy := func(string, string) error {
		_ = os.Remove(path)
		return syscall.EBUSY
	}
	rename = busy
	exchange = busy
	if err := ReplaceFile(path, []byte("new"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReplaceFile() error = %v, want os.ErrNotExist", err)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("fallback recreated the removed file (stat err %v)", errStat)
	}
	assertNoTempFiles(t, dir)
}

// A rename is authorized by the directory, not the file. An existing file the
// process may not write (made read-only on purpose) must stay untouched, as with
// os.WriteFile.
func TestWriteFileRespectsReadOnlyTarget(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root Unix user for file permission checks")
	}
	for name, write := range map[string]func(string, []byte, os.FileMode) error{
		"WriteFile":   WriteFile,
		"ReplaceFile": ReplaceFile,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(path, []byte("old"), 0o400); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := write(path, []byte("new"), 0o600); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("%s() error = %v, want os.ErrPermission", name, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != "old" {
				t.Fatalf("read-only file was replaced: %q", got)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// A deletion landing right before the file is put in place must still win: the
// commit itself has to require the target. The hook runs at that point on every
// path, so this fails on the check-then-rename fallback (its known window) and is
// skipped where no atomic exchange exists.
func TestReplaceFileDeletionBeforeCommitWins(t *testing.T) {
	if !exchangeSupported() {
		t.Skip("no atomic exchange rename on this platform")
	}
	original := beforeCommit
	t.Cleanup(func() { beforeCommit = original })
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	beforeCommit = func() {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove target: %v", err)
		}
	}
	if err := ReplaceFile(path, []byte("new"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReplaceFile() error = %v, want os.ErrNotExist", err)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("ReplaceFile() recreated the file deleted before commit (stat err %v)", errStat)
	}
	assertNoTempFiles(t, dir)
}

// When the temp file cannot get the existing file's owner (a root writer may, a
// plain user may not), the save writes in place, keeping the file and its owner.
func TestWriteFileWritesInPlaceWhenOwnerCannotBeKept(t *testing.T) {
	original := matchOwner
	t.Cleanup(func() { matchOwner = original })
	matchOwner = func(*os.File, os.FileInfo) error { return syscall.EPERM }

	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := os.Stat(path)
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("WriteFile() replaced the file whose owner it could not keep")
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}
