package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

const (
	StatisticsFileVersion = 1
	StatisticsFileName    = "usage-statistics.json"
)

// StatisticsFilePayload is the on-disk representation used for automatic
// persistence. It intentionally mirrors the management export payload so the
// file can still be imported through existing management APIs when needed.
type StatisticsFilePayload struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

// StatisticsFilePath returns the default on-disk path used for automatic usage
// statistics persistence. The file lives alongside application logs so it is
// naturally covered by existing docker volume mounts.
func StatisticsFilePath(cfg *config.Config) string {
	logDir := strings.TrimSpace(logging.ResolveLogDirectory(cfg))
	if logDir == "" {
		return StatisticsFileName
	}
	return filepath.Join(filepath.Clean(logDir), StatisticsFileName)
}

// SaveSnapshotFile writes a complete statistics snapshot to disk atomically.
func SaveSnapshotFile(path string, snapshot StatisticsSnapshot) error {
	payload := StatisticsFilePayload{
		Version:    StatisticsFileVersion,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	}
	data, errMarshal := json.MarshalIndent(payload, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("usage: marshal snapshot file: %w", errMarshal)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data)
}

// LoadSnapshotFile reads a persisted snapshot from disk. It accepts both the
// wrapped persistence payload and a bare StatisticsSnapshot for backward
// compatibility with older/manual files.
func LoadSnapshotFile(path string) (StatisticsSnapshot, error) {
	var snapshot StatisticsSnapshot

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return snapshot, errRead
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return snapshot, fmt.Errorf("usage: statistics file is empty")
	}

	var envelope map[string]json.RawMessage
	if errEnvelope := json.Unmarshal(trimmed, &envelope); errEnvelope == nil {
		if _, ok := envelope["usage"]; ok {
			var payload StatisticsFilePayload
			if errPayload := json.Unmarshal(trimmed, &payload); errPayload != nil {
				return snapshot, fmt.Errorf("usage: decode snapshot payload: %w", errPayload)
			}
			if payload.Version != 0 && payload.Version != StatisticsFileVersion {
				return snapshot, fmt.Errorf("usage: unsupported snapshot version %d", payload.Version)
			}
			return payload.Usage, nil
		}
	}

	if errSnapshot := json.Unmarshal(trimmed, &snapshot); errSnapshot != nil {
		return snapshot, fmt.Errorf("usage: decode snapshot: %w", errSnapshot)
	}
	return snapshot, nil
}

// RestoreRequestStatistics merges the persisted snapshot at path into stats.
// Missing files are treated as a no-op.
func RestoreRequestStatistics(path string, stats *RequestStatistics) (loaded bool, result MergeResult, err error) {
	if stats == nil {
		return false, result, nil
	}
	_, versionBefore, persistedBefore := stats.SnapshotWithState()
	snapshot, errLoad := LoadSnapshotFile(path)
	if errLoad != nil {
		if os.IsNotExist(errLoad) {
			return false, result, nil
		}
		return false, result, errLoad
	}
	result = stats.MergeSnapshot(snapshot)
	if versionBefore == persistedBefore {
		stats.MarkAllPersisted()
	}
	return true, result, nil
}

// PersistRequestStatistics writes the current statistics snapshot to disk when
// there are unpersisted changes. Concurrent writes are tolerated: if new
// changes arrive while a snapshot is being written, they remain pending for the
// next persistence pass.
func PersistRequestStatistics(path string, stats *RequestStatistics) (bool, error) {
	if stats == nil {
		return false, nil
	}
	snapshot, version, persistedVersion := stats.SnapshotWithState()
	if version == persistedVersion {
		return false, nil
	}
	if err := SaveSnapshotFile(path, snapshot); err != nil {
		return false, err
	}
	stats.MarkPersisted(version)
	return true, nil
}

func writeFileAtomic(path string, data []byte) error {
	target := strings.TrimSpace(path)
	if target == "" {
		return fmt.Errorf("usage: empty snapshot path")
	}
	target = filepath.Clean(target)

	dir := filepath.Dir(target)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return fmt.Errorf("usage: create snapshot directory: %w", errMkdir)
	}

	tmpFile, errCreate := os.CreateTemp(dir, "usage-statistics-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("usage: create temp snapshot file: %w", errCreate)
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		return fmt.Errorf("usage: write temp snapshot file: %w", errWrite)
	}
	if errSync := tmpFile.Sync(); errSync != nil {
		return fmt.Errorf("usage: sync temp snapshot file: %w", errSync)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		return fmt.Errorf("usage: close temp snapshot file: %w", errClose)
	}
	if errRename := os.Rename(tmpName, target); errRename != nil {
		return fmt.Errorf("usage: rename snapshot file: %w", errRename)
	}

	if dirHandle, errOpenDir := os.Open(dir); errOpenDir == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}

	return nil
}
