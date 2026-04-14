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
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const (
	defaultPersistenceFlushInterval = 5 * time.Minute
	defaultPersistenceFlushTimeout  = 5 * time.Second
	persistedStatisticsVersion      = 1
)

// PersistenceConfig controls periodic summary persistence for usage statistics.
type PersistenceConfig struct {
	Enabled       bool
	FilePath      string
	FlushInterval time.Duration
}

type persistedStatisticsFile struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Usage     StatisticsSnapshot `json:"usage"`
}

type persistenceController struct {
	mu        sync.RWMutex
	cfg       PersistenceConfig
	persisted StatisticsSnapshot
	cancel    context.CancelFunc
	done      chan struct{}
}

var defaultPersistence persistenceController

// ConfigureDefaultPersistence updates the runtime persistence configuration for the default usage statistics store.
func ConfigureDefaultPersistence(parent context.Context, cfg PersistenceConfig) error {
	cfg = normalisePersistenceConfig(cfg)

	defaultPersistence.mu.RLock()
	currentCfg := defaultPersistence.cfg
	defaultPersistence.mu.RUnlock()
	if currentCfg == cfg {
		return nil
	}

	defaultPersistence.stop(parent)

	if currentCfg.Enabled {
		if err := flushDefaultPersistenceWithConfig(parent, currentCfg); err != nil {
			return err
		}
	}

	persisted := StatisticsSnapshot{}
	if cfg.Enabled {
		loaded, err := loadPersistedUsageSnapshot(cfg.FilePath)
		if err != nil {
			return err
		}
		persisted = loaded
	}

	defaultPersistence.mu.Lock()
	defaultPersistence.cfg = cfg
	defaultPersistence.persisted = persisted
	defaultPersistence.mu.Unlock()

	if cfg.Enabled {
		defaultPersistence.start(parent, cfg)
	}
	return nil
}

// StopDefaultPersistence flushes the current summary to disk and stops the background persistence worker.
func StopDefaultPersistence(parent context.Context) error {
	defaultPersistence.mu.RLock()
	cfg := defaultPersistence.cfg
	defaultPersistence.mu.RUnlock()

	defaultPersistence.stop(parent)

	var flushErr error
	if cfg.Enabled {
		flushErr = flushDefaultPersistenceWithConfig(parent, cfg)
	}
	return flushErr
}

// SnapshotWithPersistence returns the current usage snapshot merged with any persisted summary data.
func SnapshotWithPersistence(stats *RequestStatistics, includeDetails bool) StatisticsSnapshot {
	var current StatisticsSnapshot
	if stats != nil {
		if includeDetails {
			current = stats.Snapshot()
		} else {
			current = stats.SnapshotSummary()
		}
	}
	if stats != nil && stats != GetRequestStatistics() {
		return current
	}

	defaultPersistence.mu.RLock()
	persisted := defaultPersistence.persisted
	enabled := defaultPersistence.cfg.Enabled
	defaultPersistence.mu.RUnlock()

	if !enabled || isZeroSnapshot(persisted) {
		return current
	}
	if isZeroSnapshot(current) {
		return persisted
	}
	return MergeStatisticsSnapshots(persisted, current)
}

// MergeStatisticsSnapshots combines multiple snapshots into a single summary/detail view.
func MergeStatisticsSnapshots(snapshots ...StatisticsSnapshot) StatisticsSnapshot {
	merged := NewRequestStatistics()
	for _, snapshot := range snapshots {
		if isZeroSnapshot(snapshot) {
			continue
		}
		merged.MergeSnapshot(snapshot)
	}
	return merged.Snapshot()
}

func (p *persistenceController) start(parent context.Context, cfg PersistenceConfig) {
	if !cfg.Enabled {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})

	p.mu.Lock()
	p.cancel = cancel
	p.done = done
	p.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.FlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flushCtx, flushCancel := context.WithTimeout(context.Background(), defaultPersistenceFlushTimeout)
				_ = flushDefaultPersistenceWithConfig(flushCtx, cfg)
				flushCancel()
			}
		}
	}()
}

func (p *persistenceController) stop(parent context.Context) {
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	p.cancel = nil
	p.done = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	waitDone(parent, done)
}

func flushDefaultPersistenceWithConfig(parent context.Context, cfg PersistenceConfig) error {
	cfg = normalisePersistenceConfig(cfg)
	if !cfg.Enabled || strings.TrimSpace(cfg.FilePath) == "" {
		return nil
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}

	flushCtx, cancel := context.WithTimeout(ctx, defaultPersistenceFlushTimeout)
	defer cancel()
	if err := coreusage.FlushDefault(flushCtx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("usage persistence: flush usage queue: %w", err)
	}

	snapshot := GetRequestStatistics().SnapshotSummaryAndReset()
	if isZeroSnapshot(snapshot) {
		return nil
	}

	merged, err := storePersistedUsageSnapshot(cfg.FilePath, snapshot)
	if err != nil {
		return err
	}

	defaultPersistence.mu.Lock()
	if defaultPersistence.cfg.Enabled && defaultPersistence.cfg.FilePath == cfg.FilePath {
		defaultPersistence.persisted = merged
	}
	defaultPersistence.mu.Unlock()
	return nil
}

func loadPersistedUsageSnapshot(path string) (StatisticsSnapshot, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return StatisticsSnapshot{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StatisticsSnapshot{}, nil
		}
		return StatisticsSnapshot{}, fmt.Errorf("usage persistence: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return StatisticsSnapshot{}, nil
	}
	var payload persistedStatisticsFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage persistence: decode %s: %w", path, err)
	}
	return payload.Usage, nil
}

func storePersistedUsageSnapshot(path string, delta StatisticsSnapshot) (StatisticsSnapshot, error) {
	existing, err := loadPersistedUsageSnapshot(path)
	if err != nil {
		return StatisticsSnapshot{}, err
	}
	merged := MergeStatisticsSnapshots(existing, delta)
	if err := writePersistedUsageSnapshot(path, merged); err != nil {
		return StatisticsSnapshot{}, err
	}
	return merged, nil
}

func writePersistedUsageSnapshot(path string, snapshot StatisticsSnapshot) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("usage persistence: create dir %s: %w", dir, err)
		}
	}

	payload := persistedStatisticsFile{
		Version:   persistedStatisticsVersion,
		UpdatedAt: time.Now().UTC(),
		Usage:     stripSnapshotDetails(snapshot),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("usage persistence: encode %s: %w", path, err)
	}

	tempFile, err := os.CreateTemp(dir, "usage-stats-*.tmp")
	if err != nil {
		return fmt.Errorf("usage persistence: create temp file for %s: %w", path, err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if _, errWrite := tempFile.Write(data); errWrite != nil {
		_ = tempFile.Close()
		return fmt.Errorf("usage persistence: write temp file for %s: %w", path, errWrite)
	}
	if errClose := tempFile.Close(); errClose != nil {
		return fmt.Errorf("usage persistence: close temp file for %s: %w", path, errClose)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		return fmt.Errorf("usage persistence: replace %s: %w", path, errRename)
	}
	return nil
}

func stripSnapshotDetails(snapshot StatisticsSnapshot) StatisticsSnapshot {
	stripped := snapshot
	if len(stripped.APIs) == 0 {
		return stripped
	}
	stripped.APIs = make(map[string]APISnapshot, len(snapshot.APIs))
	for apiName, apiSnapshot := range snapshot.APIs {
		clean := APISnapshot{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSnapshot.Models)),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			clean.Models[modelName] = ModelSnapshot{
				TotalRequests: modelSnapshot.TotalRequests,
				TotalTokens:   modelSnapshot.TotalTokens,
			}
		}
		stripped.APIs[apiName] = clean
	}
	return stripped
}

func isZeroSnapshot(snapshot StatisticsSnapshot) bool {
	if snapshot.TotalRequests != 0 || snapshot.SuccessCount != 0 || snapshot.FailureCount != 0 || snapshot.TotalTokens != 0 {
		return false
	}
	if len(snapshot.APIs) != 0 || len(snapshot.RequestsByDay) != 0 || len(snapshot.RequestsByHour) != 0 || len(snapshot.TokensByDay) != 0 || len(snapshot.TokensByHour) != 0 {
		return false
	}
	return true
}

func normalisePersistenceConfig(cfg PersistenceConfig) PersistenceConfig {
	cfg.FilePath = strings.TrimSpace(cfg.FilePath)
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultPersistenceFlushInterval
	}
	return cfg
}

func waitDone(parent context.Context, done <-chan struct{}) {
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}
