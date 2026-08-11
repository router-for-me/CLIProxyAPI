package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnsupportedSnapshotVersion indicates that persisted data was written by an
// incompatible usage snapshot schema.
var ErrUnsupportedSnapshotVersion = errors.New("unsupported usage snapshot version")

// Store persists versioned aggregate snapshots.
type Store interface {
	Load(context.Context) (Snapshot, error)
	Save(context.Context, Snapshot) error
}

// FileStore saves a single JSON snapshot using a same-directory atomic rename.
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore constructs a file-backed usage aggregate store.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: strings.TrimSpace(path)}
}

// Load reads and validates the configured snapshot. A missing or empty file is
// treated as empty state.
func (s *FileStore) Load(ctx context.Context) (Snapshot, error) {
	if s == nil || s.path == "" {
		return Snapshot{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return Snapshot{}, errCtx
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	data, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return Snapshot{}, nil
		}
		return Snapshot{}, fmt.Errorf("read usage snapshot: %w", errRead)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Snapshot{}, nil
	}
	var snapshot Snapshot
	if errUnmarshal := json.Unmarshal(data, &snapshot); errUnmarshal != nil {
		return Snapshot{}, fmt.Errorf("parse usage snapshot: %w", errUnmarshal)
	}
	if snapshot.Version != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("%w: %d", ErrUnsupportedSnapshotVersion, snapshot.Version)
	}
	return snapshot, nil
}

// Save writes a version-one snapshot with owner-only permissions.
func (s *FileStore) Save(ctx context.Context, snapshot Snapshot) error {
	if s == nil || s.path == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	snapshot.Version = SnapshotVersion
	data, errMarshal := json.MarshalIndent(snapshot, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal usage snapshot: %w", errMarshal)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create usage snapshot directory: %w", errMkdir)
	}
	tmpFile, errCreate := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create usage snapshot temp file: %w", errCreate)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		if errRemove := os.Remove(tmpPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			// Best-effort cleanup; the primary operation error is more useful.
			return
		}
	}
	if errChmod := tmpFile.Chmod(0o600); errChmod != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("secure usage snapshot temp file: %w", errChmod)
	}
	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("write usage snapshot temp file: %w", errWrite)
	}
	if errSync := tmpFile.Sync(); errSync != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("sync usage snapshot temp file: %w", errSync)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		cleanup()
		return fmt.Errorf("close usage snapshot temp file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, s.path); errRename != nil {
		cleanup()
		return fmt.Errorf("replace usage snapshot: %w", errRename)
	}
	if errChmod := os.Chmod(s.path, 0o600); errChmod != nil {
		return fmt.Errorf("secure usage snapshot: %w", errChmod)
	}
	return nil
}
