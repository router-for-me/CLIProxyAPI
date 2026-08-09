package loguploader

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type processLock interface {
	Close() error
}

var errFileLockBusy = errors.New("file lock is already held")

type processLockSet struct {
	locks []processLock
}

func (s *Service) acquireProcessLock() (processLock, error) {
	workLock, errWorkLock := s.acquireWorkDirLock()
	if errWorkLock != nil {
		return nil, errWorkLock
	}

	sharedPath, errSharedPath := s.sharedResourceLockPath()
	if errSharedPath != nil {
		return nil, errors.Join(errSharedPath, workLock.Close())
	}
	if errMkdir := os.MkdirAll(filepath.Dir(sharedPath), 0o750); errMkdir != nil {
		return nil, errors.Join(fmt.Errorf("create shared uploader lock directory: %w", errMkdir), workLock.Close())
	}
	sharedLock, errSharedLock := acquireFileLock(sharedPath)
	if errors.Is(errSharedLock, errFileLockBusy) {
		return nil, errors.Join(
			fmt.Errorf("another log uploader instance is already processing this logs root and upload target: %w", errSharedLock),
			workLock.Close(),
		)
	}
	if errSharedLock != nil {
		return nil, errors.Join(fmt.Errorf("acquire shared uploader resource lock: %w", errSharedLock), workLock.Close())
	}
	return &processLockSet{locks: []processLock{workLock, sharedLock}}, nil
}

func (s *Service) acquireWorkDirLock() (processLock, error) {
	if errMkdir := os.MkdirAll(s.cfg.WorkDir, 0o750); errMkdir != nil {
		return nil, fmt.Errorf("create uploader work directory: %w", errMkdir)
	}
	workLock, errWorkLock := acquireFileLock(filepath.Join(s.cfg.WorkDir, "service.lock"))
	if errors.Is(errWorkLock, errFileLockBusy) {
		return nil, fmt.Errorf("another log uploader instance is already using this work directory: %w", errWorkLock)
	}
	if errWorkLock != nil {
		return nil, fmt.Errorf("acquire log uploader work directory lock: %w", errWorkLock)
	}
	return workLock, nil
}

func (s *Service) sharedResourceLockPath() (string, error) {
	logsRoot, errCanonical := canonicalLogsRoot(s.cfg.LogsRoot)
	if errCanonical != nil {
		return "", errCanonical
	}
	identityRoot := logsRoot
	if runtime.GOOS == "windows" {
		identityRoot = strings.ToLower(identityRoot)
	}
	identity := identityRoot + "\x00" + s.target.ID
	digest := sha256.Sum256([]byte(identity))
	lockDirectory := filepath.Join(filepath.Dir(logsRoot), ".log-uploader-locks")
	return filepath.Join(lockDirectory, fmt.Sprintf("%x.lock", digest)), nil
}

func canonicalLogsRoot(path string) (string, error) {
	absolute, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return "", fmt.Errorf("resolve uploader logs root: %w", errAbsolute)
	}
	absolute = filepath.Clean(absolute)
	resolved, errResolved := filepath.EvalSymlinks(absolute)
	if errResolved == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(errResolved, os.ErrNotExist) {
		return "", fmt.Errorf("canonicalize uploader logs root: %w", errResolved)
	}

	current := absolute
	missingParts := make([]string, 0, 1)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return absolute, nil
		}
		missingParts = append(missingParts, filepath.Base(current))
		current = parent
		resolvedParent, errParent := filepath.EvalSymlinks(current)
		if errParent == nil {
			for index := len(missingParts) - 1; index >= 0; index-- {
				resolvedParent = filepath.Join(resolvedParent, missingParts[index])
			}
			return filepath.Clean(resolvedParent), nil
		}
		if !errors.Is(errParent, os.ErrNotExist) {
			return "", fmt.Errorf("canonicalize uploader logs root parent: %w", errParent)
		}
	}
}

func (l *processLockSet) Close() error {
	var closeErrors []error
	for index := len(l.locks) - 1; index >= 0; index-- {
		if l.locks[index] == nil {
			continue
		}
		closeErrors = append(closeErrors, l.locks[index].Close())
	}
	return errors.Join(closeErrors...)
}
