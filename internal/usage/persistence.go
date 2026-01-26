// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultPersistenceInterval = 5 * time.Minute
	defaultPersistenceFilename = "usage-stats.json"
)

// PersistenceConfig holds configuration for the file-based usage persistence plugin.
type PersistenceConfig struct {
	// Enabled controls whether usage statistics are persisted to disk.
	Enabled bool
	// FilePath is the path to the persistence file. If empty, defaults to usage-stats.json in the config directory.
	FilePath string
	// Interval is the duration between periodic saves. If zero, defaults to 5 minutes.
	Interval time.Duration
}

// persistencePayload is the on-disk format for persisted usage statistics.
type persistencePayload struct {
	Version   int               `json:"version"`
	UpdatedAt time.Time         `json:"updated_at"`
	Usage     StatisticsSnapshot `json:"usage"`
}

// PersistencePlugin persists usage statistics to disk and restores them on startup.
// It implements coreusage.Plugin to receive usage records.
type PersistencePlugin struct {
	cfg       PersistenceConfig
	stats     *RequestStatistics
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	lastSaved time.Time
}

// NewPersistencePlugin creates a new persistence plugin with the given configuration.
// It immediately attempts to load any existing persisted data.
//
// Parameters:
//   - cfg: The persistence configuration
//   - stats: The statistics store to persist (typically GetRequestStatistics())
//
// Returns:
//   - *PersistencePlugin: A configured persistence plugin
func NewPersistencePlugin(cfg PersistenceConfig, stats *RequestStatistics) *PersistencePlugin {
	if cfg.Interval == 0 {
		cfg.Interval = defaultPersistenceInterval
	}
	if cfg.FilePath == "" {
		cfg.FilePath = defaultPersistenceFilename
	}

	p := &PersistencePlugin{
		cfg:   cfg,
		stats: stats,
		done:  make(chan struct{}),
	}

	return p
}

// Start begins the periodic persistence goroutine and loads any existing data.
// It should be called once after the plugin is created.
func (p *PersistencePlugin) Start(ctx context.Context) {
	if p == nil || !p.cfg.Enabled {
		return
	}

	// Load existing data on startup
	if err := p.load(); err != nil {
		log.Warnf("usage persistence: failed to load existing data: %v", err)
	} else {
		log.Info("usage persistence: loaded existing statistics from disk")
	}

	// Start periodic save goroutine
	var childCtx context.Context
	childCtx, p.cancel = context.WithCancel(ctx)
	go p.runPeriodic(childCtx)
}

// Stop halts the periodic persistence and performs a final save.
func (p *PersistencePlugin) Stop() {
	if p == nil || !p.cfg.Enabled {
		return
	}

	if p.cancel != nil {
		p.cancel()
	}

	// Wait for periodic goroutine to finish
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		log.Warn("usage persistence: timeout waiting for periodic save to stop")
	}

	// Final save on shutdown
	if err := p.save(); err != nil {
		log.Errorf("usage persistence: failed to save on shutdown: %v", err)
	} else {
		log.Info("usage persistence: saved statistics to disk on shutdown")
	}
}

// HandleUsage implements coreusage.Plugin.
// The actual persistence happens periodically, not per-record.
func (p *PersistencePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	// No-op: persistence happens on a schedule, not per-record.
	// The LoggerPlugin handles in-memory aggregation.
}

// runPeriodic saves statistics to disk at the configured interval.
func (p *PersistencePlugin) runPeriodic(ctx context.Context) {
	defer close(p.done)

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.save(); err != nil {
				log.Errorf("usage persistence: periodic save failed: %v", err)
			} else {
				log.Debug("usage persistence: periodic save completed")
			}
		}
	}
}

// save writes the current statistics to disk.
func (p *PersistencePlugin) save() error {
	if p == nil || p.stats == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	snapshot := p.stats.Snapshot()
	payload := persistencePayload{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Usage:     snapshot,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(p.cfg.FilePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	// Write atomically via temp file
	tmpPath := p.cfg.FilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, p.cfg.FilePath); err != nil {
		os.Remove(tmpPath) // Clean up on failure
		return fmt.Errorf("rename: %w", err)
	}

	p.lastSaved = time.Now()
	return nil
}

// load reads persisted statistics from disk and merges them into the current store.
func (p *PersistencePlugin) load() error {
	if p == nil || p.stats == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.cfg.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing data is fine
		}
		return fmt.Errorf("read file: %w", err)
	}

	var payload persistencePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	if payload.Version != 1 {
		return fmt.Errorf("unsupported version: %d", payload.Version)
	}

	result := p.stats.MergeSnapshot(payload.Usage)
	log.Infof("usage persistence: merged %d records (%d skipped) from disk", result.Added, result.Skipped)

	return nil
}

// FilePath returns the configured persistence file path.
func (p *PersistencePlugin) FilePath() string {
	if p == nil {
		return ""
	}
	return p.cfg.FilePath
}

// LastSaved returns the time of the last successful save.
func (p *PersistencePlugin) LastSaved() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastSaved
}

// ForceSave triggers an immediate save outside the normal schedule.
func (p *PersistencePlugin) ForceSave() error {
	if p == nil || !p.cfg.Enabled {
		return nil
	}
	return p.save()
}
