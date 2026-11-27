// Package store persists small JSON documents atomically in the plugin's
// state directory and guards the directory with an advisory lock.
package store

import (
	"errors"
	"fmt"
	"github.com/MoeForge/moego/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxDocumentBytes = 64 << 20

// Store owns one state directory.
type Store struct {
	dir      string
	lock     *os.File
	writable bool
	reason   string
}

// LoadResult describes what Load found.
type LoadResult string

const (
	Loaded      LoadResult = "loaded"
	Missing     LoadResult = "missing"
	Quarantined LoadResult = "quarantined"
)

// Open creates the directory (0700) and takes the advisory lock. When another
// process holds the lock the store is read-only: Save becomes a no-op so two
// proxies never overwrite each other's state.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("state_dir_unavailable")
	}
	s := &Store{dir: dir}
	f, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		s.reason = "lock_unavailable"
		return s, nil
	}
	if err := lockFile(f); err != nil {
		f.Close()
		s.reason = "locked_by_another_process"
		return s, nil
	}
	s.lock = f
	s.writable = true
	return s, nil
}

// Writable reports whether Save persists, and why not.
func (s *Store) Writable() (bool, string) { return s.writable, s.reason }

// Dir is the state directory.
func (s *Store) Dir() string { return s.dir }

// Load decodes name into v. A file that cannot be decoded is renamed to
// name.corrupt-<unix> and reported as Quarantined so the plugin starts clean
// instead of failing.
func (s *Store) Load(name string, v any) (LoadResult, error) {
	path := filepath.Join(s.dir, name)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Missing, nil
	}
	if err != nil {
		return Missing, errors.New("state_read_failed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	f.Close()
	if err == nil && len(raw) <= maxDocumentBytes && json.Unmarshal(raw, v) == nil {
		return Loaded, nil
	}
	if s.writable {
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix()))
	}
	return Quarantined, nil
}

// Save writes v to name atomically: temp file, fsync, rename, directory fsync.
// A crash at any point leaves either the old or the new document.
func (s *Store) Save(name string, v any) error {
	if !s.writable {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return errors.New("state_encode_failed")
	}
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("state_write_failed")
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return errors.New("state_write_failed")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("state_sync_failed")
	}
	if err := f.Close(); err != nil {
		return errors.New("state_write_failed")
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.New("state_rename_failed")
	}
	syncDir(s.dir)
	return nil
}

// Close releases the lock.
func (s *Store) Close() {
	if s.lock != nil {
		unlockFile(s.lock)
		s.lock.Close()
		s.lock = nil
	}
	s.writable = false
}
