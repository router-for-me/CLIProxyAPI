package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type acquisitionMirror struct {
	path        string
	original    []byte
	mode        os.FileMode
	link        string
	existed     bool
	written     []byte
	writtenInfo os.FileInfo
}

// Runtime saves keep their local-first behavior; a failed login must not replace
// the credential the watcher was already using. The caller holds its store lock.
func snapshotAcquisitionMirror(ctx context.Context, path string) (*acquisitionMirror, error) {
	if !cliproxyauth.HasAuthCreationIntent(ctx) {
		return nil, nil
	}
	m := &acquisitionMirror{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot auth mirror: %w", err)
	}
	m.existed = true
	m.mode = info.Mode()
	if info.Mode()&os.ModeSymlink != 0 {
		m.link, err = os.Readlink(path)
	} else {
		m.original, err = os.ReadFile(path)
	}
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
	if errRestore := m.restoreWritten(); errRestore != nil {
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
	if m.link != "" {
		if errClose := file.Close(); errClose != nil {
			return errClose
		}
		if errRemove := os.Remove(tempPath); errRemove != nil {
			return errRemove
		}
		if errLink := os.Symlink(m.link, tempPath); errLink != nil {
			return errLink
		}
	} else {
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
	}
	return os.Rename(tempPath, m.path)
}
