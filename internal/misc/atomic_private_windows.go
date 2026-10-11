package misc

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func replacePrivateFile(from, to string) error {
	// Use extended absolute paths to retain support for long file names.
	paths := [2]*uint16{}
	for i, name := range []string{from, to} {
		abs, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(abs, `\\?\`) {
			if strings.HasPrefix(abs, `\\`) {
				abs = `\\?\UNC\` + abs[2:]
			} else {
				abs = `\\?\` + abs
			}
		}
		paths[i], err = windows.UTF16PtrFromString(abs)
		if err != nil {
			return err
		}
	}
	// Same-directory move: do not allow a cross-volume copy/delete fallback.
	// WRITE_THROUGH requests the native behavior; its documented flush guarantee
	// covers copy/delete moves, not a Unix directory-fsync guarantee.
	if err := windows.MoveFileEx(paths[0], paths[1], windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

func syncPrivateDirectory(string, privateWriteOps) error {
	// Windows cannot FlushFileBuffers on the read-only directory handle opened
	// by os.Open. Contents were synced before the native write-through move.
	// Do not claim POSIX directory durability or power-loss atomicity here.
	return nil
}
