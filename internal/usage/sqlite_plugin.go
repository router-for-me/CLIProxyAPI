package usage

import (
	"context"

	log "github.com/sirupsen/logrus"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// SQLitePlugin bridges SQLite persistence with in-memory statistics.
// It writes usage records to both SQLite (for persistence) and memory (for fast reads).
type SQLitePlugin struct {
	store       *SQLiteStore
	memoryStats *RequestStatistics
}

// NewSQLitePlugin creates a new SQLite-backed plugin instance.
func NewSQLitePlugin(store *SQLiteStore) *SQLitePlugin {
	return &SQLitePlugin{
		store:       store,
		memoryStats: NewRequestStatistics(),
	}
}

// HandleUsage implements coreusage.Plugin.
// It writes usage records to both SQLite (async) and in-memory stats (sync).
func (p *SQLitePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.memoryStats == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = record.RequestedAt
	}

	detail := normaliseDetail(record.Detail)
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	requestDetail := RequestDetail{
		Timestamp: timestamp,
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    detail,
		Failed:    failed,
	}

	// Async write to SQLite (don't block request processing)
	if p.store != nil {
		go func() {
			if err := p.store.Record(requestDetail, statsKey, modelName); err != nil {
				log.Warnf("usage sqlite: failed to record: %v", err)
			}
		}()
	}

	// Sync write to memory for immediate stats availability
	p.memoryStats.Record(ctx, record)
}

// LoadFromDB loads persisted data from SQLite into memory on startup.
func (p *SQLitePlugin) LoadFromDB() error {
	if p.store == nil {
		return nil
	}

	snapshot, err := p.store.LoadSnapshot()
	if err != nil {
		return err
	}

	p.memoryStats.MergeSnapshot(snapshot)
	return nil
}

// GetStats returns the in-memory statistics store for reading.
func (p *SQLitePlugin) GetStats() *RequestStatistics {
	return p.memoryStats
}
