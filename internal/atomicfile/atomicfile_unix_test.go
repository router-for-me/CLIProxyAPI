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
