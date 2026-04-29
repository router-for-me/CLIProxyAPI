package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

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

// Load reads a snapshot from disk. Missing or malformed files return an empty snapshot.
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
		return StatisticsSnapshot{}, nil
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
	return os.WriteFile(s.path, data, 0o600)
}
