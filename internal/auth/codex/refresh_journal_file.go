package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	log "github.com/sirupsen/logrus"
)

// FileRefreshJournal stores immutable claims outside credential JSON files so
// watcher reloads and credential replacement cannot erase unresolved outcomes.
type FileRefreshJournal struct{ Dir string }

func (j *FileRefreshJournal) path(key string) (string, error) {
	if j == nil || j.Dir == "" || len(key) != 64 {
		return "", fmt.Errorf("invalid refresh journal directory or key")
	}
	for _, char := range key {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return "", fmt.Errorf("invalid refresh journal key")
		}
	}
	return filepath.Join(j.Dir, ".codex-refresh", key), nil
}

func (j *FileRefreshJournal) RefreshClaim(ctx context.Context, key string) (bool, error) {
	if errContext := ctx.Err(); errContext != nil {
		return false, errContext
	}
	path, errPath := j.path(key)
	if errPath != nil {
		return false, errPath
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return false, errMkdir
	}
	if errMkdir := os.Mkdir(path, 0o700); errMkdir != nil {
		if errors.Is(errMkdir, os.ErrExist) {
			return false, nil
		}
		return false, errMkdir
	}
	if errSync := syncRefreshClaimDirectories(path); errSync != nil {
		return false, errSync
	}
	return true, nil
}

func (j *FileRefreshJournal) RefreshRead(ctx context.Context, key string) ([]byte, error) {
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	path, errPath := j.path(key)
	if errPath != nil {
		return nil, errPath
	}
	data, errRead := os.ReadFile(filepath.Join(path, "receipt"))
	if errors.Is(errRead, os.ErrNotExist) {
		if _, errStat := os.Stat(path); errStat == nil {
			return []byte(PendingRefreshReceipt), nil
		} else if !errors.Is(errStat, os.ErrNotExist) {
			return nil, errStat
		}
		return nil, nil
	}
	return data, errRead
}

func (j *FileRefreshJournal) RefreshComplete(ctx context.Context, key string, receipt []byte) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	path, errPath := j.path(key)
	if errPath != nil {
		return errPath
	}
	file, errCreate := os.CreateTemp(path, ".receipt-")
	if errCreate != nil {
		return errCreate
	}
	defer func() {
		if errRemove := os.Remove(file.Name()); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warnf("remove refresh receipt temporary file: %v", errRemove)
		}
	}()
	_, errWrite := file.Write(receipt)
	if errWrite == nil {
		errWrite = file.Sync()
	}
	errClose := file.Close()
	if errWrite != nil || errClose != nil {
		return errors.Join(errWrite, errClose)
	}
	if errRename := os.Rename(file.Name(), filepath.Join(path, "receipt")); errRename != nil {
		return errRename
	}
	return syncRefreshDirectory(path)
}

// Sync every directory entry that can have been created by MkdirAll before
// permitting an exchange. Canonicalize symlinks so newly-created target
// ancestors, rather than just the spelling of the configured path, are durable.
func syncRefreshClaimDirectories(path string) error {
	canonical, errCanonical := filepath.EvalSymlinks(path)
	if errCanonical != nil {
		return errCanonical
	}
	dir, errAbs := filepath.Abs(canonical)
	if errAbs != nil {
		return errAbs
	}
	for {
		if errSync := syncRefreshDirectory(dir); errSync != nil {
			return errSync
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func syncRefreshDirectory(path string) error {
	// Windows does not support fsync on directory handles. Atomic directory
	// creation still survives a process restart without a volatile sidecar.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, errOpen := os.Open(path)
	if errOpen != nil {
		return errOpen
	}
	errSync := dir.Sync()
	errClose := dir.Close()
	return errors.Join(errSync, errClose)
}
