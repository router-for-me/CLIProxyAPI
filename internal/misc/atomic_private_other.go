//go:build !windows

package misc

import "os"

func replacePrivateFile(from, to string) error { return os.Rename(from, to) }

func syncPrivateDirectory(dir string, ops privateWriteOps) error {
	d, err := ops.openDir(dir)
	if err != nil {
		return err
	}
	syncErr := ops.syncDir(d)
	closeErr := ops.closeDir(d)
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
