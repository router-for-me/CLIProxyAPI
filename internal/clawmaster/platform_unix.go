//go:build !windows

package clawmaster

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// errLockBusy reports a file lock another process holds.
var errLockBusy = errors.New("the lock is held by another process")

// openNoFollow opens path without following a final symlink and without blocking on a FIFO.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flag|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, uint32(perm.Perm()))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// tryLockFile takes a shared or exclusive lock on f without waiting; errLockBusy if another
// process holds a conflicting one. Closing f releases it.
func tryLockFile(f *os.File, exclusive bool) error {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return errLockBusy
	}
	return err
}

// fileInode is the inode number, so a replaced file is noticed even when its size and time match.
func fileInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// checkOwnedDirectory requires a directory this user owns that others cannot write; private, that
// only this user can use (0700).
func checkOwnedDirectory(_ string, st os.FileInfo, private bool) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o022 != 0 || (private && st.Mode().Perm() != 0o700) {
		return errors.New("profile directories must be owned by you with safe permissions (private directories: 0700)")
	}
	return nil
}

// privateFile requires an open file this user owns, regular, with one link and mode 0600.
func privateFile(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return errors.New("cannot inspect private file")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || int(sys.Uid) != os.Getuid() || sys.Nlink != 1 {
		return errors.New("private files must be owned by you, regular, unlinked, and mode 0600")
	}
	return nil
}

// readableByOthers reports permission bits that let others read a private file.
func readableByOthers(_ string, info fs.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}

// restrictToOwner is the Windows half of creating a private file or directory; on Unix the mode
// given at creation (0600, 0700) already does it.
func restrictToOwner(string) error { return nil }

// executable reports an execute bit; the native Claude must have one.
func executable(info os.FileInfo) bool { return info.Mode().Perm()&0o111 != 0 }

// stopChild asks the native Claude to exit, as a terminal closing would.
func stopChild(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// directorySyncSupported: an fsync on a directory makes a rename in it durable.
const directorySyncSupported = true

// syncReadOnlyFile makes f's contents durable; Unix allows it through a read-only descriptor.
func syncReadOnlyFile(f *os.File) error { return f.Sync() }

// profilesSupported: profiles (login, run, serve) need the locks and ownership checks above.
func profilesSupported() error { return nil }
