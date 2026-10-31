// Package atomicfile replaces files so concurrent readers see either the old or
// the new content, never a truncated or partially written file.
package atomicfile

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"

	log "github.com/sirupsen/logrus"
)

// rename, exchange, exchangeAvailable and matchOwner are replaced in tests to
// exercise the fallbacks.
var (
	rename            = os.Rename
	exchange          = exchangeFiles
	exchangeAvailable = exchangeSupported
	matchOwner        = keepOwner
)

// beforeCommit runs after the temp file is complete, right before it replaces the
// target; tests use it to land a concurrent deletion in that window.
var beforeCommit = func() {}

// WriteFile writes data to path like os.WriteFile, but through a temp file in the
// same directory that is synced and then renamed over path.
//
// A symlinked path is resolved so the link itself stays in place. An existing file
// keeps its permission bits and, on Unix, its owner and group; a new file gets perm
// less the umask, as with os.WriteFile. Other inode metadata (extended attributes,
// POSIX ACLs, SELinux labels, Windows DACLs) is not copied: the new file gets the
// defaults for its directory.
//
// It writes in place, which is what os.WriteFile does, when a rename would change
// what path refers to: a dangling symlink (the link would be replaced), a special
// file such as a device or FIFO, a file with other hard links (Unix), or an existing
// file whose owner the process cannot give the temp file. It also writes in place
// when no temp file can be created next to the target, or the rename fails (a
// single-file bind mount returns EBUSY, Windows refuses to replace an open file).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return write(path, data, perm, false)
}

// ReplaceFile is WriteFile for a file that must already exist: it never creates
// path. If the file is gone (a deletion raced the save), it returns an error
// wrapping os.ErrNotExist and leaves nothing behind, so the deletion wins. On
// Linux (renameat2 RENAME_EXCHANGE) and macOS (renamex_np RENAME_SWAP) the
// replacement is an atomic swap that fails on a missing target. Elsewhere, or on a
// filesystem without exchange support, it checks existence right before a plain
// rename, which leaves a few-syscall window. The in-place fallback opens the file
// without O_CREATE.
func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	return write(path, data, perm, true)
}

func write(path string, data []byte, perm os.FileMode, mustExist bool) error {
	target := path
	if resolved, errResolve := filepath.EvalSymlinks(path); errResolve == nil {
		target = resolved
	} else if linkInfo, errLstat := os.Lstat(path); errLstat == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		// A dangling symlink: a rename would replace the link. Writing through it
		// creates its target, as os.WriteFile does (ReplaceFile fails instead).
		return writeInPlace(path, data, perm, mustExist)
	}
	existing, errStat := os.Stat(target)
	if errStat == nil {
		if !existing.Mode().IsRegular() {
			// A device, FIFO or other special file: a rename would replace the node.
			return writeInPlace(target, data, perm, mustExist)
		}
		if linkCount(existing) > 1 {
			// A rename would leave the other hard links on the old content.
			return writeInPlace(target, data, perm, mustExist)
		}
		// The rename below is authorized by the directory, not the file. Keep the
		// os.WriteFile rule that a file this process may not write stays untouched
		// (read-only mode, write-denying ACL): opening it for writing, without
		// truncating, fails exactly when os.WriteFile would.
		probe, errOpen := os.OpenFile(target, os.O_WRONLY, 0)
		if errOpen != nil {
			return fmt.Errorf("atomicfile: write %s: %w", target, errOpen)
		}
		if errClose := probe.Close(); errClose != nil {
			return fmt.Errorf("atomicfile: write %s: %w", target, errClose)
		}
	} else if mustExist {
		return fmt.Errorf("atomicfile: replace %s: %w", target, errStat)
	}

	// The temp name must not match what watchers and scanners pick up (*.json, the
	// config file name), so it is hidden and ends in a random suffix.
	tmp, errCreate := createTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-", perm)
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

	if existing != nil {
		// Owner first: a chown can clear mode bits.
		if errOwner := matchOwner(tmp, existing); errOwner != nil {
			_ = tmp.Close()
			log.WithError(errOwner).Debugf("atomicfile: cannot keep the owner of %s, writing in place", target)
			return writeInPlace(target, data, perm, mustExist)
		}
		if errChmod := tmp.Chmod(existing.Mode().Perm()); errChmod != nil {
			_ = tmp.Close()
			return fmt.Errorf("atomicfile: chmod temp file: %w", errChmod)
		}
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
	// The test hook runs once, right before the step that puts the file in place,
	// so a deletion it lands falls in that step's real window.
	hookRan := false
	runCommitHook := func() {
		if !hookRan {
			hookRan = true
			beforeCommit()
		}
	}
	if mustExist && exchangeAvailable() {
		// Swap the temp file with the target in one step: it fails when the target
		// is gone, so a deletion racing this save always wins. The previous content
		// ends up at tmpPath and is removed.
		runCommitHook()
		errSwap := exchange(tmpPath, target)
		switch {
		case errSwap == nil:
			committed = true
			if errRemove := os.Remove(tmpPath); errRemove != nil && !os.IsNotExist(errRemove) {
				log.WithError(errRemove).Debugf("atomicfile: failed to remove replaced file %s", tmpPath)
			}
			return nil
		case errors.Is(errSwap, os.ErrNotExist):
			return fmt.Errorf("atomicfile: replace %s: %w", target, errSwap)
		case !exchangeUnsupported(errSwap):
			log.WithError(errSwap).Debugf("atomicfile: cannot replace %s, writing in place", target)
			return writeInPlace(target, data, perm, mustExist)
		}
	}
	if mustExist {
		// No atomic exchange on this platform or filesystem: check, then rename.
		// A deletion landing between the two can still be undone.
		if _, errStat := os.Lstat(target); errStat != nil {
			return fmt.Errorf("atomicfile: replace %s: %w", target, errStat)
		}
	}
	runCommitHook()
	if errRename := rename(tmpPath, target); errRename != nil {
		log.WithError(errRename).Debugf("atomicfile: cannot replace %s, writing in place", target)
		return writeInPlace(target, data, perm, mustExist)
	}
	committed = true
	return nil
}

// createTemp is os.CreateTemp with a mode: the file is created with perm less the
// umask, so a new file ends up with the mode os.WriteFile would give it.
func createTemp(dir, prefix string, perm os.FileMode) (*os.File, error) {
	for attempt := 0; ; attempt++ {
		name := filepath.Join(dir, prefix+strconv.FormatUint(rand.Uint64(), 36))
		file, errOpen := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errOpen != nil && os.IsExist(errOpen) && attempt < 100 {
			continue
		}
		return file, errOpen
	}
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
