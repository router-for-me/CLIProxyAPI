package usage

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// workItem represents a usage record to be persisted to SQLite.
type workItem struct {
	detail    RequestDetail
	apiKey    string
	modelName string
}

// SQLitePlugin bridges SQLite persistence with in-memory statistics.
// It writes usage records to both SQLite (for persistence) and memory (for fast reads).
type SQLitePlugin struct {
	store       *SQLiteStore
	memoryStats *RequestStatistics
	workChan    chan workItem
	wg          sync.WaitGroup
	stopOnce    sync.Once
}

// NewSQLitePlugin creates a new SQLite-backed plugin instance.
// It starts a worker pool with the specified number of workers for async writes.
func NewSQLitePlugin(store *SQLiteStore, workers int) *SQLitePlugin {
	if workers <= 0 {
		workers = 4 // Default to 4 workers
	}

	p := &SQLitePlugin{
		store:       store,
		memoryStats: NewRequestStatistics(),
		workChan:    make(chan workItem, 1000), // Buffered channel for 1000 items
	}

	// Start worker pool
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	return p
}

// worker processes usage records from the work channel and writes them to SQLite.
func (p *SQLitePlugin) worker() {
	defer p.wg.Done()
	for item := range p.workChan {
		if err := p.store.Record(item.detail, item.apiKey, item.modelName); err != nil {
			log.Warnf("usage sqlite: failed to record: %v", err)
		}
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
		timestamp = time.Now()
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

	// Async write to SQLite via worker pool (non-blocking with backpressure)
	if p.store != nil && p.workChan != nil {
		select {
		case p.workChan <- workItem{
			detail:    requestDetail,
			apiKey:    statsKey,
			modelName: modelName,
		}:
		default:
			log.Warn("usage sqlite: work queue full, dropping record")
		}
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

// Import persists an imported snapshot to the SQLite database.
func (p *SQLitePlugin) Import(snapshot StatisticsSnapshot) (MergeResult, error) {
	if p.store == nil {
		return MergeResult{}, nil
	}
	added, skipped, err := p.store.ImportSnapshot(snapshot)
	return MergeResult{Added: added, Skipped: skipped}, err
}

// Close gracefully shuts down the worker pool and waits for pending writes.
func (p *SQLitePlugin) Close() {
	p.stopOnce.Do(func() {
		if p.workChan != nil {
			close(p.workChan)
			p.wg.Wait()
		}
	})
}
