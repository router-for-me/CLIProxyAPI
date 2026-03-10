package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultAutoSaveInterval = 5 * time.Minute
	usageStatsFileName      = "usage-stats.dat"
	maxDetailEntries        = 10000
)

var (
	persistPath string
	persistMu   sync.Mutex
	cancelSave  context.CancelFunc
)

func resolveDataPath() string {
	if p := os.Getenv("USAGE_DATA_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	aiDir := filepath.Join(home, ".ai")
	if info, err := os.Stat(aiDir); err == nil && info.IsDir() {
		return filepath.Join(aiDir, usageStatsFileName)
	}
	return ""
}

func SaveToFile(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	stats := GetRequestStatistics()
	if stats == nil {
		return nil
	}
	stats.TrimDetails(maxDetailEntries)
	snapshot := stats.Snapshot()
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal usage snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func LoadFromFile(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read usage file: %w", err)
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("unmarshal usage snapshot: %w", err)
	}
	stats := GetRequestStatistics()
	if stats != nil {
		stats.MergeSnapshot(snapshot)
	}
	return nil
}

func InitPersistence() {
	persistMu.Lock()
	defer persistMu.Unlock()

	persistPath = resolveDataPath()
	if persistPath == "" {
		return
	}
	if err := LoadFromFile(persistPath); err != nil {
		fmt.Fprintf(os.Stderr, "[usage] failed to load stats from %s: %v\n", persistPath, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelSave = cancel
	go autoSaveLoop(ctx, persistPath, defaultAutoSaveInterval)
}

func StopPersistence() {
	persistMu.Lock()
	defer persistMu.Unlock()

	if cancelSave != nil {
		cancelSave()
		cancelSave = nil
	}
	if persistPath != "" {
		if err := SaveToFile(persistPath); err != nil {
			fmt.Fprintf(os.Stderr, "[usage] failed to save stats to %s: %v\n", persistPath, err)
		}
	}
}

func autoSaveLoop(ctx context.Context, path string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := SaveToFile(path); err != nil {
				fmt.Fprintf(os.Stderr, "[usage] auto-save failed: %v\n", err)
			}
		}
	}
}
