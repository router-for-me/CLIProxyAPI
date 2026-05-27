// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// This file implements persistence for usage statistics.
package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/sirupsen/logrus"
)

// PersistenceConfig holds configuration for usage statistics persistence.
type PersistenceConfig struct {
	// Enabled controls whether persistence is active.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Backend specifies the persistence backend: "json" or "sqlite".
	Backend string `yaml:"backend" json:"backend"`

	// Path is the file path for JSON backend or database path for SQLite.
	Path string `yaml:"path" json:"path"`

	// FlushInterval controls how often data is flushed to disk.
	// Default is 5 minutes.
	FlushInterval time.Duration `yaml:"flush-interval" json:"flush-interval"`
}

// PersistencePlugin persists usage statistics to disk.
type PersistencePlugin struct {
	stats  *RequestStatistics
	config PersistenceConfig
	mu     sync.Mutex
	done   chan struct{}
}

var globalPersistencePlugin *PersistencePlugin

// NewPersistencePlugin creates a new persistence plugin.
func NewPersistencePlugin(stats *RequestStatistics, config PersistenceConfig) *PersistencePlugin {
	if config.FlushInterval == 0 {
		config.FlushInterval = 5 * time.Minute
	}

	p := &PersistencePlugin{
		stats:  stats,
		config: config,
		done:   make(chan struct{}),
	}

	if config.Enabled {
		go p.autoFlushLoop()
	}

	return p
}

// InitPersistencePlugin initializes the global persistence plugin from configuration.
func InitPersistencePlugin(enabled bool, backend, path string, flushIntervalSeconds int) {
	if !enabled {
		return
	}

	resolvedPath := resolvePath(path)
	flushInterval := time.Duration(flushIntervalSeconds) * time.Second
	if flushInterval <= 0 {
		flushInterval = 5 * time.Minute
	}

	config := PersistenceConfig{
		Enabled:       true,
		Backend:       backend,
		Path:          resolvedPath,
		FlushInterval: flushInterval,
	}

	stats := GetRequestStatistics()
	globalPersistencePlugin = NewPersistencePlugin(stats, config)

	// Restore data from disk on startup
	if err := globalPersistencePlugin.Restore(); err != nil {
		logrus.Errorf("failed to restore usage statistics from disk: %v", err)
	}

	// Register as a plugin so it can be stopped
	coreusage.RegisterPlugin(globalPersistencePlugin)

	logrus.Infof("usage persistence enabled: backend=%s path=%s flush_interval=%s", backend, resolvedPath, flushInterval)
}

// StopPersistencePlugin stops the global persistence plugin and flushes data to disk.
func StopPersistencePlugin() {
	if globalPersistencePlugin == nil {
		return
	}

	// Flush data before stopping
	if err := globalPersistencePlugin.Flush(); err != nil {
		logrus.Errorf("failed to flush usage statistics on shutdown: %v", err)
	}

	globalPersistencePlugin.Stop()
	globalPersistencePlugin = nil
}

func resolvePath(path string) string {
	if path == "" {
		return "~/.cli-proxy-api/usage.json"
	}

	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			logrus.Warnf("failed to resolve home directory: %v, using path as-is", err)
			return path
		}
		return filepath.Join(home, path[1:])
	}

	return path
}

// HandleUsage implements coreusage.Plugin.
func (p *PersistencePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	// No-op: persistence is handled by periodic flush
}

// Flush persists current statistics to disk.
func (p *PersistencePlugin) Flush() error {
	if !p.config.Enabled {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.config.Backend {
	case "json":
		return p.flushJSON()
	default:
		return p.flushJSON()
	}
}

// Restore loads statistics from disk.
func (p *PersistencePlugin) Restore() error {
	if !p.config.Enabled {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.config.Backend {
	case "json":
		return p.restoreJSON()
	default:
		return p.restoreJSON()
	}
}

// Stop stops the auto-flush loop.
func (p *PersistencePlugin) Stop() {
	close(p.done)
}

func (p *PersistencePlugin) autoFlushLoop() {
	ticker := time.NewTicker(p.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := p.Flush(); err != nil {
				logrus.Errorf("usage persistence flush failed: %v", err)
			}
		case <-p.done:
			return
		}
	}
}

func (p *PersistencePlugin) flushJSON() error {
	snapshot := p.stats.Snapshot()

	dir := filepath.Dir(p.config.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(p.config.Path, data, 0644)
}

func (p *PersistencePlugin) restoreJSON() error {
	data, err := os.ReadFile(p.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}

	p.stats.MergeSnapshot(snapshot)
	return nil
}
