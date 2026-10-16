//go:build unix

package atomicfile

import (
	"os"
	"syscall"
)

// linkCount returns the number of hard links to the file info describes.
func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

// keepOwner gives the temp file the owner and group of the file it replaces, so a
// rename does not hand the file to the writing user (for example a root container
// saving a host user's file through a bind mount). It fails when the process may
// not give the temp file that owner.
func keepOwner(tmp *os.File, existing os.FileInfo) error {
	want, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	info, errStat := tmp.Stat()
	if errStat != nil {
		return errStat
	}
	if have, okHave := info.Sys().(*syscall.Stat_t); okHave && have.Uid == want.Uid && have.Gid == want.Gid {
		return nil
	}
	return tmp.Chown(int(want.Uid), int(want.Gid))
}
