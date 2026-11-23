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
	parent, errParent := filepath.Abs(filepath.Dir(path))
	if errParent != nil {
		return false, errParent
	}
	existing, errExisting := existingRefreshAncestor(parent)
	if errExisting != nil {
		return false, errExisting
	}
	// Always sync the journal directory, which holds the claim directory's entry.
	root := existing
	if existing == parent {
		root = filepath.Dir(parent)
	}
	if errMkdir := os.MkdirAll(parent, 0o700); errMkdir != nil {
		removeRefreshClaimParents(parent, existing)
		return false, errMkdir
	}
	if errMkdir := os.Mkdir(path, 0o700); errMkdir != nil {
		if errors.Is(errMkdir, os.ErrExist) {
			return false, nil
		}
		removeRefreshClaimParents(parent, existing)
		return false, errMkdir
	}
	if errSync := syncRefreshClaimDirectories(path, root); errSync != nil {
		if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warnf("remove unsynced refresh claim: %v", errRemove)
		}
		removeRefreshClaimParents(parent, existing)
		return false, errSync
	}
	return true, nil
}

// removeRefreshClaimParents removes the directories a failed claim created,
// from dir up to but excluding existing. A leftover directory would otherwise
// become the next claim's sync boundary, and the entry naming it would never
// be synced.
func removeRefreshClaimParents(dir, existing string) {
	for dir != existing {
		// A dangling symlink looked absent before MkdirAll; never remove it.
		info, errLstat := os.Lstat(dir)
		if errLstat == nil && !info.IsDir() {
			return
		}
		if errRemove := os.Remove(dir); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warnf("remove unsynced refresh journal directory: %v", errRemove)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
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

func (j *FileRefreshJournal) RefreshRelease(ctx context.Context, key string) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	path, errPath := j.path(key)
	if errPath != nil {
		return errPath
	}
	if _, errStat := os.Stat(filepath.Join(path, "receipt")); errStat == nil {
		return fmt.Errorf("refresh receipt already exists")
	} else if !errors.Is(errStat, os.ErrNotExist) {
		return errStat
	}
	if errRemove := os.Remove(path); errRemove != nil {
		if errors.Is(errRemove, os.ErrNotExist) {
			return nil
		}
		return errRemove
	}
	return syncRefreshDirectory(filepath.Dir(path))
}

// existingRefreshAncestor returns the absolute dir, or its deepest existing
// ancestor when dir does not exist yet.
func existingRefreshAncestor(dir string) (string, error) {
	for {
		_, errStat := os.Stat(dir)
		if errStat == nil {
			return dir, nil
		}
		if !errors.Is(errStat, os.ErrNotExist) {
			return "", errStat
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir, nil
		}
		dir = parent
	}
}

// Sync every directory entry that can have been created by MkdirAll before
// permitting an exchange: the claim and each directory up to and including
// root, which existed before MkdirAll. Directories above root were not changed
// by the claim, and sandboxed services may be unable to open them. Canonicalize
// symlinks so newly-created target ancestors, rather than just the spelling of
// the configured path, are durable.
func syncRefreshClaimDirectories(path, root string) error {
	canonical, errCanonical := filepath.EvalSymlinks(path)
	if errCanonical != nil {
		return errCanonical
	}
	dir, errAbs := filepath.Abs(canonical)
	if errAbs != nil {
		return errAbs
	}
	canonicalRoot, errCanonicalRoot := filepath.EvalSymlinks(root)
	if errCanonicalRoot != nil {
		return errCanonicalRoot
	}
	stop, errAbsRoot := filepath.Abs(canonicalRoot)
	if errAbsRoot != nil {
		return errAbsRoot
	}
	for {
		if errSync := syncRefreshDirectory(dir); errSync != nil {
			return errSync
		}
		if dir == stop {
			return nil
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
