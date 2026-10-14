// Package atomicfile replaces files so concurrent readers see either the old or
// the new content, never a truncated or partially written file.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// rename is replaced in tests to exercise the in-place fallback.
var rename = os.Rename

// WriteFile writes data to path like os.WriteFile, but through a temp file in the
// same directory that is synced and then renamed over path.
//
// A symlinked path is resolved so the link itself stays in place. An existing file
// keeps its permission bits; perm applies to a new file. When no temp file can be
// created next to the target, or the rename fails (a single-file bind mount
// returns EBUSY, Windows refuses to replace an open file), it falls back to writing
// in place, which is what os.WriteFile does.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return write(path, data, perm, false)
}

// ReplaceFile is WriteFile for a file that must already exist: it never creates
// path. If the file is gone (a deletion raced the save), it returns an error
// wrapping os.ErrNotExist and leaves nothing behind, so the deletion wins. The
// existence check runs right before the rename; the in-place fallback opens the
// file without O_CREATE.
func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	return write(path, data, perm, true)
}

func write(path string, data []byte, perm os.FileMode, mustExist bool) error {
	target := path
	if resolved, errResolve := filepath.EvalSymlinks(path); errResolve == nil {
		target = resolved
	}
	mode := perm
	if info, errStat := os.Stat(target); errStat == nil {
		mode = info.Mode().Perm()
	} else if mustExist {
		return fmt.Errorf("atomicfile: replace %s: %w", target, errStat)
	}

	// The temp name must not match what watchers and scanners pick up (*.json, the
	// config file name), so it is hidden and ends in a random suffix.
	tmp, errCreate := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if errCreate != nil {
		log.WithError(errCreate).Debugf("atomicfile: cannot create temp file next to %s, writing in place", target)
		return writeInPlace(target, data, perm, mustExist)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			if errRemove := os.Remove(tmpPath); errRemove != nil && !os.IsNotExist(errRemove) {
				log.WithError(errRemove).Debugf("atomicfile: failed to remove temp file %s", tmpPath)
			}
		}
	}()

	if errChmod := tmp.Chmod(mode); errChmod != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: chmod temp file: %w", errChmod)
	}
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: write temp file: %w", errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: sync temp file: %w", errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		return fmt.Errorf("atomicfile: close temp file: %w", errClose)
	}
	if mustExist {
		if _, errStat := os.Lstat(target); errStat != nil {
			return fmt.Errorf("atomicfile: replace %s: %w", target, errStat)
		}
	}
	if errRename := rename(tmpPath, target); errRename != nil {
		log.WithError(errRename).Debugf("atomicfile: cannot replace %s, writing in place", target)
		return writeInPlace(target, data, perm, mustExist)
	}
	committed = true
	return nil
}

// writeInPlace is the non-atomic fallback. With mustExist it opens without O_CREATE,
// so a file removed in the meantime is not brought back.
func writeInPlace(path string, data []byte, perm os.FileMode, mustExist bool) error {
	if !mustExist {
		return os.WriteFile(path, data, perm)
	}
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, perm)
	if errOpen != nil {
		return fmt.Errorf("atomicfile: replace %s: %w", path, errOpen)
	}
	if _, errWrite := file.Write(data); errWrite != nil {
		_ = file.Close()
		return fmt.Errorf("atomicfile: replace %s: %w", path, errWrite)
	}
	return file.Close()
}
