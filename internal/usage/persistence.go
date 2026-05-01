package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrMalformedSnapshot = errors.New("malformed usage statistics snapshot")

type SnapshotWriter interface {
	Save(StatisticsSnapshot) error
}

// SnapshotStore persists usage snapshots as JSON on disk.
type SnapshotStore struct {
	path string
}

// NewSnapshotStore constructs a snapshot store for path.
func NewSnapshotStore(path string) *SnapshotStore {
	if path == "" {
		path = DefaultSnapshotPath("")
	}
	return &SnapshotStore{path: path}
}

// DefaultSnapshotPath returns the snapshot path next to the config file.
func DefaultSnapshotPath(configFilePath string) string {
	configDir := filepath.Dir(configFilePath)
	if configFilePath == "" || configDir == "." {
		configDir = "."
	}
	return filepath.Join(configDir, "usage-statistics.json")
}

// Path returns the configured snapshot path.
func (s *SnapshotStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads a snapshot from disk. Missing files return an empty snapshot.
func (s *SnapshotStore) Load() (StatisticsSnapshot, error) {
	if s == nil || s.path == "" {
		return StatisticsSnapshot{}, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatisticsSnapshot{}, nil
		}
		return StatisticsSnapshot{}, err
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("%w: %v", ErrMalformedSnapshot, err)
	}
	return snapshot, nil
}

// Save writes a snapshot to disk as JSON.
func (s *SnapshotStore) Save(snapshot StatisticsSnapshot) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

type SnapshotSaver struct {
	writer SnapshotWriter
	delay  time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	pending *StatisticsSnapshot
	closed  bool
}

func NewSnapshotSaver(writer SnapshotWriter, delay time.Duration) *SnapshotSaver {
	if delay <= 0 {
		delay = time.Second
	}
	return &SnapshotSaver{writer: writer, delay: delay}
}

func (s *SnapshotSaver) SaveSoon(snapshot StatisticsSnapshot) {
	if s == nil || s.writer == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pending = &snapshot
	if s.timer == nil {
		s.timer = time.AfterFunc(s.delay, s.flushPending)
		return
	}
	s.timer.Reset(s.delay)
}

func (s *SnapshotSaver) Flush() error {
	if s == nil || s.writer == nil {
		return nil
	}
	snapshot, ok := s.takePending()
	if !ok {
		return nil
	}
	return s.writer.Save(snapshot)
}

func (s *SnapshotSaver) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.closed = true
	s.mu.Unlock()
	return s.Flush()
}

func (s *SnapshotSaver) takePending() (StatisticsSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return StatisticsSnapshot{}, false
	}
	snapshot := *s.pending
	s.pending = nil
	return snapshot, true
}

func (s *SnapshotSaver) flushPending() {
	_ = s.Flush()
}
