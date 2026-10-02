package misc

import (
	"io"
	"os"
	"path/filepath"
)

// AtomicWritePrivate replaces a credential file only after syncing its contents.
// An error after Rename is commit-uncertain: the replacement may be visible
// without confirmed directory durability. This helper does not make remote OAuth
// rotation atomic or provide refresh-owner restart recovery.
func AtomicWritePrivate(path string, data []byte) error {
	return atomicWritePrivate(path, data, privateWriteOpsDefault())
}

// Per-call operations avoid mutable global fault hooks and permit real-FS tests.
type privateWriteOps struct {
	create                                 func(string, string) (*os.File, error)
	chmod                                  func(*os.File, os.FileMode) error
	write                                  func(*os.File, []byte) (int, error)
	syncFile, closeFile, syncDir, closeDir func(*os.File) error
	rename                                 func(string, string) error
	openDir                                func(string) (*os.File, error)
}

func privateWriteOpsDefault() privateWriteOps {
	return privateWriteOps{os.CreateTemp, (*os.File).Chmod, (*os.File).Write, (*os.File).Sync, (*os.File).Close, (*os.File).Sync, (*os.File).Close, os.Rename, os.Open}
}

func atomicWritePrivate(path string, data []byte, ops privateWriteOps) error {
	dir := filepath.Dir(path)
	f, err := ops.create(dir, ".credential-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = ops.closeFile(f)
		}
		_ = os.Remove(name)
	}()
	if err = ops.chmod(f, 0600); err != nil {
		return err
	}
	n, err := ops.write(f, data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err = ops.syncFile(f); err != nil {
		return err
	}
	err = ops.closeFile(f)
	closed = true
	if err != nil {
		return err
	}
	if err = ops.rename(name, path); err != nil {
		return err
	}
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
