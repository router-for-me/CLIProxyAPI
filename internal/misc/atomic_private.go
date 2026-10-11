package misc

import (
	"io"
	"os"
	"path/filepath"
)

// AtomicWritePrivate replaces a credential file only after syncing its contents.
// On Unix it also syncs the parent directory; an error after replacement is
// commit-uncertain. On Windows it uses a native same-volume replacement with
// write-through requested, without promising directory-fsync or power-loss
// atomicity. Mode 0600 is enforced on Unix; Windows permissions inherit the
// directory ACL (Chmod does not establish a private ACL). This helper does not
// make remote OAuth rotation atomic or provide refresh-owner restart recovery.
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
	return privateWriteOps{os.CreateTemp, (*os.File).Chmod, (*os.File).Write, (*os.File).Sync, (*os.File).Close, (*os.File).Sync, (*os.File).Close, replacePrivateFile, os.Open}
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
	return syncPrivateDirectory(dir, ops)
}
