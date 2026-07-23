package codexintegration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type fileSnapshot struct {
	data   []byte
	mode   os.FileMode
	exists bool
}

func snapshotRegularFile(path string, defaultMode os.FileMode) (fileSnapshot, error) {
	data, mode, exists, err := readRegularFile(path, defaultMode)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{data: data, mode: mode, exists: exists}, nil
}

func restoreFileSnapshot(path string, snapshot fileSnapshot) error {
	if snapshot.exists {
		return atomicWriteFile(path, snapshot.data, snapshot.mode)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect rollback target %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular rollback target %s", path)
	}
	if err = os.Remove(path); err != nil {
		return fmt.Errorf("remove rollback target %s: %w", path, err)
	}
	return syncDirectory(filepath.Dir(path))
}

func restoreMovedAside(original, backup string) error {
	if backup == "" {
		return nil
	}
	if _, err := os.Lstat(original); err == nil {
		return fmt.Errorf("refusing to replace rollback target %s", original)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect rollback target %s: %w", original, err)
	}
	if err := os.Rename(backup, original); err != nil {
		return fmt.Errorf("restore %s from %s: %w", original, backup, err)
	}
	if err := syncDirectory(filepath.Dir(original)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(backup))
}

func withRollbackError(cause error, rollbackErrors ...error) error {
	errorsToJoin := []error{cause}
	for _, rollbackErr := range rollbackErrors {
		if rollbackErr != nil {
			errorsToJoin = append(errorsToJoin, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
	}
	return errors.Join(errorsToJoin...)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".cliproxyapi-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err = temp.Chmod(mode.Perm()); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set temporary file permissions for %s: %w", path, err)
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err = os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err = syncDirectory(dir); err != nil {
		return err
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open backup source %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return fmt.Errorf("create backup %s: %w", destination, err)
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy backup %s: %w", destination, err)
	}
	if err = out.Sync(); err != nil {
		return fmt.Errorf("sync backup %s: %w", destination, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("close backup %s: %w", destination, err)
	}
	ok = true
	return syncDirectory(filepath.Dir(destination))
}

func moveAside(source, backupDir, label string) (string, error) {
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect %s: %w", source, err)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	destination := uniqueBackupPath(backupDir, label)
	if err := os.Rename(source, destination); err != nil {
		return "", fmt.Errorf("move %s aside: %w", source, err)
	}
	if err := syncDirectory(filepath.Dir(source)); err != nil {
		return destination, err
	}
	return destination, syncDirectory(backupDir)
}

func uniqueBackupPath(dir, label string) string {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	base := filepath.Join(dir, label+"-"+stamp)
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %s for sync: %w", path, err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}
