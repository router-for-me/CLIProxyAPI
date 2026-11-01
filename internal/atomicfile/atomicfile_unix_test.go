//go:build unix

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A symlink whose target does not exist yet (a pre-provisioned auth link) stays a
// link: WriteFile creates the target through it, ReplaceFile creates nothing.
func TestWriteFileKeepsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := ReplaceFile(link, []byte("new"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReplaceFile() error = %v, want os.ErrNotExist", err)
	}
	if _, errStat := os.Lstat(target); !os.IsNotExist(errStat) {
		t.Fatalf("ReplaceFile() created the link target (stat err %v)", errStat)
	}

	if err := WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("WriteFile() replaced the dangling symlink with a regular file")
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("target content = %q, want new", got)
	}
	assertNoTempFiles(t, dir)
}

// Every hard link must see the new content, as with an in-place write.
func TestWriteFileKeepsHardLinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	alias := filepath.Join(dir, "alias.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Link(path, alias); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got, _ := os.ReadFile(alias); string(got) != "new" {
		t.Fatalf("hard link content = %q, want new", got)
	}
	assertNoTempFiles(t, dir)
}

// A special file behind the path (here a FIFO) is written to, not replaced.
func TestWriteFileKeepsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "auth.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A non-blocking reader lets the writer's open return.
	reader, errOpen := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errOpen != nil {
		t.Fatalf("open reader: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := reader.Close(); errClose != nil {
			t.Errorf("close reader: %v", errClose)
		}
	})
	if err := WriteFile(fifo, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if info, _ := os.Lstat(fifo); info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("WriteFile() replaced the FIFO with mode %v", info.Mode())
	}
	assertNoTempFiles(t, dir)
}

// A new file gets perm less the umask, as with os.WriteFile.
func TestWriteFileNewFileHonorsUmask(t *testing.T) {
	// Create the directory first: the umask applies to it too.
	path := filepath.Join(t.TempDir(), "auth.json")
	previous := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(previous) })

	if err := WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v, want 0400 (0600 less umask 0277)", info.Mode().Perm())
	}
}

// A root writer (the Docker image) saving a file another user owns, through a bind
// mount, must leave it owned by that user.
func TestWriteFileKeepsOwnerAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own a file as another user")
	}
	const uid, gid = 12345, 12345
	for name, write := range map[string]func(string, []byte, os.FileMode) error{
		"WriteFile":   WriteFile,
		"ReplaceFile": ReplaceFile,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := os.Chown(path, uid, gid); err != nil {
				t.Fatalf("chown: %v", err)
			}
			before, _ := os.Stat(path)
			if err := write(path, []byte("new"), 0o600); err != nil {
				t.Fatalf("write error = %v", err)
			}
			after, _ := os.Stat(path)
			st := after.Sys().(*syscall.Stat_t)
			if st.Uid != uid || st.Gid != gid {
				t.Fatalf("owner = %d:%d, want %d:%d", st.Uid, st.Gid, uid, gid)
			}
			if os.SameFile(before, after) {
				t.Fatal("the file was written in place; root should replace it with the owner kept")
			}
			if got, _ := os.ReadFile(path); string(got) != "new" {
				t.Fatalf("content = %q, want new", got)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// A new file under a symlinked parent goes into the directory the link named when
// the write started: retargeting the link before the commit must not split the
// rename and the temp cleanup across two directories and leave a temp file behind.
func TestWriteFileNewFileResolvesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	parent := filepath.Join(root, "auths")
	if err := os.Symlink(first, parent); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	original := beforeCommit
	t.Cleanup(func() { beforeCommit = original })
	beforeCommit = func() {
		// Retarget the parent link the way a directory rotation would.
		next := filepath.Join(root, "auths.next")
		if err := os.Symlink(second, next); err != nil {
			t.Errorf("symlink: %v", err)
			return
		}
		if err := os.Rename(next, parent); err != nil {
			t.Errorf("retarget: %v", err)
		}
	}

	if err := WriteFile(filepath.Join(parent, "auth.json"), []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(first, "auth.json")); err != nil || string(got) != "new" {
		t.Fatalf("file in the original directory = %q, %v; want new", got, err)
	}
	assertNoTempFiles(t, first)
	assertNoTempFiles(t, second)
}

// On a mount that rejects chmod, updating a file whose mode the temp lacks must
// still succeed, in place, instead of failing every refresh.
func TestWriteFileWritesInPlaceWhenChmodIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}
	before, errBefore := os.Stat(path)
	if errBefore != nil {
		t.Fatal(errBefore)
	}
	original := chmodTemp
	t.Cleanup(func() { chmodTemp = original })
	chmodTemp = func(*os.File, os.FileMode) error { return syscall.EPERM }

	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v, want an in-place write", err)
	}
	after, errAfter := os.Stat(path)
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if !os.SameFile(before, after) || after.Mode().Perm() != 0o640 {
		t.Fatalf("want the same file with mode 0640, got same=%v mode=%v", os.SameFile(before, after), after.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// A temp that already has the target's mode needs no chmod, so such a mount keeps
// the atomic replacement.
func TestWriteFileSkipsChmodWhenModeMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}
	original := chmodTemp
	t.Cleanup(func() { chmodTemp = original })
	var calls int
	chmodTemp = func(*os.File, os.FileMode) error { calls++; return syscall.EPERM }

	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("chmod calls = %d, want 0 when the temp already has the mode", calls)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// A mount that cannot fsync a regular file must not make every save fail: the
// replacement goes ahead without the flush. A real flush failure still fails.
func TestWriteFileReplacesWithoutUnsupportedSync(t *testing.T) {
	original := syncTemp
	t.Cleanup(func() { syncTemp = original })
	for _, errUnsupported := range []error{syscall.EINVAL, syscall.ENOSYS, syscall.ENOTSUP} {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		syncTemp = func(*os.File) error { return errUnsupported }
		if err := WriteFile(path, []byte("new"), 0o600); err != nil {
			t.Fatalf("WriteFile() with fsync %v error = %v, want the replacement", errUnsupported, err)
		}
		if got, _ := os.ReadFile(path); string(got) != "new" {
			t.Fatalf("content with fsync %v = %q, want new", errUnsupported, got)
		}
		assertNoTempFiles(t, filepath.Dir(path))
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	syncTemp = func(*os.File) error { return syscall.EIO }
	if err := WriteFile(path, []byte("new"), 0o600); !errors.Is(err, syscall.EIO) {
		t.Fatalf("WriteFile() with a failed flush = %v, want EIO", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("content after a failed flush = %q, want old kept", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// An existing file's setgid bit (an operator-applied policy) survives the
// replacement, as with the in-place writers it replaced.
func TestWriteFileKeepsSetgidBit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o640|os.ModeSetgid); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode()&os.ModeSetgid == 0 {
		t.Skip("this platform or account cannot set setgid on the file")
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & keptModeBits; got != 0o640|os.ModeSetgid {
		t.Fatalf("mode after replacement = %v, want %v", got, 0o640|os.ModeSetgid)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// After an atomic exchange the temp path holds the previous content (an old
// credential). If it cannot be removed, the save must say so instead of
// reporting a clean success.
func TestReplaceFileReportsPreviousContentLeftBehind(t *testing.T) {
	if !exchangeAvailable() {
		t.Skip("no atomic exchange on this platform")
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old-token"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	original := removeFile
	t.Cleanup(func() { removeFile = original })
	var leftover string
	removeFile = func(name string) error {
		leftover = name
		return syscall.EACCES
	}

	err := ReplaceFile(path, []byte("new-token"), 0o600)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("ReplaceFile() error = %v, want the failed removal reported", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new-token" {
		t.Fatalf("content = %q, want the replacement to have gone through", got)
	}
	if got, _ := os.ReadFile(leftover); string(got) != "old-token" {
		t.Fatalf("leftover %s holds %q, want the previous content", leftover, got)
	}
	if err := os.Remove(leftover); err != nil {
		t.Fatalf("clean up leftover: %v", err)
	}
}

// Replacing an owner-only credential, the new content must never sit in a temp
// file others can read, even though the caller asks for 0666 (less the umask)
// as os.Create did for a new file.
func TestWriteFileTempIsOwnerOnlyWhileHoldingNewContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("old-token"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	original := matchOwner
	t.Cleanup(func() { matchOwner = original })
	var tempMode os.FileMode
	var tempContent string
	matchOwner = func(tmp *os.File, existing os.FileInfo) error {
		// Runs after the data is written, before the file's own mode is applied.
		if info, err := tmp.Stat(); err == nil {
			tempMode = info.Mode().Perm()
		}
		if raw, err := os.ReadFile(tmp.Name()); err == nil {
			tempContent = string(raw)
		}
		return original(tmp, existing)
	}

	if err := WriteFile(path, []byte("new-token"), 0o666); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if tempContent != "new-token" {
		t.Fatalf("temp content at the check = %q, want the new token (the check ran too early)", tempContent)
	}
	if tempMode&0o077 != 0 {
		t.Fatalf("temp mode while holding the new token = %v, want owner-only", tempMode)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("final mode = %v, want the existing 0600", info.Mode().Perm())
	}
}
