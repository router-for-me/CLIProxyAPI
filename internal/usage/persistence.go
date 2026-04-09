package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultPersistenceFileName = "usage-statistics.json"
	defaultFlushInterval       = 30 * time.Second
)

type persistencePayload struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

// DefaultPersistencePath returns the default on-disk path for usage statistics snapshots.
func DefaultPersistencePath(configFilePath string) string {
	if configFilePath != "" {
		return filepath.Join(filepath.Dir(configFilePath), defaultPersistenceFileName)
	}
	wd, err := os.Getwd()
	if err != nil {
		return defaultPersistenceFileName
	}
	return filepath.Join(wd, defaultPersistenceFileName)
}

// LoadSnapshotFile restores persisted usage statistics into the provided store.
func LoadSnapshotFile(path string, stats *RequestStatistics) (MergeResult, error) {
	result := MergeResult{}
	if stats == nil {
		return result, fmt.Errorf("usage persistence: statistics store is nil")
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == "" || cleanPath == "." {
		return result, fmt.Errorf("usage persistence: path is required")
	}
	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("usage persistence: read snapshot: %w", err)
	}
	if len(raw) == 0 {
		return result, nil
	}
	var payload persistencePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return result, fmt.Errorf("usage persistence: decode snapshot: %w", err)
	}
	if payload.Version != 0 && payload.Version != 1 {
		return result, fmt.Errorf("usage persistence: unsupported snapshot version %d", payload.Version)
	}
	return stats.MergeSnapshot(payload.Usage), nil
}

// SaveSnapshotFile writes the current usage statistics snapshot to disk atomically.
func SaveSnapshotFile(path string, stats *RequestStatistics) error {
	if stats == nil {
		return fmt.Errorf("usage persistence: statistics store is nil")
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == "" || cleanPath == "." {
		return fmt.Errorf("usage persistence: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return fmt.Errorf("usage persistence: create directory: %w", err)
	}
	payload := persistencePayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      stats.Snapshot(),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("usage persistence: encode snapshot: %w", err)
	}
	tmpPath := cleanPath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return fmt.Errorf("usage persistence: write temp snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, cleanPath); err != nil {
		return fmt.Errorf("usage persistence: replace snapshot: %w", err)
	}
	return nil
}

// StartAutoPersistence periodically flushes usage statistics and performs a final flush on shutdown.
func StartAutoPersistence(ctx context.Context, path string, interval time.Duration, stats *RequestStatistics) {
	if ctx == nil || stats == nil {
		return
	}
	if interval <= 0 {
		interval = defaultFlushInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if err := SaveSnapshotFile(path, stats); err != nil {
					log.WithError(err).Warn("failed to flush usage statistics on shutdown")
				}
				return
			case <-ticker.C:
				if err := SaveSnapshotFile(path, stats); err != nil {
					log.WithError(err).Warn("failed to flush usage statistics snapshot")
				}
			}
		}
	}()
}
