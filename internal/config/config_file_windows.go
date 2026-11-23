package config

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Lock a fixed byte past any practical end of file so Windows mandatory byte
// locks do not interfere with ordinary file I/O or external editors.
const configLockOffsetHigh = 1 << 30

func lockConfigFile(file *os.File, exclusive bool) error {
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	overlap := windows.Overlapped{OffsetHigh: configLockOffsetHigh}
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlap)
}

func unlockConfigFile(file *os.File) error {
	overlap := windows.Overlapped{OffsetHigh: configLockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlap)
}

func isPlatformConfigReadOnly(err error) bool {
	return errors.Is(err, windows.ERROR_WRITE_PROTECT)
}

func openConfigFileForRemoval(path string) (*os.File, error) {
	name, errName := windows.UTF16PtrFromString(path)
	if errName != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: errName}
	}
	// Unlike os.OpenFile, allow unlinking the path without closing the locked handle.
	handle, errOpen := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errOpen != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: errOpen}
	}
	return os.NewFile(uintptr(handle), path), nil
}
