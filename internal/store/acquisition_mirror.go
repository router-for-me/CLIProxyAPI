package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type acquisitionMirror struct {
	path        string
	original    []byte
	mode        os.FileMode
	existed     bool
	written     []byte
	writtenInfo os.FileInfo
}

// Acquisition serialization uses a separate path so Codex can take its own
// serialization lock while the destination's refresh lock protects the baseline.
// Runtime saves retain their existing local-first behavior.
func writeAcquisitionMirror(ctx context.Context, path string, write func(string) error) (*acquisitionMirror, error) {
	if !cliproxyauth.HasAuthCreationIntent(ctx) {
		return nil, write(path)
	}
	var mirror *acquisitionMirror
	err := codex.WithFileLockContext(ctx, path, func() error {
		var errSnapshot error
		mirror, errSnapshot = snapshotAcquisitionMirror(path)
		if errSnapshot != nil {
			return errSnapshot
		}
		dir, errTemp := os.MkdirTemp(filepath.Dir(path), ".auth-acquisition-*")
		if errTemp != nil {
			return errTemp
		}
		defer func() { _ = os.RemoveAll(dir) }()
		stagedPath := filepath.Join(dir, "credential")
		if mirror.existed {
			if errSeed := os.WriteFile(stagedPath, mirror.original, 0o600); errSeed != nil {
				return errSeed
			}
		}
		if errWrite := write(stagedPath); errWrite != nil {
			return errWrite
		}
		// Capture the version before publication, with refresh writers still excluded.
		mirror.path = stagedPath
		errRemember := mirror.rememberWrite()
		mirror.path = path
		if errRemember != nil {
			return errRemember
		}
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		if errRename := os.Rename(stagedPath, path); errRename != nil {
			return errRename
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return mirror, nil
}

func snapshotAcquisitionMirror(path string) (*acquisitionMirror, error) {
	m := &acquisitionMirror{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot auth mirror: %w", err)
	}
	// In-place token writers follow links. Refuse acquisitions before they can
	// modify a referent that rollback does not own.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("acquisition auth mirror must be a regular file")
	}
	m.existed = true
	m.mode = info.Mode()
	m.original, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("snapshot auth mirror: %w", err)
	}
	return m, nil
}

func (m *acquisitionMirror) rememberWrite() error {
	if m == nil {
		return nil
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("read acquired auth mirror: %w", err)
	}
	info, err := os.Stat(m.path)
	if err != nil {
		return fmt.Errorf("inspect acquired auth mirror: %w", err)
	}
	m.written, m.writtenInfo = data, info
	return nil
}

func (m *acquisitionMirror) restore(errSave error) error {
	if m == nil || m.writtenInfo == nil || errSave == nil {
		return errSave
	}
	// Cleanup must still run after the acquisition context has expired.
	if errRestore := codex.WithFileLock(m.path, m.restoreWritten); errRestore != nil {
		return errors.Join(errSave, fmt.Errorf("restore failed acquisition mirror: %w", errRestore))
	}
	return errSave
}

func (m *acquisitionMirror) restoreWritten() error {
	current, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	info, err := os.Stat(m.path)
	if err != nil {
		return err
	}
	// Another writer's credential must survive a failed acquisition.
	if !os.SameFile(info, m.writtenInfo) || !bytes.Equal(current, m.written) {
		return fmt.Errorf("auth mirror changed after acquisition write")
	}
	if !m.existed {
		return os.Remove(m.path)
	}
	file, err := os.CreateTemp(filepath.Dir(m.path), ".auth-rollback-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if errMode := file.Chmod(m.mode.Perm()); errMode != nil {
		_ = file.Close()
		return errMode
	}
	if _, errWrite := file.Write(m.original); errWrite != nil {
		_ = file.Close()
		return errWrite
	}
	if errClose := file.Close(); errClose != nil {
		return errClose
	}

	return os.Rename(tempPath, m.path)
}
